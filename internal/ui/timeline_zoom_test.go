package ui

import (
	"context"
	"errors"
	"image"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/driver/desktop"
	fynetest "fyne.io/fyne/v2/test"

	"git.skobk.in/skobkin/yave/internal/frames"
	"git.skobk.in/skobkin/yave/internal/model"
)

// zoomedTimeline returns a 200px timeline over 100s showing [40s, 60s).
func zoomedTimeline(t *testing.T) *timeline {
	t.Helper()
	tl := newSizedTimeline(t)
	tl.SetDuration(100 * time.Second)
	tl.view = timelineView{duration: 100 * time.Second, start: 40 * time.Second, span: 20 * time.Second}
	return tl
}

func ctrlHeld() fyne.KeyModifier { return fyne.KeyModifierControl }

// near compares timestamps modulo the float32 pixel round-trip.
func near(a, b time.Duration) bool { return absDuration(a-b) <= time.Millisecond }

func TestTimelineCtrlWheelZoomsAroundCursor(t *testing.T) {
	tl := newSizedTimeline(t)
	tl.SetDuration(100 * time.Second)
	tl.modifiers = ctrlHeld
	views := 0
	tl.onView = func() { views++ }

	before := tl.timeAt(150)
	tl.Scrolled(&fyne.ScrollEvent{
		PointEvent: fyne.PointEvent{Position: fyne.NewPos(150, 10)},
		Scrolled:   fyne.Delta{DY: wheelUnitsPerNotch},
	})
	if !tl.View().zoomed() {
		t.Fatal("Ctrl+wheel up did not zoom in")
	}
	if views != 1 {
		t.Errorf("onView fired %d times, want 1", views)
	}
	if after := tl.timeAt(150); absDuration(after-before) > tl.View().perPixel(200)+time.Millisecond {
		t.Errorf("time under the cursor moved %s → %s", before, after)
	}

	tl.Scrolled(&fyne.ScrollEvent{
		PointEvent: fyne.PointEvent{Position: fyne.NewPos(150, 10)},
		Scrolled:   fyne.Delta{DY: -10 * wheelUnitsPerNotch},
	})
	if tl.View().zoomed() {
		t.Error("Ctrl+wheel down far enough must return to the fitted view")
	}
}

func TestTimelinePlainWheelScrollsOnlyWhenZoomed(t *testing.T) {
	tl := newSizedTimeline(t)
	tl.SetDuration(100 * time.Second)
	tl.modifiers = func() fyne.KeyModifier { return 0 }
	views := 0
	tl.onView = func() { views++ }

	tl.Scrolled(&fyne.ScrollEvent{Scrolled: fyne.Delta{DY: -wheelUnitsPerNotch}})
	if views != 0 || tl.View().zoomed() {
		t.Fatal("plain wheel must not change an unzoomed view")
	}

	tl = zoomedTimeline(t)
	tl.modifiers = func() fyne.KeyModifier { return 0 }
	start0, _ := tl.View().window()
	tl.Scrolled(&fyne.ScrollEvent{Scrolled: fyne.Delta{DY: -wheelUnitsPerNotch}})
	start1, end1 := tl.View().window()
	if start1 <= start0 {
		t.Errorf("wheel down should reveal later media: start %s → %s", start0, start1)
	}
	if end1-start1 != 20*time.Second {
		t.Errorf("scrolling changed the span to %s", end1-start1)
	}
	for range 200 {
		tl.Scrolled(&fyne.ScrollEvent{Scrolled: fyne.Delta{DY: -wheelUnitsPerNotch}})
	}
	if _, end := tl.View().window(); end != 100*time.Second {
		t.Errorf("scrolling must stop at the media end, window ends at %s", end)
	}
}

func TestTimelineDisabledIgnoresWheel(t *testing.T) {
	tl := newSizedTimeline(t)
	tl.SetDuration(100 * time.Second)
	tl.modifiers = ctrlHeld
	tl.Disable()
	tl.Scrolled(&fyne.ScrollEvent{Scrolled: fyne.Delta{DY: wheelUnitsPerNotch}})
	if tl.View().zoomed() {
		t.Fatal("a disabled timeline must ignore the wheel")
	}
}

func TestTimelineClickSeeksInTheVisibleWindow(t *testing.T) {
	tl := zoomedTimeline(t)
	var got time.Duration
	tl.onPlayhead = func(at time.Duration) { got = at }
	tl.MouseDown(&desktop.MouseEvent{
		PointEvent: fyne.PointEvent{Position: fyne.NewPos(100, 10)},
		Button:     desktop.MouseButtonPrimary,
	})
	if !near(got, 50*time.Second) {
		t.Fatalf("click at the middle of [40s,60s) seeks to %s, want 50s", got)
	}
}

