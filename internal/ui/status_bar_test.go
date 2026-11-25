package ui

import (
	"testing"

	"fyne.io/fyne/v2"
	fynetest "fyne.io/fyne/v2/test"
)

// Regression: the status and Start-hint labels truncate, and a truncating
// label's MinSize is a lone "…" — in the old box layout "Running" and the
// finished-run summary both showed as "…".
func TestStatusBarGivesLabelsTheirTextWidth(t *testing.T) {
	app := fynetest.NewApp()
	defer app.Quit()
	c := testController()
	c.createWidgets()
	bar := c.buildStatusBar()

	c.status.SetText("Completed in 5s → replay_libx265.mkv (31.0 MB)")
	c.startHint.SetText("Select an input file")
	bar.Resize(fyne.NewSize(1050, bar.MinSize().Height))
	if got, need := c.status.Size().Width, textWidth(c.status); got < need {
		t.Fatalf("idle status width %v < text width %v: it would show an ellipsis", got, need)
	}
	if got, need := c.startHint.Size().Width, textWidth(c.startHint); got < need {
		t.Fatalf("hint width %v < text width %v", got, need)
	}
	if c.status.Position().X+c.status.Size().Width > c.startHint.Position().X {
		t.Fatal("the status must end before the hint starts")
	}

	// Running: phase at its own width, then the bar, then the figures.
	c.setProgressVisible(true)
	setShown(c.startHint, false)
	setShown(c.start, false)
	c.status.SetText("Segment 2 of 3")
	c.progressInfo.SetText(progressInfoWidthSample)
	bar.Refresh()
	if got, need := c.status.Size().Width, textWidth(c.status); got < need {
		t.Fatalf("phase width %v < text width %v", got, need)
	}
	statusEnd := c.status.Position().X + c.status.Size().Width
	if c.progress.Position().X < statusEnd || c.progress.Size().Width < 300 {
		t.Fatalf("bar at %v width %v must follow the phase (ends %v) and fill the row",
			c.progress.Position().X, c.progress.Size().Width, statusEnd)
	}
	// textWidth adds a 1 px rounding margin the cell (a label's MinSize) lacks.
	if c.progressInfo.Size().Width < textWidth(c.progressInfo)-1 {
		t.Fatalf("figures cell %v narrower than %q", c.progressInfo.Size().Width, c.progressInfo.Text)
	}
}
