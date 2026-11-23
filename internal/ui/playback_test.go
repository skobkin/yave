package ui

import (
	"context"
	"errors"
	"image"
	"strings"
	"sync"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	fynetest "fyne.io/fyne/v2/test"

	"git.skobk.in/skobkin/yave/internal/model"
	"git.skobk.in/skobkin/yave/internal/playback"
	"git.skobk.in/skobkin/yave/internal/transcode"
)

// fakePlaybackSession is a controllable playbackSession. Like the real
// one it closes Frames and Done when cancelled or finished.
type fakePlaybackSession struct {
	opts   playback.Options
	frames chan playback.Frame
	done   chan struct{}

	mu        sync.Mutex
	cancelled bool
	err       error
	notice    string
	pos       time.Duration
	closeOnce sync.Once
}

func newFakePlaybackSession(o playback.Options) *fakePlaybackSession {
	return &fakePlaybackSession{opts: o, frames: make(chan playback.Frame, 8), done: make(chan struct{})}
}

func (f *fakePlaybackSession) Frames() <-chan playback.Frame { return f.frames }
func (f *fakePlaybackSession) Done() <-chan struct{}         { return f.done }
func (f *fakePlaybackSession) Position() time.Duration       { return f.pos }
func (f *fakePlaybackSession) Notice() string                { return f.notice }

func (f *fakePlaybackSession) Err() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.err
}

func (f *fakePlaybackSession) Cancel() {
	f.mu.Lock()
	f.cancelled = true
	f.mu.Unlock()
	f.finish(nil)
}

func (f *fakePlaybackSession) wasCancelled() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.cancelled
}

// finish ends the session as the real one would: frames close first,
// then done.
func (f *fakePlaybackSession) finish(err error) {
	f.closeOnce.Do(func() {
		f.mu.Lock()
		f.err = err
		f.mu.Unlock()
		close(f.frames)
		close(f.done)
	})
}

type playbackHarness struct {
	c        *Controller
	updates  chan func()
	mu       sync.Mutex
	sessions []*fakePlaybackSession
	startErr error
	notice   string
}

func newPlaybackHarness(t *testing.T) *playbackHarness {
	t.Helper()
	app := fynetest.NewApp()
	t.Cleanup(app.Quit)
	h := &playbackHarness{updates: make(chan func(), 256)}
	c := &Controller{
		media: model.MediaInfo{
			Duration:     10 * time.Minute,
			VideoStreams: []model.VideoStream{{Index: 0, Codec: "h264", FrameRate: "25/1"}},
			AudioStreams: []model.AudioStream{{Index: 1, Codec: "aac"}},
		},
		inputPath: "/tmp/input.mkv",
	}
	c.config.FFmpeg.FFmpegPath = "/usr/bin/ffmpeg"
	c.previews = newTrimPreviews()
	c.createWidgets()
	c.uiDispatch = func(f func()) { h.updates <- f }
	c.play.start = func(_ context.Context, o playback.Options, _ playback.AudioSink) (playbackSession, error) {
		h.mu.Lock()
		defer h.mu.Unlock()
		if h.startErr != nil {
			return nil, h.startErr
		}
		s := newFakePlaybackSession(o)
		s.notice = h.notice
		h.sessions = append(h.sessions, s)
		return s, nil
	}
	// Trimming on, with two segments; the second one is selected.
	c.trimEnabled = true
	c.setTrimControlsEnabled(true)
	c.trimSelection = model.TrimSelection{Segments: []model.TimeRange{
		{Start: 10 * time.Second, End: 20 * time.Second},
		{Start: 60 * time.Second, End: 90 * time.Second},
	}}
	c.trimSelected = 1
	c.trimTimeline.SetDuration(c.media.Duration)
	c.trimTimeline.SetSegmentsSilent(c.trimSelection.Segments)
	c.trimTimeline.SetSelected(1)
	c.trimTimeline.SetPlayhead(30 * time.Second)
	c.updatePlaybackControls()
	h.c = c
	return h
}

