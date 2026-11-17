package validation

import (
	"fmt"
	"strings"

	"git.skobk.in/skobkin/yave/internal/model"
)

// ValidateTrim checks a trim selection against the inspected media and
// the profile's video encoder. It is separate from Validate so the
// existing run-level signature (and its tests) stay untouched; the
// caller merges the results via Result.Merge, and the Trim tab calls it
// directly for inline feedback.
//
// Segments must be non-empty, within the input duration, chronological,
// and non-overlapping; adjacency (one segment ending exactly where the
// next starts) is allowed. Per-segment errors carry a "segment N: "
// prefix only when the selection has multiple segments, so single-
// segment messages stay unchanged.
func ValidateTrim(trim model.TrimSelection, media *model.MediaInfo, videoEncoder string) Result {
	var result Result
	if !trim.Active() {
		return result
	}
	if media == nil || media.Duration <= 0 {
		result.error("trim", "%s", "trimming requires an input with a known duration")
		return result
	}
	prefix := func(i int) string {
		if len(trim.Segments) > 1 {
			return fmt.Sprintf("segment %d: ", i+1)
		}
		return ""
	}
	for i, seg := range trim.Segments {
		switch {
		case seg.Start < 0 || seg.End < 0:
			result.error("trim", "%s%s", prefix(i), "trim boundaries must not be negative")
		case seg.End <= seg.Start:
			result.error("trim", "%s%s", prefix(i), "trim end must be after start")
		case seg.Start >= media.Duration:
			result.error("trim", "%s%s", prefix(i), "trim start is at or beyond the input duration")
		case seg.End > media.Duration:
			result.error("trim", "%s%s", prefix(i), "trim end exceeds the input duration")
		}
	}
	// One ordering rule covers both overlapping segments and a typed
	// reordering: with segments kept in display order, a segment that
	// starts before the previous one ends violates the chronological,
	// non-overlapping contract either way. Adjacency is fine.
	for i := 1; i < len(trim.Segments); i++ {
		if trim.Segments[i].Start < trim.Segments[i-1].End {
			result.error("trim", "%s", fmt.Sprintf(
				"trim segments must be chronological and non-overlapping: segment %d starts before segment %d ends",
				i+1, i))
			break
		}
	}
	// Stream copy cannot re-encode to an exact frame: ffmpeg stops at a
	// keyframe boundary, so the actual cut may land before the selected
	// end or extend past it by up to one GOP.
	if strings.EqualFold(videoEncoder, "copy") {
		result.warning("trim", "%s", "stream copy snaps cut boundaries to the nearest keyframes; the actual cut may differ from the selected range")
	}
	return result
}

// ValidateTrimExport checks export-mode rules that need the full profile
// semantics and only make sense at run time. Join checks apply to
// multi-segment selections; a single segment joins by direct encode, so
// the mode carries no copy restriction there. It is never called from
// profile-level validation, which must stay usable without source
// metadata.
func ValidateTrimExport(trim model.TrimSelection, mode model.TrimExportMode,
	media *model.MediaInfo, videoEncoder string, audio model.AudioSettings) Result {
	var result Result
	if !trim.Active() || mode != model.TrimExportJoin || len(trim.Segments) <= 1 {
		return result
	}
	if media == nil || media.Duration <= 0 {
		return result
	}
	if strings.EqualFold(strings.TrimSpace(videoEncoder), "copy") {
		result.error("trim.export", "%s",
			"video stream copy cannot be combined with joining segments: "+
				"copy-cut boundaries snap to keyframes and copied segments cannot be concatenated reliably; "+
				"select a video encoder, or switch the export mode to separate files")
	}
	if audio.IsStreamCopy() {
		result.warning("trim.export", "%s",
			"joining with copied audio can leave small gaps (about 20 ms) at each segment boundary "+
				"from codec priming samples; encode the audio, or switch the export mode to separate files "+
				"for gap-free parts")
	}
	return result
}

// Merge appends other's issues into r so callers can combine the
// profile-level and trim-level results into one run-level Result.
func (r *Result) Merge(other Result) {
	r.Errors = append(r.Errors, other.Errors...)
	r.Warnings = append(r.Warnings, other.Warnings...)
}
