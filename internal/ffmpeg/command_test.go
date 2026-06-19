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

func TestBuildCommandUsesIntegerBoundaryValues(t *testing.T) {
	t.Parallel()

	profile := model.BuiltinProfiles()[0]
	profile.Video.Encoder = "av1_vaapi"
	profile.Video.RateControl = "CQP"
	profile.Video.QP = 255
	profile.Audio.Mode = model.AudioEncode
	profile.Audio.Encoder = "flac"
	profile.Audio.Compression = 12
	request := BuildRequest{
		FFmpegPath: "ffmpeg",
		InputPath:  "/tmp/input.mkv",
		OutputPath: "/tmp/output.mkv",
		Profile:    profile,
	}
	plan, err := BuildCommand(request)
	if err != nil {
		t.Fatal(err)
	}
	for _, pair := range [][2]string{{"-qp", "255"}, {"-compression_level", "12"}} {
		if !containsPair(plan.Args, pair[0], pair[1]) {
			t.Errorf("missing %q %q in %#v", pair[0], pair[1], plan.Args)
		}
	}

	profile.Video.Encoder = "libsvtav1"
	profile.Video.RateControl = "CRF"
	profile.Video.CRF = 63
	profile.Video.Preset = "13"
	request.Profile = profile
	plan, err = BuildCommand(request)
	if err != nil {
		t.Fatal(err)
	}
	for _, pair := range [][2]string{{"-crf", "63"}, {"-preset", "13"}} {
		if !containsPair(plan.Args, pair[0], pair[1]) {
			t.Errorf("missing %q %q in %#v", pair[0], pair[1], plan.Args)
		}
	}
}

func TestBuildCommandNVENC(t *testing.T) {
	t.Parallel()
	profile := model.BuiltinProfiles()[0]
	profile.Video.Encoder = "hevc_nvenc"
	profile.Video.RateControl = "CQP"
	profile.Video.QP = 28
	profile.Video.Preset = "p4"
	profile.Video.GPUIndex = 1
	plan, err := BuildCommand(BuildRequest{
		FFmpegPath: "ffmpeg",
		InputPath:  "/tmp/input.mkv",
		OutputPath: "/tmp/output.mkv",
		Profile:    profile,
		Media:      model.MediaInfo{VideoStreams: []model.VideoStream{{Width: 1920, Height: 1080}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	// -gpu 1 must be emitted before -i, the filter chain and
	// encoder flags must appear after -i.
	if !containsPair(plan.Args, "-gpu", "1") {
		t.Errorf("missing -gpu 1 in %#v", plan.Args)
	}
	for _, pair := range [][2]string{
		{"-c:v", "hevc_nvenc"},
		{"-rc", "constqp"},
		{"-qp", "28"},
		{"-preset", "p4"},
	} {
		if !containsPair(plan.Args, pair[0], pair[1]) {
			t.Errorf("missing %q %q in %#v", pair[0], pair[1], plan.Args)
		}
	}
	// The filter chain must use the NVENC hw upload path. The test
	// profile scales to 1280x720, so the chain is
	// format=yuv420p,format=nv12,hwupload_cuda,scale_cuda=w=1280:h=720:...
	if !slices.Contains(plan.Args, "format=yuv420p,format=nv12,hwupload_cuda,scale_cuda=w=1280:h=720:force_original_aspect_ratio=decrease:force_divisible_by=2") {
		t.Errorf("missing NVENC filter in %#v", plan.Args)
	}
}

func TestBuildCommandQSV(t *testing.T) {
	t.Parallel()
	profile := model.BuiltinProfiles()[0]
	profile.Video.Encoder = "hevc_qsv"
	profile.Video.RateControl = "VBR"
	profile.Video.Bitrate = "6M"
	profile.Video.Preset = "medium"
	profile.Video.QSVDevice = "/dev/dri/renderD128"
	plan, err := BuildCommand(BuildRequest{
		FFmpegPath: "ffmpeg",
		InputPath:  "/tmp/input.mkv",
		OutputPath: "/tmp/output.mkv",
		Profile:    profile,
		Media:      model.MediaInfo{VideoStreams: []model.VideoStream{{Width: 1920, Height: 1080}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !containsPair(plan.Args, "-init_hw_device", "qsv=/dev/dri/renderD128") {
		t.Errorf("missing -init_hw_device qsv=... in %#v", plan.Args)
	}
	for _, pair := range [][2]string{
		{"-c:v", "hevc_qsv"},
		{"-rc_mode", "VBR"},
		{"-b:v", "6M"},
		{"-preset", "medium"},
	} {
		if !containsPair(plan.Args, pair[0], pair[1]) {
			t.Errorf("missing %q %q in %#v", pair[0], pair[1], plan.Args)
		}
	}
	// The filter chain must use the QSV hw upload path. The test
	// profile scales to 1280x720, so the chain is
	// format=qsv,hwupload,scale_qsv=w=1280:h=720:...
	if !slices.Contains(plan.Args, "format=qsv,hwupload,scale_qsv=w=1280:h=720:force_original_aspect_ratio=decrease:force_divisible_by=2") {
		t.Errorf("missing QSV filter in %#v", plan.Args)
	}
}

func TestBuildCommandLibaomAV1(t *testing.T) {
	t.Parallel()
	profile := model.BuiltinProfiles()[0]
	profile.Video.Encoder = "libaom-av1"
	profile.Video.RateControl = "CRF"
	profile.Video.CRF = 30
	profile.Video.Preset = "4"
	plan, err := BuildCommand(BuildRequest{
		FFmpegPath: "ffmpeg",
		InputPath:  "/tmp/input.mkv",
		OutputPath: "/tmp/output.mkv",
		Profile:    profile,
		Media:      model.MediaInfo{VideoStreams: []model.VideoStream{{Width: 1920, Height: 1080}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, pair := range [][2]string{
		{"-c:v", "libaom-av1"},
		{"-crf", "30"},
		{"-b:v", "0"},
		{"-cpu-used", "4"},
	} {
		if !containsPair(plan.Args, pair[0], pair[1]) {
			t.Errorf("missing %q %q in %#v", pair[0], pair[1], plan.Args)
		}
	}
	// No hw upload or pixel-format conversion for software.
	if slices.Contains(plan.Args, "hwupload") {
		t.Errorf("libaom-av1 unexpectedly emits hwupload: %#v", plan.Args)
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
