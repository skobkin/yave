package ui

import (
	"sync/atomic"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"
)

func TestTooltipPlacement(t *testing.T) {
	bounds := fyne.NewSize(400, 300)
	bubble := fyne.NewSize(100, 30)
	anchorSize := fyne.NewSize(30, 30)
	tests := []struct {
		name   string
		anchor fyne.Position
		want   fyne.Position
	}{
		{"below, left aligned", fyne.NewPos(50, 50), fyne.NewPos(50, 50+30+tooltipGap)},
		{"flips above at the bottom", fyne.NewPos(50, 260), fyne.NewPos(50, 260-30-tooltipGap)},
		{"shifts left at the right edge", fyne.NewPos(390, 50), fyne.NewPos(400-100-tooltipMargin, 50+30+tooltipGap)},
		{"stays inside at the left edge", fyne.NewPos(-20, 50), fyne.NewPos(tooltipMargin, 50+30+tooltipGap)},
	}
	for _, tt := range tests {
		if got := tooltipPlacement(tt.anchor, anchorSize, bubble, bounds); got != tt.want {
			t.Errorf("%s: got %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestTooltipScheduleAndCancel(t *testing.T) {
	app := test.NewApp()
	defer app.Quit()
	l := newTooltipLayer()
	l.dispatch = func(fn func()) { fn() }
	win := test.NewWindow(l.wrap(newTipButton(l, nil, "tip", func() {})))
	defer win.Close()
	win.Resize(fyne.NewSize(300, 200))
	anchor := newTipButton(l, nil, "x", func() {})

	var asked atomic.Int32
	text := func() string { asked.Add(1); return "hello" }

	// A cancel before the delay elapses never evaluates the tip.
	l.schedule(anchor, text, 20*time.Millisecond)
	l.cancel()
	time.Sleep(60 * time.Millisecond)
	if asked.Load() != 0 {
		t.Fatal("cancelled tooltip still evaluated its text")
	}
	if !l.bubble.Hidden {
		t.Fatal("bubble visible after cancel")
	}

	// A newer schedule retires the older one.
	l.schedule(anchor, text, 20*time.Millisecond)
	l.schedule(anchor, func() string { return "" }, 20*time.Millisecond)
	time.Sleep(60 * time.Millisecond)
	if asked.Load() != 0 {
		t.Fatal("superseded tooltip still fired")
	}
}

// Regression: Container.Show does not repaint, so on an idle window the
// bubble stayed invisible until something unrelated redrew the canvas.
func TestTooltipShowRepaintsTheBubble(t *testing.T) {
	app := test.NewApp()
	defer app.Quit()
	l := newTooltipLayer()
	var repainted []fyne.CanvasObject
	l.repaint = func(o fyne.CanvasObject) { repainted = append(repainted, o) }
	anchor := newTipButton(l, nil, "Zoom in", func() {})
	win := test.NewWindow(l.wrap(anchor))
	defer win.Close()
	win.Resize(fyne.NewSize(300, 200))

	l.show(anchor, "Zoom in")
	if l.bubble.Hidden {
		t.Fatal("bubble must be shown")
	}
	if len(repainted) != 1 || repainted[0] != l.bubble {
		t.Fatalf("show must repaint the bubble, repainted %v", repainted)
	}
}

func TestTipButtonHoverSchedulesAndMouseOutHides(t *testing.T) {
	app := test.NewApp()
	defer app.Quit()
	l := newTooltipLayer()
	var scheduled atomic.Int32
	l.dispatch = func(fn func()) { scheduled.Add(1); fn() }
	b := newTipButton(l, nil, "Fit", func() {})
	b.MouseIn(nil)
	b.MouseOut()
	time.Sleep(tooltipDelay + 100*time.Millisecond)
	if scheduled.Load() != 0 {
		t.Fatal("MouseOut must retire the pending tooltip")
	}
	if !l.bubble.Hidden {
		t.Fatal("bubble must stay hidden")
	}
}

func TestTipButtonTapRunsActionAndCancelsTip(t *testing.T) {
	app := test.NewApp()
	defer app.Quit()
	l := newTooltipLayer()
	tapped := false
	b := newTipButton(l, nil, "Go", func() { tapped = true })
	seq := l.seq
	b.Tapped(&fyne.PointEvent{})
	if !tapped {
		t.Fatal("tap did not run the action")
	}
	if l.seq == seq {
		t.Fatal("tap must cancel the tooltip")
	}
}

func TestTipButtonDisabledDoesNotTap(t *testing.T) {
	app := test.NewApp()
	defer app.Quit()
	b := newTipButton(newTooltipLayer(), nil, "Go", func() { t.Error("disabled button tapped") })
	b.Disable()
	b.Tapped(&fyne.PointEvent{})
}
