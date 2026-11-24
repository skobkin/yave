package ui

import (
	"context"
	"time"

	"fyne.io/fyne/v2"

	"git.skobk.in/skobkin/yave/internal/playback"
)

// Trim playback is an optional convenience layered over the static
// preview: a playback.Session decodes a window of the input and the
// frames it releases replace the preview image while the timeline
// playhead follows. Nothing here touches the transcoding pipeline, and
// every path that cannot play (no video stream, no FFmpeg, no audio
// device) leaves trimming fully usable.

// playbackSession is the part of *playback.Session the controller
// uses; the seam lets tests drive the UI without FFmpeg.
type playbackSession interface {
	Frames() <-chan playback.Frame
	Done() <-chan struct{}
	Cancel()
	Position() time.Duration
	Err() error
	Notice() string
	SetVolume(gain float64)
}

type playbackStarter func(ctx context.Context, o playback.Options, sink playback.AudioSink) (playbackSession, error)

func defaultPlaybackStarter(ctx context.Context, o playback.Options, sink playback.AudioSink) (playbackSession, error) {
	return playback.Start(ctx, o, sink)
}

// playbackState is the controller's playback bookkeeping, UI-thread
// only. The zero value is usable: nil seams fall back to the real
// implementations.
type playbackState struct {
	session playbackSession
	// gen invalidates late frame and end events of sessions that were
	// cancelled or replaced.
	gen uint64
	// until is the end of the running window; zero plays to the end of
	// the input.
	until time.Duration
	// seeking is true between a seek and the debounced restart, so the
	// controls keep showing "Pause" instead of flickering.
	seeking  bool
	seekTime *time.Timer

	start playbackStarter
	sink  playback.AudioSink
}

func (p *playbackState) starter() playbackStarter {
	if p.start != nil {
		return p.start
	}
	return defaultPlaybackStarter
}

func (p *playbackState) audioSink() playback.AudioSink {
	if p.sink == nil {
		p.sink = playback.NewAudioSink()
	}
	return p.sink
}

// playbackActive reports whether playback is running or about to
// resume after a seek.
func (c *Controller) playbackActive() bool {
	return c.play.session != nil || c.play.seeking
}

// canPlay reports whether Play may start now: trimming is on, the
// input has a video stream and a known duration, and no export runs.
func (c *Controller) canPlay() bool {
	return c.playBlockReason() == ""
}

// updatePlaybackControls keeps the transport bars and any running
// playback consistent with the current state. It runs from refresh().
func (c *Controller) updatePlaybackControls() {
	if !c.canPlay() && c.playbackActive() {
		c.cancelPlayback()
		return // cancelPlayback re-entered this function
	}
	c.updateTransports()
}

func setEnabled(w fyne.Disableable, enabled bool) {
	if enabled {
		w.Enable()
	} else {
		w.Disable()
	}
}

// togglePlayback is the Play/Pause button and the timeline's Space key.
// Pausing leaves the playhead where playback stopped and shows the
// exact frame there.
func (c *Controller) togglePlayback() {
	if c.playbackActive() {
		at := c.trimTimeline.Playhead()
		c.cancelPlayback()
		c.requestPreview(at)
		return
	}
	if c.canPlay() {
		c.startPlayback(c.trimTimeline.Playhead(), 0)
	}
}

// playSelection plays the selected segment from In to Out.
func (c *Controller) playSelection() {
	if !c.canPlay() || c.trimSelected < 0 || c.trimSelected >= len(c.trimSelection.Segments) {
		return
	}
	seg := c.trimSelection.Segments[c.trimSelected]
	if seg.End <= seg.Start {
		return
	}
	c.startPlayback(seg.Start, seg.End)
}

