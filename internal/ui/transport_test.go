package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"git.skobk.in/skobkin/yave/internal/model"
	"git.skobk.in/skobkin/yave/internal/profiles"
	"git.skobk.in/skobkin/yave/internal/resources"
	"git.skobk.in/skobkin/yave/internal/transcode"
)

func TestAdjacentSegment(t *testing.T) {
	s := time.Second
	segs := []model.TimeRange{
		{Start: 10 * s, End: 20 * s},
		{Start: 40 * s, End: 50 * s},
		{Start: 70 * s, End: 80 * s},
	}
	tests := []struct {
		name     string
		segments []model.TimeRange
		playhead time.Duration
		dir      int
		want     int
		wantOK   bool
	}{
		{"next from before everything", segs, 0, 1, 0, true},
		{"next from inside a segment", segs, 15 * s, 1, 1, true},
		{"next from the start of a segment skips that segment", segs, 40 * s, 1, 2, true},
		{"next from a hair before a start still jumps to that start", segs, 40*s - 5*time.Millisecond, 1, 1, true},
		{"next from the last segment", segs, 75 * s, 1, 0, false},
		{"previous from deep inside restarts the current segment", segs, 45 * s, -1, 1, true},
		{"previous from right at the start goes to the one before", segs, 40*s + 200*time.Millisecond, -1, 0, true},
		{"previous from exactly the start goes to the one before", segs, 40 * s, -1, 0, true},
		{"previous from just past the restart margin restarts", segs, 40*s + 600*time.Millisecond, -1, 1, true},
		{"previous between segments is the one that just ended", segs, 30 * s, -1, 0, true},
		{"previous from the first segment's start has nowhere to go", segs, 10 * s, -1, 0, false},
		{"previous from before everything", segs, 5 * s, -1, 0, false},
		{"no segments", nil, 5 * s, 1, 0, false},
		{"unordered segments are read by their start", []model.TimeRange{{Start: 50 * s, End: 60 * s}, {Start: 10 * s, End: 20 * s}}, 30 * s, 1, 0, true},
		{"unordered, previous", []model.TimeRange{{Start: 50 * s, End: 60 * s}, {Start: 10 * s, End: 20 * s}}, 55 * s, -1, 0, true},
	}
	for _, tt := range tests {
		got, ok := adjacentSegment(tt.segments, tt.playhead, tt.dir)
		if ok != tt.wantOK || (ok && got != tt.want) {
			t.Errorf("%s: got (%d, %v), want (%d, %v)", tt.name, got, ok, tt.want, tt.wantOK)
		}
	}
}

func TestVolumeGain(t *testing.T) {
	tests := []struct {
		percent int
		muted   bool
		want    float64
	}{
		{100, false, 1},
		{50, false, 0.25},
		{10, false, 0.01},
		{0, false, 0},
		{100, true, 0},
		{0, true, 0},
		{-20, false, 0},
		{250, false, 1},
	}
	for _, tt := range tests {
		got := volumeGain(tt.percent, tt.muted)
		if d := got - tt.want; d < -1e-9 || d > 1e-9 {
			t.Errorf("volumeGain(%d, %v) = %v, want %v", tt.percent, tt.muted, got, tt.want)
		}
	}
	// Monotonic: a higher slider never plays quieter.
	for p := 1; p <= 100; p++ {
		if volumeGain(p, false) < volumeGain(p-1, false) {
			t.Fatalf("gain falls between %d%% and %d%%", p-1, p)
		}
	}
}

func TestVolumeIconName(t *testing.T) {
	tests := []struct {
		percent int
		muted   bool
		want    string
	}{
		{100, false, resources.IconVolumeHigh},
		{50, false, resources.IconVolumeHigh},
		{49, false, resources.IconVolumeLow},
		{1, false, resources.IconVolumeLow},
		{0, false, resources.IconVolumeOff},
		{80, true, resources.IconVolumeOff},
	}
	for _, tt := range tests {
		if got := volumeIconName(tt.percent, tt.muted); got != tt.want {
			t.Errorf("volumeIconName(%d, %v) = %q, want %q", tt.percent, tt.muted, got, tt.want)
		}
	}
}

