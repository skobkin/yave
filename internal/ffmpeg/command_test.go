package ffmpeg

import (
	"slices"
	"strings"
	"testing"
	"time"

	"git.skobk.in/skobkin/yave/internal/model"
)

func TestBuildCommandVAAPI(t *testing.T) {
	t.Parallel()
	profile := model.BuiltinProfiles()[0]
	// Built-ins ship with an empty (automatic) device; command
	// generation needs the concrete resolved path.
	profile.Video.VAAPIDevice = "/dev/dri/renderD128"
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
	profile.Video.VAAPIDevice = "/dev/dri/renderD128"
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

// Copied subtitle events cannot be cut at trim boundaries: ffmpeg
// delivers an event active at the seek point in full and -t gates on
// packet start times only, so an event spanning the boundary keeps its
// whole duration and the output container outlives its video. While
// trimming is active, compatible copied subtitle streams are skipped
// with a warning instead of mapped.
func TestBuildCommandTrimsSkipCopiedSubtitles(t *testing.T) {
	t.Parallel()
	media := model.MediaInfo{
		VideoStreams:    []model.VideoStream{{Width: 1920, Height: 1080}},
		SubtitleStreams: []model.SubtitleStream{{Index: 2, Codec: "subrip"}},
	}
	trim := model.TrimSelection{Segments: []model.TimeRange{{Start: time.Second, End: 3 * time.Second}}}
	profile := model.BuiltinProfiles()[3] // "x265 Software Archive" — software encoder, no device requirements
	profile.Container = model.ContainerMKV
	profile.Subtitles.Mode = model.SubtitleCopy

	plan, err := BuildCommand(BuildRequest{
		FFmpegPath: "ffmpeg",
		InputPath:  "/tmp/input.mkv",
		OutputPath: "/tmp/output.mkv",
		Profile:    profile,
		Media:      media,
		Trim:       trim,
	})
	if err != nil {
		t.Fatal(err)
	}
	if containsPair(plan.Args, "-map", "0:2?") {
		t.Errorf("copied subtitle mapped while trimming: %#v", plan.Args)
	}
	if containsPair(plan.Args, "-c:s", "copy") {
		t.Errorf("subtitle copy codec present while trimming: %#v", plan.Args)
	}
	if !containsPair(plan.Args, "-map", "0:v:0") || !containsPair(plan.Args, "-map", "0:a:0?") {
		t.Errorf("video/audio mapping lost: %#v", plan.Args)
	}
	if len(plan.Warnings) != 1 || !strings.Contains(plan.Warnings[0], "subtitle streams are skipped") {
		t.Fatalf("warnings = %#v, want the trim subtitle-skip warning", plan.Warnings)
	}

	// Without the trim the same profile maps and copies the subtitle.
	plan, err = BuildCommand(BuildRequest{
		FFmpegPath: "ffmpeg",
		InputPath:  "/tmp/input.mkv",
		OutputPath: "/tmp/output.mkv",
		Profile:    profile,
		Media:      media,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !containsPair(plan.Args, "-map", "0:2?") || !containsPair(plan.Args, "-c:s", "copy") {
		t.Errorf("subtitle copy without a trim broken: %#v", plan.Args)
	}
	if len(plan.Warnings) != 0 {
		t.Errorf("unexpected warnings without a trim: %#v", plan.Warnings)
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
	profile.Video.VAAPIDevice = "/dev/dri/renderD128"
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
	// -gpu 1 must be emitted alongside the nvenc encoder args
	// (after -c:v hevc_nvenc) because ffmpeg only exposes it as a
	// per-output encoder option; emitting it before -i fails with
	// "Unrecognized option 'gpu'".
	if !containsPair(plan.Args, "-gpu", "1") {
		t.Errorf("missing -gpu 1 in %#v", plan.Args)
	}
	// The matching CUDA device must be initialised before -i and
	// bound to the filter graph so hwupload_cuda / scale_cuda upload
	// onto the same adapter the encoder targets on multi-GPU hosts.
	if !containsPair(plan.Args, "-init_hw_device", "cuda=cuda:1") {
		t.Errorf("missing -init_hw_device cuda=cuda:1 in %#v", plan.Args)
	}
	if !containsPair(plan.Args, "-filter_hw_device", "cuda") {
		t.Errorf("missing -filter_hw_device cuda in %#v", plan.Args)
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
	// format=yuv420p,format=nv12,hwupload_cuda,scale_cuda=w=1280:h=720.
	// scale_cuda does not accept force_original_aspect_ratio /
	// force_divisible_by, so those software-scale options must be
	// omitted even when KeepAspectRatio is set; targetDimensions has
	// already computed the aspect-fit even dimensions.
	if !slices.Contains(plan.Args, "format=yuv420p,format=nv12,hwupload_cuda,scale_cuda=w=1280:h=720") {
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
	if !containsPair(plan.Args, "-init_hw_device", "qsv=qsv:hw,child_device=/dev/dri/renderD128") {
		t.Errorf("missing -init_hw_device qsv=qsv:hw,child_device=... in %#v", plan.Args)
	}
	if !containsPair(plan.Args, "-filter_hw_device", "qsv") {
		t.Errorf("missing -filter_hw_device qsv in %#v", plan.Args)
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
	// format=nv12,hwupload,format=qsv,scale_qsv=w=1280:h=720. As with
	// scale_cuda, scale_qsv does not accept the software-scale aspect
	// options; targetDimensions pre-fitted the dimensions.
	if !slices.Contains(plan.Args, "format=nv12,hwupload,format=qsv,scale_qsv=w=1280:h=720") {
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

func TestBuildCommandTrim(t *testing.T) {
	t.Parallel()
	profile := model.BuiltinProfiles()[0]
	profile.Video.VAAPIDevice = "/dev/dri/renderD128"
	profile.Container = model.ContainerMP4
	plan, err := BuildCommand(BuildRequest{
		FFmpegPath: "/usr/bin/ffmpeg",
		InputPath:  "/tmp/input.mkv",
		OutputPath: "/tmp/output.mkv",
		Profile:    profile,
		Trim:       model.TrimSelection{Segments: []model.TimeRange{{Start: 10 * time.Second, End: 70 * time.Second}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !containsPair(plan.Args, "-ss", "10.000000") {
		t.Errorf("missing -ss 10.000000 in %#v", plan.Args)
	}
	if !containsPair(plan.Args, "-t", "60.000000") {
		t.Errorf("missing -t 60.000000 in %#v", plan.Args)
	}
	// -ss's value sits immediately before -i (input-side), after the
	// pre-input hardware flags.
	ssIndex := indexOfArg(plan.Args, "-ss")
	iIndex := indexOfArg(plan.Args, "-i")
	if ssIndex < 0 || iIndex < 0 || ssIndex+2 != iIndex {
		t.Errorf("-ss value (at %d) is not immediately before -i (at %d): %#v", ssIndex, iIndex, plan.Args)
	}
	vaapiValue := indexOfArg(plan.Args, "/dev/dri/renderD128")
	if vaapiValue < 0 || vaapiValue > ssIndex {
		t.Errorf("-ss (at %d) must come after the pre-input device flag value (at %d): %#v", ssIndex, vaapiValue, plan.Args)
	}
	// -t is an output option: after every -map and before -movflags and
	// the output path.
	tIndex := indexOfArg(plan.Args, "-t")
	lastMap := -1
	for i, arg := range plan.Args {
		if arg == "-map" {
			lastMap = i
		}
	}
	movflags := indexOfArg(plan.Args, "-movflags")
	if tIndex < 0 || tIndex < lastMap {
		t.Errorf("-t at %d must come after the last -map at %d: %#v", tIndex, lastMap, plan.Args)
	}
	if movflags < 0 || tIndex > movflags {
		t.Errorf("-t at %d must come before -movflags at %d: %#v", tIndex, movflags, plan.Args)
	}
	if plan.Args[len(plan.Args)-1] != "/tmp/output.mkv" {
		t.Errorf("output path must stay last: %#v", plan.Args)
	}
	if !strings.Contains(plan.Preview, "-ss 10.000000") {
		t.Errorf("preview missing trimmed seek: %q", plan.Preview)
	}
}

func TestBuildCommandTrimDisabled(t *testing.T) {
	t.Parallel()
	profile := model.BuiltinProfiles()[0]
	// Built-ins ship with an empty (automatic) device; command
	// generation needs the concrete resolved path.
	profile.Video.VAAPIDevice = "/dev/dri/renderD128"
	plan, err := BuildCommand(BuildRequest{
		FFmpegPath: "ffmpeg",
		InputPath:  "/tmp/input.mkv",
		OutputPath: "/tmp/output.mkv",
		Profile:    profile,
	})
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(plan.Args, "-ss") || slices.Contains(plan.Args, "-t") {
		t.Errorf("disabled trim must not emit -ss/-t: %#v", plan.Args)
	}
	// An empty (non-nil but zero-length) selection behaves the same.
	plan, err = BuildCommand(BuildRequest{
		FFmpegPath: "ffmpeg",
		InputPath:  "/tmp/input.mkv",
		OutputPath: "/tmp/output.mkv",
		Profile:    profile,
		Trim:       model.TrimSelection{Segments: []model.TimeRange{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(plan.Args, "-ss") || slices.Contains(plan.Args, "-t") {
		t.Errorf("empty trim selection must not emit -ss/-t: %#v", plan.Args)
	}
}

func TestBuildCommandTrimCopy(t *testing.T) {
	t.Parallel()
	profile := model.BuiltinProfiles()[0]
	profile.Video.Encoder = "copy"
	plan, err := BuildCommand(BuildRequest{
		FFmpegPath: "ffmpeg",
		InputPath:  "/tmp/input.mkv",
		OutputPath: "/tmp/output.mkv",
		Profile:    profile,
		Trim:       model.TrimSelection{Segments: []model.TimeRange{{Start: 2 * time.Second, End: 5 * time.Second}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !containsPair(plan.Args, "-c:v", "copy") {
		t.Errorf("video copy missing: %#v", plan.Args)
	}
	if !containsPair(plan.Args, "-ss", "2.000000") || !containsPair(plan.Args, "-t", "3.000000") {
		t.Errorf("copy+trim must keep -ss/-t: %#v", plan.Args)
	}
	if slices.Contains(plan.Args, "-vf") {
		t.Errorf("copy command unexpectedly contains a filter: %#v", plan.Args)
	}
}

func TestBuildCommandTrimSubSecond(t *testing.T) {
	t.Parallel()
	profile := model.BuiltinProfiles()[0]
	profile.Video.VAAPIDevice = "/dev/dri/renderD128"

	// Sub-millisecond truncation matters for frame accuracy: ffmpeg's
	// input-side -ss drops frames strictly before the target, so a
	// 29.97 fps frame at 66.667 ms formatted with millisecond rounding
	// becomes 0.067 — past the frame — and the seek skips it.
	cases := []struct {
		name    string
		start   time.Duration
		end     time.Duration
		wantSS  string
		wantEnd string
	}{
		{"millisecond", time.Millisecond, 1500 * time.Millisecond, "0.001000", "1.499000"},
		{"29.97fps frame must not round up", 66667 * time.Microsecond, 166667 * time.Microsecond, "0.066667", "0.100000"},
		{"sub-microsecond digits truncate", 66667500 * time.Nanosecond, 166667500 * time.Nanosecond, "0.066667", "0.100000"},
		{"zero", 0, time.Second, "0.000000", "1.000000"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			plan, err := BuildCommand(BuildRequest{
				FFmpegPath: "ffmpeg",
				InputPath:  "/tmp/input.mkv",
				OutputPath: "/tmp/output.mkv",
				Profile:    profile,
				Trim:       model.TrimSelection{Segments: []model.TimeRange{{Start: tc.start, End: tc.end}}},
			})
			if err != nil {
				t.Fatal(err)
			}
			if !containsPair(plan.Args, "-ss", tc.wantSS) {
				t.Errorf("missing -ss %s in %#v", tc.wantSS, plan.Args)
			}
			if !containsPair(plan.Args, "-t", tc.wantEnd) {
				t.Errorf("missing -t %s in %#v", tc.wantEnd, plan.Args)
			}
		})
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

// indexOfArg returns the index of the first exact occurrence of arg,
// or -1.
func indexOfArg(args []string, arg string) int {
	return slices.Index(args, arg)
}
