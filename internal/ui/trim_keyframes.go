package ui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"git.skobk.in/skobkin/yave/internal/ffprobe"
	"git.skobk.in/skobkin/yave/internal/validation"
)

// Stream copy cannot cut between keyframes: ffmpeg starts the output at
// the keyframe at or before the selected In (measured against real MKV
// and MP4 cuts — the output of -ss 4 on a 3s-GOP file starts at the 3s
// keyframe), while -t ends the cut at the selected Out to within a few
// reordered frames. The keyframe layer therefore draws keyframe ticks for
// copy profiles, snaps boundary gestures to them, and replaces the
// generic warning with the concrete start time.
const (
	// keyframeMaxSpan is the widest visible range for which ticks are
	// fetched: a packet scan demuxes the whole window, which gets slow on
	// high-bitrate files.
	keyframeMaxSpan = 10 * time.Minute
	// keyframeLookahead is the size of the on-demand window fetched at In:
	// the scan starts at the keyframe preceding In whatever its length.
	keyframeLookahead   = time.Second
	keyframeCutDebounce = 200 * time.Millisecond
)

// keyframeState owns the keyframe index and its fetches. UI thread only.
type keyframeState struct {
	index keyframeIndex
	// done records every window a fetch completed for (data-free spans),
	// so a window whose coverage stays partial — a seek that missed the
	// preceding keyframe — is not fetched again on every refresh.
	done keyframeIndex

	view        bgJob // the visible-range fetch
	pending     bool
	pendingFrom time.Duration
	pendingTo   time.Duration

	cut      bgJob // the fetch around the selected segment's In
	cutTimer *time.Timer
	cutTried time.Duration // last In a fetch was attempted for
	cutKnown bool

	failed bool
	err    error

	// fetch scans one window; seam over ffprobe.KeyframeTimes so tests
	// can intercept it.
	fetch func(ctx context.Context, executable, inputPath string, from, to time.Duration) ([]time.Duration, error)
}

func (k *keyframeState) scan() func(context.Context, string, string, time.Duration, time.Duration) ([]time.Duration, error) {
	if k.fetch != nil {
		return k.fetch
	}
	return ffprobe.KeyframeTimes
}

// stop cancels the fetches and timers; the index is kept.
func (k *keyframeState) stop() {
	k.view.stop()
	k.cut.stop()
	k.pending = false
	if k.cutTimer != nil {
		k.cutTimer.Stop()
		k.cutTimer = nil
	}
}

// clear forgets everything for a new input.
func (k *keyframeState) clear() {
	k.stop()
	k.index.reset()
	k.done.reset()
	k.failed, k.err = false, nil
	k.cutKnown = false
}

// isCopyVideo reports whether the effective video encoder is stream copy.
func isCopyVideo(encoder string) bool {
	return strings.EqualFold(strings.TrimSpace(encoder), "copy")
}

// keyframesWanted reports whether keyframe work applies right now: a
// stream-copy profile on an input with video, while trimming is on.
func (c *Controller) keyframesWanted() bool {
	if !c.trimEnabled || !c.trimCopyVideo || c.inputPath == "" || c.media.Duration <= 0 {
		return false
	}
	_, hasVideo := c.media.PrimaryVideo()
	return hasVideo
}

// updateKeyframes reconciles the keyframe layer with the current state.
// It runs from refresh(), so every request below is a cheap no-op when
// nothing changed.
func (c *Controller) updateKeyframes() {
	k := &c.previews.keyframes
	if !c.keyframesWanted() {
		if len(c.trimTimeline.Markers(markerKeyframe)) > 0 {
			c.trimTimeline.SetMarkers(markerKeyframe, nil)
		}
		k.stop()
		// Leaving stream copy (or trimming) lets a later return retry a
		// failed read instead of staying degraded for the whole input.
		k.failed, k.err = false, nil
		return
	}
	c.requestKeyframes()
	c.requestCutKeyframe()
}

// keyframeWindow returns the range ticks are fetched for: the visible
// window plus half a window of margin on each side, and whether ticks are
// available at this zoom at all.
func (c *Controller) keyframeWindow() (from, to time.Duration, ok bool) {
	start, end := c.trimTimeline.View().window()
	span := end - start
	if span <= 0 || span > keyframeMaxSpan {
		return 0, 0, false
	}
	from, to = max(start-span/2, 0), min(end+span/2, c.media.Duration)
	return from, to, to > from
}

