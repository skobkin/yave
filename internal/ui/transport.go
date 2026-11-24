package ui

import (
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"

	"git.skobk.in/skobkin/yave/internal/model"
	"git.skobk.in/skobkin/yave/internal/profiles"
	"git.skobk.in/skobkin/yave/internal/resources"
)

const (
	// segmentRestartMargin: "previous segment" restarts the current
	// segment unless the playhead is this close to its start, like a media
	// player's previous-track button.
	segmentRestartMargin = 500 * time.Millisecond
	// segmentTolerance keeps a playhead sitting on a segment start from
	// counting as before or after it.
	segmentTolerance = time.Millisecond

	volumeSliderWidth = 90
	volumeLowBelow    = 50 // below this percent the speaker shows one wave
)

// adjacentSegment returns the segment the previous (dir < 0) or next
// (dir > 0) button jumps to. Previous is the last segment starting more
// than segmentRestartMargin before the playhead — which is the current
// segment when the playhead is well inside it, and the one before when it
// is right at the start. Next is the first segment starting after the
// playhead. Pure and table-tested; segments are read by Start, so an
// out-of-order list (an invalid typed edit) still navigates sensibly.
func adjacentSegment(segments []model.TimeRange, playhead time.Duration, dir int) (int, bool) {
	best, found := 0, false
	for i, seg := range segments {
		if dir < 0 {
			if seg.Start >= playhead-segmentRestartMargin {
				continue
			}
			if !found || seg.Start > segments[best].Start {
				best, found = i, true
			}
			continue
		}
		if seg.Start <= playhead+segmentTolerance {
			continue
		}
		if !found || seg.Start < segments[best].Start {
			best, found = i, true
		}
	}
	return best, found
}

// volumeGain converts the slider percent and the mute flag into the
// linear gain handed to the audio backend. The squared curve tracks
// perceived loudness better than a straight line (50% sounds about half
// as loud as 100%, where a linear 0.5 sounds nearly as loud); muted is
// silence whatever the slider says.
func volumeGain(percent int, muted bool) float64 {
	if muted {
		return 0
	}
	f := float64(min(max(percent, 0), 100)) / 100
	return f * f
}

// volumeIconName picks the speaker icon for the state.
func volumeIconName(percent int, muted bool) string {
	switch {
	case muted || percent <= 0:
		return resources.IconVolumeOff
	case percent < volumeLowBelow:
		return resources.IconVolumeLow
	default:
		return resources.IconVolumeHigh
	}
}

// transportBar is the playback control strip: previous segment, play or
// pause, next segment, play selection, mute and volume. The Trim tab owns
// one; the full-screen overlay builds another from the same constructor,
// and every bar the controller built is kept in sync.
type transportBar struct {
	prev, play, next, playSel, mute *tipButton
	fullscreen                      *tipButton
	volume                          *widget.Slider
	box                             *fyne.Container
}

// newTransportBar builds a bar wired to the controller and registers it
// for state updates. Its buttons start disabled; updatePlaybackControls
// owns their state from then on. The full-screen overlay's bar (overlay)
// shows the exit icon on its last button.
func (c *Controller) newTransportBar(overlay bool) *transportBar {
	tb := &transportBar{}
	tb.prev = newTipButtonFunc(c.tips, resources.Icon(resources.IconPrevSegment),
		func() string { return c.segmentNavTip(-1) }, func() { c.jumpToSegment(-1) })
	tb.play = newTipButtonFunc(c.tips, resources.Icon(resources.IconPlay), c.playTip, c.togglePlayback)
	tb.next = newTipButtonFunc(c.tips, resources.Icon(resources.IconNextSegment),
		func() string { return c.segmentNavTip(1) }, func() { c.jumpToSegment(1) })
	tb.playSel = newTipButtonFunc(c.tips, resources.Icon(resources.IconPlaySelection), c.playSelectionTip, c.playSelection)
	tb.mute = newTipButtonFunc(c.tips, resources.Icon(volumeIconName(c.volumePercent, c.muted)), c.muteTip, c.toggleMute)
	fullscreenIcon := resources.IconFullscreen
	if overlay {
		fullscreenIcon = resources.IconFullscreenExit
	}
	tb.fullscreen = newTipButtonFunc(c.tips, resources.Icon(fullscreenIcon), c.fullscreenTip, c.toggleFullscreen)
	tb.volume = widget.NewSlider(0, 100)
	tb.volume.Step = 1
	tb.volume.SetValue(float64(c.volumePercent))
	tb.volume.OnChanged = func(v float64) {
		c.onVolumeSlider(v)
		c.fullscreenActivity()
	}
	// Fyne also fires OnChangeEnded for programmatic SetValue calls; only a
	// user's release may write the config, or syncing another bar's slider
	// during a drag would save on every tick.
	tb.volume.OnChangeEnded = func(float64) {
		if !c.syncingVolume {
			c.saveVolume()
		}
	}
	for _, b := range []*tipButton{tb.prev, tb.play, tb.next, tb.playSel, tb.fullscreen} {
		b.Disable()
	}
	tb.box = container.NewHBox(tb.prev, tb.play, tb.next, tb.playSel, tb.mute,
		container.NewGridWrap(fyne.NewSize(volumeSliderWidth, tb.volume.MinSize().Height), tb.volume),
		tb.fullscreen)
	c.transports = append(c.transports, tb)
	return tb
}

// --- availability and tooltips ---

