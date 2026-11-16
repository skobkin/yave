package ffmpeg

import (
	"context"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"git.skobk.in/skobkin/yave/internal/ffprobe"
	"git.skobk.in/skobkin/yave/internal/model"
)

// TestTrimIntegration is the end-to-end check that an active trim
// produces an output whose duration matches the selected range and
// whose audio and video stay in sync. It uses the real FFmpeg
// toolchain and skips when it is unavailable.
func TestTrimIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test (real FFmpeg)")
	}
	ffmpegPath, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not installed")
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe not installed")
	}

	dir := t.TempDir()
	input := dir + "/input.mp4"
	// 6-second test pattern with a sine audio track: enough room to cut
	// [1s, 4s] out of the middle.
	generate := exec.CommandContext(context.Background(), ffmpegPath, []string{ //nolint:gosec // G204: integration test drives the locally discovered FFmpeg with t.TempDir paths
		"-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc2=duration=6:size=320x240:rate=30",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=6",
		"-c:v", "libx264", "-preset", "ultrafast",
		"-c:a", "aac", "-shortest",
		input,
	}...)
	if out, err := generate.CombinedOutput(); err != nil {
		t.Fatalf("generating test media: %v: %s", err, out)
	}

	profile := model.BuiltinProfiles()[3] // "x265 Software Archive" — software encoder, no hardware requirements
	profile.Video.Preset = "ultrafast"
	// Matroska stores unrebased timestamps and reports per-stream
	// duration as N/A; MP4 durations are exact, so the assertions below
	// compare real stream durations.
	profile.Container = model.ContainerMP4
	output := dir + "/output.mp4"
	plan, err := BuildCommand(BuildRequest{
		FFmpegPath: ffmpegPath,
		InputPath:  input,
		OutputPath: output,
		Profile:    profile,
		Trim:       model.TrimSelection{Segments: []model.TimeRange{{Start: time.Second, End: 4 * time.Second}}},
		Overwrite:  true,
	})
	if err != nil {
		t.Fatal(err)
	}
	run := exec.CommandContext(context.Background(), plan.Executable, plan.Args...) //nolint:gosec // G204: the command under test, generated from test fixtures
	if out, err := run.CombinedOutput(); err != nil {
		t.Fatalf("running trimmed conversion: %v: %s", err, out)
	}

	media, err := ffprobe.Inspect(context.Background(), "ffprobe", output)
	if err != nil {
		t.Fatal(err)
	}
	const tolerance = 700 * time.Millisecond // seek and container rounding slack
	if want := 3 * time.Second; media.Duration < want-tolerance || media.Duration > want+tolerance {
		t.Errorf("output duration = %s, want %s ± %s", media.Duration, want, tolerance)
	}
	if len(media.VideoStreams) != 1 || len(media.AudioStreams) != 1 {
		t.Fatalf("expected one video and one audio stream, got %d/%d",
			len(media.VideoStreams), len(media.AudioStreams))
	}

	// Stream-level durations: video and audio must both cover roughly
	// the selected range and stay near each other (A/V sync sanity).
	videoDur, audioDur := streamDurations(t, "ffprobe", output)
	if videoDur <= 0 || audioDur <= 0 {
		t.Fatalf("missing stream durations: video %s audio %s", videoDur, audioDur)
	}
	if videoDur < 3*time.Second-tolerance || videoDur > 3*time.Second+tolerance {
		t.Errorf("video duration = %s, want ~3s", videoDur)
	}
	skew := videoDur - audioDur
	if skew < 0 {
		skew = -skew
	}
	if skew > tolerance {
		t.Errorf("A/V skew = %s, exceeds %s", skew, tolerance)
	}
}

// streamDurations queries per-stream durations with a minimal ffprobe
// CSV parse (test-only; the model carries no per-stream duration).
func streamDurations(t *testing.T, ffprobePath, output string) (time.Duration, time.Duration) {
	t.Helper()
	cmd := exec.CommandContext(context.Background(), ffprobePath, []string{ //nolint:gosec // G204: locally discovered ffprobe on a t.TempDir file
		"-v", "error",
		"-select_streams", "v:0",
		"-show_entries", "stream=duration",
		"-of", "csv=p=0",
		output,
	}...)
	videoOut, err := cmd.Output()
	if err != nil {
		t.Fatalf("probing video duration: %v", err)
	}
	cmd = exec.CommandContext(context.Background(), ffprobePath, []string{ //nolint:gosec // G204: locally discovered ffprobe on a t.TempDir file
		"-v", "error",
		"-select_streams", "a:0",
		"-show_entries", "stream=duration",
		"-of", "csv=p=0",
		output,
	}...)
	audioOut, err := cmd.Output()
	if err != nil {
		t.Fatalf("probing audio duration: %v", err)
	}
	return parseSecondsField(string(videoOut)), parseSecondsField(string(audioOut))
}

func parseSecondsField(text string) time.Duration {
	line := strings.TrimSpace(strings.SplitN(text, "\n", 2)[0])
	seconds, err := strconv.ParseFloat(line, 64)
	if err != nil {
		return 0
	}
	return time.Duration(seconds * float64(time.Second))
}
