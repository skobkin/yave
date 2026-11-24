package ui

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"git.skobk.in/skobkin/yave/internal/model"
	"git.skobk.in/skobkin/yave/internal/validation"
)

// gopFetch fakes a keyframe scan of a source with a 3s GOP: like the real
// scan it returns the keyframe the seek lands on (at or before from)
// followed by every keyframe up to to.
type gopFetch struct {
	mu     sync.Mutex
	ranges [][2]time.Duration
	err    error
	gop    time.Duration
	block  chan struct{} // when set, scans wait for it (or their context)
	// skipSeek makes the scan omit the keyframe preceding from, the
	// quirk that leaves a window's start unknown.
	skipSeek bool
}

func (g *gopFetch) calls() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.ranges)
}

func (g *gopFetch) fetch(ctx context.Context, _, _ string, from, to time.Duration) ([]time.Duration, error) {
	g.mu.Lock()
	g.ranges = append(g.ranges, [2]time.Duration{from, to})
	block, err, gop, skip := g.block, g.err, g.gop, g.skipSeek
	g.mu.Unlock()
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if err != nil {
		return nil, err
	}
	if gop == 0 {
		gop = 3 * time.Second
	}
	var out []time.Duration
	for at := from / gop * gop; at <= to; at += gop {
		if skip && at < from {
			continue
		}
		out = append(out, at)
	}
	return out, nil
}

// keyframeController is a trim controller in stream-copy mode over a
// duration-long input with video, using the fake scan.
func keyframeController(t *testing.T, duration time.Duration, g *gopFetch) (*Controller, *uiQueue) {
	t.Helper()
	c, q := stripController(t, okThumb)
	c.media = model.MediaInfo{
		Duration:     duration,
		VideoStreams: []model.VideoStream{{Index: 0, Codec: "h264"}},
	}
	c.trimTimeline.SetDuration(duration)
	c.previews.keyframes.fetch = g.fetch
	// refresh() recomputes the encoder from the profile widgets, so they
	// must agree with the stream-copy mode under test.
	c.videoEncoder.Selected = "copy"
	// Set the selection up with trimming off: resetTrimSelection refreshes,
	// which would start a keyframe scan before the test is ready for it.
	c.trimEnabled = false
	c.resetTrimSelection()
	c.trimEnabled = true
	c.updateTrimMessages("copy", model.AudioSettings{})
	return c, q
}

func TestKeyframeTicksOnlyForCopyProfiles(t *testing.T) {
	g := &gopFetch{}
	c, q := keyframeController(t, 100*time.Second, g)

	c.updateTrimMessages("libx264", model.AudioSettings{})
	c.updateKeyframes()
	if g.calls() != 0 || len(c.trimTimeline.Markers(markerKeyframe)) != 0 {
		t.Fatal("a re-encode profile must not scan keyframes or show ticks")
	}

	c.updateTrimMessages("copy", model.AudioSettings{})
	c.updateKeyframes()
	q.pump(t, "keyframe ticks", func() bool { return len(c.trimTimeline.Markers(markerKeyframe)) > 0 })
	got := c.trimTimeline.Markers(markerKeyframe)
	if got[0].At != 0 || got[1].At != 3*time.Second {
		t.Errorf("ticks = %v, want the 3s GOP starting at 0", got[:3])
	}

	// Switching away clears the layer.
	c.updateTrimMessages("libx264", model.AudioSettings{})
	c.updateKeyframes()
	if len(c.trimTimeline.Markers(markerKeyframe)) != 0 {
		t.Error("leaving stream copy must remove the ticks")
	}
}

func TestKeyframeTicksNeedVideoAndTrimming(t *testing.T) {
	g := &gopFetch{}
	c, _ := keyframeController(t, 100*time.Second, g)
	c.trimEnabled = false
	c.updateKeyframes()
	c.media.VideoStreams = nil
	c.trimEnabled = true
	c.updateKeyframes()
	if g.calls() != 0 {
		t.Errorf("%d scans for an input without video / with trimming off", g.calls())
	}
}

