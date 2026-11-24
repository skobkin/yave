package ui

import (
	"context"
	"errors"
	"fmt"
	"image"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

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
	previewMaxEntryBytes = 4 << 20                // a single decoded preview frame (640-wide portrait ≈ 2.9 MiB)
	previewImageWidth    = 640                    // the preview cell expands with the window; 320 would upscale
	previewVeilAlpha     = 0x40                   // foreground veil tinting the stale frame while a fresh one extracts
	thumbStripCount      = 10                     // target cells across the visible window
	stripViewDebounce    = 150 * time.Millisecond // zoom/scroll storms rebuild the strip once
	thumbCacheCap        = 600                    // decoded thumbnails kept across zoom levels
	thumbStripHeight     = 72                     // matches the raised timelineMinHeight so cells do not upscale
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

// trimUnavailableReason explains why "Enable trimming" is disabled, or
// returns "" when trimming is available (a known input duration).
func trimUnavailableReason(inputPath string, inspecting bool, media model.MediaInfo) string {
	switch {
	case inputPath == "":
		return "Open a video on the Input tab to cut it"
	case inspecting:
		return "Inspecting input…"
	case media.Duration > 0:
		return ""
	case len(media.VideoStreams) == 0 && (len(media.AudioStreams) > 0 || len(media.CoverArtStreams) > 0):
		return "Input has no video stream"
	default:
		return "Input duration is unknown"
	}
}

// --- enable / reset ---

// setTrimControlsEnabled toggles every trim control except the enable
// checkbox itself. The fyne.Disableable assertion covers the timeline,
// both boundary entries, and the Set In/Out/Reset buttons.
func (c *Controller) setTrimControlsEnabled(enabled bool) {
	for _, control := range c.trimControls {
		if d, ok := control.(fyne.Disableable); ok {
			if enabled {
				d.Enable()
			} else {
				d.Disable()
			}
		}
	}
}

// setTrimEnabled flips trimming on or off. First enable seeds the
// selection to the full range and lazily starts the thumbnail strip and
// the initial preview — no FFmpeg preview process ever runs merely
// because an input was selected.
func (c *Controller) setTrimEnabled(enabled bool) {
	if c.trimEnabled == enabled {
		return
	}
	c.trimEnabled = enabled
	if !enabled {
		// Disabling retires all pending preview work immediately: the
		// launch guards would otherwise silently drop a pending request
		// and leave the stale-frame indication stuck on. The frame cache
		// and the thumbnail strip are untouched.
		c.cancelPreviewWork()
	}
	c.setTrimControlsEnabled(enabled)
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
	c.cancelPlayback()
	c.trimSelection = model.TrimSelection{
		Segments: []model.TimeRange{{Start: 0, End: c.media.Duration}},
	}
	c.trimSelected = 0
	c.trimInValid, c.trimOutValid = true, true
	c.trimTimeline.SetDuration(c.media.Duration)
	c.trimTimeline.SetPlayhead(0)
	c.trimTimeline.SetSelected(0)
	c.trimTimeline.SetSegments([]model.TimeRange{{Start: 0, End: c.media.Duration}})
	c.setBoundaryDisplay(c.trimIn, formatTimestamp(0))
	c.setBoundaryDisplay(c.trimOut, formatTimestamp(c.media.Duration))
	c.updateTrimDurationLabel()
	// SetPlayhead deliberately fires no callback, so request the preview
	// at the reset position explicitly: Reset is a committed move and the
	// Frame preview must follow it. The monotonic request sequence also
	// retires any in-flight hover or explicit result from before. As a
	// committed move it also retires any queued frame walk, so a landing
	// fetch cannot yank the playhead off 0.
	c.previews.index.pending = 0
	c.requestPreview(c.trimTimeline.Playhead())
}

// resetTrimForNewInput clears every per-input trim state. Called
// synchronously from setInput so stale selections, thumbnails, or
// previews cannot survive an input change.
func (c *Controller) resetTrimForNewInput() {
	c.trimEnabled = false
	c.trimSelection = model.TrimSelection{}
	c.trimSelected = 0
	// The export mode is job-scoped like the selection: a new input
	// starts fresh on separate files. The dropdown mirrors it with its
	// callback detached — the reset is not a user edit, and changed()
	// would run a full control/preview rebuild mid-reset.
	c.trimExportMode = model.TrimExportSeparate
	onChanged := c.trimExport.OnChanged
	c.trimExport.OnChanged = nil
	c.trimExport.SetSelected(c.trimExportMode.String())
	c.trimExport.OnChanged = onChanged
	c.trimInValid, c.trimOutValid = true, true
	// SetChecked below fires setTrimEnabled(false), which early-returns
	// because trimEnabled is already false — the controls must be
	// disabled explicitly, or they stay editable with trimming off and
	// typing into them mutates the selection the next enable reuses.
	c.setTrimControlsEnabled(false)
	c.trimEnable.SetChecked(false)
	c.setBoundaryDisplay(c.trimIn, "")
	c.setBoundaryDisplay(c.trimOut, "")
	c.setTrimMessage("")
	c.trimDuration.SetText("")
	c.trimTimeline.SetDuration(0)
	c.trimTimeline.SetMarkers(markerChapter, nil)
	c.trimTimeline.SetMarkers(markerKeyframe, nil)
	c.previews.keyframes.clear()
	c.clearScenes()
	c.clearWaveform()
	c.trimTimeline.SetThumbnails(nil)
	c.trimTimeline.SetPlayhead(0)
	c.previewImage.Image = nil
	c.previewImage.Refresh()
	c.previewTimeLabel.SetText("")
	c.hoverTimeLabel.SetText("")
	c.previews.displayedAt = nil
	// The index is replaced before the status is cleared: the clear must
	// not re-surface the old input's indexing failure through the
	// setPreviewStatus guard, which would leave input A's diagnostic
	// displayed for input B until trimming is enabled again.
	c.previews.index = newFrameIndex()
	c.setPreviewStatus("")
	c.previews.cache.Clear()
	c.previews.stripRunning = false
	c.previews.stripGrid = stripGrid{}
	clear(c.previews.thumbCache)
	if c.previews.stripTimer != nil {
		c.previews.stripTimer.Stop()
		c.previews.stripTimer = nil
	}
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
// — so the stream-copy warning and the join-export checks stay
// synchronized across enable/disable, encoder and profile switches,
// export-mode changes, and input changes without depending on
// boundary-edit callbacks.
func (c *Controller) updateTrimMessages(encoder string, audio model.AudioSettings) {
	// Keyframe results arrive asynchronously and re-run this with the
	// inputs of the last refresh; the stream-copy flag follows them.
	c.trimMsgEncoder, c.trimMsgAudio = encoder, audio
	c.trimCopyVideo = isCopyVideo(encoder)
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
		// Join-mode export checks (video-copy rejection, copied-FLAC
		// encoder availability, audio-copy gap warning) merge here:
		// ValidateTrimExport is a no-op unless joining more than one
		// segment, so the separate-file mode adds nothing.
		result.Merge(validation.ValidateTrimExport(c.trimSelection, c.trimExportMode, &c.media, c.caps, encoder, audio))
		parts = append(parts, c.trimIssueLines(result)...)
		parts = append(parts, c.keyframeNotes()...)
	}
	c.setTrimMessage(strings.Join(parts, "\n"))
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

// setBoundaryDisplay writes display text into a boundary entry without
// firing its editing callback: Entry.SetText triggers OnChanged whenever
// the text differs, and the callback would re-parse the
// millisecond-truncated display text and overwrite the exact selection —
// frame-accurate boundaries (and the microsecond-precision seek times
// generated from them) would silently collapse to the displayed
// milliseconds on every keyboard shortcut, handle drag, and reset.
func (c *Controller) setBoundaryDisplay(e *widget.Entry, text string) {
	c.displayingBoundaries = true
	defer func() { c.displayingBoundaries = false }()
	e.SetText(text)
}

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
		c.trimSelection = withSegmentBoundary(c.trimSelection, c.trimSelected, at, -1)
		// Silent on purpose: SetSegments would echo the clamped coordinates
		// back through onSegments and overwrite the typed value — an
		// out-of-range entry would keep displaying its text while the
		// selection (and the generated command) silently used the
		// clamped one, with no validation error. The typed value stays
		// authoritative (updateTrimMessages surfaces ValidateTrim's
		// error) and the timeline shows the clamped view.
		c.trimTimeline.SetSegmentsSilent(c.trimSelection.Segments)
	case "out":
		c.trimOutValid = true
		c.trimSelection = withSegmentBoundary(c.trimSelection, c.trimSelected, -1, at)
		c.trimTimeline.SetSegmentsSilent(c.trimSelection.Segments)
	}
	c.updateTrimDurationLabel()
	c.refresh()
}

