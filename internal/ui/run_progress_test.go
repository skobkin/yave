package ui

import (
	"math"
	"testing"
	"time"

	"git.skobk.in/skobkin/yave/internal/model"
)

func TestRunProgressText(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		p       model.Progress
		elapsed time.Duration
		known   bool
		want    string
	}{
		{
			"known duration: elapsed / estimated total, speed, size",
			model.Progress{Percentage: 0.25, Speed: "2.86x", Bitrate: "3500.0kbits/s", TotalSize: 31 << 20},
			time.Minute, true, "1:00 / ~4:00 · 2.9× · 31.0 MB",
		},
		{
			"too early for an estimate", model.Progress{Percentage: 0.001, Speed: "N/A", Bitrate: "N/A"},
			3 * time.Second, true, "0:03",
		},
		{
			"unknown duration shows the encoded position", model.Progress{OutTime: 80 * time.Second, Speed: "1.5x"},
			10 * time.Second, false, "0:10 · 1:20 encoded · 1.5×",
		},
		{
			"complete has no estimate", model.Progress{Percentage: 1},
			time.Hour + time.Minute, true, "1:01:00",
		},
		{
			"unparsable speed is shown as reported", model.Progress{Speed: "fast"},
			time.Second, true, "0:01 · fast",
		},
	}
	for _, tt := range tests {
		if got := runProgressText(tt.p, tt.elapsed, tt.known); got != tt.want {
			t.Errorf("%s:\n got %q\nwant %q", tt.name, got, tt.want)
		}
	}
}

func TestRunPhaseText(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		label  string
		stages int
		want   string
	}{
		{"conversion", 1, "Converting"},
		{"segment 2 of 3", 3, "Segment 2 of 3"},
		{"joining segments", 4, "Joining segments"},
		{"", 2, "Converting"},
	} {
		if got := runPhaseText(tt.label, tt.stages); got != tt.want {
			t.Errorf("runPhaseText(%q, %d) = %q, want %q", tt.label, tt.stages, got, tt.want)
		}
	}
}

func TestProgressHelpersNeverProduceNonsense(t *testing.T) {
	t.Parallel()
	for _, f := range []float64{math.NaN(), -1, 2, math.Inf(1)} {
		if got := percentValue(f); got < 0 || got > 100 {
			t.Errorf("percentValue(%v) = %d", f, got)
		}
		if _, ok := estimateRemaining(f, time.Minute); ok && (f != f || f < 0.01 || f >= 1) {
			t.Errorf("estimateRemaining(%v) must be unknown", f)
		}
	}
	if got := formatClock(-time.Second); got != "0s" {
		t.Errorf("negative clock = %q", got)
	}
}

func TestProgressTitle(t *testing.T) {
	t.Parallel()
	if got := progressTitle(42, "movie.mkv", "YAVE"); got != "42% - movie.mkv - YAVE" {
		t.Fatalf("title = %q", got)
	}
	if got := progressTitle(5, "", "YAVE"); got != "5% - YAVE" {
		t.Fatalf("title = %q", got)
	}
}

func TestFormatRunSummary(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		outputs []string
		sizes   []int64
		want    string
	}{
		{"single", []string{"/o/name.mkv"}, []int64{412 << 20}, "Completed in 3m12s → name.mkv (412.0 MB)"},
		{"single unknown size", []string{"/o/name.mkv"}, []int64{0}, "Completed in 3m12s → name.mkv"},
		{"multi", []string{"/o/a_part1.mkv", "/o/a_part2.mkv"}, []int64{1 << 20, 1 << 20}, "Completed in 3m12s → 2 files (2.0 MB)"},
		{"none", nil, nil, "Completed in 3m12s"},
	}
	for _, tt := range tests {
		if got := formatRunSummary(192*time.Second, tt.outputs, tt.sizes); got != tt.want {
			t.Errorf("%s: %q, want %q", tt.name, got, tt.want)
		}
	}
	if got := formatByteSize(512); got != "512 B" {
		t.Errorf("512 B = %q", got)
	}
}

func TestRunActionsFollowRunState(t *testing.T) {
	c := dirtyTestController(t)
	c.showRunActions([]string{"/o/a.mkv"})
	if !c.openFolder.Visible() || !c.playOutput.Visible() || c.runResult.Visible() {
		t.Fatal("single output: buttons shown, no file list")
	}
	c.showRunActions([]string{"/o/a1.mkv", "/o/a2.mkv"})
	if !c.runResult.Visible() || c.runResult.Text != "a1.mkv\na2.mkv" {
		t.Fatalf("multi-part list = %q (visible %v)", c.runResult.Text, c.runResult.Visible())
	}
	c.setInput("/tmp/new-input.mkv")
	if c.openFolder.Visible() || c.status.Text != "Idle" {
		t.Fatal("a new input must reset the finished run's status and actions")
	}
}
