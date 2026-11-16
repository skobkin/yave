package ui

import (
	"context"
	"errors"
	"fmt"
	"image"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"fyne.io/fyne/v2"

	"git.skobk.in/skobkin/yave/internal/ffprobe"
	"git.skobk.in/skobkin/yave/internal/frames"
	"git.skobk.in/skobkin/yave/internal/model"
	"git.skobk.in/skobkin/yave/internal/validation"
)

// Trim preview and frame-index tuning.
const (
	previewDebounce      = 50 * time.Millisecond  // collapses key-repeat / drag storms
	hoverDebounce        = 200 * time.Millisecond // hover previews trail the pointer
	hoverQuantum         = 250 * time.Millisecond // hover previews may approximate
	previewCacheBytes    = 64 << 20               // 64 MiB of decoded frames
	previewMaxEntryBytes = 2 << 20                // a single decoded preview frame
	previewImageWidth    = 320
	thumbStripCount      = 10
	thumbStripHeight     = 48
	thumbMaxWorkers      = 2
	frameWindowLen       = 2 * time.Second // ffprobe -read_intervals window
	frameWindowCap       = 4096            // max cached frame timestamps
	framePendingCap      = 10000           // max queued frame steps
)

// trimRequest returns the selection handed to command generation. It is
// the sole trim source for both BuildRequest call sites, so the state
// can never leak into profileFromWidgets (and thus into Save). An
// invalid timecode entry returns an empty selection: the preview stays
// honest while startConversion blocks on the entry-validity issues.
func (c *Controller) trimRequest() model.TrimSelection {
	if !c.trimEnabled || !c.trimSelection.Active() || !c.trimInValid || !c.trimOutValid {
		return model.TrimSelection{}
	}
	return c.trimSelection
}

// --- enable / reset ---

// setTrimEnabled flips trimming on or off. First enable seeds the
// selection to the full range and lazily starts the thumbnail strip and
// the initial preview — no FFmpeg preview process ever runs merely
// because an input was selected.
func (c *Controller) setTrimEnabled(enabled bool) {
	if c.trimEnabled == enabled {
		return
	}
	c.trimEnabled = enabled
	for _, control := range c.trimControls {
		if d, ok := control.(fyne.Disableable); ok {
			if enabled {
				d.Enable()
			} else {
				d.Disable()
			}
		}
	}
	if enabled && c.media.Duration > 0 {
		if !c.trimSelection.Active() {
			c.resetTrimSelection()
		}
		c.scheduleStrip()
		c.requestPreview(c.trimTimeline.Playhead())
		c.ensureFrameIndex(c.trimTimeline.Playhead())
	}
	c.refresh()
}

// resetTrimSelection restores the selection to the full range, keeping
// trimming enabled (Reset's contract): the command keeps
// -ss 0.000 -t <duration> until trimming is disabled entirely.
func (c *Controller) resetTrimSelection() {
	c.trimSelection = model.TrimSelection{
		Segments: []model.TimeRange{{Start: 0, End: c.media.Duration}},
	}
	c.trimInValid, c.trimOutValid = true, true
	c.trimTimeline.SetDuration(c.media.Duration)
	c.trimTimeline.SetPlayhead(0)
	c.trimTimeline.SetRange(0, c.media.Duration)
	c.trimIn.SetText(formatTimestamp(0))
	c.trimOut.SetText(formatTimestamp(c.media.Duration))
	c.updateTrimDurationLabel()
	// SetPlayhead deliberately fires no callback, so request the preview
	// at the reset position explicitly: Reset is a committed move and the
	// Frame preview must follow it. The monotonic request sequence also
	// retires any in-flight hover or explicit result from before.
	c.requestPreview(c.trimTimeline.Playhead())
}

// resetTrimForNewInput clears every per-input trim state. Called
// synchronously from setInput so stale selections, thumbnails, or
// previews cannot survive an input change.
func (c *Controller) resetTrimForNewInput() {
	c.trimEnabled = false
	c.trimSelection = model.TrimSelection{}
	c.trimInValid, c.trimOutValid = true, true
	c.trimEnable.SetChecked(false)
	c.trimIn.SetText("")
	c.trimOut.SetText("")
	c.trimMessage.SetText("")
	c.trimDuration.SetText("")
	c.trimTimeline.SetDuration(0)
	c.trimTimeline.SetThumbnails(nil)
	c.trimTimeline.SetPlayhead(0)
	c.previewImage.Image = nil
	c.previewImage.Refresh()
	c.previewTimeLabel.SetText("")
	c.hoverTimeLabel.SetText("")
	c.setPreviewStatus("")
	c.previews.index = newFrameIndex()
	c.previews.cache.Clear()
	c.previews.stripRunning = false
}

