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

	tableSet, err := sudoku.NewTableSet(cfg.Key, cfg.ASCII, patterns)
	if err != nil {
		return nil, err
	}
	return tableSet.Candidates(), nil
}