func TestKeyframeFetchIsLazyAndBoundedByTheZoom(t *testing.T) {
	g := &gopFetch{}
	c, q := keyframeController(t, 30*time.Minute, g)
	c.refreshTrimMessages()

	c.updateKeyframes() // fitted: 30 minutes visible, far beyond the limit
	if g.calls() != 0 {
		t.Fatal("no scan while more than 10 minutes are visible")
	}
	if got := c.trimMessage.Text; !strings.Contains(got, "zoom in to see keyframe ticks") {
		t.Errorf("message = %q, want the zoom hint", got)
	}

	c.trimTimeline.SetView(timelineView{duration: 30 * time.Minute, start: 10 * time.Minute, span: 2 * time.Minute})
	c.updateKeyframes()
	q.pump(t, "the zoomed scan", func() bool { return len(c.trimTimeline.Markers(markerKeyframe)) > 0 })
	if g.calls() != 1 {
		t.Fatalf("scans = %d, want 1", g.calls())
	}
	r := g.ranges[0]
	if r[0] != 9*time.Minute || r[1] != 13*time.Minute { // the window plus half a window each side
		t.Errorf("scanned %s..%s, want 9m..13m", r[0], r[1])
	}
	c.refreshTrimMessages()
	if strings.Contains(c.trimMessage.Text, "zoom in") {
		t.Errorf("the hint must go away once ticks are available: %q", c.trimMessage.Text)
	}

	// Refreshing again, or moving within the fetched window, scans nothing.
	c.updateKeyframes()
	c.trimTimeline.SetView(timelineView{duration: 30 * time.Minute, start: 10*time.Minute + 20*time.Second, span: 2 * time.Minute})
	c.updateKeyframes()
	if g.calls() != 1 {
		t.Errorf("scans = %d after re-requests inside the fetched window, want still 1", g.calls())
	}
}

func TestKeyframeStaleScanIsIgnored(t *testing.T) {
	g := &gopFetch{block: make(chan struct{})}
	c, q := keyframeController(t, 30*time.Minute, g)
	c.trimTimeline.SetView(timelineView{duration: 30 * time.Minute, start: 0, span: 2 * time.Minute})
	c.updateKeyframes() // blocked scan #1

	// The view moves before it finishes: scan #1 is cancelled, #2 starts.
	c.trimTimeline.SetView(timelineView{duration: 30 * time.Minute, start: 20 * time.Minute, span: 2 * time.Minute})
	c.updateKeyframes()
	close(g.block)
	q.pump(t, "the second scan", func() bool { return len(c.trimTimeline.Markers(markerKeyframe)) > 0 })
	for _, m := range c.trimTimeline.Markers(markerKeyframe) {
		if m.At < 18*time.Minute {
			t.Fatalf("a cancelled scan leaked a marker at %s", m.At)
		}
	}
}

func TestKeyframeSnapForSetInAndOut(t *testing.T) {
	g := &gopFetch{}
	c, q := keyframeController(t, 100*time.Second, g)
	c.updateKeyframes()
	q.pump(t, "keyframes", func() bool { return len(c.trimTimeline.Markers(markerKeyframe)) > 0 })
	c.setBoundaryDisplay(c.trimOut, formatTimestamp(90*time.Second))
	c.trimSelection.Segments[0] = model.TimeRange{Start: 0, End: 90 * time.Second}
	c.trimTimeline.SetSegmentsSilent(c.trimSelection.Segments)

	// 1000px over 100s: the 8px reach is 0.8s.
	c.trimTimeline.SetPlayhead(9500 * time.Millisecond)
	c.setInFromPlayhead()
	if got := c.trimSelection.Segments[0].Start; got != 9*time.Second {
		t.Errorf("Set In at 9.5s = %s, want snapped back to the keyframe at 9s", got)
	}
	c.trimTimeline.SetPlayhead(11500 * time.Millisecond)
	c.setOutFromPlayhead()
	if got := c.trimSelection.Segments[0].End; got != 12*time.Second {
		t.Errorf("Set Out at 11.5s = %s, want snapped forward to the keyframe at 12s", got)
	}

	// Out of reach: untouched. In never snaps forward, Out never back.
	c.trimTimeline.SetPlayhead(10 * time.Second) // 1s from the keyframe at 9s: beyond the reach
	c.setInFromPlayhead()
	if got := c.trimSelection.Segments[0].Start; got != 10*time.Second {
		t.Errorf("Set In out of reach = %s, want exactly 10s", got)
	}
	c.trimTimeline.SetPlayhead(11800 * time.Millisecond) // 0.2s before the keyframe at 12s
	c.setInFromPlayhead()
	if got := c.trimSelection.Segments[0].Start; got != 11800*time.Millisecond {
		t.Errorf("Set In at 11.8s = %s: In must never snap forward (and 2.8s back is out of reach)", got)
	}
	c.trimTimeline.SetPlayhead(12200 * time.Millisecond) // 0.2s after the keyframe at 12s
	c.setOutFromPlayhead()
	if got := c.trimSelection.Segments[0].End; got != 12200*time.Millisecond {
		t.Errorf("Set Out at 12.2s = %s: Out must never snap back", got)
	}

	// Snap off and non-copy leave the playhead untouched.
	c.trimSnap.SetChecked(false)
	c.trimTimeline.SetPlayhead(9500 * time.Millisecond)
	c.setInFromPlayhead()
	if got := c.trimSelection.Segments[0].Start; got != 9500*time.Millisecond {
		t.Errorf("snap off: Set In = %s, want exactly 9.5s", got)
	}
	c.trimSnap.SetChecked(true)
	c.videoEncoder.Selected = "libx264"
	c.updateTrimMessages("libx264", model.AudioSettings{})
	c.setInFromPlayhead()
	if got := c.trimSelection.Segments[0].Start; got != 9500*time.Millisecond {
		t.Errorf("re-encode: Set In = %s, want exactly 9.5s", got)
	}
}

