package connutil

// CloseReader is implemented by conns that support half-close on the read side.
type CloseReader interface {
	CloseRead() error
}

// CloseWriter is implemented by conns that support half-close on the write side.
type CloseWriter interface {
	CloseWrite() error
}

// TryCloseRead calls CloseRead when supported.
func TryCloseRead(target any) error {
	if target == nil {
		return nil
	}
	if cr, ok := target.(CloseReader); ok {
		return cr.CloseRead()
	}
	return nil
}

// TryCloseWrite calls CloseWrite when supported.
func TryCloseWrite(target any) error {
	if target == nil {
		return nil
	}
	if cw, ok := target.(CloseWriter); ok {
		return cw.CloseWrite()
	}
	return nil
}

// RunClosers executes closers in order and returns the first error.
func RunClosers(closers ...func() error) error {
	var firstErr error
	for _, fn := range closers {
		if fn == nil {
			continue
		}
		if err := fn(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func AbsInt64(x int64) int64 {
	if x < 0 {
		return -x
	}
	return x
}
