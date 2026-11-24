package ui

import (
	"testing"
	"time"
)

func TestTimelineViewMapping(t *testing.T) {
	v := timelineView{duration: 100 * time.Second, start: 20 * time.Second, span: 40 * time.Second}
	const width = 400
	tests := []struct {
		name string
		at   time.Duration
		want float32
	}{
		{"window start", 20 * time.Second, 0},
		{"window middle", 40 * time.Second, 200},
		{"window end", 60 * time.Second, 400},
		{"before window is negative", 10 * time.Second, -100},
		{"after window overflows", 70 * time.Second, 500},
	}
	for _, tt := range tests {
		if got := v.timeToX(tt.at, width); got != tt.want {
			t.Errorf("%s: timeToX(%s) = %v, want %v", tt.name, tt.at, got, tt.want)
		}
	}
	for _, tt := range []struct {
		x    float32
		want time.Duration
	}{
		{0, 20 * time.Second},
		{200, 40 * time.Second},
		{400, 60 * time.Second},
		{-30, 20 * time.Second},
		{900, 60 * time.Second},
	} {
		if got := v.xToTime(tt.x, width); got != tt.want {
			t.Errorf("xToTime(%v) = %s, want %s", tt.x, got, tt.want)
		}
	}
}

func TestTimelineViewZeroValueIsUnzoomed(t *testing.T) {
	v := newTimelineView(10 * time.Second)
	if v.zoomed() {
		t.Fatal("new view reports zoomed")
	}
	if got := v.xToTime(100, 200); got != 5*time.Second {
		t.Errorf("xToTime(100/200) = %s, want 5s", got)
	}
	if got := (timelineView{}).xToTime(5, 10); got != 0 {
		t.Errorf("zero view maps to %s, want 0", got)
	}
}

func TestTimelineViewZoomAroundKeepsAnchorStable(t *testing.T) {
	const width = 400
	tests := []struct {
		name    string
		view    timelineView
		anchorX float32
		factor  float64
	}{
		{"zoom in at center", newTimelineView(100 * time.Second), 200, 2},
		{"zoom in near left edge", newTimelineView(100 * time.Second), 20, 4},
		{"zoom in near right edge", newTimelineView(100 * time.Second), 390, 4},
		{"zoom again while zoomed", timelineView{duration: 100 * time.Second, start: 30 * time.Second, span: 20 * time.Second}, 100, 2},
		{"zoom out while zoomed", timelineView{duration: 100 * time.Second, start: 30 * time.Second, span: 20 * time.Second}, 300, 0.5},
	}
	for _, tt := range tests {
		before := tt.view.xToTime(tt.anchorX, width)
		got := tt.view.zoomAround(tt.anchorX, width, tt.factor)
		after := got.xToTime(tt.anchorX, width)
		// Clamping at the media edges may legitimately shift the anchor;
		// otherwise it must stay within a pixel's worth of time.
		start, end := got.window()
		atEdge := start == 0 || end == got.duration
		tolerance := got.perPixel(width) + time.Millisecond
		if !atEdge && absDuration(after-before) > tolerance {
			t.Errorf("%s: anchor moved %s → %s", tt.name, before, after)
		}
	}
}

func TestTimelineViewZoomClamps(t *testing.T) {
	const width = 400
	v := newTimelineView(100 * time.Second)

	out := v.zoomAround(200, width, 0.1)
	if out.zoomed() {
		t.Errorf("zooming out past the whole media should stay fitted, got span %s", out.Span())
	}
	in := v
	for range 40 {
		in = in.zoomAround(200, width, 2)
	}
	if in.Span() != timelineMinSpan {
		t.Errorf("max zoom span = %s, want %s", in.Span(), timelineMinSpan)
	}
	short := newTimelineView(300*time.Millisecond).zoomAround(100, width, 8)
	if short.zoomed() || short.Span() != 300*time.Millisecond {
		t.Errorf("media shorter than the floor must stay whole, span = %s", short.Span())
	}
	if got := newTimelineView(0).zoomAround(10, width, 2); got.zoomed() || got.Span() != 0 {
		t.Errorf("zero duration zoom = %+v", got)
	}
}

func TestTimelineViewScrollLimits(t *testing.T) {
	const width = 400
	v := timelineView{duration: 100 * time.Second, start: 10 * time.Second, span: 20 * time.Second}
	tests := []struct {
		name      string
		dx        float32
		wantStart time.Duration
	}{
		{"scroll right", 100, 15 * time.Second},
		{"scroll left", -100, 5 * time.Second},
		{"clamp at left", -10_000, 0},
		{"clamp at right", 10_000, 80 * time.Second},
	}
	for _, tt := range tests {
		got := v.scrollBy(tt.dx, width)
		start, end := got.window()
		if start != tt.wantStart {
			t.Errorf("%s: start = %s, want %s", tt.name, start, tt.wantStart)
		}
		if end-start != 20*time.Second {
			t.Errorf("%s: span changed to %s", tt.name, end-start)
		}
	}
	whole := newTimelineView(100*time.Second).scrollBy(100, width)
	if whole.zoomed() {
		t.Error("scrolling an unzoomed view must not zoom it")
	}
}

func TestTimelineViewZoomToAndFit(t *testing.T) {
	v := newTimelineView(100 * time.Second)

	got := v.zoomTo(40*time.Second, 50*time.Second, 0.1)
	start, end := got.window()
	if start != 39*time.Second || end != 51*time.Second {
		t.Errorf("zoomTo window = [%s, %s], want [39s, 51s]", start, end)
	}

	edge := v.zoomTo(0, 10*time.Second, 0.5)
	if start, _ := edge.window(); start != 0 {
		t.Errorf("zoomTo at the left edge starts at %s, want 0", start)
	}

	tiny := v.zoomTo(10*time.Second, 10*time.Second+time.Millisecond, 0)
	if tiny.Span() != timelineMinSpan {
		t.Errorf("tiny range span = %s, want the %s floor", tiny.Span(), timelineMinSpan)
	}

	whole := v.zoomTo(0, 100*time.Second, 0.2)
	if whole.zoomed() {
		t.Error("zoomTo of the whole media should fit")
	}
	if got.fit().zoomed() {
		t.Error("fit() must return the unzoomed view")
	}
	if bad := v.zoomTo(5*time.Second, 5*time.Second, 0.1); bad != v {
		t.Error("an empty range must leave the view unchanged")
	}
}

func TestTimelineViewVisibleAndPerPixel(t *testing.T) {
	v := timelineView{duration: 100 * time.Second, start: 10 * time.Second, span: 20 * time.Second}
	if !v.visible(10*time.Second) || !v.visible(30*time.Second) || v.visible(31*time.Second) || v.visible(9*time.Second) {
		t.Error("visible() disagrees with the window bounds")
	}
	if got := v.perPixel(200); got != 100*time.Millisecond {
		t.Errorf("perPixel = %s, want 100ms", got)
	}
	if got := v.perPixel(0); got != 0 {
		t.Errorf("perPixel(0) = %s, want 0", got)
	}
}

func absDuration(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}
