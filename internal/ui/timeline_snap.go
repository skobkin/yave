package ui

import (
	"sort"
	"time"
)

// snapPixels is the magnetic reach of a marker: a pointer gesture within
// this many pixels of a snap target lands on it.
const snapPixels = 8

// snapMode restricts which side of the pointed position a target may sit
// on. Keyframe targets use it for stream-copy cuts: the In boundary may
// only snap back to a keyframe at or before it, the Out boundary forward
// to one at or after it, so snapping never drops selected content.
type snapMode uint8

const (
	snapAny      snapMode = iota // either side
	snapBackward                 // targets at or before the position
	snapForward                  // targets at or after the position
)

// snapCandidate is one magnetic target.
type snapCandidate struct {
	At   time.Duration
	Mode snapMode
}

// snapTime returns the candidate nearest to at within threshold that
// satisfies its mode, and whether one was found. Ties resolve to the
// earlier target. It is pure and table-tested.
func snapTime(at time.Duration, candidates []snapCandidate, threshold time.Duration) (time.Duration, bool) {
	best, bestDist, found := at, threshold+1, false
	for _, c := range candidates {
		dist := absDuration(c.At - at)
		if dist > threshold || dist > bestDist {
			continue
		}
		switch c.Mode {
		case snapBackward:
			if c.At > at {
				continue
			}
		case snapForward:
			if c.At < at {
				continue
			}
		case snapAny:
		}
		if dist < bestDist || (dist == bestDist && c.At < best) {
			best, bestDist, found = c.At, dist, true
		}
	}
	return best, found
}

// snapped applies magnetic snapping to a pointer-derived position for the
// given gesture (kind, and the segment index for handle drags; -1 for the
// playhead). It is a no-op while snapping is off, while Alt is held, or
// when there is nothing to snap to. Typed timecodes and keyboard moves
// never pass through it.
func (t *timeline) snapped(at time.Duration, kind tlDrag, seg int) time.Duration {
	if !t.snapEnabled || t.duration <= 0 || t.keyModifiers()&altModifier != 0 {
		return at
	}
	reach := t.snapReach()
	if got, ok := snapTime(at, t.snapCandidates(kind, seg, at, reach), reach); ok {
		return t.clamp(got)
	}
	return at
}

// snapReach is the magnetic reach as a duration at the current zoom.
func (t *timeline) snapReach() time.Duration {
	return time.Duration(snapPixels) * t.viewNow().perPixel(t.Size().Width)
}

// SnapTime is the exported form for the controller's boundary actions.
func (t *timeline) SnapTime(at time.Duration, kind tlDrag, seg int) time.Duration {
	return t.snapped(at, kind, seg)
}

// snapCandidates gathers the targets that can matter for a gesture at
// position at: the other segments' boundaries (never the gesture's own
// segment, so a handle cannot snap onto its partner and collapse the
// segment), the playhead for handle drags, and the markers within reach.
// Marker layers are sorted, so a layer with tens of thousands of keyframes
// costs a binary search per pointer move, not a scan. Range markers
// (chapters) also contribute their ends, which are not sorted, so those
// layers are scanned in full — they are small.
func (t *timeline) snapCandidates(kind tlDrag, seg int, at, reach time.Duration) []snapCandidate {
	var out []snapCandidate
	for i, s := range t.segments {
		if i == seg {
			continue
		}
		out = append(out, snapCandidate{At: s.Start}, snapCandidate{At: s.End})
	}
	if kind == tlIn || kind == tlOut {
		out = append(out, snapCandidate{At: t.playhead})
	}
	for k := range t.markers {
		markers := t.markers[k]
		mode := markerSnapMode(markerKind(k), kind)
		if t.markerHasEnd[k] {
			for _, m := range markers {
				out = append(out, snapCandidate{At: m.At, Mode: mode})
				if m.End > m.At {
					out = append(out, snapCandidate{At: m.End, Mode: mode})
				}
			}
			continue
		}
		first := sort.Search(len(markers), func(i int) bool { return markers[i].At >= at-reach })
		for _, m := range markers[first:] {
			if m.At > at+reach {
				break
			}
			out = append(out, snapCandidate{At: m.At, Mode: mode})
		}
	}
	return out
}

func absDuration(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}