func (h *playbackHarness) session(t *testing.T, i int) *fakePlaybackSession {
	t.Helper()
	h.mu.Lock()
	defer h.mu.Unlock()
	if i >= len(h.sessions) {
		t.Fatalf("session %d was never started (have %d)", i, len(h.sessions))
	}
	return h.sessions[i]
}

func (h *playbackHarness) sessionCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.sessions)
}

// pump runs queued UI updates on the test goroutine until cond holds.
func (h *playbackHarness) pump(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		if cond() {
			return
		}
		select {
		case f := <-h.updates:
			f()
		case <-time.After(10 * time.Millisecond):
		case <-deadline:
			t.Fatal("timed out waiting for the UI state")
		}
	}
}

// drain runs whatever updates are already queued.
func (h *playbackHarness) drain() {
	for {
		select {
		case f := <-h.updates:
			f()
		case <-time.After(30 * time.Millisecond):
			return
		}
	}
}

func TestPlaybackStartsAtPlayheadAndShowsFrames(t *testing.T) {
	h := newPlaybackHarness(t)
	c := h.c

	c.togglePlayback()

	s := h.session(t, 0)
	want := playback.Options{
		FFmpegPath: "/usr/bin/ffmpeg", Input: "/tmp/input.mkv",
		From: 30 * time.Second, FrameRate: "25/1", HasAudio: true,
	}
	if s.opts != want {
		t.Fatalf("playback options = %+v, want %+v", s.opts, want)
	}
	if !c.playbackActive() || c.trimPlay.Text != "Pause" {
		t.Errorf("after Play: active=%v button=%q, want active with a Pause button", c.playbackActive(), c.trimPlay.Text)
	}

	frame := image.NewYCbCr(image.Rect(0, 0, 2, 2), image.YCbCrSubsampleRatio420)
	s.frames <- playback.Frame{At: 31 * time.Second, Image: frame}
	h.pump(t, func() bool { return c.trimTimeline.Playhead() == 31*time.Second })
	if c.previewImage.Image != image.Image(frame) {
		t.Error("the preview image must show the playback frame")
	}
	if got := c.previewTimeLabel.Text; got != "00:00:31.000" {
		t.Errorf("preview time label = %q, want 00:00:31.000", got)
	}
}

func TestPlaybackPauseKeepsPositionAndIgnoresLateFrames(t *testing.T) {
	h := newPlaybackHarness(t)
	c := h.c
	c.togglePlayback()
	s := h.session(t, 0)
	s.frames <- playback.Frame{At: 31 * time.Second, Image: image.NewYCbCr(image.Rect(0, 0, 2, 2), image.YCbCrSubsampleRatio420)}
	h.pump(t, func() bool { return c.trimTimeline.Playhead() == 31*time.Second })

	c.togglePlayback() // pause

	if !s.wasCancelled() {
		t.Error("pausing must cancel the session")
	}
	if c.playbackActive() || c.trimPlay.Text != "Play" {
		t.Errorf("after Pause: active=%v button=%q, want idle with a Play button", c.playbackActive(), c.trimPlay.Text)
	}
	if c.previews.pendingAt == nil || *c.previews.pendingAt != 31*time.Second {
		t.Errorf("pausing must request the exact frame at the playhead, pendingAt=%v", c.previews.pendingAt)
	}
	h.drain() // late events of the cancelled session must change nothing
	if got := c.trimTimeline.Playhead(); got != 31*time.Second {
		t.Errorf("playhead = %v after pause, want 31s", got)
	}
}

func TestPlaySelectionUsesSelectedSegmentBounds(t *testing.T) {
	h := newPlaybackHarness(t)
	h.c.playSelection()
	s := h.session(t, 0)
	if s.opts.From != 60*time.Second || s.opts.Until != 90*time.Second {
		t.Errorf("selection playback window = [%v, %v), want [60s, 90s)", s.opts.From, s.opts.Until)
	}
	if got := h.c.trimTimeline.Playhead(); got != 60*time.Second {
		t.Errorf("playhead = %v, want it moved to the segment start 60s", got)
	}
}