func (c *Controller) setInFromPlayhead() {
	if !c.trimEnabled {
		return
	}
	at := c.keyframeSnap(c.trimTimeline.Playhead(), tlIn)
	c.trimInValid = true
	c.trimSelection = withSegmentBoundary(c.trimSelection, c.trimSelected, at, -1)
	c.setBoundaryDisplay(c.trimIn, formatTimestamp(at))
	// Silent on purpose: a loud SetSegments fires onSegments, which marks
	// both entries valid (unblocking conversion while the opposite entry
	// is empty or invalid) and rewrites it from the last valid value,
	// discarding the user's in-progress edit. A single-boundary action
	// owns only its own entry and validity flag — the boundary-entry
	// path applies the same rule with SetSegmentsSilent.
	c.trimTimeline.SetSegmentsSilent(c.trimSelection.Segments)
	c.updateTrimDurationLabel()
	c.refresh()
}

func (c *Controller) setOutFromPlayhead() {
	if !c.trimEnabled {
		return
	}
	at := c.keyframeSnap(c.trimTimeline.Playhead(), tlOut)
	c.trimOutValid = true
	c.trimSelection = withSegmentBoundary(c.trimSelection, c.trimSelected, -1, at)
	c.setBoundaryDisplay(c.trimOut, formatTimestamp(at))
	c.trimTimeline.SetSegmentsSilent(c.trimSelection.Segments)
	c.updateTrimDurationLabel()
	c.refresh()
}

func (c *Controller) updateTrimDurationLabel() {
	if !c.trimSelection.Active() {
		c.trimDuration.SetText("")
		return
	}
	// The row already captions this label "Duration". A single segment
	// shows its bare timestamp; multiple segments show the selected
	// segment's duration against the total.
	if len(c.trimSelection.Segments) == 1 {
		c.trimDuration.SetText(formatTimestamp(c.trimSelection.Total()))
		return
	}
	selected := segmentBoundary(c.trimSelection, c.trimSelected, 1) -
		segmentBoundary(c.trimSelection, c.trimSelected, 0)
	c.trimDuration.SetText(formatTimestamp(selected) + " of " + formatTimestamp(c.trimSelection.Total()))
}