func TestTransportButtonsAreIconOnlyWithTooltips(t *testing.T) {
	c := newPlaybackHarness(t).c
	for name, b := range map[string]*tipButton{
		"previous": c.transport.prev, "play": c.transport.play, "next": c.transport.next,
		"play selection": c.transport.playSel, "mute": c.transport.mute,
	} {
		if b.Text != "" {
			t.Errorf("%s button has text %q; the strip is icons with tooltips", name, b.Text)
		}
		if b.Icon == nil {
			t.Errorf("%s button has no icon", name)
		}
		if b.tip == nil || b.tip() == "" {
			t.Errorf("%s button has no tooltip", name)
		}
	}
	if c.trimPlay != c.transport.play || c.trimPlaySelection != c.transport.playSel {
		t.Error("trimPlay and trimPlaySelection must alias the main bar's buttons")
	}
}

func TestPlayButtonIconAndTooltipFollowState(t *testing.T) {
	h := newPlaybackHarness(t)
	c := h.c
	if !showsPlay(c.trimPlay) || c.trimPlay.tip() != "Play (Space)" {
		t.Errorf("idle: icon play=%v tip=%q", showsPlay(c.trimPlay), c.trimPlay.tip())
	}
	c.togglePlayback()
	if !showsPause(c.trimPlay) || c.trimPlay.tip() != "Pause (Space)" {
		t.Errorf("playing: icon pause=%v tip=%q", showsPause(c.trimPlay), c.trimPlay.tip())
	}
	c.togglePlayback()
	if !showsPlay(c.trimPlay) {
		t.Error("paused: the icon must swap back to play")
	}
}

func TestTransportDisabledReasons(t *testing.T) {
	h := newPlaybackHarness(t)
	c := h.c
	tb := c.transport
	for name, b := range map[string]*tipButton{"play": tb.play, "play selection": tb.playSel, "prev": tb.prev, "next": tb.next} {
		if b.Disabled() {
			t.Fatalf("%s starts disabled in a ready controller", name)
		}
	}
	input := c.inputPath
	cases := []struct {
		name     string
		mutate   func()
		undo     func()
		playOff  bool // play and play-selection disabled
		navOff   bool // previous/next disabled
		wantTipN string
		wantTipP string
	}{
		{"trimming off", func() { c.trimEnabled = false }, func() { c.trimEnabled = true }, true, true, "Enable trimming to move", "Enable trimming to play"},
		{"no video", func() { c.media.VideoStreams = nil }, func() { c.media.VideoStreams = []model.VideoStream{{Index: 0}} }, true, false, "Next segment", "no video stream"},
		{"export running", func() { c.runControl = transcode.NewRunControl() }, func() { c.runControl = nil }, true, false, "Next segment", "export runs"},
		{"no input", func() { c.inputPath = "" }, func() { c.inputPath = input }, true, false, "Next segment", "Open a video"},
		{"no segments", func() { c.trimSelection = model.TrimSelection{} }, func() {
			c.trimSelection = model.TrimSelection{Segments: []model.TimeRange{{Start: 10 * time.Second, End: 20 * time.Second}, {Start: 60 * time.Second, End: 90 * time.Second}}}
		}, false, true, "There are no segments", ""},
	}
	for _, tc := range cases {
		saved := c.trimSelection
		tc.mutate()
		c.updatePlaybackControls()
		if tc.playOff && !tb.play.Disabled() {
			t.Errorf("%s: play must be disabled", tc.name)
		}
		if tc.playOff && tc.wantTipP != "" && !strings.Contains(tb.play.tip(), tc.wantTipP) {
			t.Errorf("%s: play tooltip = %q, want the reason %q", tc.name, tb.play.tip(), tc.wantTipP)
		}
		if tc.playOff && !tb.playSel.Disabled() {
			t.Errorf("%s: play selection must be disabled", tc.name)
		}
		if tc.navOff != tb.prev.Disabled() || tc.navOff != tb.next.Disabled() {
			t.Errorf("%s: segment buttons disabled = %v/%v, want %v", tc.name, tb.prev.Disabled(), tb.next.Disabled(), tc.navOff)
		}
		if !strings.Contains(tb.next.tip(), tc.wantTipN) {
			t.Errorf("%s: next tooltip = %q, want it to mention %q", tc.name, tb.next.tip(), tc.wantTipN)
		}
		tc.undo()
		c.trimSelection = saved
		c.updatePlaybackControls()
	}
	// The volume controls are settings, not playback: they stay usable.
	c.trimEnabled = false
	c.updatePlaybackControls()
	if tb.mute.Disabled() || tb.volume.Disabled() {
		t.Error("mute and volume must stay enabled while playback is unavailable")
	}
}

