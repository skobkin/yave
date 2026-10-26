package encoder

import (
	"fmt"
	"strings"

	"git.skobk.in/skobkin/yave/internal/model"
)

// targetDimensions computes the destination (width, height) the encoder
// should resize to and reports whether scaling is required at all. The
// behaviour matches ffmpeg.targetDimensions exactly so the filter chain
// emitted by each encoder's VideoFilter matches the prior string
// contract.
//
// When MaxWidth and MaxHeight are both zero the function returns
// (0, 0, false) — the caller must not emit a scale filter in that case.
// When the source metadata is missing the requested dimensions are
// returned as-is and the "scale" boolean reflects whether at least one
// side was specified.
//
// KeepAspectRatio / PreventUpscaling are honoured identically to the
// pre-refactor command.go logic.
func targetDimensions(v model.VideoSettings, media model.MediaInfo) (int, int, bool) {
	if v.MaxWidth <= 0 && v.MaxHeight <= 0 {
		return 0, 0, false
	}
	src, ok := media.PrimaryVideo()
	if !ok || src.Width <= 0 || src.Height <= 0 {
		return even(v.MaxWidth), even(v.MaxHeight), v.MaxWidth > 0 && v.MaxHeight > 0
	}

	maxW, maxH := v.MaxWidth, v.MaxHeight
	if maxW <= 0 {
		maxW = src.Width
	}
	if maxH <= 0 {
		maxH = src.Height
	}
	if v.PreventUpscaling && src.Width <= maxW && src.Height <= maxH {
		return src.Width, src.Height, false
	}
	if !v.KeepAspectRatio {
		return even(maxW), even(maxH), src.Width != maxW || src.Height != maxH
	}

	ratio := min(float64(maxW)/float64(src.Width), float64(maxH)/float64(src.Height))
	if v.PreventUpscaling {
		ratio = min(ratio, 1)
	}
	w := even(int(float64(src.Width) * ratio))
	h := even(int(float64(src.Height) * ratio))
	return w, h, w != src.Width || h != src.Height
}

// even rounds v down to the nearest even integer (or returns v when it
// is <= 0). Mirrors ffmpeg.even.
func even(v int) int {
	if v <= 0 {
		return v
	}
	return v - v%2
}

// defaultString returns fallback when value is empty.
func defaultString(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

// scalePart builds a scale=... filter fragment matching the pre-refactor
// ffmpeg.videoFilter output for software encoders. The VAAPI / NVENC /
// QSV equivalents live in their own files because they share more
// machinery with the hw upload filter.
func scalePart(width, height int, keepAspectRatio bool) string {
	part := fmt.Sprintf("scale=w=%d:h=%d", width, height)
	if keepAspectRatio {
		part += ":force_original_aspect_ratio=decrease:force_divisible_by=2"
	}
	return part
}

// rateControlMode returns the upper-cased, trimmed rate-control mode
// for a profile. Empty string means "AUTO" or uninitialised.
func rateControlMode(v model.VideoSettings) string {
	return strings.ToUpper(strings.TrimSpace(v.RateControl))
}

// inCuratedList reports whether value matches a list entry
// case-insensitively after trimming. An empty list never matches.
func inCuratedList(list []string, value string) bool {
	want := strings.ToLower(strings.TrimSpace(value))
	for _, entry := range list {
		if strings.EqualFold(entry, want) {
			return true
		}
	}
	return false
}

// profileIssues returns the shared -profile:v findings used by every
// video encoder family: encoders that do not support the flag reject
// any non-empty profile, and encoders with a curated list reject
// values outside it (case-insensitive). The membership check is gated
// on a non-empty curated list so an encoder that accepts profiles we
// cannot enumerate never rejects everything.
func profileIssues(supports bool, profiles []string, encoderName, profile string) []Issue {
	profile = strings.TrimSpace(profile)
	if profile == "" {
		return nil
	}
	if !supports {
		return []Issue{{
			Level:   IssueError,
			Field:   "profile.video.profile",
			Message: fmt.Sprintf("%s does not support -profile:v", encoderName),
		}}
	}
	if len(profiles) > 0 && !inCuratedList(profiles, profile) {
		return []Issue{{
			Level:   IssueError,
			Field:   "profile.video.profile",
			Message: fmt.Sprintf("%s profile must be one of: %s", encoderName, strings.Join(profiles, ", ")),
		}}
	}
	return nil
}

// appendFrameRateFilter returns the leading fps= filter when frame-rate
// conversion is requested, or nil when no conversion is configured.
// The slice is intended to be prepended to the encoder's filter chain.
func appendFrameRateFilter(v model.VideoSettings) ([]string, error) {
	if !strings.EqualFold(strings.TrimSpace(v.FrameRateMode), "convert") {
		return nil, nil
	}
	if strings.TrimSpace(v.FrameRate) == "" {
		return nil, fmt.Errorf("frame rate is required when conversion is enabled")
	}
	return []string{"fps=" + v.FrameRate}, nil
}