// --- trim messages (validation + entry validity, one label) ---

// boundaryEntryProblem describes an invalid boundary entry, or "" when
// the entry is valid. An empty entry is invalid too: with trimming
// enabled the stored selection still validates and trimRequest() returns
// an empty selection, so a cleared-but-unblocking field would let Start
// silently convert the whole input without -ss/-t.
func boundaryEntryProblem(label string, valid bool, text string) string {
	if valid {
		return ""
	}
	if strings.TrimSpace(text) == "" {
		return label + " timecode is required"
	}
	return label + " timecode is invalid: " + text
}

// updateTrimMessages recomputes the Trim tab's validation message from
// current state. It runs from refresh() — the universal recompute point
// — so the stream-copy warning stays synchronized across enable/disable,
// encoder and profile switches, and input changes without depending on
// boundary-edit callbacks.
func (c *Controller) updateTrimMessages(encoder string) {
	var parts []string
	if c.trimEnabled {
		for _, problem := range []string{
			boundaryEntryProblem("In", c.trimInValid, c.trimIn.Text),
			boundaryEntryProblem("Out", c.trimOutValid, c.trimOut.Text),
		} {
			if problem != "" {
				parts = append(parts, problem)
			}
		}
		result := validation.ValidateTrim(c.trimSelection, &c.media, encoder)
		for _, issue := range result.Errors {
			parts = append(parts, "Error: "+issue.Message)
		}
		for _, issue := range result.Warnings {
			parts = append(parts, "Warning: "+issue.Message)
		}
	}
	c.trimMessage.SetText(strings.Join(parts, "\n"))
}

// trimEntryIssues appends the blocking issues for invalid timecode
// entries. Invalid text keeps the timeline on its last valid selection,
// but Start must never silently proceed with stale boundary values.
func (c *Controller) trimEntryIssues() []validation.Issue {
	var issues []validation.Issue
	if !c.trimEnabled {
		return issues
	}
	for _, boundary := range []struct {
		field, label string
		valid        bool
		text         string
	}{
		{"trim in", "In", c.trimInValid, c.trimIn.Text},
		{"trim out", "Out", c.trimOutValid, c.trimOut.Text},
	} {
		if problem := boundaryEntryProblem(boundary.label, boundary.valid, boundary.text); problem != "" {
			issues = append(issues, validation.Issue{
				Severity: validation.SeverityError, Field: boundary.field,
				Message: problem,
			})
		}
	}
	return issues
}

// --- boundary editing ---

// setTrimBoundary applies a typed boundary. Invalid text keeps the last
// valid selection (the timeline stays stable) and marks the entry
// invalid, which blocks Start; valid text replaces that boundary.
func (c *Controller) setTrimBoundary(which string, text string) {
	at, err := parseTimestamp(text)
	if err != nil {
		switch which {
		case "in":
			c.trimInValid = false
		case "out":
			c.trimOutValid = false
		}
		c.refresh()
		return
	}
	switch which {
	case "in":
		c.trimInValid = true
		c.trimSelection = withBoundary(c.trimSelection, at, -1)
		c.trimTimeline.SetRange(at, boundaryOf(c.trimSelection, 1))
	case "out":
		c.trimOutValid = true
		c.trimSelection = withBoundary(c.trimSelection, -1, at)
		c.trimTimeline.SetRange(boundaryOf(c.trimSelection, 0), at)
	}
	c.updateTrimDurationLabel()
	c.refresh()
}

func (c *Controller) setInFromPlayhead() {
	if !c.trimEnabled {
		return
	}
	at := c.trimTimeline.Playhead()
	c.trimInValid = true
	c.trimSelection = withBoundary(c.trimSelection, at, -1)
	c.trimIn.SetText(formatTimestamp(at))
	c.trimTimeline.SetRange(at, boundaryOf(c.trimSelection, 1))
	c.updateTrimDurationLabel()
	c.refresh()
}

