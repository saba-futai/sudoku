package sudoku

import "sync"

var allGrids = sync.OnceValue(GenerateAllGrids)
