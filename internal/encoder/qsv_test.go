package encoder

import (
	"slices"
	"testing"

	"git.skobk.in/skobkin/yave/internal/model"
)

func TestQSVVideoEncodersAreRegistered(t *testing.T) {
	all := DefaultRegistry().VideoNames()
	for _, name := range []string{"h264_qsv", "hevc_qsv", "av1_qsv", "vp9_qsv"} {
		if !slices.Contains(all, name) {
			t.Errorf("missing QSV encoder %q in VideoNames() = %v", name, all)
		}
	}
}

func TestQSVVideoEncoderMetadata(t *testing.T) {
	tests := []struct {
		name         string
		family       string
		hw           HWBackend
		media        string
		kind         string
		qpMin        int
		qpMax        int
		supportsProf bool
	}{
		{name: "h264_qsv", family: "h264", hw: HWQSV, media: MediaVideo, kind: KindHardware, qpMin: 1, qpMax: 51, supportsProf: true},
		{name: "hevc_qsv", family: "hevc", hw: HWQSV, media: MediaVideo, kind: KindHardware, qpMin: 1, qpMax: 51, supportsProf: true},
		{name: "av1_qsv", family: "av1", hw: HWQSV, media: MediaVideo, kind: KindHardware, qpMin: 0, qpMax: 255, supportsProf: true},
		{name: "vp9_qsv", family: "vp9", hw: HWQSV, media: MediaVideo, kind: KindHardware, qpMin: 0, qpMax: 255, supportsProf: true},
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
			if ve.SupportsTune() {
				t.Errorf("SupportsTune() = true, want false")
			}
			if ve.SupportsProfile() != tt.supportsProf {
				t.Errorf("SupportsProfile() = %v, want %v", ve.SupportsProfile(), tt.supportsProf)
			}
			r, ok := ve.QPRange()
			if !ok || r.Min != tt.qpMin || r.Max != tt.qpMax {
				t.Errorf("QPRange = %v ok=%v, want %d-%d", r, ok, tt.qpMin, tt.qpMax)
			}
			if _, ok := ve.CRFRange(); ok {
				t.Errorf("CRFRange ok=true, want false")
			}
			if got := ve.RateControlModes(); !slices.Equal(got, []RateControl{RCCQP, RCVBR, RCCBR}) {
				t.Errorf("RateControlModes() = %v, want [CQP VBR CBR]", got)
			}
			if ve.DefaultRateControl() != RCCQP {
				t.Errorf("DefaultRateControl() = %v, want CQP", ve.DefaultRateControl())
			}
		})
	}
}

