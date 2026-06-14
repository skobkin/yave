package ffmpeg

import (
	"slices"
	"testing"

	"git.skobk.in/skobkin/yave/internal/model"
)

func TestBuildCommandVAAPI(t *testing.T) {
	t.Parallel()
	profile := model.BuiltinProfiles()[0]
	plan, err := BuildCommand(BuildRequest{
		FFmpegPath: "/usr/bin/ffmpeg",
		InputPath:  "/tmp/input file.mkv",
		OutputPath: "/tmp/output.mkv",
		Profile:    profile,
		Media: model.MediaInfo{
			VideoStreams: []model.VideoStream{{Width: 1920, Height: 1080}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, pair := range [][2]string{
		{"-vaapi_device", "/dev/dri/renderD128"},
		{"-c:v", "hevc_vaapi"},
		{"-rc_mode", "CQP"},
		{"-qp", "30"},
		{"-c:a", "copy"},
	} {
		if !containsPair(plan.Args, pair[0], pair[1]) {
			t.Errorf("missing %q %q in %#v", pair[0], pair[1], plan.Args)
		}
	}
	if !slices.Contains(plan.Args, "format=nv12,hwupload,scale_vaapi=w=1280:h=720:force_original_aspect_ratio=decrease:force_divisible_by=2") {
		t.Errorf("missing VAAPI filter in %#v", plan.Args)
	}
	if plan.Preview == "" {
		t.Error("empty command preview")
	}
}

func TestBuildCommandAllAudioAndMP4SubtitleWarning(t *testing.T) {
	t.Parallel()
	profile := model.BuiltinProfiles()[2]
	profile.Audio.CopyAllStreams = true
	plan, err := BuildCommand(BuildRequest{
		FFmpegPath: "ffmpeg",
		InputPath:  "/tmp/input.mkv",
		OutputPath: "/tmp/output.mp4",
		Profile:    profile,
		Media: model.MediaInfo{
			VideoStreams: []model.VideoStream{{Width: 1920, Height: 1080}},
			SubtitleStreams: []model.SubtitleStream{
				{Index: 2, Codec: "mov_text"},
				{Index: 3, Codec: "ass"},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !containsPair(plan.Args, "-map", "0:a?") {
		t.Errorf("all audio not mapped: %#v", plan.Args)
	}
	if !containsPair(plan.Args, "-map", "0:2?") {
		t.Errorf("compatible subtitle not mapped: %#v", plan.Args)
	}
	if len(plan.Warnings) != 1 {
		t.Fatalf("expected one subtitle warning, got %#v", plan.Warnings)
	}
	if !containsPair(plan.Args, "-movflags", "+faststart") {
		t.Errorf("faststart missing: %#v", plan.Args)
	}
}

func TestVideoCopyDoesNotFilter(t *testing.T) {
	t.Parallel()
	profile := model.BuiltinProfiles()[0]
	profile.Video.Encoder = "copy"
	plan, err := BuildCommand(BuildRequest{
		FFmpegPath: "ffmpeg",
		InputPath:  "/tmp/input.mkv",
		OutputPath: "/tmp/output.mkv",
		Profile:    profile,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !containsPair(plan.Args, "-c:v", "copy") {
		t.Fatalf("video copy missing: %#v", plan.Args)
	}
	if slices.Contains(plan.Args, "-vf") {
		t.Fatalf("copy command unexpectedly contains a filter: %#v", plan.Args)
	}
}

func containsPair(args []string, key, value string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == key && args[i+1] == value {
			return true
		}
	}
	return false
}
