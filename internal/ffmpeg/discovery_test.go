package ffmpeg

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"git.skobk.in/skobkin/yave/internal/encoder"
	"git.skobk.in/skobkin/yave/internal/model"
	"git.skobk.in/skobkin/yave/internal/vaapi"
)

func TestParseEncodersFiltersAndPrefers(t *testing.T) {
	output := `Encoders:
 V..... hevc_vaapi           H.265/HEVC (VAAPI)
 V..... libx264              libx264 H.264
 V..... unrelated_encoder    Not curated
 A..... opus                 Opus
 A..... libopus              libopus Opus
 A..... mp3                  MP3
 A..... libmp3lame           libmp3lame MP3
 A..... aac                  AAC
`

	got := ParseEncoders(output)
	for _, name := range []string{"hevc_vaapi", "libx264", "libopus", "libmp3lame", "aac", "copy"} {
		if _, ok := got[name]; !ok {
			t.Errorf("expected encoder %q", name)
		}
	}
	for _, name := range []string{"unrelated_encoder", "opus", "mp3"} {
		if _, ok := got[name]; ok {
			t.Errorf("did not expect encoder %q", name)
		}
	}
	if got["copy"].Kind != KindPassthrough {
		t.Errorf("copy kind = %q", got["copy"].Kind)
	}
}

// TestParseEncodersIncludesNewFamilies confirms that the curated
// NVENC, QSV, and libaom-av1 encoders registered with the encoder
// registry round-trip through ParseEncoders. Adding a new encoder
// family should require no change to discovery.go: just register the
// encoder in defaults.go.
func TestParseEncodersIncludesNewFamilies(t *testing.T) {
	output := `Encoders:
 V..... h264_nvenc           NVIDIA NVENC H.264
 V..... hevc_nvenc           NVIDIA NVENC hevc
 V..... av1_nvenc            NVIDIA NVENC av1
 V..... h264_qsv             H.264 / AVC / MPEG-4 AVC / MPEG-4 part 10 (Intel Quick Sync Video acceleration)
 V..... hevc_qsv             HEVC (Intel Quick Sync Video acceleration)
 V..... av1_qsv              AV1 (Intel Quick Sync Video acceleration)
 V..... vp9_qsv              VP9 (Intel Quick Sync Video acceleration)
 V..... libaom-av1           libaom AV1
 V..... unrelated_encoder    Not curated
`

	got := ParseEncoders(output)
	for _, name := range []string{
		"h264_nvenc", "hevc_nvenc", "av1_nvenc",
		"h264_qsv", "hevc_qsv", "av1_qsv", "vp9_qsv",
		"libaom-av1",
	} {
		if _, ok := got[name]; !ok {
			t.Errorf("expected encoder %q in ParseEncoders result", name)
		}
	}
	if _, ok := got["unrelated_encoder"]; ok {
		t.Errorf("unrelated_encoder should be filtered out")
	}

	// Cross-check that the registry-supplied metadata matches what
	// was registered: h264_nvenc should be hardware / HWNVENC, etc.
	if got["h264_nvenc"].Kind != KindHardware {
		t.Errorf("h264_nvenc Kind = %q, want %q", got["h264_nvenc"].Kind, KindHardware)
	}
	if got["libaom-av1"].Kind != KindSoftware {
		t.Errorf("libaom-av1 Kind = %q, want %q", got["libaom-av1"].Kind, KindSoftware)
	}
}

func TestParseHWAccels(t *testing.T) {
	got := ParseHWAccels("Hardware acceleration methods:\nvaapi\ncuda\nvaapi\n\n")
	want := []string{"cuda", "vaapi"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("ParseHWAccels() = %v, want %v", got, want)
	}
}