// --- segment management ---

// addTrimSegment inserts a new segment starting at the playhead, sized
// min(5s, room-to-the-next-constraint), and selects it. The playhead
// must sit in a genuine gap with room for a positive length: starting
// a segment at or inside an existing one (half-open: Start ≤ playhead <
// End) would overlap it, and any other impossible spot gets an
// explanatory message — the button never silently no-ops and never
// relocates the request to the nearest gap.
func (c *Controller) addTrimSegment() {
	if !c.trimEnabled || !c.trimSelection.Active() {
		return
	}
	at := c.trimTimeline.Playhead()
	segs := c.trimSelection.Segments
	if !sort.SliceIsSorted(segs, func(i, j int) bool { return segs[i].Start < segs[j].Start }) {
		// Typed boundary edits may disorder the list; that is already a
		// validation error, and inserting into a disordered list has no
		// well-defined meaning.
		c.setTrimMessage("Fix the invalid (overlapping or disordered) trim segments before adding another one.")
		return
	}
	nextStart, insertAt := c.media.Duration, len(segs)
	for i, seg := range segs {
		if seg.Start <= at && at < seg.End {
			c.setTrimMessage(fmt.Sprintf("The playhead is inside segment %d — move it into a gap to add a segment there.", i+1))
			return
		}
		if seg.Start > at {
			nextStart, insertAt = seg.Start, i
			break
		}
	}
	room := nextStart - at
	if room <= 0 {
		c.setTrimMessage("No room for a new segment at the playhead — move it earlier in the video.")
		return
	}
	seg := model.TimeRange{Start: at, End: at + min(5*time.Second, room)}
	segs = slices.Insert(slices.Clone(segs), insertAt, seg)
	c.trimSelection = model.TrimSelection{Segments: segs}
	// The new segment is the selected one; its boundaries become the
	// entries' content and both entries are valid by construction. The
	// segment list lands first: SetSelected clamps against the list
	// length, so selecting the new tail index needs the grown list in.
	c.trimSelected = insertAt
	c.trimInValid, c.trimOutValid = true, true
	c.trimTimeline.SetSegmentsSilent(segs)
	c.trimTimeline.SetSelected(insertAt)
	c.setBoundaryDisplay(c.trimIn, formatTimestamp(seg.Start))
	c.setBoundaryDisplay(c.trimOut, formatTimestamp(seg.End))
	c.updateTrimDurationLabel()
	c.refresh()
}

// deleteTrimSegment removes the selected segment (never the last one —
// the Delete button is disabled at one segment). The selection moves to
// the segment that took the deleted slot, or to the new last segment,
// and the entries mirror its boundaries.
func (c *Controller) deleteTrimSegment() {
	if !c.trimEnabled || !c.trimSelection.Active() || len(c.trimSelection.Segments) <= 1 {
		return
	}
	selected := c.trimSelected
	if selected < 0 || selected >= len(c.trimSelection.Segments) {
		selected = 0
	}
	segs := slices.Delete(slices.Clone(c.trimSelection.Segments), selected, selected+1)
	c.trimSelection = model.TrimSelection{Segments: segs}
	if c.trimSelected >= len(segs) {
		c.trimSelected = len(segs) - 1
	}
	c.trimInValid, c.trimOutValid = true, true
	c.trimTimeline.SetSegmentsSilent(segs)
	c.trimTimeline.SetSelected(c.trimSelected)
	seg := segs[c.trimSelected]
	c.setBoundaryDisplay(c.trimIn, formatTimestamp(seg.Start))
	c.setBoundaryDisplay(c.trimOut, formatTimestamp(seg.End))
	c.updateTrimDurationLabel()
	c.refresh()
}

// updateSegmentButtons keeps Delete's availability honest: deleting is
// a no-op with a single segment (one must always remain), so the button
// is disabled there rather than silently doing nothing. Called from
// refresh() — the recompute point every segment mutation flows through.
func (c *Controller) updateSegmentButtons() {
	// "Segments from chapters…" exists only for inputs that have chapters,
	// and works only while trimming is on (the box layout collapses it
	// otherwise).
	if len(c.media.Chapters) > 0 {
		c.trimChapters.Show()
	} else {
		c.trimChapters.Hide()
	}
	setEnabled(c.trimChapters, c.trimEnabled && len(c.media.Chapters) > 0)
	if c.trimEnabled && c.trimSelection.Active() && len(c.trimSelection.Segments) > 1 {
		c.trimDelete.Enable()
		return
	}
	c.trimDelete.Disable()
}

// withSegmentBoundary replaces the Start (start=-1 keeps it) or End of
// the selected segment, creating the segment when absent. It never
// re-sorts: a typed edit that would disorder the segments is a
// validation error, not something the UI hides.
func withSegmentBoundary(sel model.TrimSelection, selected int, start, end time.Duration) model.TrimSelection {
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
	if selected < 0 || selected >= len(sel.Segments) {
		selected = 0
	}
	if start >= 0 {
		sel.Segments[selected].Start = start
	}
	if end >= 0 {
		sel.Segments[selected].End = end
	}
	return sel
}

