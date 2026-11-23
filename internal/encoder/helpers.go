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
	stream, ok := media.PrimaryVideo()
	if !ok || stream.Width <= 0 || stream.Height <= 0 {
		return even(v.MaxWidth), even(v.MaxHeight), v.MaxWidth > 0 && v.MaxHeight > 0
	}
	// ffmpeg autorotates frames before the filter graph, so size the
	// target against the displayed orientation, not the coded one.
	srcW, srcH := stream.DisplaySize()

	maxW, maxH := v.MaxWidth, v.MaxHeight
	if maxW <= 0 {
		maxW = srcW
	}
	if maxH <= 0 {
		maxH = srcH
	}
	if v.PreventUpscaling && srcW <= maxW && srcH <= maxH {
		return srcW, srcH, false
	}
	if !v.KeepAspectRatio {
		return even(maxW), even(maxH), srcW != maxW || srcH != maxH
	}

	ratio := min(float64(maxW)/float64(srcW), float64(maxH)/float64(srcH))
	if v.PreventUpscaling {
		ratio = min(ratio, 1)
	}
	w := even(int(float64(srcW) * ratio))
	h := even(int(float64(srcH) * ratio))
	return w, h, w != srcW || h != srcH
}

// hwScaleDimensions wraps targetDimensions for the hardware scalers that
// only take exact w/h (scale_cuda, scale_qsv). Without known source
// dimensions an aspect-preserving request cannot be computed, and forcing
// the requested box would stretch the picture, so scaling is skipped.
func hwScaleDimensions(v model.VideoSettings, media model.MediaInfo) (int, int, bool) {
	width, height, scale := targetDimensions(v, media)
	if scale && v.KeepAspectRatio {
		if stream, ok := media.PrimaryVideo(); !ok || stream.Width <= 0 || stream.Height <= 0 {
			return 0, 0, false
		}
	}
	return width, height, scale
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

// canonicalCurated returns the curated-list spelling matching value
// case-insensitively after trimming, or the trimmed value when nothing
// matches. ffmpeg's AVOption constant parser is case- and
// whitespace-sensitive, while inCuratedList accepts the trimmed
// case-insensitive form — every value validation accepts must be
// normalized through this before it is emitted, or a hand-edited
// "High" / " main " passes validation and dies at option parsing.
func canonicalCurated(list []string, value string) string {
	trimmed := strings.TrimSpace(value)
	for _, entry := range list {
		if strings.EqualFold(entry, trimmed) {
			return entry
		}
	}
	return trimmed
}

// PixelFormatBitDepth reads the bit depth out of an ffprobe pixel
// format name (yuv420p10le → 10, yuv420p → 8). Unrecognized names
// return 8, which disables depth warnings rather than fabricating one.
// Lives in encoder — next to the chain decisions that consume it —
// because validation imports encoder, not the other way around.
func PixelFormatBitDepth(format string) int {
	f := strings.ToLower(strings.TrimSpace(format))
	switch {
	case strings.Contains(f, "16le"), strings.Contains(f, "16be"):
		return 16
	case strings.Contains(f, "14le"), strings.Contains(f, "14be"):
		return 14
	case strings.Contains(f, "12le"), strings.Contains(f, "12be"):
		return 12
	case strings.Contains(f, "10le"), strings.Contains(f, "10be"):
		return 10
	case strings.Contains(f, "9le"), strings.Contains(f, "9be"):
		return 9
	}
	return 8
}

// sourceTenBit reports whether the source's primary video stream is
// 10-bit or deeper — the depths the p010 upload chain serves. A source
// without probe metadata resolves to 8-bit, failing open to the
// historical 8-bit chains rather than guessing a 10-bit one.
func sourceTenBit(media model.MediaInfo) bool {
	stream, ok := media.PrimaryVideo()
	if !ok {
		return false
	}
	return PixelFormatBitDepth(stream.PixelFormat) >= 10
}

// TenBitChain is the ONE shared decision on whether the hardware chain
// for the given encoder and -profile:v value uploads p010 (10-bit)
// instead of the family's 8-bit format. Every depth consumer — the
// VideoFilter chains, the VAAPI device requirements, the validation
// warnings, the UI hints — resolves through it, so filters, device
// picking, validation, and hints cannot disagree about the output
// depth.
//
// Explicit values: hevc main10 (all three hardware families) and the
// 10-bit VP9 profiles (vp9_vaapi "2", vp9_qsv "profile2"). Matching is
// case- and whitespace-tolerant like inCuratedList, because validation
// accepts the trimmed case-insensitive form while the builders emit
// canonicalCurated's spelling — the chain must agree with the value
// that will actually reach ffmpeg.
//
// The source-derived rule is deliberately narrow (empirically gated —
// ffmpeg n9.0.2 on Renoir, encode + ffprobe verified): only hevc_vaapi
// with an empty profile switches on a 10-bit source, because the VAAPI
// encoder selects the profile from the surface format when -profile:v
// is unset and picks Main 10 for a p010 surface. The other families
// keep the 8-bit chain for an empty profile: NVENC/QSV 10-bit is
// explicit-only by design, and nothing was verified for a
// source-derived p010 there.
//
// h264 (8-bit only in every family), av1 (Main covers 8- and 10-bit
// through the same VAProfile — there is no separate 10-bit value to
// curate and the chain format follows the curated values), and copy
// never take the 10-bit chain.
func TenBitChain(encoderName, profile string, media model.MediaInfo) bool {
	name := strings.ToLower(strings.TrimSpace(encoderName))
	p := strings.ToLower(strings.TrimSpace(profile))
	switch name {
	case "hevc_vaapi", "hevc_qsv", "hevc_nvenc":
		if p == "main10" {
			return true
		}
		if p == "" && name == "hevc_vaapi" {
			return sourceTenBit(media)
		}
	case "vp9_vaapi":
		if p == "2" {
			return true
		}
	case "vp9_qsv":
		if p == "profile2" {
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
