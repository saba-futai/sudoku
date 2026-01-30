package sudoku

const hintPositionsCount = 1820 // 16 choose 4

var hintPositions = buildHintPositions()

type hintPart struct {
	val byte
	pos byte
}

func buildHintPositions() [][4]byte {
	positions := make([][4]byte, 0, hintPositionsCount)
	for a := 0; a < 13; a++ {
		for b := a + 1; b < 14; b++ {
			for c := b + 1; c < 15; c++ {
				for d := c + 1; d < 16; d++ {
					positions = append(positions, [4]byte{byte(a), byte(b), byte(c), byte(d)})
				}
			}
		}
	}
	return positions
}

func hasUniqueMatch(grids []Grid, parts [4]hintPart) bool {
	matchCount := 0
	for _, g := range grids {
		match := true
		for _, p := range parts {
			if g[p.pos] != p.val {
				match = false
				break
			}
		}
		if match {
			matchCount++
			if matchCount > 1 {
				return false
			}
		}
	}
	return matchCount == 1
}