func TestTimelineHandleDragAtZoom(t *testing.T) {
	tl := zoomedTimeline(t)
	tl.SetSegmentsSilent([]model.TimeRange{{Start: 45 * time.Second, End: 55 * time.Second}})
	var committed []model.TimeRange
	edited := -2
	tl.onSegments = func(segs []model.TimeRange, e int) { committed, edited = segs, e }

	x := tl.xOf(45 * time.Second) // 50px
	tl.MouseDown(&desktop.MouseEvent{
		PointEvent: fyne.PointEvent{Position: fyne.NewPos(x, 10)},
		Button:     desktop.MouseButtonPrimary,
	})
	tl.Dragged(&fyne.DragEvent{PointEvent: fyne.PointEvent{Position: fyne.NewPos(x+10, 10)}}) // +1s
	tl.MouseUp(&desktop.MouseEvent{Button: desktop.MouseButtonPrimary})

	if edited != 0 || len(committed) != 1 {
		t.Fatalf("commit = %v edited %d", committed, edited)
	}
	if got := committed[0].Start; !near(got, 46*time.Second) {
		t.Fatalf("In dragged 10px at 0.1s/px = %s, want 46s", got)
	}
	if !near(committed[0].End, 55*time.Second) {
		t.Errorf("the other boundary moved to %s", committed[0].End)
	}
}

func TestTimelineOffscreenHandlesAreNotHittable(t *testing.T) {
	tl := zoomedTimeline(t)
	tl.SetSegmentsSilent([]model.TimeRange{{Start: 10 * time.Second, End: 20 * time.Second}})
	for _, x := range []float32{0, 1, 5, 100, 199, 200} {
		if kind, _ := tl.pickTarget(x); kind != tlPlayhead {
			t.Errorf("x=%v grabbed %v although both handles are scrolled out of view", x, kind)
		}
	}
	// A handle just inside the window edge is grabbable.
	tl.SetSegmentsSilent([]model.TimeRange{{Start: 41 * time.Second, End: 90 * time.Second}})
	if kind, _ := tl.pickTarget(tl.xOf(41 * time.Second)); kind != tlIn {
		t.Errorf("visible handle not grabbable: %v", kind)
	}
}

func TestTimelineOverviewBarScrolls(t *testing.T) {
	tl := zoomedTimeline(t)
	h := tl.Size().Height
	played := false
	tl.onPlayhead = func(time.Duration) { played = true }

	tl.MouseDown(&desktop.MouseEvent{
		PointEvent: fyne.PointEvent{Position: fyne.NewPos(20, h-2)}, // 10% of the width, on the overview
		Button:     desktop.MouseButtonPrimary,
	})
	if played {
		t.Fatal("a press on the overview bar must not seek")
	}
	start, end := tl.View().window()
	if !near(start, 0) || !near(end, 20*time.Second) { // centered at 10s, clamped to the media start
		t.Fatalf("overview press at 10%% → window [%s, %s)", start, end)
	}
	tl.Dragged(&fyne.DragEvent{PointEvent: fyne.PointEvent{Position: fyne.NewPos(100, h-2)}})
	start, end = tl.View().window()
	if !near(start, 40*time.Second) || !near(end, 60*time.Second) {
		t.Fatalf("dragging the overview to 50%% → window [%s, %s), want [40s, 60s)", start, end)
	}
	tl.MouseUp(&desktop.MouseEvent{Button: desktop.MouseButtonPrimary})

	// Unzoomed, the same spot is an ordinary seek.
	tl.Fit()
	tl.MouseDown(&desktop.MouseEvent{
		PointEvent: fyne.PointEvent{Position: fyne.NewPos(20, h-2)},
		Button:     desktop.MouseButtonPrimary,
	})
	if !played {
		t.Error("with no zoom there is no overview: the press must seek")
	}
}

func TestTimelineViewResetRules(t *testing.T) {
	tl := zoomedTimeline(t)
	tl.SetDuration(100 * time.Second) // same media (Reset button)
	if !tl.View().zoomed() {
		t.Error("re-applying the same duration must keep the zoom")
	}
	tl.SetDuration(50 * time.Second) // a different media
	if tl.View().zoomed() {
		t.Error("a new duration must start unzoomed")
	}
}

