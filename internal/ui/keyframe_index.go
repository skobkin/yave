package ui

import (
	"slices"
	"sort"
	"time"
)

// keyframeCap bounds the number of keyframes kept; far beyond what a
// session of scrolling normally reaches (a 0.5s GOP over seven hours).
const keyframeCap = 50_000

// keyframeSpan is one fetched stretch of the timeline: times holds every
// keyframe in [from, to], so the span answers "which keyframe precedes
// or follows t" for any t inside it.
type keyframeSpan struct {
	from, to time.Duration
	times    []time.Duration // sorted, deduplicated, inside [from, to]
}

// keyframeIndex accumulates fetched keyframe windows. Lookups answer only
// from positions a fetch actually covered: outside the covered spans the
// answer is "unknown", never a guess, so the UI cannot show an actual-cut
// time derived from a window that never saw the real keyframe. It is
// pure state (no UI, no I/O) and table-tested.
type keyframeIndex struct {
	spans []keyframeSpan // sorted, disjoint, non-touching
}

// add merges a fetched window. times is the sorted result of a keyframe
// scan of [from, to]; because that scan starts at the keyframe preceding
// from, the first returned keyframe (when it precedes from) extends the
// covered span backwards. When the scan returned a first keyframe after a
// positive from, the preceding keyframe is unknown, so coverage starts at
// that first keyframe instead of from.
func (k *keyframeIndex) add(from, to time.Duration, times []time.Duration) {
	if to < from {
		return
	}
	spanFrom := from
	if len(times) > 0 {
		switch {
		case times[0] < from:
			spanFrom = times[0]
		case from > 0:
			spanFrom = times[0]
		}
	}
	kept := make([]time.Duration, 0, len(times))
	for _, at := range times {
		if at >= spanFrom && at <= to {
			kept = append(kept, at)
		}
	}
	merged := keyframeSpan{from: spanFrom, to: to, times: kept}
	var out []keyframeSpan
	for _, s := range k.spans {
		if s.to < merged.from || s.from > merged.to { // disjoint (touching spans merge)
			out = append(out, s)
			continue
		}
		merged = mergeKeyframeSpans(merged, s)
	}
	out = append(out, merged)
	sort.Slice(out, func(i, j int) bool { return out[i].from < out[j].from })
	k.spans = out
	k.evict(merged)
}

func mergeKeyframeSpans(a, b keyframeSpan) keyframeSpan {
	times := append(slices.Clone(a.times), b.times...)
	sort.Slice(times, func(i, j int) bool { return times[i] < times[j] })
	times = slices.Compact(times)
	return keyframeSpan{from: min(a.from, b.from), to: max(a.to, b.to), times: times}
}

// evict drops the spans farthest from keep once the cap is exceeded.
func (k *keyframeIndex) evict(keep keyframeSpan) {
	for k.total() > keyframeCap && len(k.spans) > 1 {
		center := keep.from + (keep.to-keep.from)/2
		far, farDist := -1, time.Duration(-1)
		for i, s := range k.spans {
			if s.from == keep.from && s.to == keep.to {
				continue
			}
			if d := absDuration(s.from + (s.to-s.from)/2 - center); d > farDist {
				far, farDist = i, d
			}
		}
		if far < 0 {
			return
		}
		k.spans = slices.Delete(k.spans, far, far+1)
	}
}

func (k *keyframeIndex) total() int {
	n := 0
	for _, s := range k.spans {
		n += len(s.times)
	}
	return n
}

// reset forgets everything.
func (k *keyframeIndex) reset() { k.spans = nil }

// spanAt returns the span containing at.
func (k *keyframeIndex) spanAt(at time.Duration) (keyframeSpan, bool) {
	for _, s := range k.spans {
		if at >= s.from && at <= s.to {
			return s, true
		}
	}
	return keyframeSpan{}, false
}

// covers reports whether [from, to] lies inside one fetched span.
func (k *keyframeIndex) covers(from, to time.Duration) bool {
	for _, s := range k.spans {
		if from >= s.from && to <= s.to {
			return true
		}
	}
	return false
}

// before returns the last keyframe at or before at: the position a
// stream-copy cut beginning at at actually starts from. Unknown when at
// is not covered or no keyframe at or before it was seen.
func (k *keyframeIndex) before(at time.Duration) (time.Duration, bool) {
	s, ok := k.spanAt(at)
	if !ok {
		return 0, false
	}
	i := sort.Search(len(s.times), func(i int) bool { return s.times[i] > at })
	if i == 0 {
		return 0, false
	}
	return s.times[i-1], true
}

// after returns the first keyframe at or after at; unknown when at is not
// covered or no later keyframe lies inside the span.
func (k *keyframeIndex) after(at time.Duration) (time.Duration, bool) {
	s, ok := k.spanAt(at)
	if !ok {
		return 0, false
	}
	i := sort.Search(len(s.times), func(i int) bool { return s.times[i] >= at })
	if i == len(s.times) {
		return 0, false
	}
	return s.times[i], true
}

// all returns every known keyframe, sorted.
func (k *keyframeIndex) all() []time.Duration {
	out := make([]time.Duration, 0, k.total())
	for _, s := range k.spans {
		out = append(out, s.times...)
	}
	return out
}
