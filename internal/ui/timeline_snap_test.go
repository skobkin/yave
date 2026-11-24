package ui

import (
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/driver/desktop"
	fynetest "fyne.io/fyne/v2/test"

	"git.skobk.in/skobkin/yave/internal/model"
)

func TestSnapTime(t *testing.T) {
	s := time.Second
	tests := []struct {
		name      string
		at        time.Duration
		cands     []snapCandidate
		threshold time.Duration
		want      time.Duration
		wantOK    bool
	}{
		{"exact match", 10 * s, []snapCandidate{{At: 10 * s}}, s, 10 * s, true},
		{"within the threshold", 10 * s, []snapCandidate{{At: 10*s + 500*time.Millisecond}}, s, 10*s + 500*time.Millisecond, true},
		{"beyond the threshold", 10 * s, []snapCandidate{{At: 12 * s}}, s, 10 * s, false},
		{"threshold is inclusive", 10 * s, []snapCandidate{{At: 11 * s}}, s, 11 * s, true},
		{"nearest wins", 10 * s, []snapCandidate{{At: 11 * s}, {At: 10*s + 200*time.Millisecond}, {At: 9 * s}}, 2 * s, 10*s + 200*time.Millisecond, true},
		{"tie goes to the earlier target", 10 * s, []snapCandidate{{At: 11 * s}, {At: 9 * s}}, 2 * s, 9 * s, true},
		{"backward targets only snap back", 10 * s, []snapCandidate{{At: 10*s + 100*time.Millisecond, Mode: snapBackward}, {At: 8 * s, Mode: snapBackward}}, 3 * s, 8 * s, true},
		{"backward target ahead is ignored", 10 * s, []snapCandidate{{At: 11 * s, Mode: snapBackward}}, 3 * s, 10 * s, false},
		{"forward targets only snap forward", 10 * s, []snapCandidate{{At: 9*s + 900*time.Millisecond, Mode: snapForward}, {At: 12 * s, Mode: snapForward}}, 3 * s, 12 * s, true},
		{"forward target behind is ignored", 10 * s, []snapCandidate{{At: 9 * s, Mode: snapForward}}, 3 * s, 10 * s, false},
		{"exact hit satisfies either direction", 10 * s, []snapCandidate{{At: 10 * s, Mode: snapBackward}}, 0, 10 * s, true},
		{"no candidates", 10 * s, nil, 5 * s, 10 * s, false},
		{"zero threshold needs an exact hit", 10 * s, []snapCandidate{{At: 10*s + 1}}, 0, 10 * s, false},
	}
	for _, tt := range tests {
		got, ok := snapTime(tt.at, tt.cands, tt.threshold)
		if ok != tt.wantOK || got != tt.want {
			t.Errorf("%s: got (%s, %v), want (%s, %v)", tt.name, got, ok, tt.want, tt.wantOK)
		}
	}
}

func TestMarkerSnapModes(t *testing.T) {
	for _, kind := range []markerKind{markerChapter, markerScene} {
		for _, drag := range []tlDrag{tlPlayhead, tlIn, tlOut} {
			if got := markerSnapMode(kind, drag); got != snapAny {
				t.Errorf("kind %d drag %d = %d, want snapAny", kind, drag, got)
			}
		}
	}
	if markerSnapMode(markerKeyframe, tlIn) != snapBackward || markerSnapMode(markerKeyframe, tlOut) != snapForward ||
		markerSnapMode(markerKeyframe, tlPlayhead) != snapAny {
		t.Error("keyframes must snap In back, Out forward and the playhead either way")
	}
}

// snapTimeline is a 200px timeline over 100s (0.5s/px, so the 8px reach is
// 4s) with snapping on.
func snapTimeline(t *testing.T) *timeline {
	t.Helper()
	tl := newSizedTimeline(t)
	tl.SetDuration(100 * time.Second)
	tl.snapEnabled = true
	tl.modifiers = func() fyne.KeyModifier { return 0 }
	return tl
}