func TestTimelineZoomToSelectionAndFit(t *testing.T) {
	tl := newSizedTimeline(t)
	tl.SetDuration(100 * time.Second)
	tl.SetSegmentsSilent([]model.TimeRange{{Start: 10 * time.Second, End: 20 * time.Second}})
	tl.ZoomToSelection()
	start, end := tl.View().window()
	if start != 9*time.Second || end != 21*time.Second {
		t.Fatalf("zoom to selection = [%s, %s), want [9s, 21s)", start, end)
	}
	tl.Fit()
	if tl.View().zoomed() {
		t.Error("Fit must show the whole media")
	}
	empty := newSizedTimeline(t)
	empty.SetDuration(10 * time.Second)
	empty.ZoomToSelection() // no segments: no-op, no panic
}

func TestTimelineThumbnailCellsFollowTheView(t *testing.T) {
	tl := newSizedTimeline(t) // 200px
	tl.SetDuration(100 * time.Second)
	tl.SetThumbnailGrid(0, 10*time.Second, 10)
	for i := range 10 {
		tl.SetThumbnail(i, image.NewRGBA(image.Rect(0, 0, 20, 10)))
	}
	tl.view = timelineView{duration: 100 * time.Second, start: 25 * time.Second, span: 30 * time.Second} // [25s, 55s)
	tl.Refresh()
	r := fynetest.TempWidgetRenderer(t, tl).(*timelineRenderer)
	r.Layout(tl.Size())

	for _, i := range []int{0, 1, 6, 9} {
		if !r.thumbs[i].Hidden {
			t.Errorf("cell %d lies outside [25s,55s) and must be hidden", i)
		}
	}
	// Cell 2 = [20s,30s): x0 = -33.3, x1 = 33.3 → the visible right half is
	// cropped to 25s..30s, i.e. the cell's right half.
	c2 := r.thumbs[2]
	if c2.Hidden || c2.Position().X != 0 || absFloat32(c2.Size().Width-33.3333) > 0.1 {
		t.Fatalf("partial cell geometry: hidden=%v pos=%v size=%v", c2.Hidden, c2.Position(), c2.Size())
	}
	if b := c2.Image.Bounds(); b.Dx() != 10 {
		t.Errorf("partial cell shows %d of 20 source columns, want the visible right half (10)", b.Dx())
	}
	// Cell 3 = [30s,40s) is fully visible: placed through the view, uncropped.
	c3 := r.thumbs[3]
	if c3.Hidden || absFloat32(c3.Position().X-33.3333) > 0.1 || absFloat32(c3.Size().Width-66.6667) > 0.1 {
		t.Fatalf("visible cell geometry: hidden=%v pos=%v size=%v", c3.Hidden, c3.Position(), c3.Size())
	}
	if c3.Image.Bounds().Dx() != 20 {
		t.Errorf("fully visible cell was cropped to %d columns", c3.Image.Bounds().Dx())
	}
	// Cell 5 = [50s,60s) straddles the right edge.
	if c5 := r.thumbs[5]; c5.Hidden || c5.Position().X+c5.Size().Width > 200.01 {
		t.Errorf("edge cell paints outside the widget: pos=%v size=%v", c5.Position(), c5.Size())
	}
}

func TestTimelineLegacyThumbnailsSpreadOverTheDuration(t *testing.T) {
	tl := newSizedTimeline(t)
	tl.SetDuration(10 * time.Second)
	tl.SetThumbnails(make([]image.Image, 4))
	r := fynetest.TempWidgetRenderer(t, tl).(*timelineRenderer)
	r.Layout(tl.Size())
	for i, obj := range r.thumbs {
		if obj.Hidden || obj.Position().X != float32(i)*50 || obj.Size().Width != 50 {
			t.Errorf("legacy cell %d: hidden=%v pos=%v size=%v", i, obj.Hidden, obj.Position(), obj.Size())
		}
	}
}

// --- controller: strip regeneration ---

type uiQueue struct{ ch chan func() }

func newUIQueue() *uiQueue { return &uiQueue{ch: make(chan func(), 256)} }

func (q *uiQueue) dispatch(f func()) { q.ch <- f }

// pump runs queued UI work until cond holds, failing after a timeout.
func (q *uiQueue) pump(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for !cond() {
		select {
		case f := <-q.ch:
			f()
		case <-deadline:
			t.Fatalf("timed out waiting for %s", what)
		}
	}
}

