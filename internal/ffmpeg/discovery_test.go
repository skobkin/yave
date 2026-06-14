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
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}
