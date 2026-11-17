package frames

import (
	"context"
	"image"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

// requireEncoders skips the test when the installed FFmpeg cannot run
// it. The -encoders listing is matched by exact field, so a similarly
// named encoder never satisfies a requirement by substring.
func requireEncoders(t *testing.T, ffmpegPath string, want ...string) {
	t.Helper()
	out, err := exec.CommandContext(context.Background(), ffmpegPath, //nolint:gosec // G204: locally discovered FFmpeg, fixed argument
		"-hide_banner", "-encoders").Output()
	if err != nil {
		t.Skipf("listing FFmpeg encoders: %v", err)
	}
	have := map[string]bool{}
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 {
			have[fields[1]] = true
		}
	}
	var missing []string
	for _, name := range want {
		if !have[name] {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		t.Skipf("FFmpeg lacks required encoders: %s", strings.Join(missing, ", "))
	}
}

// TestPreviewIntegration is the end-to-end check that the Preview
// command's filtergraph is accepted by a real FFmpeg and that the
// width cap behaves: larger frames are downscaled to the cap with the
// aspect ratio preserved, smaller frames are never upscaled. It exists
// because the scale filter's min() expression must be quoted for
// ffmpeg's filtergraph parser (commas split filters) — a regression
// fake executables cannot catch. It skips when FFmpeg is unavailable
// or lacks libx264 (the fixture encoder; a property of the host build
// that executable discovery alone does not establish).
func TestPreviewIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test (real FFmpeg)")
	}
	ffmpegPath, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not installed")
	}
	requireEncoders(t, ffmpegPath, "libx264")

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

	t.Run("normalizes anamorphic sources", func(t *testing.T) {
		// 480x360 tagged SAR 2:1 displays as 960x360. Go's JPEG decoder
		// and Fyne only see pixel dimensions, so the extraction chain
		// must convert to square pixels before its cap scaling — the
		// preview is 320x120 (the display aspect ratio), not 320x240
		// (the raw pixel geometry squeezed into the cap).
		path := t.TempDir() + "/input.mp4"
		//nolint:gosec // G204: integration test drives the locally discovered FFmpeg with t.TempDir paths
		cmd := exec.CommandContext(context.Background(), ffmpegPath, []string{
			"-hide_banner", "-loglevel", "error",
			"-f", "lavfi", "-i", "testsrc2=duration=2:size=480x360:rate=30",
			"-vf", "setsar=2",
			"-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p", path,
		}...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("generating anamorphic test media: %v: %s", err, out)
		}
		frame, err := extractor.Preview(ctx, path, time.Second, 320)
		if err != nil {
			t.Fatal(err)
		}
		want := image.Rect(0, 0, 320, 120)
		if frame.Image == nil || frame.Image.Bounds() != want {
			t.Fatalf("anamorphic preview bounds = %v, want %v", frame.Image.Bounds(), want)
		}
	})
}
