package ui

import "testing"

func TestLastLinePosition(t *testing.T) {
	t.Parallel()
	tests := []struct {
		text    string
		row, co int
	}{
		{"", 0, 0},
		{"one", 0, 3},
		{"one\n", 0, 3},
		{"one\ntwo\n", 1, 3},
		{"one\n\nlast é", 2, 6},
	}
	for _, tt := range tests {
		if row, col := lastLinePosition(tt.text); row != tt.row || col != tt.co {
			t.Errorf("lastLinePosition(%q) = %d,%d want %d,%d", tt.text, row, col, tt.row, tt.co)
		}
	}
}
