package validation_test

import (
	"testing"

	"git.skobk.in/skobkin/yave/internal/validation"
)

func TestIntegerRanges(t *testing.T) {
	tests := []struct {
		name   string
		lookup func(string) (validation.IntegerRange, bool)
		option string
		min    int
		max    int
	}{
		{name: "H.264 VAAPI QP", lookup: validation.QPRange, option: "h264_vaapi", min: 1, max: 51},
		{name: "HEVC VAAPI QP", lookup: validation.QPRange, option: "hevc_vaapi", min: 1, max: 51},
		{name: "AV1 VAAPI QP", lookup: validation.QPRange, option: "av1_vaapi", min: 0, max: 255},
		{name: "VP9 VAAPI QP", lookup: validation.QPRange, option: "vp9_vaapi", min: 0, max: 255},
		{name: "x264 CRF", lookup: validation.CRFRange, option: "libx264", min: 0, max: 51},
		{name: "x265 CRF", lookup: validation.CRFRange, option: "libx265", min: 0, max: 51},
		{name: "SVT-AV1 CRF", lookup: validation.CRFRange, option: "libsvtav1", min: 0, max: 63},
		{name: "VP9 CRF", lookup: validation.CRFRange, option: "libvpx-vp9", min: 0, max: 63},
		{name: "SVT-AV1 preset", lookup: validation.PresetRange, option: "libsvtav1", min: 0, max: 13},
		{name: "FLAC compression", lookup: validation.AudioCompressionRange, option: "flac", min: 0, max: 12},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, ok := tt.lookup(tt.option)
			if !ok {
				t.Fatalf("range not found for %q", tt.option)
			}
			if r.Min != tt.min || r.Max != tt.max {
				t.Fatalf("range = %d-%d, want %d-%d", r.Min, r.Max, tt.min, tt.max)
			}
			for _, value := range []struct {
				value int
				valid bool
			}{
				{value: tt.min - 1, valid: false},
				{value: tt.min, valid: true},
				{value: tt.max, valid: true},
				{value: tt.max + 1, valid: false},
			} {
				if got := r.Contains(value.value); got != value.valid {
					t.Errorf("Contains(%d) = %v, want %v", value.value, got, value.valid)
				}
			}
		})
	}
}
