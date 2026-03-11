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
package app

import (
	"strings"

	"github.com/saba-futai/sudoku/internal/config"
	"github.com/saba-futai/sudoku/pkg/obfs/sudoku"
)

func BuildTables(cfg *config.Config) ([]*sudoku.Table, error) {
	patterns := cfg.CustomTables
	if len(patterns) == 0 && strings.TrimSpace(cfg.CustomTable) != "" {
		patterns = []string{cfg.CustomTable}
	}
	if len(patterns) == 0 {
		patterns = []string{""}
	}
	// Server-side convenience: when custom tables rotation is enabled, also accept the default table.
	// This avoids forcing clients to configure a custom layout in lockstep while keeping rotation available.
	if cfg != nil && cfg.Mode == "server" && len(patterns) > 0 && strings.TrimSpace(patterns[0]) != "" {
		patterns = append([]string{""}, patterns...)
	}

	tableSet, err := sudoku.NewTableSet(cfg.Key, cfg.ASCII, patterns)
	if err != nil {
		return nil, err
	}
	return tableSet.Candidates(), nil
}
