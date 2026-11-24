package ui

import "time"

// timelineMinSpan is the smallest visible window zooming may reach.
const timelineMinSpan = 500 * time.Millisecond

// timelineView is the pure x↔time mapping of the timeline: the visible
// window [start, start+span) of a media of the given duration, projected
// onto a widget width. It holds no widget state so the mapping, zoom
// anchoring and scroll limits can be table-tested, and every layer drawn
// on the timeline (thumbnails, markers, ticks, waveform) shares one
// definition of "where is time t".
//
// A zero span means "the whole duration" (the unzoomed view), so the
// zero value fits any media.
type timelineView struct {
	duration time.Duration
	start    time.Duration
	span     time.Duration
}

// newTimelineView returns the unzoomed view of a media duration.
func newTimelineView(duration time.Duration) timelineView {
	return timelineView{duration: max(duration, 0)}
}

// window returns the visible [start, end) range, normalized so that
// 0 <= start < end <= duration whenever the duration is positive.
func (v timelineView) window() (start, end time.Duration) {
	if v.duration <= 0 {
		return 0, 0
	}
	span := v.span
	if span <= 0 || span > v.duration {
		span = v.duration
	}
	start = min(max(v.start, 0), v.duration-span)
	return start, start + span
}

// Span returns the visible length.
func (v timelineView) Span() time.Duration {
	start, end := v.window()
	return end - start
}

// zoomed reports whether the view shows less than the whole media.
func (v timelineView) zoomed() bool {
	return v.duration > 0 && v.Span() < v.duration
}

// timeToX maps a timestamp onto the widget width, unclamped: positions
// outside the visible window fall outside [0, width], which lets layers
// hide objects that are scrolled out of view instead of pinning them to
// an edge.
func (v timelineView) timeToX(t time.Duration, width float32) float32 {
	start, end := v.window()
	if end <= start || width <= 0 {
		return 0
	}
	return float32(float64(t-start) / float64(end-start) * float64(width))
}

// xToTime maps an x position onto the visible window, clamped to the
// window (and so to [0, duration]).
func (v timelineView) xToTime(x, width float32) time.Duration {
	start, end := v.window()
	if end <= start || width <= 0 {
		return 0
	}
	ratio := float64(min(max(x, 0), width) / width)
	return start + time.Duration(ratio*float64(end-start))
}

// perPixel returns how much media time one pixel covers.
func (v timelineView) perPixel(width float32) time.Duration {
	if width <= 0 {
		return 0
	}
	return time.Duration(float64(v.Span()) / float64(width))
}

// visible reports whether t lies inside the visible window.
func (v timelineView) visible(t time.Duration) bool {
	start, end := v.window()
	return t >= start && t <= end
}

// minSpan is the zoom floor: the minimum span, or the whole duration for
// media shorter than that.
func (v timelineView) minSpan() time.Duration {
	return min(timelineMinSpan, v.duration)
}

// zoomAround scales the span by 1/factor (factor > 1 zooms in) keeping
// the media time under anchorX at the same pixel, then clamps the span
// to [minSpan, duration] and the window into the media.
func (v timelineView) zoomAround(anchorX, width float32, factor float64) timelineView {
	if v.duration <= 0 || width <= 0 || factor <= 0 {
		return v
	}
	anchor := v.xToTime(anchorX, width)
	ratio := float64(min(max(anchorX, 0), width) / width)
	newSpan := time.Duration(float64(v.Span()) / factor)
	newSpan = min(max(newSpan, v.minSpan()), v.duration)
	start := anchor - time.Duration(ratio*float64(newSpan))
	return timelineView{duration: v.duration, start: start, span: newSpan}.normalized()
}

// scrollBy shifts the window by dx pixels (positive reveals later media),
// clamped to the media.
func (v timelineView) scrollBy(dx, width float32) timelineView {
	if v.duration <= 0 || width <= 0 {
		return v
	}
	shift := time.Duration(float64(dx) / float64(width) * float64(v.Span()))
	start, end := v.window()
	return timelineView{duration: v.duration, start: start + shift, span: end - start}.normalized()
}

// fit returns the unzoomed view.
func (v timelineView) fit() timelineView {
	return newTimelineView(v.duration)
}

// zoomTo returns a view framing [from, to] with pad (a fraction of the
// range) of margin on each side.
func (v timelineView) zoomTo(from, to time.Duration, pad float64) timelineView {
	if v.duration <= 0 || to <= from {
		return v
	}
	margin := time.Duration(float64(to-from) * pad)
	span := min(max(to-from+2*margin, v.minSpan()), v.duration)
	center := from + (to-from)/2
	return timelineView{duration: v.duration, start: center - span/2, span: span}.normalized()
}

// normalized collapses a full-width window to the canonical unzoomed
// form and clamps the window into the media.
func (v timelineView) normalized() timelineView {
	start, end := v.window()
	if end-start >= v.duration {
		return newTimelineView(v.duration)
	}
	return timelineView{duration: v.duration, start: start, span: end - start}
}