func TestResolveExecutable(t *testing.T) {
	path, err := ResolveExecutable(os.Args[0], "ffmpeg")
	if err != nil {
		t.Fatal(err)
	}
	if path == "" {
		t.Fatal("resolved path is empty")
	}

	_, err = ResolveExecutable(filepath.Join(t.TempDir(), "missing"), "ffmpeg")
	if err == nil || !strings.Contains(err.Error(), "ffmpeg executable") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestDiscover(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test helper is a POSIX shell script")
	}
	dir := t.TempDir()
	ffmpegPath := writeExecutable(t, dir, "ffmpeg", `#!/bin/sh
case "$2" in
  -version) printf 'ffmpeg version test-1\n' ;;
  -encoders) printf 'Encoders:\n V..... libx265 test\n A..... aac test\n' ;;
  -hwaccels) printf 'Hardware acceleration methods:\nvaapi\n' ;;
  *) exit 2 ;;
esac
`)
	ffprobePath := writeExecutable(t, dir, "ffprobe", `#!/bin/sh
printf 'ffprobe version test-2\n'
`)

	got, err := Discover(context.Background(), model.FFmpegConfig{
		FFmpegPath:  ffmpegPath,
		FFprobePath: ffprobePath,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.FFmpegVersion != "ffmpeg version test-1" || got.FFprobeVersion != "ffprobe version test-2" {
		t.Fatalf("unexpected versions: %#v", got)
	}
	if _, ok := got.Encoders["libx265"]; !ok {
		t.Fatal("libx265 not discovered")
	}
	if len(got.HWAccels) != 1 || got.HWAccels[0] != "vaapi" {
		t.Fatalf("unexpected hwaccels: %v", got.HWAccels)
	}
}

func writeExecutable(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil { //nolint:gosec // G306: test helper script must be executable
		t.Fatal(err)
	}
	return path
}

func TestNarrowVAAPIToDevices(t *testing.T) {
	// Renoir-like iGPU from issue #24: H.264 and HEVC Main/Main10
	// encode, VP9 decode only.
	renoir := vaapi.DeviceInfo{Path: "/dev/dri/renderD129", EntryPoints: map[vaapi.Profile][]vaapi.Entrypoint{
		vaapi.ProfileH264ConstrainedBaseline: {vaapi.EntrypointVLD, vaapi.EntrypointEncSlice},
		vaapi.ProfileH264Main:                {vaapi.EntrypointVLD, vaapi.EntrypointEncSlice},
		vaapi.ProfileH264High:                {vaapi.EntrypointVLD, vaapi.EntrypointEncSlice},
		vaapi.ProfileHEVCMain:                {vaapi.EntrypointVLD, vaapi.EntrypointEncSlice},
		vaapi.ProfileHEVCMain10:              {vaapi.EntrypointVLD, vaapi.EntrypointEncSlice},
		vaapi.ProfileVP9Profile0:             {vaapi.EntrypointVLD},
		vaapi.ProfileVP9Profile2:             {vaapi.EntrypointVLD},
	}}
	hevcMainOnly := vaapi.DeviceInfo{Path: "/dev/dri/renderD130", EntryPoints: map[vaapi.Profile][]vaapi.Entrypoint{
		vaapi.ProfileHEVCMain: {vaapi.EntrypointVLD, vaapi.EntrypointEncSlice},
	}}
	nvdec := vaapi.DeviceInfo{Path: "/dev/dri/renderD128", EntryPoints: map[vaapi.Profile][]vaapi.Entrypoint{
		vaapi.ProfileHEVCMain:   {vaapi.EntrypointVLD},
		vaapi.ProfileHEVCMain10: {vaapi.EntrypointVLD},
	}}
	broken := vaapi.DeviceInfo{Path: "/dev/dri/renderD131", Err: "libva unavailable"}

	vaapiEncoders := func() map[string]model.Encoder {
		out := map[string]model.Encoder{"libx265": {Name: "libx265", Profiles: []string{"main", "main10"}}}
		for _, name := range []string{"h264_vaapi", "hevc_vaapi", "vp9_vaapi"} {
			e, ok := encoder.DefaultRegistry().Model(name)
			if !ok {
				t.Fatalf("%s missing from the registry", name)
			}
			out[name] = e
		}
		return out
	}

	tests := []struct {
		name         string
		devices      []vaapi.DeviceInfo
		wantMissing  []string
		wantProfiles map[string][]string
	}{
		{
			name:         "renoir drops vp9, keeps every hevc profile",
			devices:      []vaapi.DeviceInfo{renoir},
			wantMissing:  []string{"vp9_vaapi"},
			wantProfiles: map[string][]string{"hevc_vaapi": {"main", "main10", "rext"}, "libx265": {"main", "main10"}},
		},
		{
			name:         "hevc main only drops main10 and the rest (rext is served by Main at 8-bit)",
			devices:      []vaapi.DeviceInfo{hevcMainOnly},
			wantMissing:  []string{"h264_vaapi", "vp9_vaapi"},
			wantProfiles: map[string][]string{"hevc_vaapi": {"main", "rext"}},
		},
		{
			name:         "decode-only plus encode-capable GPU unions",
			devices:      []vaapi.DeviceInfo{nvdec, renoir},
			wantMissing:  []string{"vp9_vaapi"},
			wantProfiles: map[string][]string{"hevc_vaapi": {"main", "main10", "rext"}},
		},
		{
			name:        "decode-only GPU hides every VAAPI encoder",
			devices:     []vaapi.DeviceInfo{nvdec},
			wantMissing: []string{"h264_vaapi", "hevc_vaapi", "vp9_vaapi"},
		},
		{
			name:         "all probes failed fails open",
			devices:      []vaapi.DeviceInfo{broken},
			wantProfiles: map[string][]string{"hevc_vaapi": nil, "vp9_vaapi": nil},
		},
		{
			name:         "no devices fails open",
			devices:      nil,
			wantProfiles: map[string][]string{"hevc_vaapi": nil, "vp9_vaapi": nil},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			encoders := vaapiEncoders()
			before := vaapiEncoders()
			narrowVAAPIToDevices(encoders, tt.devices)
			for _, name := range tt.wantMissing {
				if _, ok := encoders[name]; ok {
					t.Errorf("%s still present", name)
				}
			}
			if _, ok := encoders["libx265"]; !ok {
				t.Error("non-VAAPI encoder libx265 must never be removed")
			}
			for name, want := range tt.wantProfiles {
				got, ok := encoders[name]
				if !ok {
					t.Errorf("%s missing", name)
					continue
				}
				if want == nil {
					want = before[name].Profiles // untouched
				}
				if !slices.Equal(got.Profiles, want) {
					t.Errorf("%s profiles = %v, want %v", name, got.Profiles, want)
				}
			}
		})
	}
}