func TestPlaySelectionNeedsAPlayableSegment(t *testing.T) {
	h := newPlaybackHarness(t)
	c := h.c
	c.trimSelection.Segments[1] = model.TimeRange{Start: 60 * time.Second, End: 60 * time.Second}
	c.updatePlaybackControls()
	if !c.transport.playSel.Disabled() || !strings.Contains(c.transport.playSel.tip(), "empty") {
		t.Errorf("empty segment: disabled=%v tip=%q", c.transport.playSel.Disabled(), c.transport.playSel.tip())
	}
}

func TestSegmentButtonsSelectAndSeek(t *testing.T) {
	h := newPlaybackHarness(t) // segments [10s,20s] and [60s,90s]; playhead 30s; segment 1 selected
	c := h.c

	c.transport.prev.OnTapped() // 30s is past segment 0's start by far: restarts... the segment before 30s is #0
	if c.trimSelected != 0 || c.trimTimeline.Playhead() != 10*time.Second {
		t.Fatalf("previous: selected %d playhead %s, want segment 0 at 10s", c.trimSelected, c.trimTimeline.Playhead())
	}
	if c.trimIn.Text != "00:00:10.000" || c.trimOut.Text != "00:00:20.000" {
		t.Errorf("entries %q/%q must follow the selected segment", c.trimIn.Text, c.trimOut.Text)
	}

	c.transport.next.OnTapped()
	if c.trimSelected != 1 || c.trimTimeline.Playhead() != 60*time.Second {
		t.Fatalf("next: selected %d playhead %s, want segment 1 at 60s", c.trimSelected, c.trimTimeline.Playhead())
	}

	c.trimTimeline.SetPlayhead(75 * time.Second)
	c.transport.prev.OnTapped() // deep inside segment 1: restarts it
	if c.trimSelected != 1 || c.trimTimeline.Playhead() != 60*time.Second {
		t.Fatalf("previous from inside: selected %d playhead %s, want segment 1 restarted at 60s", c.trimSelected, c.trimTimeline.Playhead())
	}

	c.transport.next.OnTapped() // already on the last segment
	if c.trimSelected != 1 || !strings.Contains(c.previewStatus.Text, "No later segment") {
		t.Errorf("next on the last segment: selected %d status %q, want an explanation", c.trimSelected, c.previewStatus.Text)
	}
	c.trimTimeline.SetPlayhead(5 * time.Second)
	c.transport.prev.OnTapped()
	if !strings.Contains(c.previewStatus.Text, "No earlier segment") {
		t.Errorf("status = %q", c.previewStatus.Text)
	}
}

func TestSegmentButtonsSeekWhilePlaying(t *testing.T) {
	h := newPlaybackHarness(t)
	c := h.c
	c.togglePlayback()
	first := h.session(t, 0)
	c.transport.prev.OnTapped() // jumps to segment 0 while playing: a seek, not a stop
	if !c.playbackActive() {
		t.Fatal("jumping between segments must keep playback going")
	}
	h.pump(t, func() bool { return h.sessionCount() == 2 })
	if !first.wasCancelled() {
		t.Error("the old decoder must be replaced on the seek")
	}
	if got := h.session(t, 1).opts.From; got != 10*time.Second {
		t.Errorf("playback resumed from %s, want the previous segment's start (10s)", got)
	}
}

