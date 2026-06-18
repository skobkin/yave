// Package app exposes build-time metadata populated by goreleaser via
// `-ldflags '-X git.skobk.in/skobkin/yave/internal/app.Version=...'`.
//
// The variables default to development-friendly placeholders so the package
// stays safe to import from non-release builds.
package app

import (
	"fmt"
	"strings"
	"time"
)

// DevBuildVersion is the placeholder used when no version was supplied
// at build time.
const DevBuildVersion = "dev"

var (
	// Version is set at build time via the goreleaser ldflag.
	Version = DevBuildVersion
	// BuildDate is set at build time via the goreleaser ldflag.
	BuildDate = ""
)

// BuildVersion returns the trimmed build version, falling back to
// [DevBuildVersion] when no version was supplied.
func BuildVersion() string {
	version := strings.TrimSpace(Version)
	if version == "" {
		return DevBuildVersion
	}

	return version
}

// BuildDateYMD returns the build date formatted as YYYY-MM-DD, or the
// raw string when it cannot be parsed. The goreleaser `-X` flag passes
// a RFC3339 timestamp, but older release pipelines may have stamped
// an already-formatted date, so both shapes are accepted.
func BuildDateYMD() string {
	raw := strings.TrimSpace(BuildDate)
	if raw == "" {
		return ""
	}

	if parsed, err := time.Parse(time.RFC3339, raw); err == nil {
		return parsed.Format("2006-01-02")
	}

	if len(raw) >= len("2006-01-02") {
		date := raw[:len("2006-01-02")]
		if _, err := time.Parse("2006-01-02", date); err == nil {
			return date
		}
	}

	return raw
}

// BuildVersionWithDate returns the version string followed by the build
// date in parentheses when a date is available.
func BuildVersionWithDate() string {
	version := BuildVersion()
	if buildDate := BuildDateYMD(); buildDate != "" {
		return fmt.Sprintf("%s (%s)", version, buildDate)
	}

	return version
}
