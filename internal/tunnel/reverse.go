package tunnel

// ReverseMagicByte marks a Sudoku tunnel connection that is used for reverse proxy registration.
//
// Wire format (inside the already-upgraded Sudoku tunnel stream):
//   - [ReverseMagicByte][reverseVersion]
//   - [uint32 jsonLen][jsonPayload]
//
// After successful registration, the server writes a mux preface and starts a mux session where the server
// opens streams and the client dials local targets.
const (
	ReverseMagicByte byte = 0xEC
	ReverseVersion   byte = 0x01
)
