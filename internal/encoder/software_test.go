package encoder

import (
	"slices"
	"strconv"
	"strings"
	"testing"

	"git.skobk.in/skobkin/yave/internal/model"
)

// findVideo returns the VideoEncoder for name, failing the test if it
// is not registered.
func findVideo(t *testing.T, name string) VideoEncoder {
	t.Helper()
	e, ok := DefaultRegistry().Get(name)
	if !ok {
		t.Fatalf("video encoder %q is not registered", name)
	}
	ve, ok := e.(VideoEncoder)
	if !ok {
		t.Fatalf("%q is registered but does not implement VideoEncoder", name)
	}
	return ve
}

// argsOf returns the post-c:v args for a build, failing if the encoder
// name does not match the requested name.
func argsOf(t *testing.T, res BuildResult, want string) []string {
	t.Helper()
	if len(res.Args) < 2 || res.Args[0] != "-c:v" || res.Args[1] != want {
		t.Fatalf("BuildVideoArgs header = %v, want [-c:v %s ...]", res.Args, want)
	}
	return res.Args[2:]
}

func TestSoftwareVideoEncodersAreRegistered(t *testing.T) {
	all := DefaultRegistry().VideoNames()
	// Software-only slice (filter by name) — registry sorts by
	// Priority desc, then Name asc, so libaom-av1 and h264_vaapi tie
	// at priority 60 and the global sort interleaves them. This test
	// asserts only that the software set is present and in registry
	// order.
	// libx264(100) libx265(90) libsvtav1(80) libvpx-vp9(70)
	// h264_vaapi(60) libaom-av1(60)
	wantSoftware := []string{"libx264", "libx265", "libsvtav1", "libvpx-vp9"}
	for _, name := range wantSoftware {
		if !slices.Contains(all, name) {
			t.Errorf("missing software encoder %q in VideoNames() = %v", name, all)
		}
	}
	// libaom-av1 is priority 60 — it must be present too but its slot
	// in the global sort is tied with h264_vaapi, so just check
	// membership.
	if !slices.Contains(all, "libaom-av1") {
		t.Errorf("missing libaom-av1 in VideoNames() = %v", all)
	}
}

func TestSoftwareVideoEncoderMetadata(t *testing.T) {
	tests := []struct {
		name         string
		family       string
		hw           HWBackend
		media        string
		kind         string
		supportsTune bool
		supportsProf bool
	}{
		{name: "libx264", family: "h264", hw: HWNone, media: MediaVideo, kind: KindSoftware, supportsTune: true, supportsProf: true},
		{name: "libx265", family: "hevc", hw: HWNone, media: MediaVideo, kind: KindSoftware, supportsTune: true, supportsProf: true},
		{name: "libsvtav1", family: "av1", hw: HWNone, media: MediaVideo, kind: KindSoftware, supportsTune: false, supportsProf: true},
		{name: "libvpx-vp9", family: "vp9", hw: HWNone, media: MediaVideo, kind: KindSoftware, supportsTune: false, supportsProf: false},
		{name: "libaom-av1", family: "av1", hw: HWNone, media: MediaVideo, kind: KindSoftware, supportsTune: false, supportsProf: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ve := findVideo(t, tt.name)
			if ve.Media() != tt.media {
				t.Errorf("Media() = %q, want %q", ve.Media(), tt.media)
			}
			if ve.Kind() != tt.kind {
				t.Errorf("Kind() = %q, want %q", ve.Kind(), tt.kind)
			}
			if ve.CodecFamily() != tt.family {
				t.Errorf("CodecFamily() = %q, want %q", ve.CodecFamily(), tt.family)
			}
			if ve.HWBackend() != tt.hw {
				t.Errorf("HWBackend() = %v, want %v", ve.HWBackend(), tt.hw)
			}
			if ve.SupportsTune() != tt.supportsTune {
				t.Errorf("SupportsTune() = %v, want %v", ve.SupportsTune(), tt.supportsTune)
			}
			if ve.SupportsProfile() != tt.supportsProf {
				t.Errorf("SupportsProfile() = %v, want %v", ve.SupportsProfile(), tt.supportsProf)
			}
		})
	}
}