func TestActualCutReplacesTheGenericWarning(t *testing.T) {
	g := &gopFetch{}
	c, q := keyframeController(t, 100*time.Second, g)
	c.updateKeyframes()
	q.pump(t, "keyframes", func() bool { return len(c.trimTimeline.Markers(markerKeyframe)) > 0 })

	// Before any In is known the generic warning stands.
	c.trimSelection.Segments[0] = model.TimeRange{Start: 10 * time.Second, End: 40 * time.Second}
	c.refreshTrimMessages()
	msg := c.trimMessage.Text
	if strings.Contains(msg, validation.StreamCopyKeyframeWarning) {
		t.Fatalf("the generic warning must be replaced once the keyframe is known: %q", msg)
	}
	for _, want := range []string{"Warning:", "keyframe 00:00:09.000", "1.000 s before In", "the end follows Out"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message %q lacks %q", msg, want)
		}
	}

	// In on a keyframe: a note, not a warning.
	c.trimSelection.Segments[0].Start = 9 * time.Second
	c.refreshTrimMessages()
	msg = c.trimMessage.Text
	if strings.Contains(msg, "Warning:") || !strings.Contains(msg, "Note: stream copy: In is on a keyframe") {
		t.Errorf("exact cut message = %q", msg)
	}

	// Several segments name the selected one.
	c.trimSelection.Segments = []model.TimeRange{{Start: 0, End: 5 * time.Second}, {Start: 10 * time.Second, End: 20 * time.Second}}
	c.trimSelected = 1
	c.refreshTrimMessages()
	if msg := c.trimMessage.Text; !strings.Contains(msg, "segment 2: stream copy starts this cut at the keyframe 00:00:09.000") {
		t.Errorf("multi-segment message = %q", msg)
	}
}

func TestActualCutIsFetchedOnDemandAtAnyZoom(t *testing.T) {
	g := &gopFetch{}
	c, q := keyframeController(t, 30*time.Minute, g)
	c.trimTimeline.SetView(timelineView{duration: 30 * time.Minute, start: 0, span: 2 * time.Minute}) // far from In
	c.trimSelection.Segments[0] = model.TimeRange{Start: 20*time.Minute + 10*time.Second, End: 25 * time.Minute}
	c.updateKeyframes()
	q.pump(t, "the cut keyframe", func() bool {
		return strings.Contains(c.trimMessage.Text, "keyframe 00:20:09.000")
	})
	var cutScan [2]time.Duration
	for _, r := range g.ranges {
		if r[0] == 20*time.Minute+10*time.Second {
			cutScan = r
		}
	}
	if cutScan[1]-cutScan[0] != keyframeLookahead {
		t.Errorf("the cut lookup scanned %v, want a %s window starting at In", cutScan, keyframeLookahead)
	}
}