// stripController builds a trim controller whose thumbnail, preview and
// index fetches are fakes; fetch gets every thumbnail request.
func stripController(t *testing.T, fetch func(ctx context.Context, at time.Duration) (frames.Frame, error)) (*Controller, *uiQueue) {
	t.Helper()
	c := testTrimController(t)
	q := newUIQueue()
	c.uiDispatch = q.dispatch
	c.previews.thumbFetch = func(ctx context.Context, _ *frames.Extractor, _ string, at time.Duration, _ int) (frames.Frame, error) {
		return fetch(ctx, at)
	}
	c.previews.previewFetch = func(context.Context, *frames.Extractor, string, time.Duration, int) (frames.Frame, error) {
		return frames.Frame{}, errors.New("no preview in this test")
	}
	c.previews.indexFetch = func(context.Context, string, string, time.Duration, time.Duration) ([]time.Duration, time.Duration, error) {
		return nil, 0, errors.New("no index in this test")
	}
	c.media = model.MediaInfo{Duration: 10 * time.Minute}
	c.trimTimeline.SetDuration(c.media.Duration)
	c.trimTimeline.Resize(fyne.NewSize(1000, timelineMinHeight))
	c.trimEnabled = true
	return c, q
}

func okThumb(context.Context, time.Duration) (frames.Frame, error) {
	return frames.Frame{Image: image.NewRGBA(image.Rect(0, 0, 4, 4))}, nil
}

func TestStripFillsTheGridProgressively(t *testing.T) {
	c, q := stripController(t, okThumb)
	c.scheduleStrip()
	grid := c.previews.stripGrid
	if grid.n == 0 || grid.cell != time.Minute {
		t.Fatalf("grid for the whole 10min media = %+v, want 1-minute cells", grid)
	}
	q.pump(t, "all thumbnails", func() bool {
		for _, img := range c.trimTimeline.thumbs {
			if img == nil {
				return false
			}
		}
		return len(c.trimTimeline.thumbs) == grid.n && !c.previews.stripRunning
	})
}

func TestStripRegeneratesForTheZoomedWindowAndCancelsStaleWork(t *testing.T) {
	var (
		mu        sync.Mutex
		firstAt   = map[time.Duration]bool{}
		cancelled atomic.Int32
		gate      = make(chan struct{})
	)
	fetch := func(ctx context.Context, at time.Duration) (frames.Frame, error) {
		mu.Lock()
		firstAt[at] = true
		mu.Unlock()
		if at >= 5*time.Minute { // the whole-media strip's slow cells wait for the gate
			select {
			case <-gate:
			case <-ctx.Done():
				cancelled.Add(1)
				return frames.Frame{}, ctx.Err()
			}
		}
		return okThumb(ctx, at)
	}
	c, q := stripController(t, fetch)
	c.scheduleStrip()
	whole := c.previews.stripGrid

	// Zoom into a 20s window: the debounced callback rebuilds the strip.
	c.trimTimeline.SetView(timelineView{duration: c.media.Duration, start: 100 * time.Second, span: 20 * time.Second})
	q.pump(t, "the zoomed grid", func() bool { return c.previews.stripGrid != whole })
	zoomed := c.previews.stripGrid
	if zoomed.cell >= whole.cell {
		t.Fatalf("zoomed cell %s should be finer than %s", zoomed.cell, whole.cell)
	}
	if len(c.trimTimeline.thumbs) != zoomed.n || c.trimTimeline.thumbCell != zoomed.cell {
		t.Fatalf("timeline grid not updated: %d cells of %s", len(c.trimTimeline.thumbs), c.trimTimeline.thumbCell)
	}
	q.pump(t, "stale workers to be cancelled", func() bool { return cancelled.Load() > 0 })
	q.pump(t, "zoomed thumbnails", func() bool {
		for _, img := range c.trimTimeline.thumbs {
			if img == nil {
				return false
			}
		}
		return true
	})
	close(gate)
}

func TestStripReusesCachedThumbnailsWhenScrolling(t *testing.T) {
	var calls atomic.Int32
	c, q := stripController(t, func(ctx context.Context, at time.Duration) (frames.Frame, error) {
		calls.Add(1)
		return okThumb(ctx, at)
	})
	c.trimTimeline.SetView(timelineView{duration: c.media.Duration, start: 100 * time.Second, span: 20 * time.Second})
	c.scheduleStrip()
	first := c.previews.stripGrid
	q.pump(t, "first strip", func() bool { return !c.previews.stripRunning })
	base := calls.Load()
	if int(base) != first.n {
		t.Fatalf("first strip fetched %d of %d cells", base, first.n)
	}

	// Scroll by a few seconds: same step, mostly the same sample times.
	c.trimTimeline.SetView(timelineView{duration: c.media.Duration, start: 103 * time.Second, span: 20 * time.Second})
	c.scheduleStrip()
	second := c.previews.stripGrid
	if second.cell != first.cell || second == first {
		t.Fatalf("scroll should shift the grid on the same step: %+v → %+v", first, second)
	}
	q.pump(t, "second strip", func() bool { return !c.previews.stripRunning })
	if extra := int(calls.Load() - base); extra > 3 {
		t.Errorf("scrolling re-extracted %d thumbnails; the overlap must come from the cache", extra)
	}
	for i, img := range c.trimTimeline.thumbs {
		if img == nil {
			t.Errorf("cell %d empty after a cache-assisted scroll", i)
		}
	}
}

