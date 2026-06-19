package ffmpeg

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"git.skobk.in/skobkin/yave/internal/model"
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
