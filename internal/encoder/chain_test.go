package encoder

import (
	"testing"

	"git.skobk.in/skobkin/yave/internal/model"
)

// tenBitMedia is the fixture for chain decisions: a 10-bit 4:2:0
// primary video stream, the shape ffprobe reports for main10 content.
var tenBitMedia = model.MediaInfo{
	VideoStreams: []model.VideoStream{{Index: 0, Codec: "hevc", Width: 1920, Height: 1080, PixelFormat: "yuv420p10le"}},
}

// eightBitMedia pins the 8-bit control rows.
var eightBitMedia = model.MediaInfo{
	VideoStreams: []model.VideoStream{{Index: 0, Codec: "hevc", Width: 1920, Height: 1080, PixelFormat: "yuv420p"}},
}

// TestTenBitChain pins the ONE shared chain decision. Every consumer
// (filters, device requirements, validation warnings, UI hints)
// resolves through it, so its rows are the contract: explicit 10-bit
// values per family, the single empirically-gated source-derived rule
// (hevc_vaapi auto), explicit-only everywhere else, and case/
// whitespace tolerance matching inCuratedList.
func TestTenBitChain(t *testing.T) {
	tests := []struct {
		name    string
		encoder string
		profile string
		media   model.MediaInfo
		want    bool
	}{
		{"hevc_vaapi main10", "hevc_vaapi", "main10", model.MediaInfo{}, true},
		{"hevc_qsv main10", "hevc_qsv", "main10", model.MediaInfo{}, true},
		{"hevc_nvenc main10", "hevc_nvenc", "main10", model.MediaInfo{}, true},
		{"whitespace and case variants", "hevc_vaapi", " MAIN10 ", model.MediaInfo{}, true},
		{"trailing space", "hevc_nvenc", "main10 ", model.MediaInfo{}, true},
		{"hevc main stays 8-bit", "hevc_vaapi", "main", tenBitMedia, false},
		{"hevc rext stays 8-bit", "hevc_vaapi", "rext", model.MediaInfo{}, false},
		{"hevc_vaapi auto follows a 10-bit source", "hevc_vaapi", "", tenBitMedia, true},
		{"hevc_vaapi auto stays 8-bit on 8-bit source", "hevc_vaapi", "", eightBitMedia, false},
		{"hevc_vaapi auto stays 8-bit without media", "hevc_vaapi", "", model.MediaInfo{}, false},
		{"hevc_qsv auto is explicit-only", "hevc_qsv", "", tenBitMedia, false},
		{"hevc_nvenc auto is explicit-only", "hevc_nvenc", "", tenBitMedia, false},
		{"vp9_vaapi profile 2", "vp9_vaapi", "2", model.MediaInfo{}, true},
		{"vp9_vaapi profile 2 variant", "vp9_vaapi", " 2 ", model.MediaInfo{}, true},
		{"vp9_vaapi profile 0", "vp9_vaapi", "0", tenBitMedia, false},
		{"vp9_vaapi auto is explicit-only", "vp9_vaapi", "", tenBitMedia, false},
		{"vp9_qsv profile2", "vp9_qsv", "profile2", model.MediaInfo{}, true},
		{"vp9_qsv profile2 variant", "vp9_qsv", " PROFILE2 ", model.MediaInfo{}, true},
		{"vp9_qsv profile0", "vp9_qsv", "profile0", tenBitMedia, false},
		{"vp9_vaapi does not take the qsv spelling", "vp9_vaapi", "profile2", model.MediaInfo{}, false},
		{"vp9_qsv does not take the vaapi spelling", "vp9_qsv", "2", model.MediaInfo{}, false},
		{"h264 has no 10-bit value", "h264_vaapi", "high10", tenBitMedia, false},
		{"h264_nvenc auto", "h264_nvenc", "", tenBitMedia, false},
		{"av1 main has no separate 10-bit value", "av1_vaapi", "main", tenBitMedia, false},
		{"software encoders never chain", "libx265", "main10", tenBitMedia, false},
		{"copy never chains", "copy", "main10", tenBitMedia, false},
		{"unknown encoder", "mpeg2_vaapi", "main10", model.MediaInfo{}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := TenBitChain(tt.encoder, tt.profile, tt.media); got != tt.want {
				t.Errorf("TenBitChain(%q, %q, media) = %v, want %v", tt.encoder, tt.profile, got, tt.want)
			}
		})
	}
}

