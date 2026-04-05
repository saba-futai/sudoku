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
package sudoku

const probOne = uint64(1) << 32

func pickPaddingThreshold(r randomSource, pMin, pMax int) uint64 {
	if r == nil {
		return 0
	}
	if pMin < 0 {
		pMin = 0
	}
	if pMax < pMin {
		pMax = pMin
	}
	if pMax > 100 {
		pMax = 100
	}
	if pMin > 100 {
		pMin = 100
	}

	min := uint64(pMin) * probOne / 100
	max := uint64(pMax) * probOne / 100
	if max <= min {
		return min
	}
	u := uint64(r.Uint32())
	return min + (u * (max - min) >> 32)
}

func shouldPad(r randomSource, threshold uint64) bool {
	if threshold == 0 {
		return false
	}
	if threshold >= probOne {
		return true
	}
	if r == nil {
		return false
	}
	return uint64(r.Uint32()) < threshold
}