// startPlayback replaces any running playback with a new session over
// [from, until) (until == 0: to the end). Starting at or past the end
// restarts from the beginning.
func (c *Controller) startPlayback(from, until time.Duration) {
	c.cancelPlayback()
	if !c.canPlay() {
		return
	}
	if from < 0 || from >= c.media.Duration {
		from = 0
	}
	// Playback owns the display: pending static extractions and queued
	// frame-step walks must not land on top of it.
	c.cancelPreviewWork()
	c.previews.index.pending = 0
	c.setPreviewStatus("")

	video, _ := c.media.PrimaryVideo()
	gain := c.playbackGain()
	opts := playback.Options{
		FFmpegPath: c.config.FFmpeg.FFmpegPath,
		Input:      c.inputPath,
		From:       from,
		Until:      until,
		FrameRate:  video.FrameRate,
		HasAudio:   len(c.media.AudioStreams) > 0,
		Gain:       &gain,
		MaxWidth:   c.playbackWidth(),
	}
	session, err := c.play.starter()(context.Background(), opts, c.play.audioSink())
	if err != nil {
		c.setPreviewStatus("Playback unavailable: " + err.Error())
		c.updatePlaybackControls()
		return
	}
	c.play.gen++
	gen := c.play.gen
	c.play.session, c.play.until = session, until
	c.trimTimeline.SetPlayhead(from)
	if notice := session.Notice(); notice != "" {
		c.setPreviewStatus(notice)
	}
	c.updatePlaybackControls()
	go c.pumpPlayback(session, gen)
}

// pumpPlayback forwards a session's frames to the UI thread and
// reports its end. It runs until the session closes its frame channel,
// which happens after a cancellation too.
func (c *Controller) pumpPlayback(s playbackSession, gen uint64) {
	for f := range s.Frames() {
		c.dispatchUI(func() { c.showPlaybackFrame(gen, f) })
	}
	<-s.Done()
	c.dispatchUI(func() { c.playbackEnded(s, gen) })
}

func (c *Controller) showPlaybackFrame(gen uint64, f playback.Frame) {
	if gen != c.play.gen || c.play.session == nil {
		return
	}
	c.previewImage.Image = f.Image
	c.previewImage.Refresh()
	c.previewTimeLabel.SetText(formatTimestamp(f.At))
	c.previews.displayedAt = &f.At
	c.trimTimeline.SetPlayhead(f.At)
}

// playbackEnded handles the natural end of a session (window finished
// or decoder failure). Cancelled sessions were already cleaned up and
// are recognised by their stale generation.
func (c *Controller) playbackEnded(s playbackSession, gen uint64) {
	if gen != c.play.gen || c.play.session == nil {
		return
	}
	c.play.session = nil
	c.updatePlaybackControls()
	if err := s.Err(); err != nil {
		c.setPreviewStatus("Playback stopped: " + err.Error())
		return
	}
	// Replace the last scaled playback frame with the exact preview at
	// the position playback stopped on.
	c.requestPreview(c.trimTimeline.Playhead())
}

// seekPlayback restarts playback at a committed playhead move. The old
// session is silenced at once; the restart is debounced like static
// previews so dragging the playhead does not spawn a decoder per move.
func (c *Controller) seekPlayback(at time.Duration) {
	until := c.play.until
	if until > 0 && at >= until {
		until = 0
	}
	c.cancelPlayback()
	c.play.seeking = true
	c.play.gen++
	gen := c.play.gen
	c.updatePlaybackControls()
	c.play.seekTime = time.AfterFunc(previewDebounce, func() {
		c.dispatchUI(func() {
			if c.play.gen != gen || !c.play.seeking {
				return
			}
			c.play.seeking = false
			c.startPlayback(at, until)
		})
	})
}

// cancelPlayback stops any running or pending playback immediately and
// without waiting for the decoders; the display keeps the last frame.
func (c *Controller) cancelPlayback() {
	if !c.playbackActive() {
		return
	}
	if c.play.seekTime != nil {
		c.play.seekTime.Stop()
		c.play.seekTime = nil
	}
	if c.play.session != nil {
		c.play.session.Cancel()
		c.play.session = nil
	}
	c.play.seeking = false
	c.play.gen++
	c.updatePlaybackControls()
}
