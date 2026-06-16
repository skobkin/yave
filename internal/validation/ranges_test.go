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

func TestProportionalConvert(t *testing.T) {
	tests := []struct {
		name        string
		value       int
		from        validation.IntegerRange
		to          validation.IntegerRange
		want        int
		description string
	}{
		{
			name:        "identical range preserves value",
			value:       30,
			from:        validation.IntegerRange{Min: 0, Max: 51},
			to:          validation.IntegerRange{Min: 0, Max: 51},
			want:        30,
			description: "no codec change -> no remap",
		},
		{
			name:        "av1 vaapi QP 255 to h264 vaapi QP 1-51",
			value:       255,
			from:        validation.IntegerRange{Min: 0, Max: 255},
			to:          validation.IntegerRange{Min: 1, Max: 51},
			want:        51,
			description: "max-of-from should map to max-of-to",
		},
		{
			name:        "libsvtav1 CRF 63 to libx264 CRF 0-51",
			value:       63,
			from:        validation.IntegerRange{Min: 0, Max: 63},
			to:          validation.IntegerRange{Min: 0, Max: 51},
			want:        51,
			description: "max-of-from should map to max-of-to, anchor by min",
		},
		{
			name:        "h264 vaapi QP 30 to av1 vaapi QP 0-255",
			value:       30,
			from:        validation.IntegerRange{Min: 1, Max: 51},
			to:          validation.IntegerRange{Min: 0, Max: 255},
			want:        148,
			description: "min-to-min: 1->0, max-to-max: 51->255; value 30 is ~57% across from, so ~148 in to",
		},
		{
			name:        "min-of-from maps to min-of-to",
			value:       1,
			from:        validation.IntegerRange{Min: 1, Max: 51},
			to:          validation.IntegerRange{Min: 0, Max: 255},
			want:        0,
			description: "endpoint preservation",
		},
		{
			name:        "max-of-from maps to max-of-to",
			value:       51,
			from:        validation.IntegerRange{Min: 1, Max: 51},
			to:          validation.IntegerRange{Min: 0, Max: 255},
			want:        255,
			description: "endpoint preservation",
		},
		{
			name:        "value above from range clamps to to max",
			value:       500,
			from:        validation.IntegerRange{Min: 0, Max: 255},
			to:          validation.IntegerRange{Min: 1, Max: 51},
			want:        51,
			description: "out-of-range input is clamped after remap",
		},
		{
			name:        "value below from range clamps to to min",
			value:       -10,
			from:        validation.IntegerRange{Min: 0, Max: 51},
			to:          validation.IntegerRange{Min: 0, Max: 255},
			want:        0,
			description: "out-of-range input is clamped after remap",
		},
		{
			name:        "degenerate from range falls back to clamp",
			value:       7,
			from:        validation.IntegerRange{Min: 5, Max: 5},
			to:          validation.IntegerRange{Min: 0, Max: 100},
			want:        7,
			description: "from spread of 0 is invalid; treat as direct value",
		},
		{
			name:        "smaller-from to larger-to remap is monotonic",
			value:       20,
			from:        validation.IntegerRange{Min: 0, Max: 51},
			to:          validation.IntegerRange{Min: 0, Max: 63},
			want:        25,
			description: "20/51 * 63 = 24.7 -> 25; proportional and ordered",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := validation.ProportionalConvert(tt.value, tt.from, tt.to)
			if got != tt.want {
				t.Errorf("ProportionalConvert(%d, %d-%d, %d-%d) = %d, want %d (%s)",
					tt.value, tt.from.Min, tt.from.Max, tt.to.Min, tt.to.Max, got, tt.want, tt.description)
			}
		})
	}
}

func TestProportionalConvertPreservesMonotonicity(t *testing.T) {
	from := validation.IntegerRange{Min: 0, Max: 255}
	to := validation.IntegerRange{Min: 1, Max: 51}
	last := -1
	for v := from.Min; v <= from.Max; v++ {
		got := validation.ProportionalConvert(v, from, to)
		if got < last {
			t.Errorf("monotonicity broken at v=%d: got %d < prev %d", v, got, last)
		}
		if got < to.Min || got > to.Max {
			t.Errorf("v=%d mapped to %d outside to %d-%d", v, got, to.Min, to.Max)
		}
		last = got
	}
}