// segmentBoundary returns the selected segment's Start (which=0) or End
// (which=1).
func segmentBoundary(sel model.TrimSelection, selected, which int) time.Duration {
	if !sel.Active() {
		return 0
	}
	if selected < 0 || selected >= len(sel.Segments) {
		selected = 0
	}
	if which == 0 {
		return sel.Segments[selected].Start
	}
	return sel.Segments[selected].End
}

// --- timeline callbacks ---

// onTimelinePlayhead fires for committed playhead moves. The command
// preview does not depend on the playhead, so refresh() is deliberately
// not called here — dragging must not rebuild the preview per pixel.
func (c *Controller) onTimelinePlayhead(at time.Duration) {
	// While playing, a committed move is a seek: playback resumes from
	// the new position instead of requesting a static frame.
	if c.playbackActive() {
		c.seekPlayback(at)
		return
	}
	// A manual move supersedes any queued frame walk: when the in-flight
	// window lands it applies nothing (frameStepIdle), so the playhead
	// stays where the user put it instead of jumping back to the stale
	// walk position.
	if c.previews.index.pending != 0 {
		c.previews.index.pending = 0
		c.previews.index.pendingAt = at
		c.setPreviewStatus("")
	}
	c.requestPreview(at)
}

// focusedBoundaryEntry reports the boundary entry that currently holds
// keyboard focus, or nil. Focus is a canvas-level fact in Fyne; entries
// must be attached to a window for the query to resolve (as they are in
// the real layout).
func (c *Controller) focusedBoundaryEntry() fyne.Focusable {
	canvas := fyne.CurrentApp().Driver().CanvasForObject(c.trimIn)
	if canvas == nil {
		return nil
	}
	return canvas.Focused()
}

// onTimelineSegments commits new segment boundaries from a handle drag
// and from programmatic loud SetSegments calls (Reset, boundary
// shortcuts without a single-boundary handler). A drag (edited >= 0)
// commits only the segment whose handle moved: the timeline's copy
// displays clamped coordinates, and committing it wholesale would
// silently clamp away an out-of-range boundary typed into a different
// segment — the exact silent rewrite setTrimBoundary's SetSegmentsSilent
// call exists to prevent — and clear that entry's validation error. A
// programmatic echo (edited < 0) carries the whole authoritative copy
// back and commits it as-is. An entry that currently holds keyboard
// focus is the one being typed into — rewriting it mid-edit corrupts
// the value (valid prefixes like "00:00" parse and apply immediately,
// and the user's next keystrokes would land inside the reformatted
// text), so the focused entry keeps its in-flight text as authoritative
// and only the other entry mirrors the commit. The entries always
// describe the selected segment.
func (c *Controller) onTimelineSegments(segments []model.TimeRange, edited int) {
	c.trimInValid, c.trimOutValid = true, true
	if edited >= 0 && edited < len(segments) && edited < len(c.trimSelection.Segments) {
		c.trimSelection.Segments[edited] = segments[edited]
	} else {
		c.trimSelection = model.TrimSelection{Segments: segments}
	}
	selected := c.trimTimeline.Selected()
	c.trimSelected = selected
	if selected < len(segments) {
		focused := c.focusedBoundaryEntry()
		if focused != c.trimIn {
			c.setBoundaryDisplay(c.trimIn, formatTimestamp(segments[selected].Start))
		}
		if focused != c.trimOut {
			c.setBoundaryDisplay(c.trimOut, formatTimestamp(segments[selected].End))
		}
	}
	c.updateTrimDurationLabel()
	c.refresh()
}

// onTimelineSelected mirrors the newly selected segment's boundaries
// into the entries. A segment switch is a committed view change: any
// in-progress (possibly invalid) text belonged to the previous segment,
// so both entries revert to the stored boundaries via the display-write
// guard and validity flags reset — the stored segments are all valid by
// construction (invalid entries never mutate them).
func (c *Controller) onTimelineSelected(index int) {
	if !c.trimEnabled {
		return
	}
	c.trimSelected = index
	if index < len(c.trimSelection.Segments) {
		seg := c.trimSelection.Segments[index]
		c.trimInValid, c.trimOutValid = true, true
		c.setBoundaryDisplay(c.trimIn, formatTimestamp(seg.Start))
		c.setBoundaryDisplay(c.trimOut, formatTimestamp(seg.End))
	}
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
	text := formatTimestamp(*at)
	if title, ok := c.chapterTitleAt(*at); ok {
		text += "  ·  " + title
	}
	c.hoverTimeLabel.SetText(text)
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
		clear(c.previews.thumbCache)
	}
	return c.previews.extractor
}

// onFFmpegExecutableChanged reacts to a configured FFmpeg path change
// (settings save and detection): it eagerly drops the old extractor and
// its cached frames — results from the previous build must never
// satisfy a cache hit — cancels pending preview work, and refreshes the
// preview under the new executable while trimming stays enabled.
// Recovery does not depend on the old extraction returning: a hung old
// process cannot strand the pending indication. The re-request prefers
// the position the user is actually waiting for (the newest pending
// request) over the one currently displayed.
func (c *Controller) onFFmpegExecutableChanged() {
	if ext := c.previews.extractor; ext != nil && c.previews.extractorPath == c.config.FFmpeg.FFmpegPath {
		return // already bound to the configured executable
	}
	pending := c.previews.pendingAt // captured before cancel clears it
	shown := c.previews.displayedAt
	c.cancelPlayback() // the running decoder belongs to the old executable
	c.cancelPreviewWork()
	c.ensureExtractor() // path differs → rebuild + cache clear
	if !c.trimEnabled || c.inputPath == "" || c.media.Duration <= 0 {
		return
	}
	pos := c.trimTimeline.Playhead()
	if shown != nil {
		pos = *shown
	}
	if pending != nil {
		pos = *pending // the awaited frame wins over the shown one
	}
	pos = min(max(pos, 0), c.media.Duration)
	c.requestPreview(pos)
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
	c.previews.pendingAt = &at
	c.setPreviewPending(true)
	c.startPreviewTimer(&c.previews.previewTimer, previewDebounce, at)
}

