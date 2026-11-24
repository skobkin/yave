package ui

import (
	"reflect"
	"strings"
	"testing"

	"git.skobk.in/skobkin/yave/internal/model"
)

func TestShortVersion(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{
		"ffmpeg version n9.1 Copyright (c) 2000-2026 the FFmpeg developers":              "n9.1",
		"ffprobe version 7.1-full_build-www.gyan.dev Copyright (c) 2007-2024 the FFmpeg": "7.1-full_build-www.gyan.dev",
		"ffmpeg version N-118000-g1234abcd-20250101 Copyright (c) 2000-2025":             "N-118000-g1234abcd-20250101",
		"some wrapper output":   "some wrapper output",
		"trailing word version": "trailing word version",
		"   ":                   "unknown version",
	} {
		if got := shortVersion(in); got != want {
			t.Errorf("shortVersion(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDetectionLines(t *testing.T) {
	t.Parallel()
	caps := vaapiDualGPUFixture()
	caps.FFmpegVersion = "ffmpeg version n9.1 Copyright (c) 2000-2026 the FFmpeg developers"
	caps.FFprobeVersion = "ffprobe version n9.1 Copyright (c) 2007-2026 the FFmpeg developers"
	caps.FFmpegPath, caps.FFprobePath = "/usr/bin/ffmpeg", "/usr/bin/ffprobe"
	caps.HWAccels = []string{"cuda", "vaapi"}
	caps.HWVAAPIAvailable, caps.HWNVENCAvailable, caps.NVENCGPUCount = true, true, 2
	broken := caps.VAAPIDevices[0]
	broken.Path, broken.Err = "/dev/dri/renderD130", "vaInitialize failed"
	caps.VAAPIDevices = append(caps.VAAPIDevices, broken)

	got := detectionView(caps)
	want := ffmpegView{
		Title:  "FFmpeg n9.1",
		Badges: []string{"VAAPI", "NVENC ×2"},
		Rows: []infoRow{
			{Label: "HW accel", Details: "cuda, vaapi"},
			{Label: "VAAPI", Details: "renderD128 GeForce GTX 1660 Ti Mobile (decode only) · renderD129 Renoir · renderD130 GeForce GTX 1660 Ti Mobile (unavailable)"},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("detectionView() =\n%#v\nwant\n%#v", got, want)
	}
	if text := got.String(); strings.Contains(text, "Copyright") || strings.Contains(text, "Mesa") {
		t.Errorf("banner or driver strings belong to Copy only:\n%s", text)
	}

	copied := detectionCopyText(caps)
	for _, want := range []string{
		caps.FFmpegVersion, caps.FFprobeVersion, "FFmpeg path: /usr/bin/ffmpeg", "ffprobe path: /usr/bin/ffprobe",
		"Hardware acceleration: cuda, vaapi", "Mesa Gallium driver (radeonsi)",
	} {
		if !strings.Contains(copied, want) {
			t.Errorf("copy text missing %q in:\n%s", want, copied)
		}
	}
}

func TestDetectionLinesMismatchedVersionsAndNoHardware(t *testing.T) {
	t.Parallel()
	got := detectionView(model.Capabilities{
		FFmpegVersion:  "ffmpeg version 8.0 Copyright",
		FFprobeVersion: "ffprobe version 7.1 Copyright",
	})
	want := ffmpegView{Title: "ffmpeg 8.0 · ffprobe 7.1", Badges: []string{softwareOnlyBadge}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("detectionView() = %#v, want %#v", got, want)
	}
}