func (c *Controller) setOutFromPlayhead() {
	if !c.trimEnabled {
		return
	}
	at := c.trimTimeline.Playhead()
	c.trimOutValid = true
	c.trimSelection = withBoundary(c.trimSelection, -1, at)
	c.trimOut.SetText(formatTimestamp(at))
	c.trimTimeline.SetRange(boundaryOf(c.trimSelection, 0), at)
	c.updateTrimDurationLabel()
	c.refresh()
}

func (c *Controller) updateTrimDurationLabel() {
	if !c.trimSelection.Active() {
		c.trimDuration.SetText("")
		return
	}
	c.trimDuration.SetText("Selected: " + formatTimestamp(c.trimSelection.Total()))
}

// withBoundary replaces the Start (start=-1 keeps it) or End of the
// selection's first segment, creating the segment when absent.
func withBoundary(sel model.TrimSelection, start, end time.Duration) model.TrimSelection {
	if !sel.Active() {
		sel.Segments = []model.TimeRange{{Start: start, End: end}}
		if start < 0 {
			sel.Segments[0].Start = 0
		}
		if end < 0 {
			sel.Segments[0].End = 0
		}
		return sel
	}
	if start >= 0 {
		sel.Segments[0].Start = start
	}
	if end >= 0 {
		sel.Segments[0].End = end
	}
	return sel
}

func boundaryOf(sel model.TrimSelection, index int) time.Duration {
	if !sel.Active() {
		return 0
	}
	if index == 0 {
		return sel.Segments[0].Start
	}
	return sel.Segments[0].End
}

// --- timeline callbacks ---

// onTimelinePlayhead fires for committed playhead moves. The command
// preview does not depend on the playhead, so refresh() is deliberately
// not called here — dragging must not rebuild the preview per pixel.
func (c *Controller) onTimelinePlayhead(at time.Duration) {
	c.requestPreview(at)
}

// onTimelineRange commits new boundaries from a handle drag.
func (c *Controller) onTimelineRange(in, out time.Duration) {
	c.trimInValid, c.trimOutValid = true, true
	c.trimSelection = withBoundary(c.trimSelection, in, out)
	c.trimIn.SetText(formatTimestamp(in))
	c.trimOut.SetText(formatTimestamp(out))
	c.updateTrimDurationLabel()
	c.refresh()
}

// onTimelineHover reports the hovered position. Hovering shows the
// timestamp immediately and (debounced) replaces the displayed preview,
// but never moves the committed playhead; MouseOut restores it.
func (c *Controller) onTimelineHover(at *time.Duration) {
	if at == nil {
		c.hoverTimeLabel.SetText("")
		c.restoreCommittedPreview()
		return
	}
	c.hoverTimeLabel.SetText(formatTimestamp(*at))
	c.requestHover(*at)
}

// --- previews ---

// ensureExtractor binds the extractor to the currently configured
// executable, rebuilding it (and dropping its cache) when the FFmpeg
// path changes so obsolete instances never feed current previews.
func (c *Controller) ensureExtractor() *frames.Extractor {
	path := c.config.FFmpeg.FFmpegPath
	if c.previews.extractor == nil || c.previews.extractorPath != path {
		c.previews.extractor = frames.NewExtractor(path)
		c.previews.extractorPath = path
		c.previews.cache.Clear()
	}
	return c.previews.extractor
}

// requestPreview asks for the frame at an exact committed position
// (keyboard steps, entries, Set In/Out, drag commits). Explicit
// positions are never quantized.
func (c *Controller) requestPreview(at time.Duration) {
	if !c.trimEnabled || c.inputPath == "" || c.media.Duration <= 0 {
		return
	}
	c.previews.seq++
	if img, ok := c.previews.cache.Get(at); ok {
		c.displayPreview(img, at)
		return
	}
	c.startPreviewTimer(&c.previews.previewTimer, previewDebounce, at)
}

// requestHover asks for the frame at a hovered position, quantized:
// transient hover previews may approximate, committed positions never
// do.
func (c *Controller) requestHover(at time.Duration) {
	if !c.trimEnabled || c.inputPath == "" || c.media.Duration <= 0 {
		return
	}
	q := at / hoverQuantum * hoverQuantum
	c.previews.seq++
	if img, ok := c.previews.cache.Get(q); ok {
		c.displayPreview(img, q)
		return
	}
	c.startPreviewTimer(&c.previews.hoverTimer, hoverDebounce, q)
}