func pointerDown(tl *timeline, at time.Duration) {
	tl.MouseDown(&desktop.MouseEvent{
		PointEvent: fyne.PointEvent{Position: fyne.NewPos(tl.xOf(at), 10)},
		Button:     desktop.MouseButtonPrimary,
	})
}

func pointerDrag(tl *timeline, at time.Duration) {
	tl.Dragged(&fyne.DragEvent{PointEvent: fyne.PointEvent{Position: fyne.NewPos(tl.xOf(at), 10)}})
}

func TestHandleDragSnapsToChapterMarker(t *testing.T) {
	tl := snapTimeline(t)
	tl.SetMarkers(markerChapter, []timelineMarker{{At: 50 * time.Second, End: 70 * time.Second, Label: "Mid"}})
	tl.SetSegmentsSilent([]model.TimeRange{{Start: 20 * time.Second, End: 90 * time.Second}})

	pointerDown(tl, 20*time.Second)
	pointerDrag(tl, 48*time.Second) // 2s away: inside the 4s reach
	if got := tl.Segments()[0].Start; !near(got, 50*time.Second) {
		t.Fatalf("In dragged near the chapter start = %s, want it on 50s", got)
	}
	pointerDrag(tl, 40*time.Second) // 10s away: free
	if got := tl.Segments()[0].Start; !near(got, 40*time.Second) {
		t.Fatalf("In dragged away = %s, want 40s", got)
	}
	tl.MouseUp(&desktop.MouseEvent{Button: desktop.MouseButtonPrimary})

	// The chapter's end is a target for the Out handle.
	pointerDown(tl, 90*time.Second)
	pointerDrag(tl, 71*time.Second)
	if got := tl.Segments()[0].End; !near(got, 70*time.Second) {
		t.Fatalf("Out dragged near the chapter end = %s, want 70s", got)
	}
}

func TestSnapRespectsToggleAndAlt(t *testing.T) {
	setup := func() *timeline {
		tl := snapTimeline(t)
		tl.SetMarkers(markerChapter, []timelineMarker{{At: 50 * time.Second, End: 60 * time.Second}})
		tl.SetSegmentsSilent([]model.TimeRange{{Start: 20 * time.Second, End: 90 * time.Second}})
		return tl
	}

	off := setup()
	off.snapEnabled = false
	pointerDown(off, 20*time.Second)
	pointerDrag(off, 48*time.Second)
	if got := off.Segments()[0].Start; !near(got, 48*time.Second) {
		t.Errorf("snap off: In = %s, want the free 48s", got)
	}

	alt := setup()
	alt.modifiers = func() fyne.KeyModifier { return fyne.KeyModifierAlt }
	pointerDown(alt, 20*time.Second)
	pointerDrag(alt, 48*time.Second)
	if got := alt.Segments()[0].Start; !near(got, 48*time.Second) {
		t.Errorf("Alt held: In = %s, want the free 48s", got)
	}
}

func TestSnapReachShrinksWithZoom(t *testing.T) {
	tl := snapTimeline(t)
	tl.SetMarkers(markerChapter, []timelineMarker{{At: 50 * time.Second}})
	tl.SetSegmentsSilent([]model.TimeRange{{Start: 41 * time.Second, End: 90 * time.Second}})
	// Zoomed to a 20s window, 8px is 0.8s: a 2s miss no longer snaps.
	tl.view = timelineView{duration: 100 * time.Second, start: 40 * time.Second, span: 20 * time.Second}

	pointerDown(tl, 41*time.Second)
	pointerDrag(tl, 48*time.Second)
	if got := tl.Segments()[0].Start; !near(got, 48*time.Second) {
		t.Fatalf("zoomed, 2s from the marker: In = %s, want free 48s", got)
	}
	pointerDrag(tl, 49500*time.Millisecond) // 0.5s away: inside the reach
	if got := tl.Segments()[0].Start; !near(got, 50*time.Second) {
		t.Fatalf("zoomed, 0.5s from the marker: In = %s, want 50s", got)
	}
}

