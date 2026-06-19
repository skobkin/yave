package encoder

import (
	"slices"
	"strconv"
	"strings"
	"testing"

	"git.skobk.in/skobkin/yave/internal/model"
)

func TestVAAPIVideoEncodersAreRegistered(t *testing.T) {
	all := DefaultRegistry().VideoNames()
	// Sorted by Priority desc, then Name asc:
	// libx264(100) libx265(90) libsvtav1(80) libvpx-vp9(70)
	// h264_vaapi(60) libaom-av1(60)
	// hevc_vaapi(50) av1_vaapi(40) vp9_vaapi(30)
	want := []string{
		"libx264", "libx265", "libsvtav1", "libvpx-vp9",
		"h264_vaapi", "libaom-av1",
		"hevc_vaapi", "av1_vaapi", "vp9_vaapi",
	}
	if !slices.Equal(all, want) {
		t.Errorf("VideoNames() = %v, want %v", all, want)
	}
}

func TestVAAPIVideoEncoderMetadata(t *testing.T) {
	tests := []struct {
		name    string
		family  string
		qpMin   int
		qpMax   int
		hw      HWBackend
		media   string
		kind    string
	}{
		{name: "h264_vaapi", family: "h264", qpMin: 1, qpMax: 51, hw: HWVAAPI, media: MediaVideo, kind: KindHardware},
		{name: "hevc_vaapi", family: "hevc", qpMin: 1, qpMax: 51, hw: HWVAAPI, media: MediaVideo, kind: KindHardware},
		{name: "av1_vaapi", family: "av1", qpMin: 0, qpMax: 255, hw: HWVAAPI, media: MediaVideo, kind: KindHardware},
		{name: "vp9_vaapi", family: "vp9", qpMin: 0, qpMax: 255, hw: HWVAAPI, media: MediaVideo, kind: KindHardware},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ve := findVideo(t, tt.name)
			if ve.CodecFamily() != tt.family {
				t.Errorf("CodecFamily() = %q, want %q", ve.CodecFamily(), tt.family)
			}
			if ve.HWBackend() != tt.hw {
				t.Errorf("HWBackend() = %v, want %v", ve.HWBackend(), tt.hw)
			}
			if ve.Media() != tt.media {
				t.Errorf("Media() = %q, want %q", ve.Media(), tt.media)
			}
			if ve.Kind() != tt.kind {
				t.Errorf("Kind() = %q, want %q", ve.Kind(), tt.kind)
			}
			r, ok := ve.QPRange()
			if !ok {
				t.Fatalf("QPRange not ok")
			}
			if r.Min != tt.qpMin || r.Max != tt.qpMax {
				t.Errorf("QPRange = %d-%d, want %d-%d", r.Min, r.Max, tt.qpMin, tt.qpMax)
			}
			if ve.SupportsProfile() != true {
				t.Errorf("SupportsProfile() = false, want true")
			}
			if ve.SupportsTune() != false {
				t.Errorf("SupportsTune() = true, want false")
			}
			if _, ok := ve.CRFRange(); ok {
				t.Errorf("CRFRange ok=true, want false")
			}
			if _, ok := ve.PresetRange(); ok {
				t.Errorf("PresetRange ok=true, want false")
			}
		})
	}
}

func TestVAAPIRateControlModes(t *testing.T) {
	ve := findVideo(t, "hevc_vaapi")
	got := ve.RateControlModes()
	want := []RateControl{RCAuto, RCCQP, RCVBR, RCCBR}
	if !slices.Equal(got, want) {
		t.Errorf("RateControlModes = %v, want %v", got, want)
	}
	if ve.DefaultRateControl() != RCAuto {
		t.Errorf("DefaultRateControl = %v, want AUTO", ve.DefaultRateControl())
	}
}