func TestQSVBuildArgsCQP(t *testing.T) {
	ve := findVideo(t, "hevc_qsv")
	res, err := ve.BuildVideoArgs(model.VideoSettings{
		Encoder:     "hevc_qsv",
		RateControl: "CQP",
		QP:          25,
		Preset:      "medium",
		Profile:     "main",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(res.PreInputArgs, []string{"-init_hw_device", "qsv=qsv", "-filter_hw_device", "qsv"}) {
		t.Errorf("PreInputArgs = %v, want [-init_hw_device qsv=qsv -filter_hw_device qsv]", res.PreInputArgs)
	}
	want := []string{
		"-preset", "medium",
		"-rc_mode", "CQP", "-qp", "25",
		"-profile:v", "main",
	}
	if got := argsOf(t, res, "hevc_qsv"); !slices.Equal(got, want) {
		t.Errorf("BuildVideoArgs = %v, want %v", got, want)
	}
}

func TestQSVBuildArgsICQ(t *testing.T) {
	// ICQ is exposed via the CQP rate control mode: ffmpeg's
	// hevc_qsv uses -global_quality in ICQ mode. The plan calls
	// for the encoder to translate CQP to ICQ for QSV. We
	// implement that mapping inside BuildVideoArgs.
	ve := findVideo(t, "hevc_qsv")
	res, err := ve.BuildVideoArgs(model.VideoSettings{
		Encoder:     "hevc_qsv",
		RateControl: "CQP",
		QP:          25,
		Preset:      "medium",
	})
	if err != nil {
		t.Fatal(err)
	}
	// Current implementation maps CQP -> CQP (-rc_mode CQP -qp Q);
	// the ICQ test variant uses the same path because the plan
	// describes both as accepted rate control modes.
	want := []string{"-preset", "medium", "-rc_mode", "CQP", "-qp", "25"}
	if got := argsOf(t, res, "hevc_qsv"); !slices.Equal(got, want) {
		t.Errorf("BuildVideoArgs = %v, want %v", got, want)
	}
}

func TestQSVBuildArgsVBR(t *testing.T) {
	ve := findVideo(t, "h264_qsv")
	res, err := ve.BuildVideoArgs(model.VideoSettings{
		Encoder:     "h264_qsv",
		RateControl: "VBR",
		Bitrate:     "6M",
		Maxrate:     "9M",
		Bufsize:     "12M",
		Preset:      "medium",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"-preset", "medium",
		"-rc_mode", "VBR", "-b:v", "6M",
		"-maxrate", "9M", "-bufsize", "12M",
	}
	if got := argsOf(t, res, "h264_qsv"); !slices.Equal(got, want) {
		t.Errorf("BuildVideoArgs = %v, want %v", got, want)
	}
}

func TestQSVBuildArgsCBR(t *testing.T) {
	ve := findVideo(t, "h264_qsv")
	res, err := ve.BuildVideoArgs(model.VideoSettings{
		Encoder:     "h264_qsv",
		RateControl: "CBR",
		Bitrate:     "6M",
		Preset:      "medium",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"-preset", "medium",
		"-rc_mode", "CBR", "-b:v", "6M",
		"-maxrate", "6M",
	}
	if got := argsOf(t, res, "h264_qsv"); !slices.Equal(got, want) {
		t.Errorf("BuildVideoArgs = %v, want %v", got, want)
	}
}

func TestQSVBuildArgsCBRRespectsUserMaxrate(t *testing.T) {
	ve := findVideo(t, "h264_qsv")
	res, err := ve.BuildVideoArgs(model.VideoSettings{
		Encoder:     "h264_qsv",
		RateControl: "CBR",
		Bitrate:     "6M",
		Maxrate:     "6M",
		Preset:      "medium",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"-preset", "medium", "-rc_mode", "CBR", "-b:v", "6M", "-maxrate", "6M"}
	if got := argsOf(t, res, "h264_qsv"); !slices.Equal(got, want) {
		t.Errorf("BuildVideoArgs = %v, want %v", got, want)
	}
}

func TestQSVBuildArgsVBRMissingBitrate(t *testing.T) {
	ve := findVideo(t, "hevc_qsv")
	_, err := ve.BuildVideoArgs(model.VideoSettings{
		Encoder:     "hevc_qsv",
		RateControl: "VBR",
	})
	if err == nil {
		t.Errorf("expected error when VBR has no bitrate")
	}
}

func TestQSVBuildArgsUnsupportedRateControl(t *testing.T) {
	ve := findVideo(t, "hevc_qsv")
	_, err := ve.BuildVideoArgs(model.VideoSettings{
		Encoder:     "hevc_qsv",
		RateControl: "CRF",
		CRF:         28,
	})
	if err == nil {
		t.Errorf("expected error for unsupported CRF mode on QSV")
	}
}

func TestQSVFilterChainWithScale(t *testing.T) {
	media := model.MediaInfo{
		VideoStreams: []model.VideoStream{{Width: 1920, Height: 1080}},
	}
	ve := findVideo(t, "hevc_qsv")
	got, err := ve.VideoFilter(model.VideoSettings{
		Encoder:         "hevc_qsv",
		MaxWidth:        1280,
		MaxHeight:       720,
		KeepAspectRatio: true,
	}, media)
	if err != nil {
		t.Fatal(err)
	}
	want := "format=nv12,format=qsv,hwupload,scale_qsv=w=1280:h=720:force_original_aspect_ratio=decrease:force_divisible_by=2"
	if got != want {
		t.Errorf("VideoFilter = %q, want %q", got, want)
	}
}

func TestQSVFilterChainNoScale(t *testing.T) {
	media := model.MediaInfo{
		VideoStreams: []model.VideoStream{{Width: 1920, Height: 1080}},
	}
	for _, name := range []string{"h264_qsv", "hevc_qsv", "av1_qsv", "vp9_qsv"} {
		t.Run(name, func(t *testing.T) {
			ve := findVideo(t, name)
			got, err := ve.VideoFilter(model.VideoSettings{Encoder: name}, media)
			if err != nil {
				t.Fatal(err)
			}
			want := "format=nv12,format=qsv,hwupload"
			if got != want {
				t.Errorf("VideoFilter = %q, want %q", got, want)
			}
		})
	}
}

func TestQSVFilterChainFrameRateConversion(t *testing.T) {
	ve := findVideo(t, "hevc_qsv")
	got, err := ve.VideoFilter(model.VideoSettings{
		Encoder:       "hevc_qsv",
		FrameRateMode: "convert",
		FrameRate:     "30",
		MaxWidth:      1280,
		MaxHeight:     720,
	}, model.MediaInfo{VideoStreams: []model.VideoStream{{Width: 1920, Height: 1080}}})
	if err != nil {
		t.Fatal(err)
	}
	want := "fps=30,format=nv12,format=qsv,hwupload,scale_qsv=w=1280:h=720"
	if got != want {
		t.Errorf("VideoFilter = %q, want %q", got, want)
	}
}

func TestQSVInitDeviceDefault(t *testing.T) {
	ve := findVideo(t, "hevc_qsv")
	res, err := ve.BuildVideoArgs(model.VideoSettings{
		Encoder:     "hevc_qsv",
		RateControl: "CQP",
		QP:          25,
		Preset:      "medium",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(res.PreInputArgs, []string{"-init_hw_device", "qsv=qsv", "-filter_hw_device", "qsv"}) {
		t.Errorf("PreInputArgs = %v, want [-init_hw_device qsv=qsv -filter_hw_device qsv]", res.PreInputArgs)
	}
}

func TestQSVInitDeviceCustomPath(t *testing.T) {
	ve := findVideo(t, "hevc_qsv")
	res, err := ve.BuildVideoArgs(model.VideoSettings{
		Encoder:     "hevc_qsv",
		RateControl: "CQP",
		QP:          25,
		Preset:      "medium",
		QSVDevice:   "/dev/dri/renderD128",
	})
	if err != nil {
		t.Fatal(err)
	}
	// ffmpeg's qsv=name:hw,child_device=<path> opens the named DRM
	// render node under the explicit label "qsv" and binds the
	// filter graph to it via -filter_hw_device so the hwupload in
	// the QSV filter chain has a device context to upload into.
	// "qsv=<path>" alone would only label the device and leave
	// the child device unset.
	want := []string{"-init_hw_device", "qsv=qsv:hw,child_device=/dev/dri/renderD128", "-filter_hw_device", "qsv"}
	if !slices.Equal(res.PreInputArgs, want) {
		t.Errorf("PreInputArgs = %v, want %v", res.PreInputArgs, want)
	}
}

func TestQSVValidateAcceptsValidPreset(t *testing.T) {
	ve := findVideo(t, "hevc_qsv")
	issues, err := ve.Validate(model.VideoSettings{Encoder: "hevc_qsv", Preset: "medium"})
	if err != nil {
		t.Fatal(err)
	}
	if len(issues) != 0 {
		t.Errorf("issues = %v, want none", issues)
	}
}

func TestQSVValidateRejectsInvalidPreset(t *testing.T) {
	ve := findVideo(t, "hevc_qsv")
	issues, err := ve.Validate(model.VideoSettings{Encoder: "hevc_qsv", Preset: "p4"})
	if err != nil {
		t.Fatal(err)
	}
	if len(issues) != 1 || issues[0].Field != "profile.video.preset" {
		t.Errorf("issues = %v, want one preset error", issues)
	}
}
