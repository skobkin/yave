package ffmpeg

import (
	"context"
	"strings"
)

// probeEncoderProfiles returns the -profile values the build's ffmpeg
// enumerates for encoderName via `ffmpeg -h encoder=<name>`, or nil
// when the option is a free-form string (e.g. libx264) or absent
// entirely (e.g. libsvtav1). Absence of an enumeration is not evidence
// of non-support — -profile is a generic AVCodecContext option every
// encoder parses — so callers keep their curated list in that case and
// on probe errors.
func probeEncoderProfiles(ctx context.Context, executable, encoderName string) ([]string, error) {
	output, err := commandOutput(ctx, executable, "-hide_banner", "-h", "encoder="+encoderName)
	if err != nil {
		return nil, err
	}
	return parseProfileValues(output), nil
}

// parseProfileValues extracts the enumerated value names of the
// -profile option from `ffmpeg -h encoder=` output. Only <int>
// options enumerate values; a <string> -profile or no -profile at all
// yields nil. Scanning stops at the first non-value line so the value
// rows of a following option (e.g. -level) are never picked up.
func parseProfileValues(output string) []string {
	var values []string
	inProfile := false
	for line := range strings.SplitSeq(output, "\n") {
		if strings.HasPrefix(line, "  -profile ") {
			// Option header at the two-space indent ffmpeg uses. Only
			// <int> options are followed by enumerated value rows.
			inProfile = strings.Contains(line, "<int>")
			continue
		}
		if !inProfile {
			continue
		}
		name, ok := profileValueName(line)
		if !ok {
			// Any non-value line (next option header, blank line)
			// ends the -profile section.
			inProfile = false
			continue
		}
		values = append(values, name)
	}
	return values
}

// profileValueName matches a `ffmpeg -h` enumerated value row:
// five-plus spaces of indent, a value name, whitespace, and the
// integer the name maps to (e.g. "     main10          2   E..V....").
func profileValueName(line string) (string, bool) {
	if !strings.HasPrefix(line, "     ") {
		return "", false
	}
	trimmed := strings.TrimLeft(line, " ")
	name, rest, found := strings.Cut(trimmed, " ")
	if !found || name == "" {
		return "", false
	}
	number, _, found := strings.Cut(strings.TrimLeft(rest, " "), " ")
	if !found || !isDecimal(number) {
		return "", false
	}
	return name, true
}

// isDecimal reports whether s is a plain non-negative integer.
func isDecimal(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// intersectProfiles keeps the curated values the build also
// enumerates, in curated order, matching case-insensitively. An
// enumerated list that matches nothing curated yields nil; the caller
// treats that as "no narrowing information" and keeps the curated
// list rather than offering an empty dropdown.
func intersectProfiles(curated, enumerated []string) []string {
	var out []string
	for _, c := range curated {
		for _, e := range enumerated {
			if strings.EqualFold(c, e) {
				out = append(out, c)
				break
			}
		}
	}
	return out
}