func TestVAAPIBuildArgsAuto(t *testing.T) {
	ve := findVideo(t, "hevc_vaapi")
	res, err := ve.BuildVideoArgs(model.VideoSettings{
		Encoder:     "hevc_vaapi",
		VAAPIDevice: "/dev/dri/renderD128",
		RateControl: "AUTO",
		Bitrate:     "8M",
		Profile:     "main",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(res.PreInputArgs, []string{"-vaapi_device", "/dev/dri/renderD128"}) {
		t.Errorf("PreInputArgs = %v", res.PreInputArgs)
	}
	want := []string{"-c:v", "hevc_vaapi", "-b:v", "8M", "-profile:v", "main"}
	if !slices.Equal(res.Args, want) {
		t.Errorf("Args = %v, want %v", res.Args, want)
	}
}

func TestVAAPIBuildArgsCQP(t *testing.T) {
	ve := findVideo(t, "av1_vaapi")
	res, err := ve.BuildVideoArgs(model.VideoSettings{
		Encoder:     "av1_vaapi",
		VAAPIDevice: "/dev/dri/renderD128",
		RateControl: "CQP",
		QP:          200,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(res.PreInputArgs, []string{"-vaapi_device", "/dev/dri/renderD128"}) {
		t.Errorf("PreInputArgs = %v", res.PreInputArgs)
	}
	want := []string{"-c:v", "av1_vaapi", "-rc_mode", "CQP", "-qp", "200"}
	if !slices.Equal(res.Args, want) {
		t.Errorf("Args = %v, want %v", res.Args, want)
	}
}

func TestVAAPIBuildArgsVBR(t *testing.T) {
	ve := findVideo(t, "hevc_vaapi")
	res, err := ve.BuildVideoArgs(model.VideoSettings{
		Encoder:     "hevc_vaapi",
		VAAPIDevice: "/dev/dri/renderD128",
		RateControl: "VBR",
		Bitrate:     "8M",
		Maxrate:     "12M",
		Bufsize:     "16M",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"-c:v", "hevc_vaapi", "-rc_mode", "VBR", "-b:v", "8M", "-maxrate", "12M", "-bufsize", "16M"}
	if !slices.Equal(res.Args, want) {
		t.Errorf("Args = %v, want %v", res.Args, want)
	}
}

func TestVAAPIBuildArgsCBR(t *testing.T) {
	ve := findVideo(t, "hevc_vaapi")
	res, err := ve.BuildVideoArgs(model.VideoSettings{
		Encoder:     "hevc_vaapi",
		VAAPIDevice: "/dev/dri/renderD128",
		RateControl: "CBR",
		Bitrate:     "6M",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"-c:v", "hevc_vaapi", "-rc_mode", "CBR", "-b:v", "6M"}
	if !slices.Equal(res.Args, want) {
		t.Errorf("Args = %v, want %v", res.Args, want)
	}
}

func TestVAAPIBuildArgsVBRMissingBitrate(t *testing.T) {
	ve := findVideo(t, "hevc_vaapi")
	_, err := ve.BuildVideoArgs(model.VideoSettings{
		Encoder:     "hevc_vaapi",
		VAAPIDevice: "/dev/dri/renderD128",
		RateControl: "VBR",
	})
	if err == nil {
		t.Errorf("VBR without bitrate returned no error")
	}
}

func TestVAAPIBuildArgsUnsupportedRateControl(t *testing.T) {
	ve := findVideo(t, "hevc_vaapi")
	_, err := ve.BuildVideoArgs(model.VideoSettings{
		Encoder:     "hevc_vaapi",
		VAAPIDevice: "/dev/dri/renderD128",
		RateControl: "CONSTQP",
	})
	if err == nil {
		t.Errorf("unsupported rate control returned no error")
	}
}

func TestVAAPIBuildArgsEmptyDevice(t *testing.T) {
	// An empty VAAPI device must not emit -vaapi_device in PreInputArgs.
	// The UI surfaces this as an error in validation.
	ve := findVideo(t, "hevc_vaapi")
	res, err := ve.BuildVideoArgs(model.VideoSettings{
		Encoder:     "hevc_vaapi",
		VAAPIDevice: "",
		RateControl: "AUTO",
		Bitrate:     "4M",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.PreInputArgs) != 0 {
		t.Errorf("PreInputArgs = %v, want empty", res.PreInputArgs)
	}
}

func TestVAAPIVideoFilterWithScale(t *testing.T) {
	media := model.MediaInfo{
		VideoStreams: []model.VideoStream{{Width: 1920, Height: 1080}},
	}
	ve := findVideo(t, "hevc_vaapi")
	got, err := ve.VideoFilter(model.VideoSettings{
		Encoder:         "hevc_vaapi",
		MaxWidth:        1280,
		MaxHeight:       720,
		KeepAspectRatio: true,
	}, media)
	if err != nil {
		t.Fatal(err)
	}
	want := "format=nv12,hwupload,scale_vaapi=w=1280:h=720:force_original_aspect_ratio=decrease:force_divisible_by=2"
	if got != want {
		t.Errorf("VideoFilter = %q, want %q", got, want)
	}
}

func TestVAAPIVideoFilterNoScale(t *testing.T) {
	media := model.MediaInfo{
		VideoStreams: []model.VideoStream{{Width: 1920, Height: 1080}},
	}
	for _, name := range []string{"h264_vaapi", "hevc_vaapi", "av1_vaapi", "vp9_vaapi"} {
		t.Run(name, func(t *testing.T) {
			ve := findVideo(t, name)
			got, err := ve.VideoFilter(model.VideoSettings{Encoder: name}, media)
			if err != nil {
				t.Fatal(err)
			}
			want := "format=nv12,hwupload"
			if got != want {
				t.Errorf("VideoFilter = %q, want %q", got, want)
			}
		})
	}
}

func TestVAAPIVideoFilterFrameRateConversion(t *testing.T) {
	media := model.MediaInfo{
		VideoStreams: []model.VideoStream{{Width: 1920, Height: 1080}},
	}
	ve := findVideo(t, "hevc_vaapi")
	got, err := ve.VideoFilter(model.VideoSettings{
		Encoder:       "hevc_vaapi",
		FrameRateMode: "convert",
		FrameRate:     "60",
		MaxWidth:      1280,
		MaxHeight:     720,
	}, media)
	if err != nil {
		t.Fatal(err)
	}
	want := "fps=60,format=nv12,hwupload,scale_vaapi=w=1280:h=720"
	if got != want {
		t.Errorf("VideoFilter = %q, want %q", got, want)
	}
}

func TestVAAPIBuildArgsParityWithCommandTests(t *testing.T) {
	t.Run("hevc_vaapi CQP 30 produces identical flags to ffmpeg test", func(t *testing.T) {
		ve := findVideo(t, "hevc_vaapi")
		res, err := ve.BuildVideoArgs(model.VideoSettings{
			Encoder:     "hevc_vaapi",
			VAAPIDevice: "/dev/dri/renderD128",
			RateControl: "CQP",
			QP:          30,
		})
		if err != nil {
			t.Fatal(err)
		}
		if !containsPair(res.Args, "-rc_mode", "CQP") || !containsPair(res.Args, "-qp", strconv.Itoa(30)) {
			t.Errorf("missing CQP/QP pair in %#v", res.Args)
		}
		if !containsPair(res.PreInputArgs, "-vaapi_device", "/dev/dri/renderD128") {
			t.Errorf("missing -vaapi_device in %#v", res.PreInputArgs)
		}
	})

	t.Run("av1_vaapi QP 255 produces identical flags to ffmpeg test", func(t *testing.T) {
		ve := findVideo(t, "av1_vaapi")
		res, err := ve.BuildVideoArgs(model.VideoSettings{
			Encoder:     "av1_vaapi",
			VAAPIDevice: "/dev/dri/renderD128",
			RateControl: "CQP",
			QP:          255,
		})
		if err != nil {
			t.Fatal(err)
		}
		if !containsPair(res.Args, "-qp", "255") {
			t.Errorf("missing -qp 255 in %#v", res.Args)
		}
	})

	t.Run("hevc_vaapi VAAPI device is in pre-input args, not args", func(t *testing.T) {
		ve := findVideo(t, "hevc_vaapi")
		res, err := ve.BuildVideoArgs(model.VideoSettings{
			Encoder:     "hevc_vaapi",
			VAAPIDevice: "/dev/dri/renderD128",
			RateControl: "CQP",
			QP:          25,
		})
		if err != nil {
			t.Fatal(err)
		}
		if slices.Contains(res.Args, "-vaapi_device") {
			t.Errorf("-vaapi_device must not appear in Args: %#v", res.Args)
		}
		if strings.Contains(strings.Join(res.PreInputArgs, " "), "-c:v") {
			t.Errorf("PreInputArgs must not contain -c:v: %#v", res.PreInputArgs)
		}
	})
}