func TestMissingPrecedingKeyframeDoesNotLoopAndKeepsTheWarning(t *testing.T) {
	g := &gopFetch{skipSeek: true} // scans never include the keyframe before from
	c, q := keyframeController(t, 30*time.Minute, g)
	// In lies far outside the visible window, so only the on-demand
	// lookup can answer — and this scan omits the keyframe before In.
	c.trimTimeline.SetView(timelineView{duration: 30 * time.Minute, start: 0, span: 2 * time.Minute})
	c.trimSelection.Segments[0] = model.TimeRange{Start: 20*time.Minute + 10*time.Second, End: 25 * time.Minute}
	c.updateKeyframes()
	cutScans := func() int {
		n := 0
		g.mu.Lock()
		defer g.mu.Unlock()
		for _, r := range g.ranges {
			if r[1]-r[0] == keyframeLookahead {
				n++
			}
		}
		return n
	}
	q.pump(t, "the cut lookup", func() bool { return cutScans() >= 1 })
	// Let the lookup land, then hammer refresh: it must not scan again.
	time.Sleep(2 * keyframeCutDebounce)
	for drained := false; !drained; {
		select {
		case f := <-q.ch:
			f()
		default:
			drained = true
		}
	}
	for range 10 {
		c.updateKeyframes()
	}
	time.Sleep(2 * keyframeCutDebounce)
	if got := cutScans(); got != 1 {
		t.Fatalf("cut lookups = %d, want exactly 1 for a window that cannot answer", got)
	}
	if !strings.Contains(c.trimMessage.Text, validation.StreamCopyKeyframeWarning) {
		t.Errorf("without a known keyframe the generic warning must stay: %q", c.trimMessage.Text)
	}
}

func TestKeyframeFailureDegradesToTheWarning(t *testing.T) {
	g := &gopFetch{err: errors.New("ffprobe exploded")}
	c, q := keyframeController(t, 100*time.Second, g)
	c.updateKeyframes()
	q.pump(t, "the failure", func() bool { return c.previews.keyframes.failed })

	msg := c.trimMessage.Text
	if !strings.Contains(msg, validation.StreamCopyKeyframeWarning) || !strings.Contains(msg, "ffprobe exploded") {
		t.Errorf("message = %q, want the generic warning plus the diagnostic", msg)
	}
	if len(c.trimTimeline.Markers(markerKeyframe)) != 0 {
		t.Error("no ticks after a failure")
	}
	n := g.calls()
	for range 5 {
		c.updateKeyframes()
	}
	if g.calls() != n {
		t.Error("a failed input must not be re-scanned on every refresh")
	}
	// Set In keeps working, unsnapped.
	c.trimTimeline.SetPlayhead(9500 * time.Millisecond)
	c.setInFromPlayhead()
	if got := c.trimSelection.Segments[0].Start; got != 9500*time.Millisecond {
		t.Errorf("Set In after a failure = %s, want exactly 9.5s", got)
	}

	// Leaving stream copy and coming back retries.
	g.mu.Lock()
	g.err = nil
	g.mu.Unlock()
	c.updateTrimMessages("libx264", model.AudioSettings{})
	c.updateKeyframes()
	c.updateTrimMessages("copy", model.AudioSettings{})
	c.updateKeyframes()
	q.pump(t, "the retry", func() bool { return len(c.trimTimeline.Markers(markerKeyframe)) > 0 })
}

func TestNewInputClearsKeyframes(t *testing.T) {
	g := &gopFetch{}
	c, q := keyframeController(t, 100*time.Second, g)
	c.updateKeyframes()
	q.pump(t, "keyframes", func() bool { return len(c.trimTimeline.Markers(markerKeyframe)) > 0 })

	c.resetTrimForNewInput()
	if len(c.trimTimeline.Markers(markerKeyframe)) != 0 || len(c.previews.keyframes.index.all()) != 0 {
		t.Error("a new input must drop the old input's keyframes")
	}
	if c.previews.keyframes.failed {
		t.Error("a new input starts without a failure")
	}
}

func TestWarningStaysGenericForReencodeAndMessagesRespectTrimOff(t *testing.T) {
	g := &gopFetch{}
	c, _ := keyframeController(t, 100*time.Second, g)
	c.updateTrimMessages("libx264", model.AudioSettings{})
	if strings.Contains(c.trimMessage.Text, "stream copy") {
		t.Errorf("a re-encode must say nothing about stream copy: %q", c.trimMessage.Text)
	}
	c.trimEnabled = false
	c.updateTrimMessages("copy", model.AudioSettings{})
	if c.trimMessage.Text != "" {
		t.Errorf("trimming off: message = %q, want none", c.trimMessage.Text)
	}
}
