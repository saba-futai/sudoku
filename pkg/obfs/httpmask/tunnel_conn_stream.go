package httpmask

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"
)

func dialStream(ctx context.Context, serverAddress string, opts TunnelDialOptions) (net.Conn, error) {
	// "stream" mode uses split-stream to stay CDN-friendly by default.
	return dialStreamSplit(ctx, serverAddress, opts)
}

type streamSplitConn struct {
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

func (c *streamSplitConn) Close() error {
	_ = c.closeWithError(io.ErrClosedPipe)

	if c.cancel != nil {
		c.cancel()
	}

	bestEffortCloseSession(c.client, c.closeURL, c.headerHost, TunnelModeStream, c.auth)
	return nil
}

func dialStreamSplit(ctx context.Context, serverAddress string, opts TunnelDialOptions) (net.Conn, error) {
	info, err := dialSession(ctx, serverAddress, opts, TunnelModeStream)
	if err != nil {
		return nil, err
	}

	connCtx, cancel := context.WithCancel(context.Background())
	c := &streamSplitConn{
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
			rxc:         make(chan []byte, 256),
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

func (c *streamSplitConn) pullLoop() {
	const (
		// requestTimeout must be long enough for continuous high-throughput streams (e.g. mux + large downloads).
		// If it is too short, the client cancels the response mid-body and corrupts the byte stream.
		requestTimeout = 2 * time.Minute
		readChunkSize  = 32 * 1024
		idleBackoff    = 25 * time.Millisecond
		maxDialRetry   = 12
		minBackoff     = 10 * time.Millisecond
		maxBackoff     = 250 * time.Millisecond
	)

	var (
		dialRetry int
		backoff   = minBackoff
	)
	buf := make([]byte, readChunkSize)
	for {
		select {
		case <-c.closed:
			return
		default:
		}

		reqCtx, cancel := context.WithTimeout(c.ctx, requestTimeout)
		req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, c.pullURL, nil)
		if err != nil {
			cancel()
			_ = c.Close()
			return
		}
		req.Host = c.headerHost
		applyTunnelHeaders(req.Header, c.headerHost, TunnelModeStream)
		applyTunnelAuth(req, c.auth, TunnelModeStream, http.MethodGet, "/stream")

		resp, err := c.client.Do(req)
		if err != nil {
			cancel()
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
			_ = c.Close()
			return
		}
		dialRetry = 0
		backoff = minBackoff

		if resp.StatusCode != http.StatusOK {
			_ = resp.Body.Close()
			cancel()
			_ = c.Close()
			return
		}

		readAny := false
		for {
			n, rerr := resp.Body.Read(buf)
			if n > 0 {
				readAny = true
				payload := make([]byte, n)
				copy(payload, buf[:n])
				select {
				case c.rxc <- payload:
				case <-c.closed:
					_ = resp.Body.Close()
					cancel()
					return
				}
			}
			if rerr != nil {
				_ = resp.Body.Close()
				cancel()
				if errors.Is(rerr, io.EOF) {
					// Long-poll ended; retry.
					break
				}
				_ = c.Close()
				return
			}
		}
		cancel()
		if !readAny {
			// Avoid tight loop if the server replied quickly with an empty body.
			select {
			case <-time.After(idleBackoff):
			case <-c.closed:
				return
			}
		}
	}
}

func (c *streamSplitConn) pushLoop() {
	const (
		maxBatchBytes  = 256 * 1024
		flushInterval  = 5 * time.Millisecond
		requestTimeout = 20 * time.Second
		maxDialRetry   = 12
		minBackoff     = 10 * time.Millisecond
		maxBackoff     = 250 * time.Millisecond
	)

	var (
		buf   bytes.Buffer
		timer = time.NewTimer(flushInterval)
	)
	defer timer.Stop()

	flush := func() error {
		if buf.Len() == 0 {
			return nil
		}

		reqCtx, cancel := context.WithTimeout(c.ctx, requestTimeout)
		req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, c.pushURL, bytes.NewReader(buf.Bytes()))
		if err != nil {
			cancel()
			return err
		}
		req.Host = c.headerHost
		applyTunnelHeaders(req.Header, c.headerHost, TunnelModeStream)
		applyTunnelAuth(req, c.auth, TunnelModeStream, http.MethodPost, "/api/v1/upload")
		req.Header.Set("Content-Type", "application/octet-stream")

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
		return nil
	}

	flushWithRetry := func() error {
		return retryDial(c.closed, func() error { return io.ErrClosedPipe }, maxDialRetry, minBackoff, maxBackoff, flush)
	}
	resetTimer(timer, flushInterval)

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
			if buf.Len()+len(b) > maxBatchBytes {
				if err := flushWithRetry(); err != nil {
					_ = c.Close()
					return
				}
				resetTimer(timer, flushInterval)
			}
			_, _ = buf.Write(b)
			if buf.Len() >= maxBatchBytes {
				if err := flushWithRetry(); err != nil {
					_ = c.Close()
					return
				}
				resetTimer(timer, flushInterval)
			}
		case <-timer.C:
			if err := flushWithRetry(); err != nil {
				_ = c.Close()
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
					if buf.Len()+len(b) > maxBatchBytes {
						if err := flushWithRetry(); err != nil {
							_ = c.Close()
							return
						}
					}
					_, _ = buf.Write(b)
				default:
					_ = flushWithRetry()
					bestEffortCloseSession(c.client, c.finURL, c.headerHost, TunnelModeStream, c.auth)
					return
				}
			}
		case <-c.closed:
			_ = flushWithRetry()
			return
		}
	}
}