func TestPlaybackAtEndRestartsFromBeginning(t *testing.T) {
	h := newPlaybackHarness(t)
	h.c.trimTimeline.SetPlayhead(h.c.media.Duration)
	h.c.togglePlayback()
	if got := h.session(t, 0).opts.From; got != 0 {
		t.Errorf("Play at the end started from %v, want 0", got)
	}
}

func TestPlaybackNaturalEndRestoresControlsAndExactFrame(t *testing.T) {
	h := newPlaybackHarness(t)
	c := h.c
	c.togglePlayback()
	s := h.session(t, 0)

	s.finish(nil)
	h.pump(t, func() bool { return !c.playbackActive() })

	if c.trimPlay.Text != "Play" {
		t.Errorf("button = %q after the end, want Play", c.trimPlay.Text)
	}
	if c.previews.pendingAt == nil {
		t.Error("the end of playback must request the exact preview at the final position")
	}
}

func TestPlaybackFailureIsReportedAndKeepsTrimUsable(t *testing.T) {
	h := newPlaybackHarness(t)
	c := h.c
	c.togglePlayback()
	h.session(t, 0).finish(errors.New("ffmpeg playback decoder failed: exit status 1: Invalid data"))
	h.pump(t, func() bool { return !c.playbackActive() })

	if got := c.previewStatus.Text; !strings.Contains(got, "Playback stopped") || !strings.Contains(got, "Invalid data") {
		t.Errorf("preview status = %q, want the failure with FFmpeg's diagnostics", got)
	}
	c.togglePlayback() // can try again
	if h.sessionCount() != 2 {
		t.Errorf("a failed playback must not block the next attempt (sessions = %d)", h.sessionCount())
	}
}

func TestPlaybackStartFailureShowsStatus(t *testing.T) {
	h := newPlaybackHarness(t)
	h.startErr = errors.New("start video playback: exec: \"ffmpeg\": executable file not found")
	h.c.togglePlayback()
	if h.c.playbackActive() {
		t.Error("a failed start must leave playback idle")
	}
	if got := h.c.previewStatus.Text; !strings.Contains(got, "Playback unavailable") {
		t.Errorf("preview status = %q, want an unavailable notice", got)
	}
	if h.c.trimPlay.Text != "Play" {
		t.Errorf("button = %q, want Play", h.c.trimPlay.Text)
	}
}

func TestPlaybackNoticeIsShown(t *testing.T) {
	h := newPlaybackHarness(t)
	h.notice = "Playing without audio: open audio output: no device"
	h.c.togglePlayback()
	if got := h.c.previewStatus.Text; !strings.Contains(got, "Playing without audio") {
		t.Errorf("preview status = %q, want the video-only notice", got)
	}
	if !h.c.playbackActive() {
		t.Error("a notice must not stop playback")
	}
}

func TestPlaybackSeekRestartsAtNewPositionDebounced(t *testing.T) {
	h := newPlaybackHarness(t)
	c := h.c
	c.playSelection() // window [60s, 90s)
	first := h.session(t, 0)

	// A burst of committed moves, as a drag produces, restarts once.
	c.onTimelinePlayhead(65 * time.Second)
	c.onTimelinePlayhead(70 * time.Second)

	if !first.wasCancelled() {
		t.Error("a seek must silence the running session at once")
	}
	if !c.playbackActive() || c.trimPlay.Text != "Pause" {
		t.Errorf("during the seek debounce: active=%v button=%q, want still Pause", c.playbackActive(), c.trimPlay.Text)
	}
	h.pump(t, func() bool { return h.sessionCount() == 2 })
	h.drain()

	if h.sessionCount() != 2 {
		t.Fatalf("a seek burst started %d sessions, want 1 restart", h.sessionCount()-1)
	}
	second := h.session(t, 1)
	if second.opts.From != 70*time.Second || second.opts.Until != 90*time.Second {
		t.Errorf("restart window = [%v, %v), want [70s, 90s): the selection end is kept", second.opts.From, second.opts.Until)
	}
}

func TestPlaybackSeekPastSelectionEndPlaysToTheEnd(t *testing.T) {
	h := newPlaybackHarness(t)
	h.c.playSelection()
	h.c.onTimelinePlayhead(120 * time.Second)
	h.pump(t, func() bool { return h.sessionCount() == 2 })
	if got := h.session(t, 1).opts.Until; got != 0 {
		t.Errorf("seeking outside the selection kept Until=%v, want open-ended", got)
	}
}

