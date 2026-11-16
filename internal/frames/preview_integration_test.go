package frames

import (
	"context"
	"image"
	"os/exec"
	"strconv"
	"testing"
	"time"
)

// TestPreviewIntegration is the end-to-end check that the Preview
// command's filtergraph is accepted by a real FFmpeg and that the
// width cap behaves: larger frames are downscaled to the cap with the
// aspect ratio preserved, smaller frames are never upscaled. It exists
// because the scale filter's min() expression must be quoted for
// ffmpeg's filtergraph parser (commas split filters) — a regression
// fake executables cannot catch.
func TestPreviewIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test (real FFmpeg)")
	}
	ffmpegPath, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not installed")
	}

	generate := func(t *testing.T, w, h int) string {
		t.Helper()
		path := t.TempDir() + "/input.mp4"
		//nolint:gosec // G204: integration test drives the locally discovered FFmpeg with t.TempDir paths
		cmd := exec.CommandContext(context.Background(), ffmpegPath, []string{
			"-hide_banner", "-loglevel", "error",
			"-f", "lavfi", "-i", "testsrc2=duration=2:size=" + strconv.Itoa(w) + "x" + strconv.Itoa(h) + ":rate=30",
			"-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p", path,
		}...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("generating %dx%d test media: %v: %s", w, h, err, out)
		}
		return path
	}

	extractor := NewExtractor(ffmpegPath)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	t.Run("downscales to the cap", func(t *testing.T) {
		path := generate(t, 640, 480)
		frame, err := extractor.Preview(ctx, path, time.Second, 320)
		if err != nil {
			t.Fatal(err)
		}
		want := image.Rect(0, 0, 320, 240)
		if frame.Image == nil || frame.Image.Bounds() != want {
			t.Fatalf("preview bounds = %v, want %v", frame.Image.Bounds(), want)
		}
	})

	t.Run("never upscales", func(t *testing.T) {
		path := generate(t, 160, 120)
		frame, err := extractor.Preview(ctx, path, time.Second, 320)
		if err != nil {
			t.Fatal(err)
		}
		want := image.Rect(0, 0, 160, 120)
		if frame.Image == nil || frame.Image.Bounds() != want {
			t.Fatalf("preview bounds = %v, want %v", frame.Image.Bounds(), want)
		}
	})
}