func TestVolumeAppliesLiveAndToNewSessions(t *testing.T) {
	h := newPlaybackHarness(t)
	c := h.c
	c.togglePlayback()
	s := h.session(t, 0)
	if g := s.opts.Gain; g == nil || *g != 1 {
		t.Fatalf("initial gain = %v, want unity", g)
	}

	c.transport.volume.SetValue(50)
	if got := s.setVolumes(); len(got) == 0 || got[len(got)-1] != 0.25 {
		t.Errorf("session volumes = %v, want the last to be 0.25 (50%%)", got)
	}
	c.transport.mute.OnTapped()
	if got := s.setVolumes(); got[len(got)-1] != 0 {
		t.Errorf("after mute volumes = %v, want silence last", got)
	}
	if !c.muted || c.transport.mute.Icon != resources.Icon(resources.IconVolumeOff) || c.transport.mute.tip() != "Unmute" {
		t.Errorf("mute state: muted=%v tip=%q", c.muted, c.transport.mute.tip())
	}
	c.transport.mute.OnTapped()
	if got := s.setVolumes(); got[len(got)-1] != 0.25 {
		t.Errorf("after unmute volumes = %v, want 0.25 restored", got)
	}

	// A new session starts at the current gain (a muted one silent).
	c.togglePlayback() // pause
	c.transport.mute.OnTapped()
	c.togglePlayback() // play again
	next := h.session(t, 1)
	if g := next.opts.Gain; g == nil || *g != 0 {
		t.Errorf("a session started while muted has gain %v, want 0", g)
	}
}

func TestMovingTheSliderUnmutes(t *testing.T) {
	c := newPlaybackHarness(t).c
	c.transport.mute.OnTapped()
	if !c.muted {
		t.Fatal("mute did not engage")
	}
	c.transport.volume.SetValue(70)
	if c.muted || c.volumePercent != 70 {
		t.Errorf("moving the slider: muted=%v volume=%d, want unmuted at 70", c.muted, c.volumePercent)
	}
	if c.transport.mute.Icon != resources.Icon(resources.IconVolumeHigh) {
		t.Error("the speaker icon must follow")
	}
	// Dragging to zero is silence but not "muted": the volume is what it is.
	c.transport.volume.SetValue(0)
	if c.muted || c.transport.mute.Icon != resources.Icon(resources.IconVolumeOff) {
		t.Errorf("volume 0: muted=%v", c.muted)
	}
}

func TestEveryTransportBarStaysInSync(t *testing.T) {
	c := newPlaybackHarness(t).c
	second := c.newTransportBar(false) // e.g. the full-screen overlay
	if len(c.transports) != 2 {
		t.Fatalf("transports = %d, want 2", len(c.transports))
	}
	c.transport.volume.SetValue(33)
	if second.volume.Value != 33 || second.mute.Icon != resources.Icon(resources.IconVolumeLow) {
		t.Errorf("second bar: volume %v icon low=%v", second.volume.Value, second.mute.Icon == resources.Icon(resources.IconVolumeLow))
	}
	second.mute.OnTapped()
	if !c.muted || c.transport.mute.Icon != resources.Icon(resources.IconVolumeOff) {
		t.Error("muting from one bar must show on the other")
	}
	c.togglePlayback()
	if !showsPause(second.play) {
		t.Error("play state must show on every bar")
	}
	c.updatePlaybackControls()
	if second.play.Disabled() != c.transport.play.Disabled() {
		t.Error("button availability must match across bars")
	}
}