func TestSnapNeverCollapsesTheDraggedSegment(t *testing.T) {
	tl := snapTimeline(t)
	tl.SetSegmentsSilent([]model.TimeRange{{Start: 20 * time.Second, End: 30 * time.Second}})
	// The handle's own partner is no target: dragging In next to Out must
	// not snap onto it, while the neighbor clamp still holds.
	pointerDown(tl, 20*time.Second)
	pointerDrag(tl, 28*time.Second)
	if got := tl.Segments()[0].Start; !near(got, 28*time.Second) {
		t.Fatalf("In = %s, want free 28s (its own Out is not a snap target)", got)
	}
}

func TestSnapAppliesToOtherSegmentsAndTheClamp(t *testing.T) {
	tl := snapTimeline(t)
	tl.SetSegmentsSilent([]model.TimeRange{
		{Start: 10 * time.Second, End: 40 * time.Second},
		{Start: 60 * time.Second, End: 90 * time.Second},
	})
	// Out of segment 0 dragged right: it snaps to segment 1's Start (the
	// clamp allows equality: adjacent segments).
	pointerDown(tl, 40*time.Second)
	pointerDrag(tl, 58*time.Second)
	if got := tl.Segments()[0].End; !near(got, 60*time.Second) {
		t.Fatalf("Out = %s, want it snapped onto the next segment's start", got)
	}
	// Past the neighbor: the clamp, not the snap, decides.
	pointerDrag(tl, 80*time.Second)
	if got := tl.Segments()[0].End; !near(got, 60*time.Second) {
		t.Fatalf("Out = %s, want clamped at the next segment's start", got)
	}
}

func TestPlayheadClickSnapsAndHandlesSnapToPlayhead(t *testing.T) {
	tl := snapTimeline(t)
	tl.SetMarkers(markerScene, []timelineMarker{{At: 30 * time.Second}})
	var got time.Duration
	tl.onPlayhead = func(at time.Duration) { got = at }
	pointerDown(tl, 28*time.Second)
	if !near(got, 30*time.Second) {
		t.Fatalf("playhead click near a scene cut = %s, want 30s", got)
	}

	// With the playhead parked at 50s, a handle dragged near it snaps on.
	tl.SetSegmentsSilent([]model.TimeRange{{Start: 10 * time.Second, End: 90 * time.Second}})
	tl.SetPlayhead(50 * time.Second)
	tl.MouseUp(&desktop.MouseEvent{Button: desktop.MouseButtonPrimary})
	pointerDown(tl, 10*time.Second)
	pointerDrag(tl, 52*time.Second)
	if got := tl.Segments()[0].Start; !near(got, 50*time.Second) {
		t.Fatalf("In near the playhead = %s, want 50s", got)
	}
}

func TestKeyframeMarkersSnapDirectionally(t *testing.T) {
	tl := snapTimeline(t)
	tl.SetMarkers(markerKeyframe, []timelineMarker{{At: 40 * time.Second}, {At: 60 * time.Second}})
	tl.SetSegmentsSilent([]model.TimeRange{{Start: 10 * time.Second, End: 90 * time.Second}})

	pointerDown(tl, 10*time.Second)
	pointerDrag(tl, 42*time.Second) // keyframe at 40 is behind: In snaps back to it
	if got := tl.Segments()[0].Start; !near(got, 40*time.Second) {
		t.Fatalf("In = %s, want snapped back to the keyframe at 40s", got)
	}
	pointerDrag(tl, 38*time.Second) // keyframe at 40 is ahead: In must not snap forward
	if got := tl.Segments()[0].Start; !near(got, 38*time.Second) {
		t.Fatalf("In = %s, want free 38s (no forward snap for In)", got)
	}
	tl.MouseUp(&desktop.MouseEvent{Button: desktop.MouseButtonPrimary})

	pointerDown(tl, 90*time.Second)
	pointerDrag(tl, 58*time.Second) // keyframe at 60 is ahead: Out snaps forward
	if got := tl.Segments()[0].End; !near(got, 60*time.Second) {
		t.Fatalf("Out = %s, want snapped forward to 60s", got)
	}
	pointerDrag(tl, 62*time.Second) // keyframe at 60 is behind: Out must not snap back
	if got := tl.Segments()[0].End; !near(got, 62*time.Second) {
		t.Fatalf("Out = %s, want free 62s (no backward snap for Out)", got)
	}
}

