package ui

import (
	"reflect"
	"strings"
	"testing"

	"git.skobk.in/skobkin/yave/internal/model"
	"git.skobk.in/skobkin/yave/internal/vaapi"
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

func TestDetectionView(t *testing.T) {
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
		Rows:   []infoRow{{Label: "HW accel", Details: "cuda, vaapi"}},
		GPUs: []gpuPill{
			{
				Text: "NVIDIA GeForce GTX 1660 Ti Mobile · decode only",
				Tip:  "/dev/dri/renderD128\nVA-API: decode only\nVA-API driver: VA-API NVDEC driver [direct backend]\nKernel driver: nvidia\nPCI ID: 10de:2191",
			},
			{
				Text: "AMD Renoir",
				Tip:  "/dev/dri/renderD129\nVA-API: encodes\nVA-API driver: Mesa Gallium driver (radeonsi)\nKernel driver: amdgpu\nPCI ID: 1002:1636",
			},
			{
				Text:    "NVIDIA GeForce GTX 1660 Ti Mobile · unavailable",
				Tip:     "/dev/dri/renderD130\nVA-API probe failed: vaInitialize failed\nVA-API driver: VA-API NVDEC driver [direct backend]\nKernel driver: nvidia\nPCI ID: 10de:2191",
				Warning: true,
			},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("detectionView() =\n%#v\nwant\n%#v", got, want)
	}
	if text := got.String(); strings.Contains(text, "Copyright") || strings.Contains(text, "Mesa") || strings.Contains(text, "renderD") {
		t.Errorf("banners, driver strings and node paths belong to Copy or tooltips only:\n%s", text)
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

func TestDetectionViewMismatchedVersionsAndNoHardware(t *testing.T) {
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

func TestGPUPillsFallBackToWindowsAdapters(t *testing.T) {
	t.Parallel()
	got := gpuPills(model.Capabilities{GPUAdapters: []model.GPUAdapter{
		{Index: 0, Name: "NVIDIA GeForce GTX 1660 Ti", VendorID: 0x10DE},
		{Index: 1, Name: "Microsoft Basic Render Driver", VendorID: microsoftBasicRenderVendor},
		{Index: 2, Name: " ", VendorID: 0x1002},
	}})
	want := []gpuPill{
		{Text: "NVIDIA GeForce GTX 1660 Ti", Tip: "DXGI adapter 0 · PCI vendor 10DE"},
		{Text: "Adapter 2", Tip: "DXGI adapter 2 · PCI vendor 1002"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("gpuPills() = %#v, want %#v", got, want)
	}
	if pills := gpuPills(model.Capabilities{}); pills != nil {
		t.Fatalf("no inventory must show no GPUs, got %#v", pills)
	}
}

func TestGPUPillUnidentifiedNode(t *testing.T) {
	t.Parallel()
	got := gpuPills(model.Capabilities{VAAPIDevices: []vaapi.DeviceInfo{{Path: "/dev/dri/renderD128"}}})
	want := []gpuPill{{Text: "renderD128", Tip: "/dev/dri/renderD128\nVA-API: no profiles reported"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("gpuPills() = %#v, want %#v", got, want)
	}
}
