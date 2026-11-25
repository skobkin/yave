package ui

import (
	"testing"

	"fyne.io/fyne/v2"
	fynetest "fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"
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
	c.runView.set(runFigures{Fraction: 0.24, Percent: "24%", Time: runTimeSample, Speed: runSpeedSample, Size: runSizeSample})
	bar.Refresh()
	if got, need := c.status.Size().Width, textWidth(c.status); got < need {
		t.Fatalf("phase width %v < text width %v", got, need)
	}
	statusEnd := c.status.Position().X + c.status.Size().Width
	view := c.runView.box
	if view.Position().X < statusEnd || c.runView.bar.Size().Width < 300 {
		t.Fatalf("progress view at %v (bar width %v) must follow the phase (ends %v) and leave the bar room",
			view.Position().X, c.runView.bar.Size().Width, statusEnd)
	}
	for _, value := range []*widget.Label{c.runView.percent, c.runView.time, c.runView.speed, c.runView.size} {
		// textWidth adds a 1 px rounding margin a label's MinSize lacks.
		if value.Size().Width < textWidth(value)-1 {
			t.Errorf("cell %v narrower than %q", value.Size().Width, value.Text)
		}
	}
}

func TestGrowCellWidensButNeverShrinks(t *testing.T) {
	app := fynetest.NewApp()
	defer app.Quit()
	label := widget.NewLabel("0:01")
	g := &growCell{widest: 50}
	if w := g.MinSize([]fyne.CanvasObject{label}).Width; w != 50 {
		t.Fatalf("short text: width %v, want the sample's 50", w)
	}
	label.SetText(runTimeSample + runTimeSample)
	wide := g.MinSize([]fyne.CanvasObject{label}).Width
	if wide <= 50 {
		t.Fatalf("long text must widen the cell, got %v", wide)
	}
	label.SetText("0:02")
	if w := g.MinSize([]fyne.CanvasObject{label}).Width; w != wide {
		t.Fatalf("the cell shrank back to %v from %v", w, wide)
	}
}