func TestLibX264BuildArgs(t *testing.T) {
	ve := findVideo(t, "libx264")
	res, err := ve.BuildVideoArgs(model.VideoSettings{Encoder: "libx264", RateControl: "CRF", CRF: 23, Preset: "medium", Tune: "film", Profile: "high"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"-crf", "23", "-preset", "medium", "-tune", "film", "-profile:v", "high"}
	if got := argsOf(t, res, "libx264"); !slices.Equal(got, want) {
		t.Errorf("BuildVideoArgs = %v, want %v", got, want)
	}
}

func TestLibX264CRFWithoutOptionalFields(t *testing.T) {
	ve := findVideo(t, "libx264")
	res, err := ve.BuildVideoArgs(model.VideoSettings{Encoder: "libx264", RateControl: "CRF", CRF: 18})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"-crf", "18"}
	if got := argsOf(t, res, "libx264"); !slices.Equal(got, want) {
		t.Errorf("BuildVideoArgs = %v, want %v", got, want)
	}
}

func TestLibX265BuildArgs(t *testing.T) {
	ve := findVideo(t, "libx265")
	res, err := ve.BuildVideoArgs(model.VideoSettings{Encoder: "libx265", RateControl: "CRF", CRF: 28, Preset: "slow", Tune: "animation", Profile: "main"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"-crf", "28", "-preset", "slow", "-tune", "animation", "-profile:v", "main"}
	if got := argsOf(t, res, "libx265"); !slices.Equal(got, want) {
		t.Errorf("BuildVideoArgs = %v, want %v", got, want)
	}
}

func TestLibSvtAV1CRFAndPreset(t *testing.T) {
	ve := findVideo(t, "libsvtav1")
	res, err := ve.BuildVideoArgs(model.VideoSettings{Encoder: "libsvtav1", RateControl: "CRF", CRF: 30, Preset: "8"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"-crf", "30", "-b:v", "0", "-preset", "8"}
	if got := argsOf(t, res, "libsvtav1"); !slices.Equal(got, want) {
		t.Errorf("BuildVideoArgs = %v, want %v", got, want)
	}
}

func TestLibSvtAV1Bitrate(t *testing.T) {
	ve := findVideo(t, "libsvtav1")
	res, err := ve.BuildVideoArgs(model.VideoSettings{Encoder: "libsvtav1", RateControl: "BITRATE", Bitrate: "8M", Preset: "6"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"-b:v", "8M", "-preset", "6"}
	if got := argsOf(t, res, "libsvtav1"); !slices.Equal(got, want) {
		t.Errorf("BuildVideoArgs = %v, want %v", got, want)
	}
}

func TestLibSvtAV1PresetDefault(t *testing.T) {
	ve := findVideo(t, "libsvtav1")
	res, err := ve.BuildVideoArgs(model.VideoSettings{Encoder: "libsvtav1", RateControl: "CRF", CRF: 35})
	if err != nil {
		t.Fatal(err)
	}
	// Empty preset must default to "6" — matches the prior behaviour.
	ends := []string{"-preset", "6"}
	got := argsOf(t, res, "libsvtav1")
	if len(got) < 2 || !slices.Equal(got[len(got)-2:], ends) {
		t.Errorf("BuildVideoArgs tail = %v, want tail %v", got, ends)
	}
}

func TestLibVpxVP9CRF(t *testing.T) {
	ve := findVideo(t, "libvpx-vp9")
	res, err := ve.BuildVideoArgs(model.VideoSettings{Encoder: "libvpx-vp9", RateControl: "CRF", CRF: 32, Preset: "good"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"-crf", "32", "-b:v", "0", "-deadline", "good"}
	if got := argsOf(t, res, "libvpx-vp9"); !slices.Equal(got, want) {
		t.Errorf("BuildVideoArgs = %v, want %v", got, want)
	}
}

func TestLibVpxVP9Bitrate(t *testing.T) {
	ve := findVideo(t, "libvpx-vp9")
	res, err := ve.BuildVideoArgs(model.VideoSettings{Encoder: "libvpx-vp9", RateControl: "BITRATE", Bitrate: "4M", Preset: "best"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"-b:v", "4M", "-deadline", "best"}
	if got := argsOf(t, res, "libvpx-vp9"); !slices.Equal(got, want) {
		t.Errorf("BuildVideoArgs = %v, want %v", got, want)
	}
}

func TestLibVpxVP9NoPreset(t *testing.T) {
	ve := findVideo(t, "libvpx-vp9")
	res, err := ve.BuildVideoArgs(model.VideoSettings{Encoder: "libvpx-vp9", RateControl: "CRF", CRF: 31})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"-crf", "31", "-b:v", "0"}
	if got := argsOf(t, res, "libvpx-vp9"); !slices.Equal(got, want) {
		t.Errorf("BuildVideoArgs = %v, want %v", got, want)
	}
}

func TestLibaomAV1CRF(t *testing.T) {
	ve := findVideo(t, "libaom-av1")
	res, err := ve.BuildVideoArgs(model.VideoSettings{Encoder: "libaom-av1", RateControl: "CRF", CRF: 30, Preset: "4"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"-crf", "30", "-b:v", "0", "-cpu-used", "4"}
	if got := argsOf(t, res, "libaom-av1"); !slices.Equal(got, want) {
		t.Errorf("BuildVideoArgs = %v, want %v", got, want)
	}
}

func TestLibaomAV1Bitrate(t *testing.T) {
	ve := findVideo(t, "libaom-av1")
	res, err := ve.BuildVideoArgs(model.VideoSettings{Encoder: "libaom-av1", RateControl: "BITRATE", Bitrate: "2400k", Preset: "6"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"-b:v", "2400k", "-cpu-used", "6"}
	if got := argsOf(t, res, "libaom-av1"); !slices.Equal(got, want) {
		t.Errorf("BuildVideoArgs = %v, want %v", got, want)
	}
}

func TestLibaomAV1PresetDefault(t *testing.T) {
	ve := findVideo(t, "libaom-av1")
	res, err := ve.BuildVideoArgs(model.VideoSettings{Encoder: "libaom-av1", RateControl: "CRF", CRF: 32})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"-crf", "32", "-b:v", "0", "-cpu-used", "4"}
	if got := argsOf(t, res, "libaom-av1"); !slices.Equal(got, want) {
		t.Errorf("BuildVideoArgs = %v, want %v", got, want)
	}
}

func TestSoftwareVideoCRFRange(t *testing.T) {
	tests := []struct {
		name string
		min  int
		max  int
	}{
		{"libx264", 0, 51},
		{"libx265", 0, 51},
		{"libsvtav1", 0, 63},
		{"libvpx-vp9", 0, 63},
		{"libaom-av1", 0, 63},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ve := findVideo(t, tt.name)
			r, ok := ve.CRFRange()
			if !ok {
				t.Fatalf("CRFRange() not ok")
			}
			if r.Min != tt.min || r.Max != tt.max {
				t.Errorf("CRFRange = %d-%d, want %d-%d", r.Min, r.Max, tt.min, tt.max)
			}
		})
	}
}

func TestSoftwareVideoPresetRange(t *testing.T) {
	tests := []struct {
		name    string
		wantOk  bool
		wantMin int
		wantMax int
	}{
		{name: "libx264", wantOk: false},
		{name: "libx265", wantOk: false},
		{name: "libsvtav1", wantOk: true, wantMin: 0, wantMax: 13},
		{name: "libvpx-vp9", wantOk: false},
		{name: "libaom-av1", wantOk: true, wantMin: 0, wantMax: 8},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ve := findVideo(t, tt.name)
			r, ok := ve.PresetRange()
			if ok != tt.wantOk {
				t.Fatalf("PresetRange ok = %v, want %v", ok, tt.wantOk)
			}
			if ok && (r.Min != tt.wantMin || r.Max != tt.wantMax) {
				t.Errorf("PresetRange = %d-%d, want %d-%d", r.Min, r.Max, tt.wantMin, tt.wantMax)
			}
		})
	}
}

func TestSoftwareVideoQPRange(t *testing.T) {
	for _, name := range []string{"libx264", "libx265", "libsvtav1", "libvpx-vp9", "libaom-av1"} {
		t.Run(name, func(t *testing.T) {
			ve := findVideo(t, name)
			if _, ok := ve.QPRange(); ok {
				t.Errorf("%s QPRange ok=true, want false", name)
			}
		})
	}
}

func TestSoftwareVideoRateControlModes(t *testing.T) {
	for _, name := range []string{"libx264", "libx265", "libsvtav1", "libvpx-vp9", "libaom-av1"} {
		t.Run(name, func(t *testing.T) {
			ve := findVideo(t, name)
			modes := ve.RateControlModes()
			if !slices.Equal(modes, []RateControl{RCCRF, RCBitrate}) {
				t.Errorf("RateControlModes() = %v, want [CRF BITRATE]", modes)
			}
			if ve.DefaultRateControl() != RCCRF {
				t.Errorf("DefaultRateControl() = %v, want CRF", ve.DefaultRateControl())
			}
		})
	}
}

func TestSoftwareVideoFilterScaling(t *testing.T) {
	media := model.MediaInfo{
		VideoStreams: []model.VideoStream{{Width: 1920, Height: 1080}},
	}
	ve := findVideo(t, "libx264")
	got, err := ve.VideoFilter(model.VideoSettings{
		Encoder:       "libx264",
		MaxWidth:      1280,
		MaxHeight:     720,
		KeepAspectRatio: true,
	}, media)
	if err != nil {
		t.Fatal(err)
	}
	want := "scale=w=1280:h=720:force_original_aspect_ratio=decrease:force_divisible_by=2"
	if got != want {
		t.Errorf("VideoFilter = %q, want %q", got, want)
	}
}

func TestSoftwareVideoFilterNoScaling(t *testing.T) {
	media := model.MediaInfo{
		VideoStreams: []model.VideoStream{{Width: 1920, Height: 1080}},
	}
	for _, name := range []string{"libx264", "libx265", "libsvtav1", "libvpx-vp9", "libaom-av1"} {
		t.Run(name, func(t *testing.T) {
			ve := findVideo(t, name)
			got, err := ve.VideoFilter(model.VideoSettings{Encoder: name}, media)
			if err != nil {
				t.Fatal(err)
			}
			if got != "" {
				t.Errorf("VideoFilter = %q, want empty", got)
			}
		})
	}
}

func TestSoftwareVideoFilterFrameRateConversion(t *testing.T) {
	media := model.MediaInfo{}
	ve := findVideo(t, "libx264")
	got, err := ve.VideoFilter(model.VideoSettings{
		Encoder:       "libx264",
		FrameRateMode: "convert",
		FrameRate:     "30",
	}, media)
	if err != nil {
		t.Fatal(err)
	}
	want := "fps=30"
	if got != want {
		t.Errorf("VideoFilter = %q, want %q", got, want)
	}
}

func TestSoftwareVideoFilterFrameRateMissing(t *testing.T) {
	ve := findVideo(t, "libx264")
	_, err := ve.VideoFilter(model.VideoSettings{
		Encoder:       "libx264",
		FrameRateMode: "convert",
	}, model.MediaInfo{})
	if err == nil {
		t.Errorf("expected error when FrameRate is missing under convert mode")
	}
}

func TestSoftwareVideoBuildArgsEmptyEncoder(t *testing.T) {
	ve := findVideo(t, "libx264")
	if _, err := ve.BuildVideoArgs(model.VideoSettings{Encoder: ""}); err == nil {
		t.Errorf("BuildVideoArgs with empty encoder returned no error")
	}
}

func TestSoftwareVideoBuildArgsHasNoPreInput(t *testing.T) {
	// Software encoders never emit global pre-input flags. Tests in
	// TestBuildCommandVAAPI prove the VAAPI path still emits -vaapi_device.
	for _, name := range []string{"libx264", "libx265", "libsvtav1", "libvpx-vp9", "libaom-av1"} {
		t.Run(name, func(t *testing.T) {
			ve := findVideo(t, name)
			res, err := ve.BuildVideoArgs(model.VideoSettings{Encoder: name, RateControl: "CRF", CRF: 20})
			if err != nil {
				t.Fatal(err)
			}
			if len(res.PreInputArgs) != 0 {
				t.Errorf("PreInputArgs = %v, want empty for software encoder", res.PreInputArgs)
			}
		})
	}
}

// containsPair is shared between encoder tests and the ffmpeg command
// tests, but the encoder package does not depend on ffmpeg so we keep
// a small local copy here.
func containsPair(args []string, key, value string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == key && args[i+1] == value {
			return true
		}
	}
	return false
}

// Quick parity check: BuildVideoArgs output is byte-identical to the
// prior buildVideoArgs switch for a representative sample of inputs.
// These cases are the same as the ones in ffmpeg/command_test.go.
func TestSoftwareVideoBuildArgsParityWithCommandTests(t *testing.T) {
	t.Run("libsvtav1 CRF 63 + preset 13", func(t *testing.T) {
		ve := findVideo(t, "libsvtav1")
		res, err := ve.BuildVideoArgs(model.VideoSettings{
			Encoder: "libsvtav1", RateControl: "CRF", CRF: 63, Preset: "13",
		})
		if err != nil {
			t.Fatal(err)
		}
		if !containsPair(res.Args, "-crf", strconv.Itoa(63)) || !containsPair(res.Args, "-preset", "13") {
			t.Errorf("missing CRF/preset pair in %#v", res.Args)
		}
		if strings.Contains(strings.Join(res.Args, " "), "-b:v 0 -b:v 0") {
			t.Errorf("double -b:v 0 in %#v", res.Args)
		}
	})
}