// restoreCommittedPreview returns the display to the committed playhead
// after a hover leaves: cache first, then a fresh explicit request.
func (c *Controller) restoreCommittedPreview() {
	if !c.trimEnabled || c.inputPath == "" || c.media.Duration <= 0 {
		return
	}
	at := c.trimTimeline.Playhead()
	c.previews.seq++ // retire any in-flight hover result
	if img, ok := c.previews.cache.Get(at); ok {
		c.displayPreview(img, at)
		return
	}
	c.startPreviewTimer(&c.previews.previewTimer, previewDebounce, at)
}

func (c *Controller) startPreviewTimer(timer **time.Timer, delay time.Duration, at time.Duration) {
	seq := c.previews.seq
	if *timer != nil {
		(*timer).Stop()
	}
	*timer = time.AfterFunc(delay, func() {
		c.dispatchUI(func() {
			c.launchPreview(at, seq)
		})
	})
}

// launchPreview cancels the in-flight extraction (bounded to ≤1 preview
// process) and starts the frame request; the result is shown only if
// this request is still the newest one.
func (c *Controller) launchPreview(at time.Duration, seq uint64) {
	if !c.trimEnabled || c.previews.seq != seq {
		return
	}
	if img, ok := c.previews.cache.Get(at); ok {
		c.displayPreview(img, at)
		return
	}
	if c.previews.previewCancel != nil {
		c.previews.previewCancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	c.previews.previewCancel = cancel
	gen := c.inputGeneration
	ext := c.ensureExtractor()
	path := c.inputPath
	go func() {
		frame, err := ext.Preview(ctx, path, at, previewImageWidth)
		c.dispatchUI(func() {
			if c.inputGeneration != gen || c.ensureExtractor() != ext {
				return
			}
			if c.previews.seq != seq || !c.trimEnabled {
				return
			}
			if err != nil {
				c.setPreviewStatus("Preview unavailable: " + err.Error())
				return
			}
			c.setPreviewStatus("")
			c.previews.cache.Put(at, frame.Image)
			c.displayPreview(frame.Image, at)
		})
	}()
}

func (c *Controller) displayPreview(img image.Image, at time.Duration) {
	c.previewImage.Image = img
	c.previewImage.Refresh()
	c.previewTimeLabel.SetText(formatTimestamp(at))
}

func (c *Controller) setPreviewStatus(text string) {
	c.previewStatus.SetText(text)
}

// --- thumbnail strip ---

// scheduleStrip generates the timeline's thumbnails for the current
// input: one input-side-seek ffmpeg process per slot, at most
// thumbMaxWorkers in flight, slots rendering progressively. It runs
// once per input (lazy — only after trimming is enabled) and reuses
// results while the same input stays loaded.
func (c *Controller) scheduleStrip() {
	if c.inputPath == "" || c.media.Duration <= 0 || c.previews.stripRunning {
		return
	}
	c.previews.stripRunning = true
	c.trimTimeline.SetThumbnails(make([]image.Image, thumbStripCount))
	ctx, cancel := context.WithCancel(context.Background())
	c.previews.stripCancel = cancel
	gen := c.inputGeneration
	ext := c.ensureExtractor()
	path := c.inputPath
	// Midpoints avoid intro/black frames sitting exactly at 0.
	positions := make([]time.Duration, thumbStripCount)
	for i := range positions {
		positions[i] = c.media.Duration * time.Duration(2*i+1) / time.Duration(2*thumbStripCount)
	}
	var wg sync.WaitGroup
	sem := make(chan struct{}, thumbMaxWorkers)
	for slot, at := range positions {
		wg.Add(1)
		go func(slot int, at time.Duration) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			frame, err := ext.Thumbnail(ctx, path, at, thumbStripHeight)
			c.dispatchUI(func() {
				if c.inputGeneration != gen || c.ensureExtractor() != ext || ctx.Err() != nil {
					return
				}
				if err != nil {
					return // the slot keeps its placeholder; the next input retries
				}
				c.trimTimeline.SetThumbnail(slot, frame.Image)
			})
		}(slot, at)
	}
	go func() {
		wg.Wait()
		c.dispatchUI(func() {
			if c.inputGeneration == gen {
				c.previews.stripRunning = false
			}
		})
	}()
}

// --- frame index (frame-accurate stepping) ---

// frameStepState is the outcome of one frame-step request.
type frameStepState uint8

const (
	frameStepMoved frameStepState = iota
	frameStepLoading
	frameStepEdge        // start/end of the video reached
	frameStepUnavailable // indexing failed; stepping is off
)