// TestSourceTenBit pins the depth heuristic boundaries: 10- and deeper
// formats count, 8/9-bit do not, and a source without a probe record
// fails open to 8-bit.
func TestSourceTenBit(t *testing.T) {
	for _, format := range []string{
		"yuv420p10le", "p010le", "yuv420p12le", "yuv444p16le",
	} {
		media := model.MediaInfo{VideoStreams: []model.VideoStream{{PixelFormat: format}}}
		if !sourceTenBit(media) {
			t.Errorf("sourceTenBit(%q) = false, want true", format)
		}
	}
	for _, format := range []string{"yuv420p", "yuv420p9le", "nv12", ""} {
		media := model.MediaInfo{VideoStreams: []model.VideoStream{{PixelFormat: format}}}
		if sourceTenBit(media) {
			t.Errorf("sourceTenBit(%q) = true, want false", format)
		}
	}
	if sourceTenBit(model.MediaInfo{}) {
		t.Errorf("sourceTenBit(no streams) = true, want false")
	}
	if got := PixelFormatBitDepth("yuv420p10le"); got != 10 {
		t.Errorf("PixelFormatBitDepth = %d, want 10", got)
	}
	if got := PixelFormatBitDepth("nonsense"); got != 8 {
		t.Errorf("PixelFormatBitDepth(unrecognized) = %d, want 8", got)
	}
}

// TestVAAPIVideoFilterTenBit pins the p010 upload chain shapes: the
// 8-bit strings stay byte-identical to the historical chain, the
// 10-bit chain swaps only the upload format, and the scaler rides the
// same chain unchanged.
func TestVAAPIVideoFilterTenBit(t *testing.T) {
	ve := findVideo(t, "hevc_vaapi")

	// Explicit main10 → p010, no scaler.
	got, err := ve.VideoFilter(model.VideoSettings{Encoder: "hevc_vaapi", Profile: "main10"}, model.MediaInfo{})
	if err != nil {
		t.Fatal(err)
	}
	if want := "format=p010le,hwupload"; got != want {
		t.Errorf("VideoFilter(main10) = %q, want %q", got, want)
	}

	// Auto profile + 10-bit source → the same p010 chain (E3).
	got, err = ve.VideoFilter(model.VideoSettings{Encoder: "hevc_vaapi"}, tenBitMedia)
	if err != nil {
		t.Fatal(err)
	}
	if want := "format=p010le,hwupload"; got != want {
		t.Errorf("VideoFilter(auto, 10-bit source) = %q, want %q", got, want)
	}

	// 10-bit chain through a real resize (E2): scaler preserved.
	got, err = ve.VideoFilter(model.VideoSettings{
		Encoder: "hevc_vaapi", Profile: "main10",
		MaxWidth: 256, MaxHeight: 144, KeepAspectRatio: true,
	}, model.MediaInfo{VideoStreams: []model.VideoStream{{Width: 640, Height: 480}}})
	if err != nil {
		t.Fatal(err)
	}
	if want := "format=p010le,hwupload,scale_vaapi=w=192:h=144:force_original_aspect_ratio=decrease:force_divisible_by=2"; got != want {
		t.Errorf("VideoFilter(main10, scale) = %q, want %q", got, want)
	}

	// VP9 profile 2 → p010.
	vp9 := findVideo(t, "vp9_vaapi")
	got, err = vp9.VideoFilter(model.VideoSettings{Encoder: "vp9_vaapi", Profile: "2"}, model.MediaInfo{})
	if err != nil {
		t.Fatal(err)
	}
	if want := "format=p010le,hwupload"; got != want {
		t.Errorf("VideoFilter(vp9 \"2\") = %q, want %q", got, want)
	}

	// The 8-bit chain is untouched by a 10-bit SOURCE alone when the
	// profile is explicit 8-bit or the encoder is not depth-aware.
	h264 := findVideo(t, "h264_vaapi")
	got, err = h264.VideoFilter(model.VideoSettings{Encoder: "h264_vaapi"}, tenBitMedia)
	if err != nil {
		t.Fatal(err)
	}
	if want := "format=nv12,hwupload"; got != want {
		t.Errorf("VideoFilter(h264, 10-bit source) = %q, want the 8-bit chain %q", got, want)
	}
}

