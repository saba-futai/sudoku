package sudoku

// drainPending copies buffered decoded bytes into p.
// It returns (n, true) when pending data existed, otherwise (0, false).
func drainPending(p []byte, pending *[]byte) (int, bool) {
	if pending == nil || len(*pending) == 0 {
		return 0, false
	}
	n := copy(p, *pending)
	if n == len(*pending) {
		*pending = (*pending)[:0]
		return n, true
	}
	remaining := len(*pending) - n
	copy(*pending, (*pending)[n:])
	*pending = (*pending)[:remaining]
	return n, true
}