// frameIndex maintains a bounded sliding window of decoded-frame
// timestamps (ffprobe best_effort_timestamp_time, normalized to the
// -ss timeline) for frame-accurate navigation. All methods run on the
// UI thread, so state is inherently serialized: rapid repeated presses
// accumulate into `pending` and can never produce out-of-order or lost
// requests. Pending steps are never clamped to the cached window or
// discarded — stepping past the window edge fetches the adjacent window
// (single-flight) and resumes; the walk stops only at the video's
// actual first/last frame or on a fetch error.
//
// Precision note: best_effort_timestamp_time is ffprobe's most reliable
// presentation-time estimate (unlike packet pts_time it is per decoded,
// presentation-ordered frame and survives B-frame reordering); a stream
// whose timestamps are entirely missing surfaces via the failure path.
type frameIndex struct {
	times       []time.Duration // sorted, normalized, inside [windowStart, windowEnd]
	windowStart time.Duration
	windowEnd   time.Duration
	inflight    bool
	failed      bool
	fetchErr    error

	pending   int           // signed queued steps while loading
	pendingAt time.Duration // position the pending steps apply to
	capacity  int
}

func newFrameIndex() *frameIndex {
	return &frameIndex{capacity: frameWindowCap}
}

// step resolves one frame step from `at`. When loading, the step is
// queued and the controller must ensure the index window for the
// returned position.
func (f *frameIndex) step(at time.Duration, dir int, duration time.Duration) (time.Duration, frameStepState) {
	if f.failed {
		return at, frameStepUnavailable
	}
	if !f.covers(at) {
		f.queue(dir, at)
		return at, frameStepLoading
	}
	if next, ok := lookupStep(f.times, at, dir); ok {
		return next, frameStepMoved
	}
	// `at` is covered but has no neighbor in the cached frames: either
	// the video genuinely ends/starts here, or the neighbor lies in the
	// adjacent window.
	if dir > 0 && f.windowEnd >= duration || dir < 0 && f.windowStart <= 0 {
		return at, frameStepEdge
	}
	f.queue(dir, at)
	return at, frameStepLoading
}

func (f *frameIndex) queue(dir int, at time.Duration) {
	f.pendingAt = at
	f.pending += dir
	if f.pending > framePendingCap {
		f.pending = framePendingCap
	}
	if f.pending < -framePendingCap {
		f.pending = -framePendingCap
	}
}

// covers reports whether `at` lies inside the known window. The empty
// window covers only 0, which is where every input starts.
func (f *frameIndex) covers(at time.Duration) bool {
	return f.windowStart <= at && at <= f.windowEnd
}

// applyWindow merges a fetched window and applies any queued steps.
func (f *frameIndex) applyWindow(from, to time.Duration, times []time.Duration, duration time.Duration) (time.Duration, frameStepState) {
	f.inflight = false
	if from <= f.windowEnd && to >= f.windowStart {
		// Overlapping/adjacent: merge into one sorted list over the
		// union range.
		merged := make([]time.Duration, 0, len(f.times)+len(times))
		merged = append(merged, f.times...)
		merged = append(merged, times...)
		sort.Slice(merged, func(i, j int) bool { return merged[i] < merged[j] })
		f.times = dedupe(merged)
		f.windowStart = minDuration(f.windowStart, from)
		f.windowEnd = maxDuration(f.windowEnd, to)
	} else {
		f.times = append([]time.Duration{}, times...)
		f.windowStart, f.windowEnd = from, to
	}
	if len(f.times) > f.capacity {
		drop := len(f.times) - f.capacity
		f.times = f.times[drop:]
		f.windowStart = f.times[0]
	}
	return f.applyPending(duration)
}

// applyError marks indexing failed: frame stepping becomes unavailable
// while time-based seeking and everything else keep working. Queued
// steps are consumed (the playhead does not jump).
func (f *frameIndex) applyError(err error) {
	f.inflight = false
	f.failed = true
	f.fetchErr = err
	f.pending = 0
}

