package httpmask

import (
	"errors"
	"io"
	"net"
	"net/url"
	"time"
)

func isDialError(err error) bool {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return isDialError(urlErr.Err)
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		if opErr.Op == "dial" || opErr.Op == "connect" {
			return true
		}
	}
	return false
}

func resetTimer(t *time.Timer, d time.Duration) {
	if t == nil {
		return
	}
	if !t.Stop() {
		select {
		case <-t.C:
		default:
		}
	}
	t.Reset(d)
}

func retryDial(closed <-chan struct{}, closedErr func() error, maxRetry int, minBackoff, maxBackoff time.Duration, fn func() error) error {
	backoff := minBackoff
	for tries := 0; ; tries++ {
		if err := fn(); err == nil {
			return nil
		} else if isDialError(err) && tries < maxRetry {
			select {
			case <-time.After(backoff):
			case <-closed:
				if closedErr != nil {
					return closedErr()
				}
				return io.ErrClosedPipe
			}
			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
			continue
		} else {
			return err
		}
	}
}
