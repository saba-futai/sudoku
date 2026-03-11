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
package apis

import (
	"testing"

	"github.com/saba-futai/sudoku/pkg/obfs/sudoku"
)

func TestValidateClient(t *testing.T) {
	cfg := &ProtocolConfig{
		Table:              sudoku.NewTable("seed", "prefer_ascii"),
		Key:                "k",
		AEADMethod:         "chacha20-poly1305",
		PaddingMin:         5,
		PaddingMax:         10,
		EnablePureDownlink: true,
		ServerAddress:      "1.1.1.1:443",
		TargetAddress:      "example.com:80",
	}
	if err := cfg.ValidateClient(); err != nil {
		t.Fatalf("ValidateClient unexpected error: %v", err)
	}

	cfg.PaddingMax = 1
	cfg.PaddingMin = 2
	if err := cfg.Validate(); err == nil {
		t.Fatalf("expected padding validation error")
	}

	cfg.PaddingMin = 0
	cfg.PaddingMax = 0
	cfg.AEADMethod = "bad"
	if err := cfg.Validate(); err == nil {
		t.Fatalf("expected invalid AEAD error")
	}

	cfg.AEADMethod = "none"
	cfg.Table = nil
	if err := cfg.Validate(); err == nil {
		t.Fatalf("expected table nil error")
	}

	cfg.Table = sudoku.NewTable("seed", "prefer_ascii")
	cfg.ServerAddress = ""
	if err := cfg.ValidateClient(); err == nil {
		t.Fatalf("expected server address error")
	}
	cfg.ServerAddress = "1.1.1.1:443"
	cfg.TargetAddress = ""
	if err := cfg.ValidateClient(); err == nil {
		t.Fatalf("expected target address error")
	}

	cfg.TargetAddress = "example.com:80"
	cfg.EnablePureDownlink = false
	cfg.AEADMethod = "none"
	if err := cfg.Validate(); err == nil {
		t.Fatalf("expected downlink AEAD validation error")
	}
}

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.AEADMethod == "" || cfg.PaddingMin == 0 || cfg.PaddingMax == 0 || !cfg.EnablePureDownlink {
		t.Fatalf("defaults not set")
	}
}