// applyPending walks the queued steps; when the walk leaves the cached
// window, the remainder stays queued and the controller fetches the
// adjacent window.
func (f *frameIndex) applyPending(duration time.Duration) (time.Duration, frameStepState) {
	if f.pending == 0 {
		return 0, frameStepEdge
	}
	dir := 1
	if f.pending < 0 {
		dir = -1
	}
	remaining := f.pending
	if remaining < 0 {
		remaining = -remaining
	}
	at := f.pendingAt
	for remaining > 0 {
		if !f.covers(at) {
			f.pendingAt = at
			f.pending = remaining * dir
			return at, frameStepLoading
		}
		next, ok := lookupStep(f.times, at, dir)
		if !ok {
			if dir > 0 && f.windowEnd >= duration || dir < 0 && f.windowStart <= 0 {
				// The actual edge of the video: stop, queue empty.
				f.pending = 0
				return at, frameStepEdge
			}
			f.pendingAt = at
			f.pending = remaining * dir
			return at, frameStepLoading
		}
		at = next
		remaining--
	}
	f.pending = 0
	return at, frameStepMoved
}

// pendingCount exposes the queued steps for the loading status.
func (f *frameIndex) pendingCount() int { return f.pending }

// lookupStep returns the neighbor of `at` in direction dir from the
// sorted, deduplicated timestamp list. Pure function, table-tested.
func lookupStep(times []time.Duration, at time.Duration, dir int) (time.Duration, bool) {
	if dir > 0 {
		i := sort.Search(len(times), func(i int) bool { return times[i] > at })
		if i < len(times) {
			return times[i], true
		}
		return 0, false
	}
	i := sort.Search(len(times), func(i int) bool { return times[i] >= at })
	if i > 0 {
		return times[i-1], true
	}
	return 0, false
}

func dedupe(sorted []time.Duration) []time.Duration {
	out := sorted[:0]
	for i, v := range sorted {
		if i == 0 || v != sorted[i-1] {
			out = append(out, v)
		}
	}
	return out
}

func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}

func maxDuration(a, b time.Duration) time.Duration {
	if a > b {
		return a
	}
	return b
}

// ensureFrameIndex starts (single-flight) the frame-window fetch around
// `at`. Callers invoke it only after a step found no neighbor in the
// cached frames — the first step on a fresh input (whose empty initial
// window "covers" position 0) and steps past either cached edge. That
// is why coverage of `at` itself is not part of the guard: requiring it
// would block exactly those fetches and leave frame stepping stuck in
// the loading state forever.
func (c *Controller) ensureFrameIndex(at time.Duration) {
	index := c.previews.index
	if index.failed || index.inflight || c.inputPath == "" {
		return
	}
	from := at - frameWindowLen
	if from < 0 {
		from = 0
	}
	to := at + frameWindowLen
	if c.media.Duration > 0 && to > c.media.Duration {
		to = c.media.Duration
	}
	index.inflight = true
	ctx, cancel := context.WithCancel(context.Background())
	c.previews.indexCancel = cancel
	gen := c.inputGeneration
	path := c.inputPath
	probe := c.config.FFmpeg.FFprobePath
	go func() {
		times, _, err := c.previews.indexFetch(ctx, probe, path, from, to)
		c.dispatchUI(func() {
			if c.inputGeneration != gen {
				return
			}
			if err != nil {
				c.previews.index.applyError(err)
				c.setPreviewStatus("Frame-accurate stepping unavailable: " + err.Error())
				return
			}
			at, state := c.previews.index.applyWindow(from, to, times, c.media.Duration)
			c.applyFrameStep(at, state)
		})
	}()
}