// playBlockReason explains why playback cannot start now, or "".
func (c *Controller) playBlockReason() string {
	_, hasVideo := c.media.PrimaryVideo()
	switch {
	case c.inputPath == "" || c.media.Duration <= 0:
		return "Open a video on the Input tab first"
	case !c.trimEnabled:
		return "Enable trimming to play"
	case !hasVideo:
		return "The input has no video stream"
	case c.runControl != nil:
		return "Not available while an export runs"
	}
	return ""
}

func (c *Controller) playTip() string {
	if reason := c.playBlockReason(); reason != "" {
		return reason
	}
	if c.playbackActive() {
		return "Pause (Space)"
	}
	return "Play (Space)"
}

// selectedSegmentPlayable reports whether the selected segment has a
// positive length to play.
func (c *Controller) selectedSegmentPlayable() bool {
	if c.trimSelected < 0 || c.trimSelected >= len(c.trimSelection.Segments) {
		return false
	}
	seg := c.trimSelection.Segments[c.trimSelected]
	return seg.End > seg.Start
}

func (c *Controller) playSelectionTip() string {
	if reason := c.playBlockReason(); reason != "" {
		return reason
	}
	if !c.selectedSegmentPlayable() {
		return "The selected segment is empty"
	}
	return "Play the selected segment from In to Out"
}

// segmentNavBlockReason explains why the segment buttons are unavailable.
// They need no video: moving between segments is a trim action.
func (c *Controller) segmentNavBlockReason() string {
	switch {
	case !c.trimEnabled:
		return "Enable trimming to move between segments"
	case len(c.trimSelection.Segments) == 0:
		return "There are no segments"
	}
	return ""
}

func (c *Controller) segmentNavTip(dir int) string {
	if reason := c.segmentNavBlockReason(); reason != "" {
		return reason
	}
	if dir < 0 {
		return "Previous segment"
	}
	return "Next segment"
}

func (c *Controller) muteTip() string {
	if c.muted {
		return "Unmute"
	}
	return "Mute"
}

// updateTransports reconciles every bar with the controller state. It
// runs from updatePlaybackControls, i.e. from refresh().
func (c *Controller) updateTransports() {
	canPlay := c.playBlockReason() == ""
	navigable := c.segmentNavBlockReason() == ""
	for _, tb := range c.transports {
		if c.playbackActive() {
			tb.play.SetIcon(resources.Icon(resources.IconPause))
		} else {
			tb.play.SetIcon(resources.Icon(resources.IconPlay))
		}
		setEnabled(tb.play, canPlay)
		setEnabled(tb.playSel, canPlay && c.selectedSegmentPlayable())
		setEnabled(tb.prev, navigable)
		setEnabled(tb.next, navigable)
		// Leaving full-screen must always be possible.
		setEnabled(tb.fullscreen, c.fs.active || c.fullscreenBlockReason() == "")
	}
}

// jumpToSegment is the previous/next segment buttons: it selects the
// target segment and moves the playhead to its start (seeking, while
// playing).
func (c *Controller) jumpToSegment(dir int) {
	if c.segmentNavBlockReason() != "" {
		return
	}
	index, ok := adjacentSegment(c.trimSelection.Segments, c.trimTimeline.Playhead(), dir)
	if !ok {
		if dir < 0 {
			c.setPreviewStatus("No earlier segment")
		} else {
			c.setPreviewStatus("No later segment")
		}
		return
	}
	c.selectSegment(index)
}

// --- volume ---

// loadVolume reads the saved volume and mute state.
func (c *Controller) loadVolume() {
	c.volumePercent = 100
	if p := c.config.UI.PlaybackVolume; p != nil {
		c.volumePercent = min(max(*p, 0), 100)
	}
	c.muted = c.config.UI.PlaybackMuted
}

// playbackGain is the gain playback should use right now.
func (c *Controller) playbackGain() float64 { return volumeGain(c.volumePercent, c.muted) }

// onVolumeSlider follows any bar's slider live. Moving it above zero
// unmutes, like a media player.
func (c *Controller) onVolumeSlider(v float64) {
	if c.syncingVolume {
		return
	}
	c.volumePercent = min(max(int(v+0.5), 0), 100)
	if c.muted && c.volumePercent > 0 {
		c.muted = false
	}
	c.applyVolume()
}

// toggleMute is the mute button.
func (c *Controller) toggleMute() {
	c.muted = !c.muted
	c.applyVolume()
	c.saveVolume()
}

// applyVolume pushes the state to the running playback and every bar.
func (c *Controller) applyVolume() {
	c.syncingVolume = true
	for _, tb := range c.transports {
		tb.volume.SetValue(float64(c.volumePercent))
		tb.mute.SetIcon(resources.Icon(volumeIconName(c.volumePercent, c.muted)))
	}
	c.syncingVolume = false
	if c.play.session != nil {
		c.play.session.SetVolume(c.playbackGain())
	}
}

// saveVolume persists the volume and mute state in the config file.
func (c *Controller) saveVolume() {
	volume := c.volumePercent
	if c.config.UI.PlaybackVolume != nil && *c.config.UI.PlaybackVolume == volume && c.config.UI.PlaybackMuted == c.muted {
		return
	}
	c.config.UI.PlaybackVolume, c.config.UI.PlaybackMuted = &volume, c.muted
	if c.configPath != "" {
		_ = profiles.Save(c.configPath, c.config) //nolint:errcheck // a UI preference; the next change retries
	}
}
