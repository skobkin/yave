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
