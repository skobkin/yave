package encoder

import (
	"slices"
	"strings"
	"testing"

	"git.skobk.in/skobkin/yave/internal/model"
)

func TestNVENCVideoEncodersAreRegistered(t *testing.T) {
	all := DefaultRegistry().VideoNames()
	for _, name := range []string{"h264_nvenc", "hevc_nvenc", "av1_nvenc"} {
		if !slices.Contains(all, name) {
			t.Errorf("missing NVENC encoder %q in VideoNames() = %v", name, all)
		}
	}
}

func TestNVENCVideoEncoderMetadata(t *testing.T) {
	tests := []struct {
		name         string
		family       string
		hw           HWBackend
		media        string
		kind         string
		qpMin        int
		qpMax        int
		supportsProf bool
		modes        []RateControl
		defaultMode  RateControl
	}{
		{name: "h264_nvenc", family: "h264", hw: HWNVENC, media: MediaVideo, kind: KindHardware, qpMin: 0, qpMax: 51, supportsProf: true, modes: []RateControl{RCCQP, RCVBR, RCCBR}, defaultMode: RCCQP},
		{name: "hevc_nvenc", family: "hevc", hw: HWNVENC, media: MediaVideo, kind: KindHardware, qpMin: 0, qpMax: 51, supportsProf: true, modes: []RateControl{RCCQP, RCVBR, RCCBR}, defaultMode: RCCQP},
		{name: "av1_nvenc", family: "av1", hw: HWNVENC, media: MediaVideo, kind: KindHardware, qpMin: 0, qpMax: 255, supportsProf: false, modes: []RateControl{RCCQP, RCVBR, RCCBR}, defaultMode: RCCQP},
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
			if !ve.SupportsPreset() {
				t.Errorf("SupportsPreset() = false, want true (NVENC always reads -preset)")
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
			if got := ve.RateControlModes(); !slices.Equal(got, tt.modes) {
				t.Errorf("RateControlModes() = %v, want %v", got, tt.modes)
			}
			if ve.DefaultRateControl() != tt.defaultMode {
				t.Errorf("DefaultRateControl() = %v, want %v", ve.DefaultRateControl(), tt.defaultMode)
			}
		})
	}
}