// requestHover asks for the frame at a hovered position, quantized:
// transient hover previews may approximate, committed positions never
// do. Requests for the frame that is already pending or in flight
// coalesce — re-bumping the sequence per mouse-move would restart the
// debounce and retire the in-flight extraction for that very frame,
// postponing it indefinitely while the pointer stays in one bucket.
func (c *Controller) requestHover(at time.Duration) {
	if !c.trimEnabled || c.inputPath == "" || c.media.Duration <= 0 || c.playbackActive() {
		return
	}
	q := at / hoverQuantum * hoverQuantum
	if c.previews.pendingAt != nil && *c.previews.pendingAt == q {
		return
	}
	c.previews.seq++
	if img, ok := c.previews.cache.Get(q); ok {
		c.displayPreview(img, q)
		return
	}
	c.previews.pendingAt = &q
	c.setPreviewPending(true)
	c.startPreviewTimer(&c.previews.hoverTimer, hoverDebounce, q)
}

// restoreCommittedPreview returns the display to the committed playhead
// after a hover leaves: cache first, then a fresh explicit request.
func (c *Controller) restoreCommittedPreview() {
	if !c.trimEnabled || c.inputPath == "" || c.media.Duration <= 0 || c.playbackActive() {
		return
	}
	at := c.trimTimeline.Playhead()
	c.previews.seq++ // retire any in-flight hover result
	if img, ok := c.previews.cache.Get(at); ok {
		c.displayPreview(img, at)
		return
	}
	c.previews.pendingAt = &at
	c.setPreviewPending(true)
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
	fetch := c.previews.previewFetch
	go func() {
		frame, err := fetch(ctx, ext, path, at, previewImageWidth)
		c.dispatchUI(func() {
			if c.inputGeneration != gen {
				return // the new input's reset owns the display
			}
			if c.ensureExtractor() != ext {
				// The configured executable changed after this request
				// launched. onFFmpegExecutableChanged already handles
				// that proactively; this re-request only covers a change
				// that raced the old extraction's return — and only
				// while this request is still the newest one.
				if !c.trimEnabled || c.previews.seq != seq {
					return
				}
				c.requestPreview(at)
				return
			}
			if c.previews.seq != seq || !c.trimEnabled {
				return
			}
			if err != nil {
				// Nothing more is coming for this request; clearing the
				// pending target also lets the same bucket re-request
				// instead of coalescing a failure.
				c.previews.pendingAt = nil
				c.setPreviewPending(false)
				c.setPreviewStatus("Preview unavailable: " + err.Error())
				return
			}
			c.setPreviewStatus("")
			c.previews.cache.Put(at, frame.Image)
			c.displayPreview(frame.Image, at)
		})
	}()
}

// displayPreview shows a frame and resolves the pending request state.
// Every caller corresponds to the newest request (all call sites are
// seq-current), so this is the single place that clears the pending
// indication and target — covering cache hits in the request paths,
// launchPreview's own cache branch, and asynchronous success alike.
func (c *Controller) displayPreview(img image.Image, at time.Duration) {
	c.setPreviewPending(false)
	c.previews.pendingAt = nil
	c.previewImage.Image = img
	c.previewImage.Refresh()
	c.previewTimeLabel.SetText(formatTimestamp(at))
	c.previews.displayedAt = &at
}

// setPreviewPending toggles the frame-extraction cue: the stale frame
// tints under a translucent veil, a spinner starts spinning over the
// preview image's center, and the timeline's ghost hover marker turns
// accent-colored.
func (c *Controller) setPreviewPending(on bool) {
	if c.previews.pendingPreview == on {
		return
	}
	c.previews.pendingPreview = on
	if on {
		// Tint the stale frame with a translucent foreground veil.
		// Fading the image itself (Translucency) blends it into the
		// black letterbox behind it — invisible over dark footage —
		// while a foreground veil reads on any content and theme.
		c.previewVeil.FillColor = translucent(theme.Color(theme.ColorNameForeground), previewVeilAlpha)
		c.previewVeil.Hidden = false
		c.previewVeil.Refresh()
		// Show starts the rotation; the animation only ever runs while
		// the spinner is visible.
		c.previewSpinner.Show()
	} else {
		c.previewVeil.Hidden = true
		// Hide stops the rotation together with the widget.
		c.previewSpinner.Hide()
	}
	c.previewImage.Refresh()
	c.trimTimeline.SetPreviewPending(on)
}

// setTrimMessage updates the validation/entry-problem line. Empty text
// hides the label, so the idle message line costs no vertical space
// between the timeline and the boundary row (box layouts skip hidden
// children).
func (c *Controller) setTrimMessage(text string) {
	setStatusLabel(c.trimMessage, text)
}

