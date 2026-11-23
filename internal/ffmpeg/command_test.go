package ffmpeg

import (
	"os"
	"path/filepath"
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

// TestBuildCommandHW10BitFilterChains pins the exact 10-bit filter
// strings the three hardware families emit: explicit main10 profiles
// ride p010 upload chains with the profile value passed through, and
// the VAAPI auto profile on a 10-bit source emits the same chain
// without any -profile:v flag. These are the exact strings the
// empirical E1–E4 runs executed through ffmpeg and probed back as
// Main 10 / yuv420p10le.
func TestBuildCommandHW10BitFilterChains(t *testing.T) {
	t.Parallel()
	tenBit := model.MediaInfo{
		VideoStreams: []model.VideoStream{{Width: 1920, Height: 1080, PixelFormat: "yuv420p10le"}},
	}
	base := func(encoder string) model.Profile {
		return model.Profile{
			Name: "t", Container: model.ContainerMKV, OutputTemplate: "{dir}/{name}.{ext}",
			Video: model.VideoSettings{
				Encoder: encoder, RateControl: "CQP", QP: 30, Profile: "main10",
			},
			Audio:     model.AudioSettings{Mode: model.AudioCopy, Encoder: "copy"},
			Subtitles: model.SubtitleSettings{Mode: model.SubtitleCopy},
		}
	}
	assertFilter := func(t *testing.T, plan model.RunPlan, want string) {
		t.Helper()
		if !slices.Contains(plan.Args, want) {
			t.Errorf("plan args %#v miss the 10-bit chain %q", plan.Args, want)
		}
	}

	t.Run("hevc_vaapi main10", func(t *testing.T) {
		p := base("hevc_vaapi")
		p.Video.VAAPIDevice = "/dev/dri/renderD129"
		plan, err := BuildCommand(BuildRequest{
			FFmpegPath: "ffmpeg", InputPath: "in.mkv", OutputPath: "out.mkv", Profile: p, Media: tenBit,
		})
		if err != nil {
			t.Fatal(err)
		}
		assertFilter(t, plan, "format=p010le,hwupload")
		if !containsPair(plan.Args, "-profile:v", "main10") {
			t.Errorf("plan args %#v miss -profile:v main10", plan.Args)
		}
		if !containsPair(plan.Args, "-vaapi_device", "/dev/dri/renderD129") {
			t.Errorf("plan args %#v miss -vaapi_device", plan.Args)
		}
	})

	t.Run("hevc_vaapi auto on a 10-bit source emits no profile flag", func(t *testing.T) {
		p := base("hevc_vaapi")
		p.Video.Profile = ""
		p.Video.VAAPIDevice = "/dev/dri/renderD129"
		plan, err := BuildCommand(BuildRequest{
			FFmpegPath: "ffmpeg", InputPath: "in.mkv", OutputPath: "out.mkv", Profile: p, Media: tenBit,
		})
		if err != nil {
			t.Fatal(err)
		}
		assertFilter(t, plan, "format=p010le,hwupload")
		if slices.Contains(plan.Args, "-profile:v") {
			t.Errorf("auto profile must not emit -profile:v: %#v", plan.Args)
		}
	})

	t.Run("hevc_nvenc main10", func(t *testing.T) {
		plan, err := BuildCommand(BuildRequest{
			FFmpegPath: "ffmpeg", InputPath: "in.mkv", OutputPath: "out.mkv", Profile: base("hevc_nvenc"), Media: tenBit,
		})
		if err != nil {
			t.Fatal(err)
		}
		assertFilter(t, plan, "format=p010le,hwupload_cuda")
		if !containsPair(plan.Args, "-profile:v", "main10") {
			t.Errorf("plan args %#v miss -profile:v main10", plan.Args)
		}
	})

	t.Run("hevc_qsv main10", func(t *testing.T) {
		plan, err := BuildCommand(BuildRequest{
			FFmpegPath: "ffmpeg", InputPath: "in.mkv", OutputPath: "out.mkv", Profile: base("hevc_qsv"), Media: tenBit,
		})
		if err != nil {
			t.Fatal(err)
		}
		assertFilter(t, plan, "format=p010le,hwupload,format=qsv")
		if !containsPair(plan.Args, "-profile:v", "main10") {
			t.Errorf("plan args %#v miss -profile:v main10", plan.Args)
		}
	})
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

// Regression: with the input-side -ss, copied audio from before the cut
// arrived with negative rebased timestamps that muxers resolve by
// shifting the whole timeline (Matroska has no edit lists) — a [2s,3s)
// cut measured 3.018s with its video starting at 1.977s under ~2s of
// preroll audio. Re-encoded output now carries an output-side -ss 0
// that discards exactly those packets; video copy keeps the whole-GOP
// behavior, where dropping negative packets would strand the stream
// mid-GOP.
func TestBuildCommandTrimDropsCopiedAudioPreroll(t *testing.T) {
	t.Parallel()
	profile := model.BuiltinProfiles()[3] // software encoder, no device requirements
	profile.Container = model.ContainerMKV
	trim := model.TrimSelection{Segments: []model.TimeRange{{Start: 2 * time.Second, End: 3 * time.Second}}}

	plan, err := BuildCommand(BuildRequest{
		FFmpegPath: "ffmpeg",
		InputPath:  "/tmp/input.mkv",
		OutputPath: "/tmp/output.mkv",
		Profile:    profile,
		Trim:       trim,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !containsPair(plan.Args, "-ss", "0.000000") {
		t.Fatalf("output-side zero seek missing: %#v", plan.Args)
	}
	// The zero seek is output-side: after -i, at or before -t.
	iIndex := indexOfArg(plan.Args, "-i")
	zeroIndex := -1
	for i := iIndex; i < len(plan.Args); i++ {
		if plan.Args[i] == "-ss" {
			zeroIndex = i
			break
		}
	}
	tIndex := indexOfArg(plan.Args, "-t")
	if iIndex < 0 || zeroIndex < iIndex || tIndex < zeroIndex {
		t.Errorf("zero seek at %d must sit between -i (at %d) and -t (at %d): %#v", zeroIndex, iIndex, tIndex, plan.Args)
	}

	// Video copy keeps the keyframe-snap behavior: no output-side zero
	// seek, or the dropped negative packets would leave the copied
	// stream starting mid-GOP.
	profile.Video.Encoder = "copy"
	plan, err = BuildCommand(BuildRequest{
		FFmpegPath: "ffmpeg",
		InputPath:  "/tmp/input.mkv",
		OutputPath: "/tmp/output.mkv",
		Profile:    profile,
		Trim:       trim,
	})
	if err != nil {
		t.Fatal(err)
	}
	if containsPair(plan.Args, "-ss", "0.000000") {
		t.Errorf("video copy must not emit the zero seek: %#v", plan.Args)
	}
	if !containsPair(plan.Args, "-ss", "2.000000") || !containsPair(plan.Args, "-t", "1.000000") {
		t.Errorf("video copy trim flags broken: %#v", plan.Args)
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

func TestBuildCommandRejectsMultipleTrimSegments(t *testing.T) {
	t.Parallel()
	profile := model.BuiltinProfiles()[1]
	_, err := BuildCommand(BuildRequest{
		FFmpegPath: "/usr/bin/ffmpeg",
		InputPath:  "/tmp/input.mkv",
		OutputPath: "/tmp/output.mkv",
		Profile:    profile,
		Media:      model.MediaInfo{VideoStreams: []model.VideoStream{{Width: 640, Height: 360}}},
		Trim: model.TrimSelection{Segments: []model.TimeRange{
			{Start: time.Second, End: 2 * time.Second},
			{Start: 3 * time.Second, End: 4 * time.Second},
		}},
	})
	if err == nil {
		t.Fatal("BuildCommand must reject a multi-segment trim selection")
	}
	if !strings.Contains(err.Error(), "single trim segment") {
		t.Fatalf("error should mention the single-segment contract: %v", err)
	}
}

func TestBuildConcatCommand(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		container     string
		wantMuxer     string
		wantFaststart bool
		overwrite     bool
	}{
		{name: "mp4", container: model.ContainerMP4, wantMuxer: "mp4", wantFaststart: true},
		{name: "mkv", container: model.ContainerMKV, wantMuxer: "matroska"},
		{name: "webm", container: model.ContainerWebM, wantMuxer: "webm"},
		{name: "overwrite allows -y", container: model.ContainerMKV, wantMuxer: "matroska", overwrite: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			plan, err := BuildConcatCommand(ConcatRequest{
				FFmpegPath: "/usr/bin/ffmpeg",
				ListPath:   "/tmp/yave-join-123/concat.txt",
				OutputPath: "/tmp/yave-join-123/final.mp4",
				Container:  tt.container,
				Overwrite:  tt.overwrite,
			})
			if err != nil {
				t.Fatal(err)
			}
			args := plan.Args
			if got := args[0]; got != "-hide_banner" {
				t.Errorf("first arg = %q, want -hide_banner", got)
			}
			wantOverwrite := "-n"
			if tt.overwrite {
				wantOverwrite = "-y"
			}
			if args[1] != wantOverwrite {
				t.Errorf("second arg = %q, want %q", args[1], wantOverwrite)
			}
			for _, flag := range []string{"-progress", "-nostats"} {
				if indexOfArg(args, flag) < 0 {
					t.Errorf("missing %s in %#v", flag, args)
				}
			}
			iF, iConcat := indexOfArg(args, "-f"), indexOfArg(args, "concat")
			iSafe, iList, iInput := indexOfArg(args, "-safe"), indexOfArg(args, "/tmp/yave-join-123/concat.txt"), indexOfArg(args, "-i")
			if iConcat != iF+1 || iInput != iSafe+2 || iList != iInput+1 {
				t.Errorf("concat demuxer flags out of order: %#v", args)
			}
			if iSafe == -1 || args[iSafe+1] != "0" {
				t.Error("expected -safe 0")
			}
			if !containsPair(args, "-map", "0") || indexOfArg(args, "-map") < iInput {
				t.Errorf("-map 0 must follow -i: %#v", args)
			}
			if !containsPair(args, "-c", "copy") {
				t.Errorf("missing -c copy in %#v", args)
			}
			if iMux := indexOfArg(args, tt.wantMuxer); iMux < 0 || args[iMux-1] != "-f" {
				t.Errorf("expected explicit -f %s: %#v", tt.wantMuxer, args)
			}
			if iOut := len(args) - 1; args[iOut] != "/tmp/yave-join-123/final.mp4" {
				t.Errorf("output path must be last: %#v", args)
			}
			if iFast := indexOfArg(args, "-movflags"); tt.wantFaststart {
				if iFast < 0 || !containsPair(args, "-movflags", "+faststart") {
					t.Errorf("mp4 output needs -movflags +faststart: %#v", args)
				}
			} else if iFast >= 0 {
				t.Errorf("-movflags must only appear for mp4: %#v", args)
			}
			if plan.OutputPath != "/tmp/yave-join-123/final.mp4" || plan.Preview == "" {
				t.Errorf("plan mismatch: %#v", plan)
			}
		})
	}
}

func TestBuildConcatCommandAudioArgs(t *testing.T) {
	t.Parallel()
	build := func(audioArgs []string) []string {
		t.Helper()
		plan, err := BuildConcatCommand(ConcatRequest{
			FFmpegPath: "/usr/bin/ffmpeg",
			ListPath:   "/tmp/yave-join-123/concat.txt",
			OutputPath: "/tmp/yave-join-123/final.mkv",
			Container:  model.ContainerMKV,
			AudioArgs:  audioArgs,
		})
		if err != nil {
			t.Fatal(err)
		}
		return plan.Args
	}

	args := build([]string{"-c:a", "flac", "-compression_level", "5"})
	if !containsPair(args, "-c", "copy") {
		t.Errorf("blanket -c copy must stay for video/subtitles: %#v", args)
	}
	iCopy, iFlac := indexOfArg(args, "copy"), indexOfArg(args, "-c:a")
	if iFlac < 0 {
		t.Fatalf("missing -c:a override: %#v", args)
	}
	if iFlac < iCopy {
		t.Errorf("-c:a must follow -c copy to override audio only: %#v", args)
	}
	if !containsPair(args, "-c:a", "flac") || !containsPair(args, "-compression_level", "5") {
		t.Errorf("audio re-encode args missing or reordered: %#v", args)
	}

	nilArgs := build(nil)
	if indexOfArg(nilArgs, "-c:a") >= 0 {
		t.Errorf("no AudioArgs must leave plain copy: %#v", nilArgs)
	}
}

func TestJoinAudioArgs(t *testing.T) {
	t.Parallel()
	media := func(codecs ...string) model.MediaInfo {
		streams := make([]model.AudioStream, len(codecs))
		for i, codec := range codecs {
			streams[i] = model.AudioStream{Index: i, Codec: codec}
		}
		return model.MediaInfo{AudioStreams: streams}
	}
	tests := []struct {
		name      string
		audio     model.AudioSettings
		media     model.MediaInfo
		wantArgs  []string
		wantError string
	}{
		{
			name:     "flac encode joins with the profile's encoder args",
			audio:    model.AudioSettings{Mode: model.AudioEncode, Encoder: "flac", Compression: 8},
			wantArgs: []string{"-c:a", "flac", "-compression_level", "8"},
		},
		{
			name:     "aac encode keeps copy",
			audio:    model.AudioSettings{Mode: model.AudioEncode, Encoder: "aac", Bitrate: "192k"},
			media:    media("aac"),
			wantArgs: nil,
		},
		{
			name:     "copied first-stream flac re-encodes with defaults",
			audio:    model.AudioSettings{Mode: model.AudioCopy, Encoder: model.AudioCopy},
			media:    media("flac"),
			wantArgs: []string{"-c:a:0", "flac"},
		},
		{
			name:     "copied aac keeps copy",
			audio:    model.AudioSettings{Mode: model.AudioCopy, Encoder: model.AudioCopy},
			media:    media("aac"),
			wantArgs: nil,
		},
		{
			// A source deeper than 24 bits would be truncated by the
			// encoder's s32 default, so the join adds -strict experimental.
			name:     "copied 32-bit flac adds strict experimental",
			audio:    model.AudioSettings{Mode: model.AudioCopy, Encoder: model.AudioCopy},
			media:    model.MediaInfo{AudioStreams: []model.AudioStream{{Index: 0, Codec: "flac", BitsPerSample: 32}}},
			wantArgs: []string{"-c:a:0", "flac", "-strict", "experimental"},
		},
		{
			name:     "copied 24-bit flac keeps the default depth",
			audio:    model.AudioSettings{Mode: model.AudioCopy, Encoder: model.AudioCopy},
			media:    model.MediaInfo{AudioStreams: []model.AudioStream{{Index: 0, Codec: "flac", BitsPerSample: 24}}},
			wantArgs: []string{"-c:a:0", "flac"},
		},
		{
			// -strict is output-global: any mapped deep stream enables it.
			name:     "copy all streams adds strict when the flac stream is deep",
			audio:    model.AudioSettings{Mode: model.AudioCopy, Encoder: model.AudioCopy, CopyAllStreams: true},
			media:    model.MediaInfo{AudioStreams: []model.AudioStream{{Index: 0, Codec: "aac"}, {Index: 1, Codec: "flac", BitsPerSample: 32}}},
			wantArgs: []string{"-c:a:1", "flac", "-strict", "experimental"},
		},
		{
			// Encode mode segments already define the output depth via the
			// profile; the source's depth must not leak strict in there.
			name:     "encode mode ignores the source depth",
			audio:    model.AudioSettings{Mode: model.AudioEncode, Encoder: "flac", Compression: 8},
			media:    model.MediaInfo{AudioStreams: []model.AudioStream{{Index: 0, Codec: "flac", BitsPerSample: 32}}},
			wantArgs: []string{"-c:a", "flac", "-compression_level", "8"},
		},
		{
			name:     "copy all streams targets only the flac stream",
			audio:    model.AudioSettings{Mode: model.AudioCopy, Encoder: model.AudioCopy, CopyAllStreams: true},
			media:    media("aac", "flac"),
			wantArgs: []string{"-c:a:1", "flac"},
		},
		{
			name:     "copy all streams with flac first leaves aac copied",
			audio:    model.AudioSettings{Mode: model.AudioCopy, Encoder: model.AudioCopy, CopyAllStreams: true},
			media:    media("flac", "aac"),
			wantArgs: []string{"-c:a:0", "flac"},
		},
		{
			name:     "copy all streams with two flac streams targets both",
			audio:    model.AudioSettings{Mode: model.AudioCopy, Encoder: model.AudioCopy, CopyAllStreams: true},
			media:    media("flac", "aac", "flac"),
			wantArgs: []string{"-c:a:0", "flac", "-c:a:2", "flac"},
		},
		{
			name:     "single-stream copy ignores a second flac stream",
			audio:    model.AudioSettings{Mode: model.AudioCopy, Encoder: model.AudioCopy},
			media:    media("aac", "flac"),
			wantArgs: nil,
		},
		{
			name:     "copied audio with no streams keeps copy",
			audio:    model.AudioSettings{Mode: model.AudioCopy, Encoder: model.AudioCopy},
			media:    model.MediaInfo{},
			wantArgs: nil,
		},
		{
			name:      "unknown encode-mode encoder errors",
			audio:     model.AudioSettings{Mode: model.AudioEncode, Encoder: "madeup"},
			wantError: `unsupported audio encoder "madeup"`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			args, err := JoinAudioArgs(model.Profile{Audio: tt.audio}, tt.media)
			if tt.wantError != "" {
				if err == nil || err.Error() != tt.wantError {
					t.Fatalf("err = %v, want %q", err, tt.wantError)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(args, tt.wantArgs) {
				t.Errorf("args = %#v, want %#v", args, tt.wantArgs)
			}
		})
	}
}

func TestBuildConcatCommandRejectsUnknownContainer(t *testing.T) {
	t.Parallel()
	if _, err := BuildConcatCommand(ConcatRequest{
		FFmpegPath: "/usr/bin/ffmpeg", ListPath: "l", OutputPath: "o", Container: "avi",
	}); err == nil {
		t.Fatal("unknown container must be rejected")
	}
	if _, err := BuildConcatCommand(ConcatRequest{ListPath: "l", OutputPath: "o", Container: "mp4"}); err == nil {
		t.Fatal("empty ffmpeg path must be rejected")
	}
	if _, err := BuildConcatCommand(ConcatRequest{FFmpegPath: "f", OutputPath: "o", Container: "mp4"}); err == nil {
		t.Fatal("empty list path must be rejected")
	}
	if _, err := BuildConcatCommand(ConcatRequest{FFmpegPath: "f", ListPath: "l", Container: "mp4"}); err == nil {
		t.Fatal("empty output path must be rejected")
	}
}

func TestWriteConcatList(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	list := filepath.Join(dir, "concat.txt")
	parts := []string{"/staging/segment001.mkv", "/staging/it's a quote.mkv", "/staging/segment003.mkv"}
	if err := WriteConcatList(list, parts); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(list) //nolint:gosec // G304: reads back the list this test just wrote into its temp dir
	if err != nil {
		t.Fatal(err)
	}
	want := "file '/staging/segment001.mkv'\n" +
		"file '/staging/it'\\''s a quote.mkv'\n" +
		"file '/staging/segment003.mkv'\n"
	if string(raw) != want {
		t.Fatalf("list contents = %q, want %q", raw, want)
	}
	if info, err := os.Stat(list); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("list permissions = %v, want 0600 (err %v)", info.Mode().Perm(), err)
	}
	if err := WriteConcatList(filepath.Join(dir, "nl.txt"), []string{"/a\nb.mkv"}); err == nil {
		t.Fatal("newline in a segment path must be rejected")
	}
	if err := WriteConcatList(filepath.Join(dir, "empty.txt"), nil); err == nil {
		t.Fatal("empty part list must be rejected")
	}
}

func TestBuildCommandNeverMapsCoverArtAsVideo(t *testing.T) {
	t.Parallel()
	profile := model.BuiltinProfiles()[3]
	tests := []struct {
		name  string
		media model.MediaInfo
		want  string
	}{
		{
			name:  "cover after the video keeps 0:v:0",
			media: model.MediaInfo{VideoStreams: []model.VideoStream{{Index: 0, Width: 1920, Height: 1080}}, CoverArtStreams: []model.VideoStream{{Index: 2}}},
			want:  "0:v:0",
		},
		{
			name:  "cover before the video maps the real stream by index",
			media: model.MediaInfo{VideoStreams: []model.VideoStream{{Index: 1, Width: 1920, Height: 1080}}, CoverArtStreams: []model.VideoStream{{Index: 0}}},
			want:  "0:1",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			plan, err := BuildCommand(BuildRequest{
				FFmpegPath: "ffmpeg", InputPath: "/tmp/in.mp4", OutputPath: "/tmp/out.mkv",
				Profile: profile, Media: tc.media,
			})
			if err != nil {
				t.Fatal(err)
			}
			if !containsPair(plan.Args, "-map", tc.want) {
				t.Fatalf("args %v lack -map %s", plan.Args, tc.want)
			}
		})
	}
}

func TestBuildCommandStreamExclusion(t *testing.T) {
	t.Parallel()
	base := model.BuiltinProfiles()[3] // x265 software archive
	base.Audio = model.AudioSettings{Mode: model.AudioEncode, Encoder: "aac", Bitrate: "128k"}
	media := model.MediaInfo{
		VideoStreams: []model.VideoStream{{Index: 0, Width: 1920, Height: 1080}},
		AudioStreams: []model.AudioStream{{Index: 1, Codec: "aac"}},
	}
	build := func(t *testing.T, mutate func(*model.Profile), trim model.TrimSelection) []string {
		t.Helper()
		p := base
		mutate(&p)
		plan, err := BuildCommand(BuildRequest{
			FFmpegPath: "ffmpeg", InputPath: "/in.mkv", OutputPath: "/out.mka",
			Profile: p, Media: media, Trim: trim,
		})
		if err != nil {
			t.Fatal(err)
		}
		return plan.Args
	}
	has := func(args []string, want string) bool { return slices.Contains(args, want) }

	t.Run("normal keeps both", func(t *testing.T) {
		args := build(t, func(*model.Profile) {}, model.TrimSelection{})
		if has(args, "-vn") || has(args, "-an") || !containsPair(args, "-map", "0:v:0") || !containsPair(args, "-map", "0:a:0?") {
			t.Fatalf("unexpected args %v", args)
		}
	})
	t.Run("audio only", func(t *testing.T) {
		args := build(t, func(p *model.Profile) { p.ExcludeVideo = true }, model.TrimSelection{})
		if !has(args, "-vn") || has(args, "-an") || containsPair(args, "-map", "0:v:0") || has(args, "-vf") || has(args, "libx265") {
			t.Fatalf("audio-only args %v", args)
		}
		if !containsPair(args, "-map", "0:a:0?") || !containsPair(args, "-c:a", "aac") {
			t.Fatalf("audio not encoded: %v", args)
		}
		if !containsPair(args, "-f", "matroska") {
			t.Fatalf("audio-only output must pin the muxer: %v", args)
		}
	})
	t.Run("video only", func(t *testing.T) {
		args := build(t, func(p *model.Profile) { p.ExcludeAudio = true }, model.TrimSelection{})
		if !has(args, "-an") || has(args, "-vn") || containsPair(args, "-map", "0:a:0?") || has(args, "-c:a") {
			t.Fatalf("video-only args %v", args)
		}
		if !containsPair(args, "-map", "0:v:0") {
			t.Fatalf("video not mapped: %v", args)
		}
	})
	t.Run("audio only trim", func(t *testing.T) {
		trim := model.TrimSelection{Segments: []model.TimeRange{{Start: time.Second, End: 3 * time.Second}}}
		args := build(t, func(p *model.Profile) { p.ExcludeVideo = true }, trim)
		if !containsPair(args, "-ss", "1.000000") || !containsPair(args, "-t", "2.000000") {
			t.Fatalf("trim args missing: %v", args)
		}
	})
	t.Run("both excluded is rejected", func(t *testing.T) {
		p := base
		p.ExcludeVideo, p.ExcludeAudio = true, true
		if _, err := BuildCommand(BuildRequest{FFmpegPath: "ffmpeg", InputPath: "/in", OutputPath: "/out", Profile: p, Media: media}); err == nil {
			t.Fatal("expected an error")
		}
	})
}
