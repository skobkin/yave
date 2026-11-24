package ui

import (
	"context"
	"math"
	"time"
)

// bgJob owns one cancellable background computation. Starting a job
// cancels the previous one, and gen lets completion callbacks that were
// dispatched to the UI thread detect that they belong to a retired run.
// All methods run on the UI thread.
type bgJob struct {
	cancel context.CancelFunc
	gen    uint64
}

// start retires the running job and returns the new job's context and
// generation.
func (j *bgJob) start(parent context.Context) (context.Context, uint64) {
	j.stop()
	ctx, cancel := context.WithCancel(parent)
	j.cancel = cancel
	j.gen++
	return ctx, j.gen
}

// stop cancels the running job, if any. It bumps the generation so a
// completion already in flight is recognized as stale.
func (j *bgJob) stop() {
	if j.cancel != nil {
		j.cancel()
		j.cancel = nil
	}
	j.gen++
}

// current reports whether gen is still the newest job.
func (j *bgJob) current(gen uint64) bool { return j.gen == gen }

// stripGrid is the thumbnail strip's cell layout on the media timeline:
// n cells of width cell, the first starting at start. Cells sit on a
// multiple-of-cell grid, so scrolling at one zoom level reuses the same
// sample times (and so the cached thumbnails) instead of resampling at
// every offset.
type stripGrid struct {
	start time.Duration
	cell  time.Duration
	n     int
}

// stripSteps is the ladder of cell widths, chosen so sample times stay
// stable and human-sized across zoom levels.
var stripSteps = []time.Duration{
	10 * time.Millisecond, 20 * time.Millisecond, 50 * time.Millisecond,
	100 * time.Millisecond, 200 * time.Millisecond, 500 * time.Millisecond,
	time.Second, 2 * time.Second, 5 * time.Second, 10 * time.Second,
	15 * time.Second, 30 * time.Second, time.Minute, 2 * time.Minute,
	5 * time.Minute, 10 * time.Minute, 15 * time.Minute, 30 * time.Minute,
	time.Hour, 2 * time.Hour,
}

// niceStripStep returns the ladder step closest (in ratio) to ideal.
func niceStripStep(ideal time.Duration) time.Duration {
	if ideal <= stripSteps[0] {
		return stripSteps[0]
	}
	best, bestDiff := stripSteps[len(stripSteps)-1], math.Inf(1)
	for _, step := range stripSteps {
		diff := math.Abs(math.Log(float64(step) / float64(ideal)))
		if diff < bestDiff {
			best, bestDiff = step, diff
		}
	}
	return best
}

// stripGridFor lays out the thumbnail cells covering the window
// [winStart, winEnd) of a media: about thumbStripCount cells across the
// window on the step ladder, plus one cell of margin on each side so a
// small scroll does not expose empty slots. The zero grid means "nothing
// to draw".
func stripGridFor(winStart, winEnd, duration time.Duration) stripGrid {
	if duration <= 0 || winEnd <= winStart {
		return stripGrid{}
	}
	cell := niceStripStep((winEnd - winStart) / thumbStripCount)
	first := max(int64(winStart/cell)-1, 0)
	last := int64((winEnd + cell - 1) / cell) // exclusive upper bound, one margin cell past the window
	last = min(last+1, int64((duration+cell-1)/cell))
	if last <= first {
		return stripGrid{}
	}
	return stripGrid{start: time.Duration(first) * cell, cell: cell, n: int(last - first)}
}

// sampleAt returns the media position cell i is sampled at: the middle
// of the part of the cell that lies inside the media (midpoints avoid
// intro/black frames sitting exactly at 0).
func (g stripGrid) sampleAt(i int, duration time.Duration) time.Duration {
	from := g.start + time.Duration(i)*g.cell
	to := min(from+g.cell, duration)
	return from + (to-from)/2
}