// applyFrameStep moves the committed playhead when a queued walk
// finished, keeps the loading status fresh, and requests the preview
// for the new committed position.
func (c *Controller) applyFrameStep(at time.Duration, state frameStepState) {
	switch state {
	case frameStepMoved:
		c.setPreviewStatus("")
		c.trimTimeline.SetPlayhead(at)
		c.requestPreview(at)
	case frameStepLoading:
		if pending := c.previews.index.pendingCount(); pending != 0 {
			c.setPreviewStatus(fmt.Sprintf("Loading frame index… (%d frames queued)", absInt(pending)))
		} else {
			c.setPreviewStatus("Loading frame index…")
		}
		c.ensureFrameIndex(at)
	case frameStepEdge:
		c.setPreviewStatus("")
	case frameStepUnavailable:
		// status already set by applyError
	}
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// --- lifecycle ---

// cancelTrimWork stops every trim-related timer and subprocess context.
// Called on input change and before the application quits so no
// extraction outlives the UI.
func (c *Controller) cancelTrimWork() {
	if c.previews.previewTimer != nil {
		c.previews.previewTimer.Stop()
		c.previews.previewTimer = nil
	}
	if c.previews.hoverTimer != nil {
		c.previews.hoverTimer.Stop()
		c.previews.hoverTimer = nil
	}
	if c.previews.previewCancel != nil {
		c.previews.previewCancel()
		c.previews.previewCancel = nil
	}
	if c.previews.stripCancel != nil {
		c.previews.stripCancel()
		c.previews.stripCancel = nil
	}
	if c.previews.indexCancel != nil {
		c.previews.indexCancel()
		c.previews.indexCancel = nil
	}
}

// trimPreviews owns every async trim resource: extraction, caches,
// timers, and cancellation. Fields are touched on the UI thread only
// (timer callbacks hop via dispatchUI); goroutines capture what they
// need before launch.
type trimPreviews struct {
	extractor     *frames.Extractor
	extractorPath string
	cache         *frames.Cache
	index         *frameIndex

	previewTimer  *time.Timer
	hoverTimer    *time.Timer
	previewCancel context.CancelFunc
	stripCancel   context.CancelFunc
	indexCancel   context.CancelFunc

	// indexFetch reads one frame-timestamp window; seam over
	// ffprobe.FrameTimes so tests can intercept the fetch.
	indexFetch func(ctx context.Context, executable, inputPath string, from, to time.Duration) ([]time.Duration, time.Duration, error)

	seq          uint64 // newest request wins the display
	stripRunning bool
}

func newTrimPreviews() trimPreviews {
	return trimPreviews{
		cache:      frames.NewCache(previewCacheBytes, previewMaxEntryBytes),
		index:      newFrameIndex(),
		indexFetch: ffprobe.FrameTimes,
	}
}

// --- timestamps ---

// formatTimestamp renders a duration as HH:MM:SS.mmm (rounded to
// milliseconds) — the Trim tab's boundary field format.
func formatTimestamp(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	ms := int64(d / time.Millisecond)
	return fmt.Sprintf("%02d:%02d:%02d.%03d", ms/3_600_000, (ms/60_000)%60, (ms/1_000)%60, ms%1000)
}

// parseTimestamp parses [H:]MM:SS[.mmm] with minutes/seconds in
// 0..59 and milliseconds in 0..999. Anything else (empty, negative,
// out-of-range fields, trailing junk) is an error.
func parseTimestamp(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, errors.New("empty timecode")
	}
	parts := strings.Split(s, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return 0, fmt.Errorf("invalid timecode %q", s)
	}
	var hours int64
	minutes, seconds := parts[len(parts)-2], parts[len(parts)-1]
	if len(parts) == 3 {
		parsed, err := strconv.ParseInt(parts[0], 10, 64)
		if err != nil || parsed < 0 {
			return 0, fmt.Errorf("invalid hours in %q", s)
		}
		hours = parsed
	}
	min, err := parseTimeField(minutes, 59)
	if err != nil {
		return 0, fmt.Errorf("invalid minutes in %q", s)
	}
	secText, millis := seconds, int64(0)
	if dot := strings.Index(seconds, "."); dot >= 0 {
		secText = seconds[:dot]
		frac := seconds[dot+1:]
		if len(frac) == 0 || len(frac) > 3 {
			return 0, fmt.Errorf("invalid fraction in %q", s)
		}
		for _, ch := range frac {
			if ch < '0' || ch > '9' {
				return 0, fmt.Errorf("invalid fraction in %q", s)
			}
		}
		parsed, _ := strconv.ParseInt(frac, 10, 64)
		for scale := int64(len(frac)); scale < 3; scale++ {
			parsed *= 10
		}
		millis = parsed
	}
	sec, err := parseTimeField(secText, 59)
	if err != nil {
		return 0, fmt.Errorf("invalid seconds in %q", s)
	}
	if millis > 999 {
		return 0, fmt.Errorf("invalid milliseconds in %q", s)
	}
	return time.Duration(hours*3_600_000+min*60_000+sec*1_000+millis) * time.Millisecond, nil
}

func parseTimeField(text string, maxValue int64) (int64, error) {
	if text == "" {
		return 0, errors.New("empty time field")
	}
	parsed, err := strconv.ParseInt(text, 10, 64)
	if err != nil || parsed < 0 || parsed > maxValue {
		return 0, fmt.Errorf("field %q out of range", text)
	}
	return parsed, nil
}