func TestNVENCBuildArgsCQP(t *testing.T) {
	ve := findVideo(t, "hevc_nvenc")
	res, err := ve.BuildVideoArgs(model.VideoSettings{
		Encoder:     "hevc_nvenc",
		RateControl: "CQP",
		QP:          28,
		Preset:      "p4",
		Profile:     "main",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.PreInputArgs) != 0 {
		t.Errorf("PreInputArgs = %v, want empty (no GPUIndex)", res.PreInputArgs)
	}
	want := []string{
		"-preset", "p4",
		"-rc", "constqp", "-qp", "28",
		"-profile:v", "main",
	}
	if got := argsOf(t, res, "hevc_nvenc"); !slices.Equal(got, want) {
		t.Errorf("BuildVideoArgs = %v, want %v", got, want)
	}
}

func TestNVENCBuildArgsCQPDefaultPreset(t *testing.T) {
	ve := findVideo(t, "hevc_nvenc")
	res, err := ve.BuildVideoArgs(model.VideoSettings{
		Encoder:     "hevc_nvenc",
		RateControl: "CQP",
		QP:          28,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"-preset", "p4", "-rc", "constqp", "-qp", "28"}
	if got := argsOf(t, res, "hevc_nvenc"); !slices.Equal(got, want) {
		t.Errorf("BuildVideoArgs = %v, want %v", got, want)
	}
}

func TestNVENCBuildArgsVBR(t *testing.T) {
	ve := findVideo(t, "av1_nvenc")
	res, err := ve.BuildVideoArgs(model.VideoSettings{
		Encoder:     "av1_nvenc",
		RateControl: "VBR",
		Bitrate:     "8M",
		Maxrate:     "12M",
		Bufsize:     "16M",
		Preset:      "p4",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"-preset", "p4",
		"-rc", "vbr", "-b:v", "8M",
		"-maxrate", "12M", "-bufsize", "16M",
	}
	if got := argsOf(t, res, "av1_nvenc"); !slices.Equal(got, want) {
		t.Errorf("BuildVideoArgs = %v, want %v", got, want)
	}
}

func TestNVENCBuildArgsCBR(t *testing.T) {
	ve := findVideo(t, "h264_nvenc")
	res, err := ve.BuildVideoArgs(model.VideoSettings{
		Encoder:     "h264_nvenc",
		RateControl: "CBR",
		Bitrate:     "6M",
		Preset:      "p4",
	})
	if err != nil {
		t.Fatal(err)
	}
	// CBR must set maxrate == b:v (or the user must have provided
	// the same). User did not set Maxrate, so we auto-promote.
	want := []string{
		"-preset", "p4",
		"-rc", "cbr", "-b:v", "6M",
		"-maxrate", "6M",
	}
	if got := argsOf(t, res, "h264_nvenc"); !slices.Equal(got, want) {
		t.Errorf("BuildVideoArgs = %v, want %v", got, want)
	}
}

func TestNVENCBuildArgsCBRRespectsUserMaxrate(t *testing.T) {
	ve := findVideo(t, "h264_nvenc")
	res, err := ve.BuildVideoArgs(model.VideoSettings{
		Encoder:     "h264_nvenc",
		RateControl: "CBR",
		Bitrate:     "6M",
		Maxrate:     "6M",
		Preset:      "p4",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"-preset", "p4", "-rc", "cbr", "-b:v", "6M", "-maxrate", "6M"}
	if got := argsOf(t, res, "h264_nvenc"); !slices.Equal(got, want) {
		t.Errorf("BuildVideoArgs = %v, want %v", got, want)
	}
}

func TestNVENCBuildArgsVBRMissingBitrate(t *testing.T) {
	ve := findVideo(t, "hevc_nvenc")
	_, err := ve.BuildVideoArgs(model.VideoSettings{
		Encoder:     "hevc_nvenc",
		RateControl: "VBR",
	})
	if err == nil {
		t.Errorf("expected error when VBR has no bitrate")
	}
}

func TestNVENCBuildArgsUnsupportedRateControl(t *testing.T) {
	ve := findVideo(t, "hevc_nvenc")
	_, err := ve.BuildVideoArgs(model.VideoSettings{
		Encoder:     "hevc_nvenc",
		RateControl: "CRF",
		CRF:         28,
	})
	if err == nil {
		t.Errorf("expected error for unsupported CRF mode on NVENC")
	}
}

func TestNVENCFilterChainWithScale(t *testing.T) {
	media := model.MediaInfo{
		VideoStreams: []model.VideoStream{{Width: 1920, Height: 1080}},
	}
	ve := findVideo(t, "hevc_nvenc")
	got, err := ve.VideoFilter(model.VideoSettings{
		Encoder:         "hevc_nvenc",
		MaxWidth:        1280,
		MaxHeight:       720,
		KeepAspectRatio: true,
	}, media)
	if err != nil {
		t.Fatal(err)
	}
	want := "format=yuv420p,format=nv12,hwupload_cuda,scale_cuda=w=1280:h=720"
	if got != want {
		t.Errorf("VideoFilter = %q, want %q", got, want)
	}
	if strings.Contains(got, "force_original_aspect_ratio") {
		t.Errorf("scale_cuda must not receive software-scale aspect options: %q", got)
	}
	if strings.Contains(got, "force_divisible_by") {
		t.Errorf("scale_cuda must not receive software-scale divisible options: %q", got)
	}
}

func TestNVENCFilterChainNoScale(t *testing.T) {
	media := model.MediaInfo{
		VideoStreams: []model.VideoStream{{Width: 1920, Height: 1080}},
	}
	for _, name := range []string{"h264_nvenc", "hevc_nvenc", "av1_nvenc"} {
		t.Run(name, func(t *testing.T) {
			ve := findVideo(t, name)
			got, err := ve.VideoFilter(model.VideoSettings{Encoder: name}, media)
			if err != nil {
				t.Fatal(err)
			}
			want := "format=yuv420p,format=nv12,hwupload_cuda"
			if got != want {
				t.Errorf("VideoFilter = %q, want %q", got, want)
			}
		})
	}
}

func TestNVENCFilterChainFrameRateConversion(t *testing.T) {
	ve := findVideo(t, "hevc_nvenc")
	got, err := ve.VideoFilter(model.VideoSettings{
		Encoder:       "hevc_nvenc",
		FrameRateMode: "convert",
		FrameRate:     "30",
		MaxWidth:      1280,
		MaxHeight:     720,
	}, model.MediaInfo{VideoStreams: []model.VideoStream{{Width: 1920, Height: 1080}}})
	if err != nil {
		t.Fatal(err)
	}
	want := "fps=30,format=yuv420p,format=nv12,hwupload_cuda,scale_cuda=w=1280:h=720"
	if got != want {
		t.Errorf("VideoFilter = %q, want %q", got, want)
	}
}

func TestNVENCOptionalGPUIndex(t *testing.T) {
	ve := findVideo(t, "hevc_nvenc")
	res, err := ve.BuildVideoArgs(model.VideoSettings{
		Encoder:     "hevc_nvenc",
		RateControl: "CQP",
		QP:          28,
		GPUIndex:    1,
		Preset:      "p4",
	})
	if err != nil {
		t.Fatal(err)
	}
	// -gpu is an nvenc encoder option, so it lives with the rest of
	// the per-stream args (after -c:v) rather than in PreInputArgs.
	// The matching CUDA device is still brought up before -i so the
	// hwupload_cuda / scale_cuda steps upload onto the same adapter
	// the encoder targets on multi-GPU hosts.
	wantPre := []string{
		"-init_hw_device", "cuda=cuda:1",
		"-filter_hw_device", "cuda",
	}
	if !slices.Equal(res.PreInputArgs, wantPre) {
		t.Errorf("PreInputArgs = %v, want %v", res.PreInputArgs, wantPre)
	}
	if !slices.Equal(res.Args, []string{"-c:v", "hevc_nvenc", "-gpu", "1", "-preset", "p4", "-rc", "constqp", "-qp", "28"}) {
		t.Errorf("Args = %v", res.Args)
	}
}

func TestNVENCNoGPUIndexByDefault(t *testing.T) {
	ve := findVideo(t, "av1_nvenc")
	res, err := ve.BuildVideoArgs(model.VideoSettings{
		Encoder:     "av1_nvenc",
		RateControl: "CQP",
		QP:          28,
		Preset:      "p4",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.PreInputArgs) != 0 {
		t.Errorf("PreInputArgs = %v, want empty when GPUIndex=0", res.PreInputArgs)
	}
}

func TestNVENCValidateAcceptsValidPreset(t *testing.T) {
	ve := findVideo(t, "hevc_nvenc")
	issues, err := ve.Validate(model.VideoSettings{Encoder: "hevc_nvenc", Preset: "p4"})
	if err != nil {
		t.Fatal(err)
	}
	if len(issues) != 0 {
		t.Errorf("issues = %v, want none", issues)
	}
}

func TestNVENCValidateRejectsInvalidPreset(t *testing.T) {
	ve := findVideo(t, "hevc_nvenc")
	issues, err := ve.Validate(model.VideoSettings{Encoder: "hevc_nvenc", Preset: "p9"})
	if err != nil {
		t.Fatal(err)
	}
	if len(issues) != 1 || issues[0].Field != "profile.video.preset" {
		t.Errorf("issues = %v, want one preset error", issues)
	}
}

func TestNVENCValidateAV1NoProfile(t *testing.T) {
	ve := findVideo(t, "av1_nvenc")
	issues, err := ve.Validate(model.VideoSettings{Encoder: "av1_nvenc", Profile: "main"})
	if err != nil {
		t.Fatal(err)
	}
	if len(issues) != 1 || issues[0].Field != "profile.video.profile" {
		t.Errorf("issues = %v, want one profile error", issues)
	}
	if !strings.Contains(issues[0].Message, "does not support") {
		t.Errorf("issue message = %q, want it to explain the rejection", issues[0].Message)
	}
}

func TestNVENCValidateH264AcceptsProfile(t *testing.T) {
	ve := findVideo(t, "h264_nvenc")
	issues, err := ve.Validate(model.VideoSettings{Encoder: "h264_nvenc", Profile: "main"})
	if err != nil {
		t.Fatal(err)
	}
	if len(issues) != 0 {
		t.Errorf("issues = %v, want none for h264_nvenc + profile", issues)
	}
}

func TestNVENCValidateCBRMatchingMaxrate(t *testing.T) {
	ve := findVideo(t, "hevc_nvenc")
	issues, err := ve.Validate(model.VideoSettings{
		Encoder:     "hevc_nvenc",
		RateControl: "CBR",
		Bitrate:     "6M",
		Maxrate:     "6M",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, issue := range issues {
		if issue.Field == "profile.video.maxrate" {
			t.Errorf("unexpected maxrate issue for matching values: %v", issue)
		}
	}
}

func TestNVENCValidateCBRBlankMaxrateOK(t *testing.T) {
	ve := findVideo(t, "hevc_nvenc")
	issues, err := ve.Validate(model.VideoSettings{
		Encoder:     "hevc_nvenc",
		RateControl: "CBR",
		Bitrate:     "6M",
		Maxrate:     "", // builder auto-promotes
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, issue := range issues {
		if issue.Field == "profile.video.maxrate" {
			t.Errorf("unexpected maxrate issue when Maxrate is blank: %v", issue)
		}
	}
}

func TestNVENCValidateCBRMismatchedMaxrate(t *testing.T) {
	ve := findVideo(t, "hevc_nvenc")
	issues, err := ve.Validate(model.VideoSettings{
		Encoder:     "hevc_nvenc",
		RateControl: "CBR",
		Bitrate:     "6M",
		Maxrate:     "9M",
	})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, issue := range issues {
		if issue.Field == "profile.video.maxrate" {
			found = true
			if !strings.Contains(issue.Message, "CBR requires maxrate to equal bitrate") {
				t.Errorf("issue message = %q, want it to explain the requirement", issue.Message)
			}
		}
	}
	if !found {
		t.Errorf("expected a maxrate issue, got %#v", issues)
	}
}

func TestNVENCValidateCBRMismatchedMaxrateIsCaseInsensitive(t *testing.T) {
	ve := findVideo(t, "h264_nvenc")
	issues, err := ve.Validate(model.VideoSettings{
		Encoder:     "h264_nvenc",
		RateControl: "cbr",
		Bitrate:     "6M",
		Maxrate:     "9M",
	})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, issue := range issues {
		if issue.Field == "profile.video.maxrate" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a maxrate issue for lowercase cbr, got %#v", issues)
	}
}