// requestKeyframes fetches the keyframes of the visible range unless they
// are known (or being fetched). Stale fetches are cancelled when the view
// moves on.
func (c *Controller) requestKeyframes() {
	k := &c.previews.keyframes
	if !c.keyframesWanted() || k.failed {
		return
	}
	from, to, ok := c.keyframeWindow()
	if !ok || k.done.covers(from, to) {
		return
	}
	if k.pending && from >= k.pendingFrom && to <= k.pendingTo {
		return
	}
	ctx, job := k.view.start(context.Background())
	k.pending, k.pendingFrom, k.pendingTo = true, from, to
	gen := c.inputGeneration
	path, probe, scan := c.inputPath, c.config.FFmpeg.FFprobePath, k.scan()
	go func() {
		times, err := scan(ctx, probe, path, from, to)
		c.dispatchUI(func() {
			if c.inputGeneration != gen || !k.view.current(job) {
				return // a newer view or input owns the layer
			}
			k.pending = false
			if err != nil {
				c.keyframeFailed(err)
				return
			}
			c.applyKeyframes(from, to, times)
			c.requestKeyframes() // the view may have moved while this ran
		})
	}()
}

// requestCutKeyframe makes sure the keyframe at or before the selected
// segment's In is known, with a small debounced fetch of its own so it
// works at any zoom and survives view-driven cancellations.
func (c *Controller) requestCutKeyframe() {
	k := &c.previews.keyframes
	in, ok := c.selectedIn()
	if !ok || k.failed {
		return
	}
	if _, known := k.index.before(in); known {
		return
	}
	if k.cutKnown && k.cutTried == in {
		return // already attempted for this In; do not loop on a quirky window
	}
	k.cutTried, k.cutKnown = in, true
	if k.cutTimer != nil {
		k.cutTimer.Stop()
	}
	gen := c.inputGeneration
	k.cutTimer = time.AfterFunc(keyframeCutDebounce, func() {
		c.dispatchUI(func() {
			if c.inputGeneration != gen || !c.keyframesWanted() {
				return
			}
			c.fetchCutKeyframe(in)
		})
	})
}

func (c *Controller) fetchCutKeyframe(in time.Duration) {
	k := &c.previews.keyframes
	to := min(in+keyframeLookahead, c.media.Duration)
	if to <= in {
		return
	}
	ctx, job := k.cut.start(context.Background())
	gen := c.inputGeneration
	path, probe, scan := c.inputPath, c.config.FFmpeg.FFprobePath, k.scan()
	go func() {
		times, err := scan(ctx, probe, path, in, to)
		c.dispatchUI(func() {
			if c.inputGeneration != gen || !k.cut.current(job) {
				return
			}
			if err != nil {
				c.keyframeFailed(err)
				return
			}
			c.applyKeyframes(in, to, times)
		})
	}()
}

// applyKeyframes merges a fetched window and refreshes everything that
// shows keyframes.
func (c *Controller) applyKeyframes(from, to time.Duration, times []time.Duration) {
	k := &c.previews.keyframes
	k.index.add(from, to, times)
	k.done.add(from, to, nil)
	c.syncKeyframeMarkers()
	c.refreshTrimMessages()
}

// keyframeFailed records that keyframes cannot be read for this input:
// ticks and snapping stay off and the generic warning remains. Nothing is
// retried until the input or the profile changes.
func (c *Controller) keyframeFailed(err error) {
	k := &c.previews.keyframes
	k.failed, k.err = true, err
	k.pending = false
	c.syncKeyframeMarkers()
	c.refreshTrimMessages()
}

// syncKeyframeMarkers mirrors the known keyframes onto the timeline.
func (c *Controller) syncKeyframeMarkers() {
	if !c.keyframesWanted() {
		c.trimTimeline.SetMarkers(markerKeyframe, nil)
		return
	}
	all := c.previews.keyframes.index.all()
	markers := make([]timelineMarker, len(all))
	for i, at := range all {
		markers[i] = timelineMarker{At: at}
	}
	c.trimTimeline.SetMarkers(markerKeyframe, markers)
}

