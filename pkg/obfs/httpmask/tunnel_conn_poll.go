package httpmask

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"
)

type pollConn struct {
	queuedConn

	ctx    context.Context
	cancel context.CancelFunc

	client     *http.Client
	pushURL    string
	pullURL    string
	finURL     string
	closeURL   string
	headerHost string
	auth       *tunnelAuth
}

func (c *pollConn) closeWithError(err error) error {
	_ = c.queuedConn.closeWithError(err)
	if c.cancel != nil {
		c.cancel()
	}
	bestEffortCloseSession(c.client, c.closeURL, c.headerHost, TunnelModePoll, c.auth)
	return nil
}

func (c *pollConn) Close() error {
	return c.closeWithError(io.ErrClosedPipe)
}

func dialPoll(ctx context.Context, serverAddress string, opts TunnelDialOptions) (net.Conn, error) {
	info, err := dialSession(ctx, serverAddress, opts, TunnelModePoll)
	if err != nil {
		return nil, err
	}

	connCtx, cancel := context.WithCancel(context.Background())
	c := &pollConn{
		ctx:        connCtx,
		cancel:     cancel,
		client:     info.client,
		pushURL:    info.pushURL,
		pullURL:    info.pullURL,
		finURL:     info.finURL,
		closeURL:   info.closeURL,
		headerHost: info.headerHost,
		auth:       info.auth,
		queuedConn: queuedConn{
			rxc:         make(chan []byte, 128),
			closed:      make(chan struct{}),
			writeCh:     make(chan []byte, 256),
			writeClosed: make(chan struct{}),
			localAddr:   &net.TCPAddr{},
			remoteAddr:  &net.TCPAddr{},
		},
	}

	go c.pullLoop()
	go c.pushLoop()
	outConn := net.Conn(c)
	if opts.Upgrade != nil {
		upgraded, err := opts.Upgrade(c)
		if err != nil {
			_ = c.Close()
			return nil, err
		}
		if upgraded != nil {
			outConn = upgraded
		}
	}
	return outConn, nil
}

func (c *pollConn) pullLoop() {
	const (
		maxDialRetry = 12
		minBackoff   = 10 * time.Millisecond
		maxBackoff   = 250 * time.Millisecond
	)

	var (
		dialRetry int
		backoff   = minBackoff
	)
	for {
		select {
		case <-c.closed:
			return
		default:
		}

		req, err := http.NewRequestWithContext(c.ctx, http.MethodGet, c.pullURL, nil)
		if err != nil {
			_ = c.closeWithError(err)
			return
		}
		req.Host = c.headerHost
		applyTunnelHeaders(req.Header, c.headerHost, TunnelModePoll)
		applyTunnelAuth(req, c.auth, TunnelModePoll, http.MethodGet, "/stream")

		resp, err := c.client.Do(req)
		if err != nil {
			if isDialError(err) && dialRetry < maxDialRetry {
				dialRetry++
				select {
				case <-time.After(backoff):
				case <-c.closed:
					return
				}
				backoff *= 2
				if backoff > maxBackoff {
					backoff = maxBackoff
				}
				continue
			}
			_ = c.closeWithError(fmt.Errorf("poll pull request failed: %w", err))
			return
		}
		dialRetry = 0
		backoff = minBackoff

		if resp.StatusCode != http.StatusOK {
			_ = resp.Body.Close()
			_ = c.closeWithError(fmt.Errorf("poll pull bad status: %s", resp.Status))
			return
		}

		scanner := bufio.NewScanner(resp.Body)
		for scanner.Scan() {
			line := scanner.Text()
			if line == "" {
				continue
			}
			payload, err := base64.StdEncoding.DecodeString(line)
			if err != nil {
				_ = resp.Body.Close()
				_ = c.closeWithError(fmt.Errorf("poll pull decode failed: %w", err))
				return
			}
			select {
			case c.rxc <- payload:
			case <-c.closed:
				_ = resp.Body.Close()
				return
			}
		}
		_ = resp.Body.Close()
		if err := scanner.Err(); err != nil {
			_ = c.closeWithError(fmt.Errorf("poll pull scan failed: %w", err))
			return
		}
	}
}

