package encoder

import (
	"strings"
	"testing"

	"git.skobk.in/skobkin/yave/internal/model"
)

func TestTargetDimensionsUsesDisplayedOrientation(t *testing.T) {
	settings := model.VideoSettings{MaxWidth: 1280, MaxHeight: 720, KeepAspectRatio: true}
	tests := []struct {
		name         string
		stream       model.VideoStream
		wantW, wantH int
	}{
		{"landscape", model.VideoStream{Width: 1920, Height: 1080}, 1280, 720},
		{"portrait coded as landscape, rotated 90", model.VideoStream{Width: 1920, Height: 1080, Rotation: 90}, 404, 720},
		{"rotated 270", model.VideoStream{Width: 1920, Height: 1080, Rotation: 270}, 404, 720},
		{"rotated 180 keeps orientation", model.VideoStream{Width: 1920, Height: 1080, Rotation: 180}, 1280, 720},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w, h, scale := targetDimensions(settings, model.MediaInfo{VideoStreams: []model.VideoStream{tc.stream}})
			if !scale || w != tc.wantW || h != tc.wantH {
				t.Fatalf("got %dx%d scale=%v, want %dx%d", w, h, scale, tc.wantW, tc.wantH)
			}
		})
	}
}

func TestHardwareScalersDoNotStretchUnknownSource(t *testing.T) {
	for _, name := range []string{"hevc_nvenc", "hevc_qsv"} {
		t.Run(name, func(t *testing.T) {
			got, err := findVideo(t, name).VideoFilter(model.VideoSettings{
				Encoder: name, MaxWidth: 1280, MaxHeight: 720, KeepAspectRatio: true,
			}, model.MediaInfo{})
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(got, "scale_cuda") || strings.Contains(got, "scale_qsv") {
				t.Fatalf("filter %q forces an exact box on a source of unknown size", got)
			}
		})
	}
}
