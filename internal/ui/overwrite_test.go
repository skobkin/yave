package ui

import (
	"path/filepath"
	"testing"

	"git.skobk.in/skobkin/yave/internal/templates"
)

func TestFreeOutputBase(t *testing.T) {
	single := func(base string) []string { return []string{base} }
	parts := func(base string) []string {
		return []string{templates.SegmentPath(base, 1, 2), templates.SegmentPath(base, 2, 2)}
	}
	tests := []struct {
		name     string
		existing []string
		targets  func(string) []string
		want     string
	}{
		{"first free", nil, single, "out_1.mkv"},
		{"skips taken", []string{"out_1.mkv", "out_2.mkv"}, single, "out_3.mkv"},
		{"fills gap", []string{"out_1.mkv", "out_3.mkv"}, single, "out_2.mkv"},
		{"multi-part renames as a unit", []string{"out_1_part02.mkv"}, parts, "out_2.mkv"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			for _, name := range tt.existing {
				writeFile(t, filepath.Join(dir, name), "x")
			}
			got, ok := freeOutputBase(filepath.Join(dir, "out.mkv"), filepath.Join(dir, "in.mkv"), tt.targets)
			if !ok || got != filepath.Join(dir, tt.want) {
				t.Fatalf("got %q ok=%v, want %q", got, ok, tt.want)
			}
		})
	}
}

func TestFreeOutputBaseNeverPicksTheInput(t *testing.T) {
	dir := t.TempDir()
	input := filepath.Join(dir, "out_1.mkv") // the input itself looks like the first free name
	got, ok := freeOutputBase(filepath.Join(dir, "out.mkv"), input, func(b string) []string { return []string{b} })
	if !ok || got != filepath.Join(dir, "out_2.mkv") {
		t.Fatalf("got %q", got)
	}
}