func TestVolumePersistsInTheConfig(t *testing.T) {
	c := newPlaybackHarness(t).c
	c.configPath = filepath.Join(t.TempDir(), "config.yaml")
	c.config = model.Config{Version: 1, FFmpeg: c.config.FFmpeg}
	if err := profiles.Normalize(&c.config); err != nil {
		t.Fatal(err)
	}

	// A drag: live ticks update the playback and every bar, but must not
	// touch the disk (a second bar is synced programmatically each tick).
	c.newTransportBar(false)
	c.onVolumeSlider(40)
	c.onVolumeSlider(41)
	c.onVolumeSlider(40)
	if c.config.UI.PlaybackVolume != nil {
		t.Fatal("the config must not be written on slider ticks, nor by syncing the other bars")
	}
	c.transport.volume.OnChangeEnded(40) // the slider is released
	loaded, err := profiles.Load(c.configPath)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.UI.PlaybackVolume == nil || *loaded.UI.PlaybackVolume != 40 || loaded.UI.PlaybackMuted {
		t.Fatalf("saved volume = %v muted=%v, want 40 unmuted", loaded.UI.PlaybackVolume, loaded.UI.PlaybackMuted)
	}
	c.transport.mute.OnTapped() // muting saves immediately
	loaded, err = profiles.Load(c.configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !loaded.UI.PlaybackMuted || *loaded.UI.PlaybackVolume != 40 {
		t.Errorf("saved state = %v muted=%v, want 40 muted (the volume to restore is kept)", loaded.UI.PlaybackVolume, loaded.UI.PlaybackMuted)
	}
}

func TestVolumeIsRestoredFromTheConfig(t *testing.T) {
	c := newPlaybackHarness(t).c
	vol := 35
	c.config.UI.PlaybackVolume, c.config.UI.PlaybackMuted = &vol, true
	c.loadVolume()
	if c.volumePercent != 35 || !c.muted {
		t.Fatalf("loaded %d muted=%v", c.volumePercent, c.muted)
	}
	bar := c.newTransportBar(false) // a bar built after loading reflects it
	if bar.volume.Value != 35 || bar.mute.Icon != resources.Icon(resources.IconVolumeOff) {
		t.Errorf("new bar: volume %v", bar.volume.Value)
	}
	for in, want := range map[int]int{-5: 0, 150: 100} {
		v := in
		c.config.UI.PlaybackVolume = &v
		c.loadVolume()
		if c.volumePercent != want {
			t.Errorf("configured %d loads as %d, want %d", in, c.volumePercent, want)
		}
	}
	c.config.UI = model.UIConfig{}
	c.loadVolume()
	if c.volumePercent != 100 || c.muted {
		t.Errorf("no saved state: volume %d muted=%v, want 100 unmuted", c.volumePercent, c.muted)
	}
}

func TestVolumeSyncAcrossBarsDoesNotWriteTheConfig(t *testing.T) {
	c := newPlaybackHarness(t).c
	c.configPath = filepath.Join(t.TempDir(), "config.yaml")
	c.newTransportBar(false)
	c.onVolumeSlider(20)
	c.applyVolume()
	if _, err := os.Stat(c.configPath); err == nil {
		t.Error("syncing the sliders wrote the config file")
	}
}

// TestTrimControlsStayNarrowEvenWithTheLongestHint pins the Trim tab's
// minimum width: the controls sit in a Border below the preview, so their
// widest row sets the window's minimum width. The icon strip, snap check
// and zoom buttons share the enable row; the "why trimming is unavailable"
// hint has its own line precisely so a long reason cannot widen it.
func TestTrimControlsStayNarrowEvenWithTheLongestHint(t *testing.T) {
	c := testTrimController(t)
	c.buildLayout()
	setStatusLabel(c.trimHint, "Open a video on the Input tab to cut it")
	const limit = 850 // comfortably inside the 1050px default window
	if got := c.trimControlsBox.MinSize().Width; got > limit {
		t.Errorf("trim controls need %.0fpx, want at most %d: a row grew too wide", got, limit)
	}
}
