/*
Copyright (C) 2026 by saba <contact me via issue>

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU General Public License as published by
the Free Software Foundation, either version 3 of the License, or
(at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
GNU General Public License for more details.

You should have received a copy of the GNU General Public License
along with this program. If not, see <http://www.gnu.org/licenses/>.

In addition, no derivative work may use the name or imply association
with this application without prior consent.
*/
package tunnel

import (
	"bufio"
	"errors"
	"fmt"
	"io"

	"github.com/SUDOKU-ASCII/sudoku/pkg/obfs/sudoku"
)

// TableProbeFunc returns nil when the current probe bytes are sufficient to identify table as a match.
// It should return io.EOF/io.ErrUnexpectedEOF when more bytes are needed, and any other error on mismatch.
type TableProbeFunc func(probe []byte, table *sudoku.Table) error

type HandshakeObfsProbeFunc func(probe []byte, table *sudoku.Table, uplinkMode ObfsUplinkMode) error

type HandshakeObfsSelection struct {
	Table      *sudoku.Table
	UplinkMode ObfsUplinkMode
}

type handshakeObfsCandidate struct {
	table      *sudoku.Table
	uplinkMode ObfsUplinkMode
}

func drainBuffered(r *bufio.Reader) ([]byte, error) {
	n := r.Buffered()
	if n <= 0 {
		return nil, nil
	}
	out := make([]byte, n)
	_, err := io.ReadFull(r, out)
	return out, err
}

func isProbeNeedMore(err error) bool {
	return errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)
}

// SelectTableByProbe detects which Sudoku table the client used by reading incremental bytes and
// calling probe(probeBytes, table) for each remaining candidate.
//
// It returns the selected table and all bytes consumed from r (including any buffered bytes),
// so the caller can replay them into the next layer without losing data.
func SelectTableByProbe(r *bufio.Reader, tables []*sudoku.Table, probe TableProbeFunc) (*sudoku.Table, []byte, error) {
	if probe == nil {
		return nil, nil, fmt.Errorf("nil probe func")
	}
	if len(tables) > 255 {
		return nil, nil, fmt.Errorf("too many table candidates: %d", len(tables))
	}

	candidates := make([]*sudoku.Table, 0, len(tables))
	for _, table := range tables {
		if table != nil {
			candidates = append(candidates, table)
		}
	}
	if len(candidates) == 0 {
		return nil, nil, fmt.Errorf("no table candidates")
	}

	return selectProbeCandidates(r, candidates, func(probeBytes []byte, table *sudoku.Table) error {
		return probe(probeBytes, table)
	})
}

// SelectHandshakeObfsByProbe detects the Sudoku table and uplink mode used by the client handshake.
// The caller provides a probe that must return nil on a match, io.EOF/io.ErrUnexpectedEOF when more
// bytes are needed, and any other error on mismatch.
func SelectHandshakeObfsByProbe(r *bufio.Reader, tables []*sudoku.Table, probe HandshakeObfsProbeFunc) (*HandshakeObfsSelection, []byte, error) {
	if probe == nil {
		return nil, nil, fmt.Errorf("nil probe func")
	}

	expanded := make([]handshakeObfsCandidate, 0, len(tables)*2)
	for _, table := range tables {
		if table == nil {
			continue
		}
		expanded = append(expanded,
			handshakeObfsCandidate{table: table, uplinkMode: ObfsUplinkPure},
			handshakeObfsCandidate{table: table, uplinkMode: ObfsUplinkPacked},
		)
	}
	if len(expanded) == 0 {
		return nil, nil, fmt.Errorf("no table candidates")
	}

	selected, probeBytes, err := selectProbeCandidates(r, expanded, func(probeBytes []byte, candidate handshakeObfsCandidate) error {
		return probe(probeBytes, candidate.table, candidate.uplinkMode)
	})
	if err != nil {
		return nil, probeBytes, err
	}
	return &HandshakeObfsSelection{
		Table:      selected.table,
		UplinkMode: selected.uplinkMode,
	}, probeBytes, nil
}

func selectProbeCandidates[T any](r *bufio.Reader, candidates []T, probe func([]byte, T) error) (T, []byte, error) {
	var zero T

	const (
		maxProbeBytes = 64 * 1024
		readChunk     = 4 * 1024
	)
	if r == nil {
		return zero, nil, fmt.Errorf("nil reader")
	}
	if len(candidates) == 0 {
		return zero, nil, fmt.Errorf("no table candidates")
	}

	probeBytes, err := drainBuffered(r)
	if err != nil {
		return zero, nil, fmt.Errorf("drain buffered bytes failed: %w", err)
	}

	tmp := make([]byte, readChunk)
	for {
		if len(candidates) == 1 {
			tail, err := drainBuffered(r)
			if err != nil {
				return zero, nil, fmt.Errorf("drain buffered bytes failed: %w", err)
			}
			probeBytes = append(probeBytes, tail...)
			return candidates[0], probeBytes, nil
		}

		needMore := false
		nextCandidates := candidates[:0]
		for _, candidate := range candidates {
			err := probe(probeBytes, candidate)
			if err == nil {
				tail, err := drainBuffered(r)
				if err != nil {
					return zero, nil, fmt.Errorf("drain buffered bytes failed: %w", err)
				}
				probeBytes = append(probeBytes, tail...)
				return candidate, probeBytes, nil
			}
			if isProbeNeedMore(err) {
				needMore = true
				nextCandidates = append(nextCandidates, candidate)
			}
		}
		candidates = nextCandidates

		if len(candidates) == 0 || !needMore {
			return zero, probeBytes, fmt.Errorf("handshake table selection failed")
		}
		if len(probeBytes) >= maxProbeBytes {
			return zero, probeBytes, fmt.Errorf("handshake probe exceeded %d bytes", maxProbeBytes)
		}

		n, err := r.Read(tmp)
		if n > 0 {
			probeBytes = append(probeBytes, tmp[:n]...)
		}
		if err != nil {
			return zero, probeBytes, fmt.Errorf("handshake probe read failed: %w", err)
		}
	}
}