func TestManualFrameStepStopsPlayback(t *testing.T) {
	h := newPlaybackHarness(t)
	h.c.togglePlayback()
	s := h.session(t, 0)

	h.c.trimTimeline.stepFrame(h.c.trimTimeline.Playhead(), 1)

	if !s.wasCancelled() || h.c.playbackActive() {
		t.Error("stepping frames manually must stop playback")
	}
}

func TestPlaybackStopsOnEveryOwnerChange(t *testing.T) {
	tests := []struct {
		name string
		act  func(*Controller)
	}{
		{"input change", func(c *Controller) { c.cancelTrimWork() }},
		{"reset selection", func(c *Controller) { c.resetTrimSelection() }},
		{"trimming disabled", func(c *Controller) { c.trimEnabled = false; c.updatePlaybackControls() }},
		{"input lost its video", func(c *Controller) { c.media.VideoStreams = nil; c.updatePlaybackControls() }},
		{"export started", func(c *Controller) { c.runControl = transcode.NewRunControl(); c.updatePlaybackControls() }},
		{"export job starts", func(c *Controller) { c.cancelPlayback() }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newPlaybackHarness(t)
			h.c.togglePlayback()
			s := h.session(t, 0)
			tt.act(h.c)
			if !s.wasCancelled() || h.c.playbackActive() {
				t.Errorf("playback still running after %s", tt.name)
			}
			if h.c.trimPlay.Text != "Play" {
				t.Errorf("button = %q, want Play", h.c.trimPlay.Text)
			}
		})
	}
}

func TestPlaybackControlsAvailability(t *testing.T) {
	t.Run("audio-only input cannot play", func(t *testing.T) {
		h := newPlaybackHarness(t)
		h.c.media.VideoStreams = nil
		h.c.updatePlaybackControls()
		if !h.c.trimPlay.Disabled() || !h.c.trimPlaySelection.Disabled() {
			t.Error("Play buttons must be disabled without a video stream")
		}
		h.c.togglePlayback()
		if h.sessionCount() != 0 {
			t.Error("Play must not start without a video stream")
		}
	})
	t.Run("trimming off disables playback", func(t *testing.T) {
		h := newPlaybackHarness(t)
		h.c.trimEnabled = false
		h.c.updatePlaybackControls()
		if !h.c.trimPlay.Disabled() {
			t.Error("Play must be disabled while trimming is off")
		}
	})
	t.Run("ready input enables both buttons", func(t *testing.T) {
		h := newPlaybackHarness(t)
		if h.c.trimPlay.Disabled() || h.c.trimPlaySelection.Disabled() {
			t.Error("Play and Play selection must be enabled for a ready input")
		}
	})
	t.Run("trim keeps working without playback", func(t *testing.T) {
		h := newPlaybackHarness(t)
		h.c.media.VideoStreams = nil
		h.c.updatePlaybackControls()
		h.c.setInFromPlayhead()
		if h.c.trimSelection.Segments[1].Start != 30*time.Second {
			t.Errorf("Set In did not work with playback unavailable: %v", h.c.trimSelection.Segments)
		}
	})
}

func TestHoverPreviewSuppressedWhilePlaying(t *testing.T) {
	h := newPlaybackHarness(t)
	h.c.togglePlayback()
	h.c.requestHover(5 * time.Second)
	if h.c.previews.pendingAt != nil {
		t.Error("hover must not request static previews while playback owns the display")
	}
}

func TestSpaceTogglesPlayback(t *testing.T) {
	h := newPlaybackHarness(t)
	h.c.trimTimeline.TypedKey(&fyne.KeyEvent{Name: fyne.KeySpace})
	if !h.c.playbackActive() {
		t.Error("Space on the timeline must start playback")
	}
	h.c.trimTimeline.TypedKey(&fyne.KeyEvent{Name: fyne.KeySpace})
	if h.c.playbackActive() {
		t.Error("Space must pause running playback")
	}
}
