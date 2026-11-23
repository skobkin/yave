package ui

import (
	"testing"

	"git.skobk.in/skobkin/yave/internal/model"
)

func TestMissingStreams(t *testing.T) {
	t.Parallel()
	video := []model.VideoStream{{Index: 0}}
	audio := []model.AudioStream{{Index: 1}}
	tests := []struct {
		name                 string
		path                 string
		inspecting           bool
		media                model.MediaInfo
		wantVideo, wantAudio bool
	}{
		{"no input", "", false, model.MediaInfo{}, false, false},
		{"still inspecting", "/a", true, model.MediaInfo{AudioStreams: audio}, false, false},
		{"probe failed or empty", "/a", false, model.MediaInfo{}, false, false},
		{"normal", "/a", false, model.MediaInfo{VideoStreams: video, AudioStreams: audio}, false, false},
		{"video only", "/a", false, model.MediaInfo{VideoStreams: video}, false, true},
		{"audio only", "/a", false, model.MediaInfo{AudioStreams: audio}, true, false},
		{"audio with cover art", "/a", false, model.MediaInfo{AudioStreams: audio, CoverArtStreams: video}, true, false},
	}
	for _, tt := range tests {
		v, a := missingStreams(tt.path, tt.inspecting, tt.media)
		if v != tt.wantVideo || a != tt.wantAudio {
			t.Errorf("%s: missing = %v/%v, want %v/%v", tt.name, v, a, tt.wantVideo, tt.wantAudio)
		}
	}
}

func TestStreamOverrideDoesNotChangeSavedProfile(t *testing.T) {
	c := dirtyTestController(t)
	c.inputPath = "/in.mp3"
	c.media = model.MediaInfo{AudioStreams: []model.AudioStream{{Index: 0, Codec: "mp3"}}}
	c.refresh()

	if c.includeVideo.Checked || !c.includeVideo.Disabled() {
		t.Fatal("an input without video must uncheck and disable Include video")
	}
	if got := c.profileFromWidgets(); got.ExcludeVideo {
		t.Fatal("the override must not leak into the saved profile")
	}
	if c.profileDirty() {
		t.Fatal("an input-driven override must not mark the profile dirty")
	}
	if !c.commandProfile().ExcludeVideo {
		t.Fatal("the run profile must exclude the missing stream")
	}

	c.media = model.MediaInfo{
		VideoStreams: []model.VideoStream{{Index: 0, Codec: "h264", Width: 1280, Height: 720}},
		AudioStreams: []model.AudioStream{{Index: 1, Codec: "aac"}},
	}
	c.refresh()
	if !c.includeVideo.Checked || c.includeVideo.Disabled() {
		t.Fatal("the next input with video must restore the checkbox")
	}
	if c.commandProfile().ExcludeVideo {
		t.Fatal("override must clear with the next input")
	}
}

func TestUserExclusionIsSavedAndRestored(t *testing.T) {
	c := dirtyTestController(t)
	c.includeAudio.SetChecked(false)
	if !c.profileFromWidgets().ExcludeAudio || !c.profileDirty() {
		t.Fatal("unchecking Include audio must change the profile")
	}
	c.saveCurrentProfile()
	if !c.config.Profiles[0].ExcludeAudio {
		t.Fatal("exclusion not saved")
	}
	c.loadProfile(c.config.Profiles[0])
	if c.includeAudio.Checked {
		t.Fatal("exclusion not restored on load")
	}
}

func TestTemplateFields(t *testing.T) {
	t.Parallel()
	base := model.Profile{
		Container: model.ContainerMKV,
		Video:     model.VideoSettings{Encoder: "libx265"},
		Audio:     model.AudioSettings{Mode: model.AudioEncode, Encoder: "aac", Bitrate: "128k"},
	}
	ext, codec, vs, as := templateFields(base)
	if ext != "mkv" || codec != "libx265" || vs == "" || as == "" {
		t.Fatalf("normal: %q %q %q %q", ext, codec, vs, as)
	}
	audioOnly := base
	audioOnly.ExcludeVideo = true
	ext, codec, vs, as = templateFields(audioOnly)
	if ext != "mka" || codec != "aac" || vs != "" || as == "" {
		t.Fatalf("audio only: %q %q %q %q", ext, codec, vs, as)
	}
	audioOnly.Container = model.ContainerMP4
	if ext, _, _, _ = templateFields(audioOnly); ext != "m4a" {
		t.Fatalf("mp4 audio-only ext = %q", ext)
	}
	muted := base
	muted.ExcludeAudio = true
	ext, _, vs, as = templateFields(muted)
	if ext != "mkv" || vs == "" || as != "" {
		t.Fatalf("muted: %q %q %q", ext, vs, as)
	}
}
