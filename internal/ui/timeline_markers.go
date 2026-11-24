package ui

import (
	"image/color"
	"sort"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
)

// altModifier is the key that bypasses magnetic snapping.
const altModifier = fyne.KeyModifierAlt

// markerKind identifies one layer of timeline markers; each layer has its
// own color and snap behavior and is replaced as a whole.
type markerKind uint8

const (
	markerChapter markerKind = iota
	markerScene
	markerKeyframe
	markerKindCount
)

// timelineMarker is one marker on the timeline. End is only meaningful
// for range-like markers (chapters) and doubles as a snap target.
type timelineMarker struct {
	At    time.Duration
	End   time.Duration
	Label string
}

const (
	markerCapWidth   = 3  // the flag at the top of a marker
	markerCapHeight  = 14 //
	markerLineWidth  = 1  // the faint full-height line under it
	markerMinSpacing = 3  // markers closer than this (px) to the previous one are not drawn
)

// markerColor is the layer's color.
func markerColor(kind markerKind) color.NRGBA {
	switch kind {
	case markerChapter:
		return color.NRGBA{R: 255, G: 193, B: 7, A: 255}
	case markerScene:
		return color.NRGBA{R: 0, G: 188, B: 212, A: 255}
	default:
		return color.NRGBA{R: 200, G: 200, B: 200, A: 255}
	}
}

// markerSnapMode returns how a layer's targets constrain a gesture.
// Keyframes snap directionally for the boundary handles (see snapMode);
// everything else snaps to either side.
func markerSnapMode(kind markerKind, drag tlDrag) snapMode {
	if kind != markerKeyframe {
		return snapAny
	}
	switch drag {
	case tlIn:
		return snapBackward
	case tlOut:
		return snapForward
	default:
		return snapAny
	}
}

// SetMarkers replaces one marker layer. Markers are kept sorted by
// position; the controller owns their content.
func (t *timeline) SetMarkers(kind markerKind, markers []timelineMarker) {
	if kind >= markerKindCount {
		return
	}
	sorted := append([]timelineMarker(nil), markers...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].At < sorted[j].At })
	t.markers[kind] = sorted
	t.Refresh()
}

// Markers returns a copy of one layer.
func (t *timeline) Markers(kind markerKind) []timelineMarker {
	if kind >= markerKindCount {
		return nil
	}
	return append([]timelineMarker(nil), t.markers[kind]...)
}

// MarkerTimes returns the positions of every marker of the given layers,
// sorted and deduplicated — the stops of the previous/next marker buttons.
func (t *timeline) MarkerTimes(kinds ...markerKind) []time.Duration {
	var times []time.Duration
	for _, kind := range kinds {
		for _, m := range t.Markers(kind) {
			times = append(times, m.At)
		}
	}
	sort.Slice(times, func(i, j int) bool { return times[i] < times[j] })
	out := times[:0]
	for i, at := range times {
		if i == 0 || at != times[i-1] {
			out = append(out, at)
		}
	}
	return out
}

// markerLabelAt returns the label of the range marker (chapter) of a layer
// containing at.
func (t *timeline) markerLabelAt(kind markerKind, at time.Duration) (string, bool) {
	markers := t.markers[kind]
	for i := len(markers) - 1; i >= 0; i-- {
		if m := markers[i]; at >= m.At && at < m.End {
			return m.Label, true
		}
	}
	return "", false
}

// adjacentMarker returns the stop strictly after (dir > 0) or before
// (dir < 0) at. A 1 ms tolerance keeps a playhead that sits on a marker —
// give or take pixel round-off — from selecting that same marker again.
func adjacentMarker(times []time.Duration, at time.Duration, dir int) (time.Duration, bool) {
	const tolerance = time.Millisecond
	if dir > 0 {
		for _, m := range times {
			if m > at+tolerance {
				return m, true
			}
		}
		return 0, false
	}
	for i := len(times) - 1; i >= 0; i-- {
		if times[i] < at-tolerance {
			return times[i], true
		}
	}
	return 0, false
}

// layoutMarkers draws the visible markers of every layer from a pooled
// list of rectangles: a flag at the top edge plus a faint full-height
// line. Markers outside the window are skipped, and markers that would
// land within markerMinSpacing of the previous one in their layer are
// thinned, so thousands of scene cuts at a wide zoom stay cheap.
func (r *timelineRenderer) layoutMarkers(size fyne.Size) {
	t := r.t
	used := 0
	next := func() *canvas.Rectangle {
		if used == len(r.markerRects) {
			r.markerRects = append(r.markerRects, canvas.NewRectangle(color.Transparent))
		}
		rect := r.markerRects[used]
		used++
		rect.Hidden = false
		return rect
	}
	view := t.viewNow()
	start, end := view.window()
	for kind := range markerKindCount {
		base := markerColor(kind)
		lastX := float32(-1e9)
		markers := t.markers[kind]
		// Binary search the first marker that can be visible.
		first := sort.Search(len(markers), func(i int) bool { return markers[i].At >= start })
		for _, m := range markers[first:] {
			if m.At > end {
				break
			}
			x := t.xOf(m.At)
			if x-lastX < markerMinSpacing {
				continue
			}
			lastX = x
			line := next()
			line.FillColor = translucent(base, 0x50)
			line.Move(fyne.NewPos(x-markerLineWidth/2, 0))
			line.Resize(fyne.NewSize(markerLineWidth, size.Height))
			cap := next()
			cap.FillColor = base
			cap.Move(fyne.NewPos(x-markerCapWidth/2, 0))
			cap.Resize(fyne.NewSize(markerCapWidth, markerCapHeight))
		}
	}
	for _, rect := range r.markerRects[used:] {
		rect.Hidden = true
	}
}
