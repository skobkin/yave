package validation

import "strings"

// IntegerRange defines an inclusive range for a curated integer option.
type IntegerRange struct {
	Min int
	Max int
}

func (r IntegerRange) Contains(value int) bool {
	return value >= r.Min && value <= r.Max
}

func (r IntegerRange) Clamp(value int) int {
	if value < r.Min {
		return r.Min
	}
	if value > r.Max {
		return r.Max
	}
	return value
}

// ProportionalConvert maps a value from one inclusive integer range into
// another using a best-effort linear remap and clamping to the new bounds.
//
// It is intended for codec switches where the same conceptual setting (QP or
// CRF) has a different numeric range. The mapping is purely positional:
// value == from.Min -> to.Min, value == from.Max -> to.Max, with linear
// interpolation in between and Clamp applied to the result. A degenerate
// from-range (Min == Max) is treated as "keep value, clamp to to".
func ProportionalConvert(value int, from, to IntegerRange) int {
	if from.Max == from.Min {
		return to.Clamp(value)
	}
	if from == to {
		return to.Clamp(value)
	}
	// Integer linear remap: preserve endpoints, avoid floating-point drift
	// for the common equal-spread case. numerator and denominator are both
	// non-negative when from is a valid range and value is in it.
	spread := from.Max - from.Min
	offset := value - from.Min
	mapped := to.Min + (offset*(to.Max-to.Min)+spread/2)/spread
	return to.Clamp(mapped)
}

func QPRange(encoder string) (IntegerRange, bool) {
	switch strings.ToLower(strings.TrimSpace(encoder)) {
	case "h264_vaapi", "hevc_vaapi":
		return IntegerRange{Min: 1, Max: 51}, true
	case "av1_vaapi", "vp9_vaapi":
		return IntegerRange{Min: 0, Max: 255}, true
	default:
		return IntegerRange{}, false
	}
}

func CRFRange(encoder string) (IntegerRange, bool) {
	switch strings.ToLower(strings.TrimSpace(encoder)) {
	case "libx264", "libx265":
		return IntegerRange{Min: 0, Max: 51}, true
	case "libsvtav1", "libvpx-vp9":
		return IntegerRange{Min: 0, Max: 63}, true
	default:
		return IntegerRange{}, false
	}
}

func PresetRange(encoder string) (IntegerRange, bool) {
	if strings.EqualFold(strings.TrimSpace(encoder), "libsvtav1") {
		return IntegerRange{Min: 0, Max: 13}, true
	}
	return IntegerRange{}, false
}

func AudioCompressionRange(encoder string) (IntegerRange, bool) {
	if strings.EqualFold(strings.TrimSpace(encoder), "flac") {
		return IntegerRange{Min: 0, Max: 12}, true
	}
	return IntegerRange{}, false
}