// selectedIn returns the selected segment's In, when there is one.
func (c *Controller) selectedIn() (time.Duration, bool) {
	if !c.trimSelection.Active() || c.trimSelected < 0 || c.trimSelected >= len(c.trimSelection.Segments) {
		return 0, false
	}
	return c.trimSelection.Segments[c.trimSelected].Start, true
}

// keyframeSnap applies directional keyframe snapping to a boundary taken
// from the playhead (Set In / Set Out, Ctrl+[ / Ctrl+]): In moves back to
// the keyframe at or before it, Out forward to the one at or after it,
// within the same on-screen reach as pointer snapping. Typed timecodes
// are never passed through here.
func (c *Controller) keyframeSnap(at time.Duration, kind tlDrag) time.Duration {
	if !c.trimTimeline.snapEnabled || !c.keyframesWanted() || c.previews.keyframes.failed {
		return at
	}
	reach := c.trimTimeline.snapReach()
	index := &c.previews.keyframes.index
	switch kind {
	case tlIn:
		if kf, ok := index.before(at); ok && at-kf <= reach {
			return kf
		}
	case tlOut:
		if kf, ok := index.after(at); ok && kf-at <= reach {
			return kf
		}
	}
	return at
}

// actualCutText describes where the selected segment's stream-copy cut
// really starts, or "" while that is not known. A cut whose In sits on a
// keyframe starts exactly there.
func (c *Controller) actualCutText() (text string, exact bool) {
	if !c.keyframesWanted() || c.previews.keyframes.failed {
		return "", false
	}
	in, ok := c.selectedIn()
	if !ok {
		return "", false
	}
	kf, known := c.previews.keyframes.index.before(in)
	if !known {
		return "", false
	}
	prefix := ""
	if len(c.trimSelection.Segments) > 1 {
		prefix = fmt.Sprintf("segment %d: ", c.trimSelected+1)
	}
	const tail = "the end follows Out to within a few frames"
	if early := in - kf; early >= time.Millisecond {
		return fmt.Sprintf("%sstream copy starts this cut at the keyframe %s, %.3f s before In; %s",
			prefix, formatTimestamp(kf), early.Seconds(), tail), false
	}
	return fmt.Sprintf("%sstream copy: In is on a keyframe, so the cut starts exactly there; %s", prefix, tail), true
}

// keyframeNotes returns the status lines the keyframe layer adds to the
// trim message: why ticks are missing, or that keyframes cannot be read.
func (c *Controller) keyframeNotes() []string {
	if !c.keyframesWanted() {
		return nil
	}
	if c.previews.keyframes.failed {
		return []string{"Note: keyframe positions could not be read: " + c.previews.keyframes.err.Error()}
	}
	if _, _, ok := c.keyframeWindow(); !ok {
		return []string{fmt.Sprintf("Note: zoom in to see keyframe ticks (more than %d minutes are visible)", int(keyframeMaxSpan/time.Minute))}
	}
	return nil
}

// trimIssueLines renders the trim validation result as message lines,
// replacing the generic stream-copy warning with the concrete cut
// description when it is known.
func (c *Controller) trimIssueLines(result validation.Result) []string {
	cut, exact := c.actualCutText()
	var lines []string
	for _, issue := range result.Errors {
		lines = append(lines, "Error: "+issue.Message)
	}
	for _, issue := range result.Warnings {
		switch {
		case issue.Message == validation.StreamCopyKeyframeWarning && cut != "" && exact:
			lines = append(lines, "Note: "+cut)
		case issue.Message == validation.StreamCopyKeyframeWarning && cut != "":
			lines = append(lines, "Warning: "+cut)
		default:
			lines = append(lines, "Warning: "+issue.Message)
		}
	}
	return lines
}

// refreshTrimMessages recomputes the trim message from the inputs the
// last refresh() used; asynchronous keyframe results call it so they do
// not need a full refresh.
func (c *Controller) refreshTrimMessages() {
	c.updateTrimMessages(c.trimMsgEncoder, c.trimMsgAudio)
}
