package ui

import (
	"fmt"
	"path/filepath"
	"strings"

	"git.skobk.in/skobkin/yave/internal/model"
	"git.skobk.in/skobkin/yave/internal/vaapi"
)

// ffmpegView is the FFmpeg panel's content: the version as a title, the
// hardware encoding backends as badges, a detail row for the build's
// hwaccel methods, and the host's GPUs as named pills whose tooltips
// carry the technical identifiers (render node or adapter index, drivers,
// PCI IDs). The full banner lines stay in detectionCopyText.
type ffmpegView struct {
	Title  string
	Badges []string
	Rows   []infoRow
	GPUs   []gpuPill
}

// gpuPill is one GPU of the FFmpeg panel: a readable name (with a status
// suffix when its VA-API probe matters), the hover details, and whether
// it is flagged (probe failed).
type gpuPill struct {
	Text    string
	Tip     string
	Warning bool
}

// String renders the view as plain text.
func (v ffmpegView) String() string {
	lines := []string{v.Title + "  " + strings.Join(v.Badges, " ")}
	for _, r := range v.Rows {
		lines = append(lines, r.String())
	}
	if len(v.GPUs) > 0 {
		names := make([]string, len(v.GPUs))
		for i, g := range v.GPUs {
			names[i] = g.Text
		}
		lines = append(lines, "GPUs  "+strings.Join(names, infoSeparator))
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
	view.GPUs = gpuPills(caps)
	return view
}

// gpuPills lists the host's GPUs: the probed render nodes on Linux, the
// DXGI adapters on Windows, nothing where neither inventory exists.
func gpuPills(caps model.Capabilities) []gpuPill {
	if len(caps.VAAPIDevices) > 0 {
		pills := make([]gpuPill, 0, len(caps.VAAPIDevices))
		for _, d := range caps.VAAPIDevices {
			pills = append(pills, renderNodePill(d))
		}
		return pills
	}
	var pills []gpuPill
	for _, a := range caps.GPUAdapters {
		if a.VendorID == microsoftBasicRenderVendor {
			continue // the software rasterizer, not a GPU
		}
		name := strings.TrimSpace(a.Name)
		if name == "" {
			name = fmt.Sprintf("Adapter %d", a.Index)
		}
		pills = append(pills, gpuPill{Text: name, Tip: fmt.Sprintf("DXGI adapter %d · PCI vendor %04X", a.Index, a.VendorID)})
	}
	return pills
}

// microsoftBasicRenderVendor is the PCI vendor ID DXGI reports for the
// Microsoft Basic Render Driver (WARP).
const microsoftBasicRenderVendor uint32 = 0x1414

func renderNodePill(d vaapi.DeviceInfo) gpuPill {
	name := d.Identity.BrandedName()
	if name == "" {
		name = filepath.Base(d.Path)
	}
	tip := []string{d.Path}
	pill := gpuPill{Text: name}
	switch {
	case !d.Known():
		pill.Text += infoSeparator + "unavailable"
		pill.Warning = true
		tip = append(tip, "VA-API probe failed: "+d.Err)
	case len(d.EntryPoints) == 0:
		tip = append(tip, "VA-API: no profiles reported")
	case d.HasAnyEncodingEntrypoint():
		tip = append(tip, "VA-API: encodes")
	default:
		pill.Text += infoSeparator + "decode only"
		tip = append(tip, "VA-API: decode only")
	}
	if d.Driver != "" {
		tip = append(tip, "VA-API driver: "+d.Driver)
	}
	if d.Identity.Driver != "" {
		tip = append(tip, "Kernel driver: "+d.Identity.Driver)
	}
	if d.Identity.HasIDs() {
		tip = append(tip, "PCI ID: "+d.Identity.Vendor+":"+d.Identity.Device)
	}
	pill.Tip = strings.Join(tip, "\n")
	return pill
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
