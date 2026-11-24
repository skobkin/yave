package ui

import (
	"fmt"
	"path/filepath"
	"strings"

	"git.skobk.in/skobkin/yave/internal/model"
)

// ffmpegView is the FFmpeg panel's content: the version as a title, the
// hardware encoding backends as badges, and detail rows for the build's
// hwaccel methods and the probed VAAPI devices (by GPU name). The full
// banner lines and VA-API driver strings stay in detectionCopyText.
type ffmpegView struct {
	Title  string
	Badges []string
	Rows   []infoRow
}

// String renders the view as plain text.
func (v ffmpegView) String() string {
	lines := []string{v.Title + "  " + strings.Join(v.Badges, " ")}
	for _, r := range v.Rows {
		lines = append(lines, r.String())
	}
	return strings.Join(lines, "\n")
}

func detectionView(caps model.Capabilities) ffmpegView {
	ffmpegVersion, ffprobeVersion := shortVersion(caps.FFmpegVersion), shortVersion(caps.FFprobeVersion)
	view := ffmpegView{Title: "FFmpeg " + ffmpegVersion, Badges: hardwareBackendBadges(caps)}
	if ffmpegVersion != ffprobeVersion {
		// A mismatched pair usually means two installations on PATH;
		// both versions are worth seeing.
		view.Title = "ffmpeg " + ffmpegVersion + infoSeparator + "ffprobe " + ffprobeVersion
	}
	if len(caps.HWAccels) > 0 {
		view.Rows = append(view.Rows, infoRow{Label: "HW accel", Details: strings.Join(caps.HWAccels, ", ")})
	}
	if len(caps.VAAPIDevices) > 0 {
		parts := make([]string, 0, len(caps.VAAPIDevices))
		for _, d := range caps.VAAPIDevices {
			text := joinNonEmpty(" ", filepath.Base(d.Path), d.Identity.DisplayName())
			switch {
			case !d.Known():
				text += " (unavailable)"
			case len(d.EntryPoints) > 0 && !d.HasAnyEncodingEntrypoint():
				text += " (decode only)"
			}
			parts = append(parts, text)
		}
		view.Rows = append(view.Rows, infoRow{Label: "VAAPI", Details: strings.Join(parts, infoSeparator)})
	}
	return view
}

// detectionCopyText is the full detection report for the clipboard.
func detectionCopyText(caps model.Capabilities) string {
	text := fmt.Sprintf("%s\n%s\nFFmpeg path: %s\nffprobe path: %s\nHardware acceleration: %s\nHardware devices: %s",
		caps.FFmpegVersion, caps.FFprobeVersion, caps.FFmpegPath, caps.FFprobePath,
		strings.Join(caps.HWAccels, ", "), hardwareDevicesSummary(caps))
	if devices := vaapiDevicesSummary(caps); devices != "" {
		text += "\nVAAPI devices: " + devices
	}
	return text
}

// shortVersion extracts the version token from an FFmpeg/ffprobe banner
// line ("ffmpeg version n9.1 Copyright ..." → "n9.1"). A line without
// the "version" keyword is returned as-is.
func shortVersion(line string) string {
	line = strings.TrimSpace(line)
	if line == "" {
		return unknownText + " version"
	}
	fields := strings.Fields(line)
	for i, f := range fields[:len(fields)-1] {
		if f == "version" {
			return fields[i+1]
		}
	}
	return line
}

// hardwareBackendBadges names the hardware encoding backends whose
// probes succeeded: "VAAPI", "NVENC ×2"; "Software only" when none did.
func hardwareBackendBadges(caps model.Capabilities) []string {
	var backends []string
	if caps.HWVAAPIAvailable {
		backends = append(backends, "VAAPI")
	}
	if caps.HWQSVAvailable {
		backends = append(backends, "QSV")
	}
	if caps.HWNVENCAvailable {
		if caps.NVENCGPUCount > 1 {
			backends = append(backends, fmt.Sprintf("NVENC ×%d", caps.NVENCGPUCount))
		} else {
			backends = append(backends, "NVENC")
		}
	}
	if len(backends) == 0 {
		return []string{softwareOnlyBadge}
	}
	return backends
}

// softwareOnlyBadge stands in for the backend badges when no hardware
// encoder is usable.
const softwareOnlyBadge = "Software only"