// setPreviewStatus shows the transient extraction/indexing line; empty
// text clears it. Exception: while frame indexing has failed, clearing
// re-surfaces the failure instead — it persists for the input (stepping
// stays off, arrow presses no-op), and a successful preview after a
// Shift+arrow seek must not erase the only explanation of why. A new
// input replaces the index, so the failure and its message retire
// together.
func (c *Controller) setPreviewStatus(text string) {
	if index := c.previews.index; text == "" && index != nil && index.failed && index.fetchErr != nil {
		text = "Frame-accurate stepping unavailable: " + index.fetchErr.Error()
	}
	setStatusLabel(c.previewStatus, text)
}

// setStatusLabel shows the label only while it has text: hidden labels
// are skipped by Fyne's box layouts, collapsing their row entirely.
func setStatusLabel(label *widget.Label, text string) {
	label.SetText(text)
	if text == "" {
		label.Hide()
		return
	}
	label.Show()
}

// --- thumbnail strip ---

// scheduleStrip generates the timeline's thumbnails for the visible
// window: one input-side-seek ffmpeg process per uncached cell, at most
// thumbMaxWorkers in flight, cells rendering progressively. Cells sit on
// a stable time grid (stripGridFor), so thumbnails already extracted at
// the current zoom level are reused when the window scrolls or returns.
// A request for the grid already shown is a no-op; a different grid
// retires the previous generation's workers. It runs lazily — only after
// trimming is enabled — and again (debounced) when the view changes.
func (c *Controller) scheduleStrip() {
	if c.inputPath == "" || c.media.Duration <= 0 {
		return
	}
	start, end := c.trimTimeline.View().window()
	grid := stripGridFor(start, end, c.media.Duration)
	if grid.n == 0 || grid == c.previews.stripGrid {
		return
	}
	c.previews.stripGrid = grid
	c.previews.stripRunning = true
	c.trimTimeline.SetThumbnailGrid(grid.start, grid.cell, grid.n)
	ctx, job := c.previews.strip.start(context.Background())
	gen := c.inputGeneration
	ext := c.ensureExtractor()
	path := c.inputPath
	fetch := c.previews.thumbFetch
	if fetch == nil {
		fetch = func(ctx context.Context, ext *frames.Extractor, path string, at time.Duration, height int) (frames.Frame, error) {
			return ext.Thumbnail(ctx, path, at, height)
		}
	}
	if c.previews.thumbCache == nil {
		c.previews.thumbCache = map[time.Duration]image.Image{}
	}
	duration := c.media.Duration

	var wg sync.WaitGroup
	sem := make(chan struct{}, thumbMaxWorkers)
	for slot := range grid.n {
		at := grid.sampleAt(slot, duration)
		if img, ok := c.previews.thumbCache[at]; ok {
			c.trimTimeline.SetThumbnail(slot, img)
			continue
		}
		wg.Add(1)
		go func(slot int, at time.Duration) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			if ctx.Err() != nil {
				return // retired while queued: skip the process entirely
			}
			frame, err := fetch(ctx, ext, path, at, thumbStripHeight)
			c.dispatchUI(func() {
				if c.inputGeneration != gen || c.ensureExtractor() != ext || ctx.Err() != nil {
					return
				}
				if err != nil {
					return // the slot keeps its placeholder; the next grid retries
				}
				if len(c.previews.thumbCache) >= thumbCacheCap {
					clear(c.previews.thumbCache)
				}
				c.previews.thumbCache[at] = frame.Image
				c.trimTimeline.SetThumbnail(slot, frame.Image)
			})
		}(slot, at)
	}
	go func() {
		wg.Wait()
		c.dispatchUI(func() {
			if c.inputGeneration == gen && c.previews.strip.current(job) {
				c.previews.stripRunning = false
			}
		})
	}()
}

// onTimelineView reacts to a user zoom or scroll: the strip is rebuilt
// for the new window once the gesture settles, retiring stale work.
func (c *Controller) onTimelineView() {
	if !c.trimEnabled {
		return
	}
	if c.previews.stripTimer != nil {
		c.previews.stripTimer.Stop()
	}
	gen := c.inputGeneration
	c.previews.stripTimer = time.AfterFunc(stripViewDebounce, func() {
		c.dispatchUI(func() {
			if c.inputGeneration == gen && c.trimEnabled {
				c.scheduleStrip()
				c.requestKeyframes()
				c.refreshTrimMessages() // the "zoom in for keyframes" note follows the view
			}
		})
	})
}

// --- frame index (frame-accurate stepping) ---

// frameStepState is the outcome of one frame-step request.
type frameStepState uint8

const (
	frameStepMoved frameStepState = iota
	frameStepLoading
	frameStepEdge        // start/end of the video reached
	frameStepUnavailable // indexing failed; stepping is off
	frameStepIdle        // a landing fetch has no queued steps to apply
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
	videoEnd  time.Duration // video stream's end from the latest fetch (0 unknown)
}