func (c *pollConn) pushLoop() {
	const (
		maxBatchBytes   = 64 * 1024
		flushInterval   = 5 * time.Millisecond
		maxLineRawBytes = 16 * 1024
		maxDialRetry    = 12
		minBackoff      = 10 * time.Millisecond
		maxBackoff      = 250 * time.Millisecond
	)

	var (
		buf        bytes.Buffer
		pendingRaw int
		timer      = time.NewTimer(flushInterval)
	)
	defer timer.Stop()

	flush := func() error {
		if buf.Len() == 0 {
			return nil
		}

		reqCtx, cancel := context.WithTimeout(c.ctx, 20*time.Second)
		req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, c.pushURL, bytes.NewReader(buf.Bytes()))
		if err != nil {
			cancel()
			return err
		}
		req.Host = c.headerHost
		applyTunnelHeaders(req.Header, c.headerHost, TunnelModePoll)
		applyTunnelAuth(req, c.auth, TunnelModePoll, http.MethodPost, "/api/v1/upload")
		req.Header.Set("Content-Type", "text/plain")

		resp, err := c.client.Do(req)
		if err != nil {
			cancel()
			return err
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4*1024))
		_ = resp.Body.Close()
		cancel()
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("bad status: %s", resp.Status)
		}

		buf.Reset()
		pendingRaw = 0
		return nil
	}

	flushWithRetry := func() error {
		return retryDial(c.closed, c.closedErr, maxDialRetry, minBackoff, maxBackoff, flush)
	}
	resetTimer(timer, flushInterval)

	enqueue := func(b []byte) error {
		for len(b) > 0 {
			chunk := b
			if len(chunk) > maxLineRawBytes {
				chunk = b[:maxLineRawBytes]
			}
			b = b[len(chunk):]

			encLen := base64.StdEncoding.EncodedLen(len(chunk))
			if pendingRaw+len(chunk) > maxBatchBytes || buf.Len()+encLen+1 > maxBatchBytes*2 {
				if err := flushWithRetry(); err != nil {
					return err
				}
			}

			tmp := make([]byte, encLen)
			base64.StdEncoding.Encode(tmp, chunk)
			buf.Write(tmp)
			buf.WriteByte('\n')
			pendingRaw += len(chunk)
		}
		return nil
	}

	for {
		select {
		case b, ok := <-c.writeCh:
			if !ok {
				_ = flushWithRetry()
				return
			}
			if len(b) == 0 {
				continue
			}

			if err := enqueue(b); err != nil {
				_ = c.closeWithError(fmt.Errorf("poll push flush failed: %w", err))
				return
			}

			if pendingRaw >= maxBatchBytes {
				if err := flushWithRetry(); err != nil {
					_ = c.closeWithError(fmt.Errorf("poll push flush failed: %w", err))
					return
				}
				resetTimer(timer, flushInterval)
			}
		case <-timer.C:
			if err := flushWithRetry(); err != nil {
				_ = c.closeWithError(fmt.Errorf("poll push flush failed: %w", err))
				return
			}
			resetTimer(timer, flushInterval)
		case <-c.writeClosed:
			// Drain any already-accepted writes so CloseWrite does not lose data.
			for {
				select {
				case b := <-c.writeCh:
					if len(b) == 0 {
						continue
					}
					if err := enqueue(b); err != nil {
						_ = c.closeWithError(fmt.Errorf("poll push flush failed: %w", err))
						return
					}
				default:
					_ = flushWithRetry()
					bestEffortCloseSession(c.client, c.finURL, c.headerHost, TunnelModePoll, c.auth)
					return
				}
			}
		case <-c.closed:
			_ = flushWithRetry()
			return
		}
	}
}