func TestTimelineMarkers(t *testing.T) {
	tl := newSizedTimeline(t)
	tl.SetDuration(100 * time.Second)
	tl.SetMarkers(markerChapter, []timelineMarker{
		{At: 30 * time.Second, End: 60 * time.Second, Label: "B"},
		{At: 0, End: 30 * time.Second, Label: "A"},
	})
	tl.SetMarkers(markerScene, []timelineMarker{{At: 30 * time.Second}, {At: 45 * time.Second}})

	if got := tl.Markers(markerChapter); len(got) != 2 || got[0].Label != "A" {
		t.Errorf("markers must be sorted by position: %v", got)
	}
	times := tl.MarkerTimes(markerChapter, markerScene)
	want := []time.Duration{0, 30 * time.Second, 45 * time.Second}
	if len(times) != len(want) {
		t.Fatalf("MarkerTimes = %v, want %v (deduplicated)", times, want)
	}
	for i := range want {
		if times[i] != want[i] {
			t.Errorf("MarkerTimes = %v, want %v", times, want)
		}
	}
	if label, ok := tl.markerLabelAt(markerChapter, 45*time.Second); !ok || label != "B" {
		t.Errorf("label at 45s = %q, %v", label, ok)
	}
	if _, ok := tl.markerLabelAt(markerChapter, 60*time.Second); ok {
		t.Error("a chapter is half-open: 60s belongs to no chapter")
	}
	tl.SetMarkers(markerChapter, nil)
	if got := tl.Markers(markerChapter); len(got) != 0 {
		t.Errorf("cleared layer still has %v", got)
	}
	tl.SetMarkers(markerKindCount, []timelineMarker{{At: 1}}) // out of range: ignored
}

func TestMarkerLayoutCullsAndThins(t *testing.T) {
	tl := newSizedTimeline(t) // 200px over 100s
	tl.SetDuration(100 * time.Second)
	var many []timelineMarker
	for i := range 40 { // every 2.5s: 5px apart at 2px/s
		many = append(many, timelineMarker{At: time.Duration(i) * 2500 * time.Millisecond})
	}
	tl.SetMarkers(markerScene, many)
	r := fynetest.TempWidgetRenderer(t, tl).(*timelineRenderer)

	visible := func() int {
		n := 0
		for _, rect := range r.markerRects {
			if !rect.Hidden {
				n++
			}
		}
		return n
	}
	r.Layout(tl.Size())
	if got := visible(); got != 2*40 { // 5px apart: nothing thinned (flag + line each)
		t.Errorf("fit view draws %d rects, want %d", got, 2*40)
	}

	// A marker on every millisecond thins to the minimum spacing.
	dense := make([]timelineMarker, 0, 100_000)
	for i := range 100_000 {
		dense = append(dense, timelineMarker{At: time.Duration(i) * time.Millisecond})
	}
	tl.SetMarkers(markerScene, dense)
	r.Layout(tl.Size())
	if got := visible(); got > 2*(200/markerMinSpacing+1) {
		t.Errorf("dense layer draws %d rects, want at most %d", got, 2*(200/markerMinSpacing+1))
	}

	// Scrolled away, nothing is drawn.
	tl.view = timelineView{duration: 100 * time.Second, start: 200 * time.Second, span: 20 * time.Second}
	tl.SetMarkers(markerScene, []timelineMarker{{At: 5 * time.Second}})
	r.Layout(tl.Size())
	if got := visible(); got != 0 {
		t.Errorf("off-screen marker draws %d rects", got)
	}
}