// mediaEnd returns the position frame stepping may not pass: the video
// stream's end when the probe knows it, else the container duration.
// The container duration counts an audio tail as video still to come —
// walking to the edge on a 3s video beside a 1h audio track would
// otherwise chain ~1800 window fetches through the tail before
// reporting the edge.
func (f *frameIndex) mediaEnd(duration time.Duration) time.Duration {
	if f.videoEnd > 0 {
		return f.videoEnd
	}
	return duration
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
	if f.pending != 0 {
		// A walk is already in flight: the new press joins it and will be
		// applied from the walk position when the next window lands. It
		// must not take the immediate cached-neighbor shortcut below —
		// the displayed playhead is stale (loading never commits), so the
		// shortcut moves from the wrong position, commits it, and the
		// playhead callback clears the queued remainder: steps lost.
		f.queue(dir, f.pendingAt)
		if f.pending == 0 {
			// The opposite press cancelled the walk's remaining steps
			// exactly. The walk has already moved to pendingAt (every
			// applied step consumed queue), so commit that position
			// instead of reporting loading — a landing window would find
			// nothing queued (frameStepIdle), stranding the walk position
			// and leaving the loading status stuck. This is distinct from
			// retirement by a manual seek, which resets pendingAt to the
			// seek target before the fetch lands.
			return f.pendingAt, frameStepMoved
		}
		return f.pendingAt, frameStepLoading
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
	end := f.mediaEnd(duration)
	if dir > 0 && f.windowEnd >= end || dir < 0 && f.windowStart <= 0 {
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
// videoEnd is the video stream's end the fetch reported (0 unknown);
// the edge checks stop the walk there instead of at the container
// duration.
func (f *frameIndex) applyWindow(from, to time.Duration, times []time.Duration, duration, videoEnd time.Duration) (time.Duration, frameStepState) {
	f.inflight = false
	if videoEnd > 0 {
		f.videoEnd = videoEnd
	}
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
		// The retained slice must leave the pending walk able to move:
		// anchored at the walk position and extending in the walk
		// direction, so the neighbor applyPending needs survives the
		// trim. Keeping the half of the window the walk position lies in
		// (the earlier rule) looped on dense sources, where a single
		// fetch window holds more frames than the capacity (2500fps in a
		// 2s window vs 4096 entries): the kept half ended exactly at
		// pendingAt, the walk's neighbor fell in the evicted half, and
		// every landing re-fetched and re-evicted the identical range
		// forever. An earlier rule still — plain front/back eviction by
		// walk direction — dropped the frames a walk parked outside the
		// window had just fetched, the same loop shape. When no frame
		// sits at-or-across the walk position, the midpoint fallback
		// keeps the contiguous half around it.
		total := len(f.times)
		start, end := 0, total
		switch {
		case f.pending > 0:
			if p := lastFrameAtOrBefore(f.times, f.pendingAt); p >= 0 {
				start, end = p, min(p+f.capacity, total)
			}
		case f.pending < 0:
			if q := firstFrameAtOrAfter(f.times, f.pendingAt); q >= 0 {
				start, end = max(q+1-f.capacity, 0), q+1
			}
		default:
			drop := total - f.capacity
			if f.pendingAt <= (f.windowStart+f.windowEnd)/2 {
				end = total - drop
			} else {
				start = drop
			}
		}
		f.times = f.times[start:end]
		// A clamped edge marks the frames dropped beyond it as unknown
		// again; a whole kept side keeps the fetched bound, preserving
		// the knowledge that its gap holds no frames.
		if start > 0 {
			f.windowStart = f.times[0]
		}
		if end < total {
			f.windowEnd = f.times[len(f.times)-1]
		}
	}
	return f.applyPending(duration)
}

// lastFrameAtOrBefore returns the index of the last cached frame at or
// before `at`, or -1 when every frame lies after it. times is sorted.
func lastFrameAtOrBefore(times []time.Duration, at time.Duration) int {
	return sort.Search(len(times), func(i int) bool { return times[i] > at }) - 1
}

// firstFrameAtOrAfter returns the index of the first cached frame at or
// after `at`, or -1 when every frame lies before it. times is sorted.
func firstFrameAtOrAfter(times []time.Duration, at time.Duration) int {
	i := sort.Search(len(times), func(i int) bool { return times[i] >= at })
	if i == len(times) {
		return -1
	}
	return i
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
		// A fetch landed after the queue was retired (manual seek, reset)
		// or nothing was ever queued: nothing to apply — and the zero
		// position is not real movement, so it must not be reported as an
		// edge the controller would commit.
		return 0, frameStepIdle
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
			end := f.mediaEnd(duration)
			if dir > 0 && f.windowEnd >= end || dir < 0 && f.windowStart <= 0 {
				// The actual edge of the video: stop, queue empty.
				f.pending = 0
				return at, frameStepEdge
			}
			// The walk position is covered but has no neighbor: the next
			// frame lies across a gap longer than the window, or after an
			// audio tail beyond the last video frame. Return the window
			// edge as the fetch anchor so the controller's next fetch
			// extends outward — fetching around the unchanged walk
			// position would re-probe the same range forever. The walk
			// itself resumes from pendingAt (the real position), so the
			// eventual edge commit cannot drift off the last frame.
			f.pendingAt = at
			f.pending = remaining * dir
			if dir > 0 {
				return f.windowEnd, frameStepLoading
			}
			return f.windowStart, frameStepLoading
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
		times, videoEnd, err := c.previews.indexFetch(ctx, probe, path, from, to)
		c.dispatchUI(func() {
			if c.inputGeneration != gen {
				return
			}
			if err != nil {
				c.previews.index.applyError(err)
				c.setPreviewStatus("Frame-accurate stepping unavailable: " + err.Error())
				return
			}
			at, state := c.previews.index.applyWindow(from, to, times, c.media.Duration, videoEnd)
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
		// The walk may have moved partway before the video's actual edge
		// (queued steps beyond the last frame): commit that partial
		// movement instead of discarding it. A direct edge from step()
		// reports the current position — SetPlayhead is then a no-op and
		// the preview request a cheap cache hit.
		c.setPreviewStatus("")
		c.trimTimeline.SetPlayhead(at)
		c.requestPreview(at)
	case frameStepIdle:
		// Nothing queued; keep whatever status the latest action set.
	case frameStepUnavailable:
		// The failure message is already shown (set by applyError and
		// preserved by setPreviewStatus); pressing an unavailable step
		// re-asserts it so the no-op is never silent.
		c.setPreviewStatus("")
	}
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// --- lifecycle ---

// cancelPreviewWork stops the preview-specific async work: the debounce
// timers and the in-flight extraction, clearing the pending indication.
// It deliberately keeps the frame cache (disabling trimming or switching
// executables must not discard already-extracted frames) and leaves the
// thumbnail strip and frame index alone.
func (c *Controller) cancelPreviewWork() {
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
	c.previews.pendingAt = nil
	c.setPreviewPending(false)
}

// cancelTrimWork stops every trim-related timer and subprocess context.
// Called on input change and before the application quits so no
// extraction outlives the UI.
func (c *Controller) cancelTrimWork() {
	c.cancelPlayback()
	c.cancelPreviewWork()
	c.previews.keyframes.stop()
	c.scenes.job.stop()
	c.scenes.running = false
	c.waves.job.stop()
	c.waves.running = false
	c.previews.strip.stop()
	if c.previews.stripTimer != nil {
		c.previews.stripTimer.Stop()
		c.previews.stripTimer = nil
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
	indexCancel   context.CancelFunc

	// strip owns the thumbnail workers; stripTimer debounces view changes;
	// stripGrid is the grid currently shown (zero: none) and thumbCache
	// the decoded cells, keyed by sample time, reused across zoom levels.
	strip      bgJob
	stripTimer *time.Timer
	// keyframes holds the stream-copy keyframe index and its fetches.
	keyframes  keyframeState
	stripGrid  stripGrid
	thumbCache map[time.Duration]image.Image

	// indexFetch reads one frame-timestamp window; seam over
	// ffprobe.FrameTimes so tests can intercept the fetch. The second
	// value is the video stream's end (0 unknown) the walk stops at.
	indexFetch func(ctx context.Context, executable, inputPath string, from, to time.Duration) ([]time.Duration, time.Duration, error)
	// previewFetch extracts one preview frame; seam over
	// Extractor.Preview so tests can intercept (and block on) the fetch.
	previewFetch func(ctx context.Context, ext *frames.Extractor, path string, at time.Duration, maxWidth int) (frames.Frame, error)
	// thumbFetch extracts one strip thumbnail; seam over
	// Extractor.Thumbnail so tests can intercept (and block on) the fetch.
	thumbFetch func(ctx context.Context, ext *frames.Extractor, path string, at time.Duration, height int) (frames.Frame, error)

	seq uint64 // newest request wins the display

	// pendingPreview mirrors the visual pending cue (dimmed stale frame,
	// busy bar, accent ghost marker); pendingAt is the target of the
	// newest request while it is pending or in flight — quantized for
	// hover requests, exact for committed ones — doubling as the
	// hover-coalescing key and as the position restored across FFmpeg
	// changes. displayedAt is the timestamp currently shown. All three
	// are nil/false once the newest request resolved.
	pendingPreview bool
	pendingAt      *time.Duration
	displayedAt    *time.Duration

	stripRunning bool
}

func newTrimPreviews() trimPreviews {
	return trimPreviews{
		cache:      frames.NewCache(previewCacheBytes, previewMaxEntryBytes),
		index:      newFrameIndex(),
		indexFetch: ffprobe.FrameTimes,
		thumbCache: map[time.Duration]image.Image{},
		thumbFetch: func(ctx context.Context, ext *frames.Extractor, path string, at time.Duration, height int) (frames.Frame, error) {
			return ext.Thumbnail(ctx, path, at, height)
		},
		previewFetch: func(ctx context.Context, ext *frames.Extractor, path string, at time.Duration, maxWidth int) (frames.Frame, error) {
			return ext.Preview(ctx, path, at, maxWidth)
		},
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
// 0..59 and milliseconds in 0..999, or bare seconds (S[.mmm], where the
// seconds may exceed 59). Anything else (empty, negative, out-of-range
// fields, trailing junk) is an error.
func parseTimestamp(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, errors.New("empty timecode")
	}
	parts := strings.Split(s, ":")
	if len(parts) > 3 {
		return 0, fmt.Errorf("invalid timecode %q", s)
	}
	var hours int64
	// Bare seconds ("90.5") have no minutes field and may exceed 59.
	minutes, seconds := "0", parts[len(parts)-1]
	maxSeconds := int64(59)
	if len(parts) >= 2 {
		minutes = parts[len(parts)-2]
	} else {
		maxSeconds = maxBareSeconds
	}
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
	sec, err := parseTimeField(secText, maxSeconds)
	if err != nil {
		return 0, fmt.Errorf("invalid seconds in %q", s)
	}
	if millis > 999 {
		return 0, fmt.Errorf("invalid milliseconds in %q", s)
	}
	return time.Duration(hours*3_600_000+min*60_000+sec*1_000+millis) * time.Millisecond, nil
}

// maxBareSeconds bounds a bare-seconds entry (about 11 years) so the
// millisecond arithmetic in parseTimestamp cannot overflow.
const maxBareSeconds = 1 << 28

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