// TestQSVNVENCVideoFilterTenBit pins the p010 chains for the other two
// families: explicit values only, and an empty profile keeps the
// historical 8-bit chain even on a 10-bit source.
func TestQSVNVENCVideoFilterTenBit(t *testing.T) {
	t.Run("hevc_qsv main10", func(t *testing.T) {
		ve := findVideo(t, "hevc_qsv")
		got, err := ve.VideoFilter(model.VideoSettings{Encoder: "hevc_qsv", Profile: "main10"}, model.MediaInfo{})
		if err != nil {
			t.Fatal(err)
		}
		if want := "format=p010le,hwupload,format=qsv"; got != want {
			t.Errorf("VideoFilter = %q, want %q", got, want)
		}
	})
	t.Run("hevc_qsv auto stays 8-bit on a 10-bit source", func(t *testing.T) {
		ve := findVideo(t, "hevc_qsv")
		got, err := ve.VideoFilter(model.VideoSettings{Encoder: "hevc_qsv"}, tenBitMedia)
		if err != nil {
			t.Fatal(err)
		}
		if want := "format=nv12,hwupload,format=qsv"; got != want {
			t.Errorf("VideoFilter = %q, want the 8-bit chain %q", got, want)
		}
	})
	t.Run("hevc_nvenc main10 replaces both 8-bit steps", func(t *testing.T) {
		ve := findVideo(t, "hevc_nvenc")
		got, err := ve.VideoFilter(model.VideoSettings{Encoder: "hevc_nvenc", Profile: "main10"}, model.MediaInfo{})
		if err != nil {
			t.Fatal(err)
		}
		if want := "format=p010le,hwupload_cuda"; got != want {
			t.Errorf("VideoFilter = %q, want %q", got, want)
		}
	})
	t.Run("hevc_nvenc main10 with scaler (E4 shape)", func(t *testing.T) {
		ve := findVideo(t, "hevc_nvenc")
		got, err := ve.VideoFilter(model.VideoSettings{
			Encoder: "hevc_nvenc", Profile: "main10",
			MaxWidth: 256, MaxHeight: 144,
		}, model.MediaInfo{VideoStreams: []model.VideoStream{{Width: 640, Height: 480}}})
		if err != nil {
			t.Fatal(err)
		}
		if want := "format=p010le,hwupload_cuda,scale_cuda=w=256:h=144"; got != want {
			t.Errorf("VideoFilter = %q, want %q", got, want)
		}
	})
	t.Run("hevc_nvenc auto stays 8-bit on a 10-bit source", func(t *testing.T) {
		ve := findVideo(t, "hevc_nvenc")
		got, err := ve.VideoFilter(model.VideoSettings{Encoder: "hevc_nvenc"}, tenBitMedia)
		if err != nil {
			t.Fatal(err)
		}
		if want := "format=yuv420p,format=nv12,hwupload_cuda"; got != want {
			t.Errorf("VideoFilter = %q, want the 8-bit chain %q", got, want)
		}
	})
}