func TestStripSameGridIsANoop(t *testing.T) {
	var calls atomic.Int32
	c, q := stripController(t, func(ctx context.Context, at time.Duration) (frames.Frame, error) {
		calls.Add(1)
		return okThumb(ctx, at)
	})
	c.scheduleStrip()
	q.pump(t, "strip", func() bool { return !c.previews.stripRunning })
	n := calls.Load()
	c.scheduleStrip() // e.g. trimming re-enabled
	c.scheduleStrip()
	if calls.Load() != n || c.previews.stripRunning {
		t.Error("an unchanged grid must not restart the strip")
	}
}

func TestNewInputResetsZoomStripAndThumbnailCache(t *testing.T) {
	c, q := stripController(t, okThumb)
	c.trimTimeline.SetView(timelineView{duration: c.media.Duration, start: 100 * time.Second, span: 20 * time.Second})
	c.scheduleStrip()
	q.pump(t, "strip", func() bool { return !c.previews.stripRunning })
	if len(c.previews.thumbCache) == 0 {
		t.Fatal("expected cached thumbnails")
	}

	c.resetTrimForNewInput()
	if c.trimTimeline.View().zoomed() {
		t.Error("zoom must reset with the input")
	}
	if c.previews.stripGrid != (stripGrid{}) || len(c.previews.thumbCache) != 0 {
		t.Error("strip grid and thumbnail cache must reset with the input")
	}
}

func TestZoomButtonsFollowTrimEnableAndDriveTheTimeline(t *testing.T) {
	c, _ := stripController(t, okThumb)
	c.trimEnabled = false
	c.setTrimControlsEnabled(false)
	for _, b := range []*tipButton{c.trimZoomIn, c.trimZoomOut, c.trimZoomFit, c.trimZoomSel} {
		if !b.Disabled() {
			t.Fatal("zoom buttons must be disabled while trimming is off")
		}
	}
	c.setTrimControlsEnabled(true)
	c.trimTimeline.SetSegmentsSilent([]model.TimeRange{{Start: time.Minute, End: 2 * time.Minute}})

	c.trimZoomIn.OnTapped()
	if !c.trimTimeline.View().zoomed() {
		t.Fatal("zoom-in button did not zoom")
	}
	c.trimZoomOut.OnTapped()
	if c.trimTimeline.View().zoomed() {
		t.Error("zoom-out after zoom-in must return to the fitted view")
	}
	c.trimZoomSel.OnTapped()
	if start, end := c.trimTimeline.View().window(); start != 54*time.Second || end != 126*time.Second {
		t.Errorf("zoom to selection = [%s, %s), want [54s, 2m6s)", start, end)
	}
	c.trimZoomFit.OnTapped()
	if c.trimTimeline.View().zoomed() {
		t.Error("fit button did not fit")
	}
}

func TestTimelinePlayheadStaysInView(t *testing.T) {
	tl := zoomedTimeline(t) // [40s, 60s)
	views := 0
	tl.onView = func() { views++ }

	tl.SetPlayhead(50 * time.Second)
	if views != 0 {
		t.Error("a playhead inside the window must not scroll it")
	}
	tl.SetPlayhead(80 * time.Second)
	start, end := tl.View().window()
	if !near(start, 70*time.Second) || !near(end, 90*time.Second) || views != 1 {
		t.Errorf("playhead at 80s → window [%s, %s) after %d view change(s), want centered [70s, 90s)", start, end, views)
	}
	tl.SetPlayhead(100 * time.Second)
	if _, end := tl.View().window(); end != 100*time.Second {
		t.Errorf("the window must stay inside the media, ends at %s", end)
	}
	tl.SetPlayhead(0)
	if start, _ := tl.View().window(); start != 0 {
		t.Errorf("playhead at 0 → window starts at %s", start)
	}

	fitted := newSizedTimeline(t)
	fitted.SetDuration(100 * time.Second)
	fitted.onView = func() { t.Error("an unzoomed view has nothing to reveal") }
	fitted.SetPlayhead(70 * time.Second)
}
