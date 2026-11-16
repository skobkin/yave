package validation

import (
	"strings"

	"git.skobk.in/skobkin/yave/internal/model"
)

// ValidateTrim checks a trim selection against the inspected media and
// the profile's video encoder. It is separate from Validate so the
// existing run-level signature (and its tests) stay untouched; the
// caller merges the results via Result.Merge, and the Trim tab calls it
// directly for inline feedback.
//
// NOTE: Phase 1 accepts at most one segment. The >1 rejection below is
// a temporary restriction until multi-interval support lands (see
// model.TrimSelection — the plural model shape is deliberate).
func ValidateTrim(trim model.TrimSelection, media *model.MediaInfo, videoEncoder string) Result {
	var result Result
	if !trim.Active() {
		return result
	}
	if len(trim.Segments) > 1 {
		// Temporary Phase-1 restriction, not a model limitation.
		result.error("trim", "%s", "trimming supports a single segment in this version")
		return result
	}
	if media == nil || media.Duration <= 0 {
		result.error("trim", "%s", "trimming requires an input with a known duration")
		return result
	}
	seg := trim.Segments[0]
	if seg.Start < 0 || seg.End < 0 {
		result.error("trim", "%s", "trim boundaries must not be negative")
		return result
	}
	if seg.End <= seg.Start {
		result.error("trim", "%s", "trim end must be after start")
		return result
	}
	if seg.Start >= media.Duration {
		result.error("trim", "%s", "trim start is at or beyond the input duration")
		return result
	}
	if seg.End > media.Duration {
		result.error("trim", "%s", "trim end exceeds the input duration")
		return result
	}
	// Stream copy cannot re-encode to an exact frame: ffmpeg stops at a
	// keyframe boundary, so the actual cut may land before the selected
	// end or extend past it by up to one GOP.
	if strings.EqualFold(videoEncoder, "copy") {
		result.warning("trim", "%s", "stream copy snaps cut boundaries to the nearest keyframes; the actual cut may differ from the selected range")
	}
	return result
}

// Merge appends other's issues into r so callers can combine the
// profile-level and trim-level results into one run-level Result.
func (r *Result) Merge(other Result) {
	r.Errors = append(r.Errors, other.Errors...)
	r.Warnings = append(r.Warnings, other.Warnings...)
}
