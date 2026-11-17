package ffmpeg

import (
	"context"
	"os"
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
// toolchain and skips when it is unavailable or lacks the required
// encoders (codec availability is a property of the host build, and
// executable discovery alone does not establish it).
//
// It runs against both containers: MP4 muxes an edit list that hides
// copied-audio preroll, while Matroska shifts the whole timeline — the
// container where the preroll regression was visible.
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
	// libx264 generates the fixture, aac its audio, libx265 is the
	// profile under test ("x265 Software Archive").
	requireEncoders(t, ffmpegPath, "libx264", "aac", "libx265")

	dir := t.TempDir()
	input := dir + "/input.mp4"
	writeTrimFixture(t, ffmpegPath, input, dir+"/subs.srt")

	// Matroska stores unrebased timestamps and reports per-stream
	// duration as N/A; MP4 durations are exact, so the MP4 variant can
	// compare real stream durations.
	cases := []struct {
		name      string
		container string
		ext       string
		subCodec  string
	}{
		{"mp4", model.ContainerMP4, ".mp4", "mov_text"},
		{"mkv", model.ContainerMKV, ".mkv", "subrip"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			profile := model.BuiltinProfiles()[3] // "x265 Software Archive" — software encoder, no hardware requirements
			profile.Video.Preset = "ultrafast"
			profile.Container = tc.container
			profile.Subtitles.Mode = model.SubtitleCopy
			output := dir + "/output-" + tc.name + tc.ext
			plan, err := BuildCommand(BuildRequest{
				FFmpegPath: ffmpegPath,
				InputPath:  input,
				OutputPath: output,
				Profile:    profile,
				Media: model.MediaInfo{
					VideoStreams:    []model.VideoStream{{Width: 320, Height: 240}},
					SubtitleStreams: []model.SubtitleStream{{Index: 2, Codec: tc.subCodec}},
				},
				Trim:      model.TrimSelection{Segments: []model.TimeRange{{Start: time.Second, End: 4 * time.Second}}},
				Overwrite: true,
			})
			if err != nil {
				t.Fatal(err)
			}
			run := exec.CommandContext(context.Background(), plan.Executable, plan.Args...) //nolint:gosec // G204: the command under test, generated from test fixtures
			if out, err := run.CombinedOutput(); err != nil {
				t.Fatalf("running trimmed conversion: %v: %s", err, out)
			}
			if len(plan.Warnings) != 1 || !strings.Contains(plan.Warnings[0], "subtitle streams are skipped") {
				t.Errorf("plan warnings = %#v, want the trim subtitle-skip warning", plan.Warnings)
			}

			media, err := ffprobe.Inspect(context.Background(), "ffprobe", output)
			if err != nil {
				t.Fatal(err)
			}
			// The copied subtitle event spans the whole input and cannot
			// be cut: the trimmed output must have skipped the stream
			// instead of letting the event set the container duration.
			if len(media.SubtitleStreams) != 0 {
				t.Errorf("trimmed output contains subtitle streams: %+v", media.SubtitleStreams)
			}
			const tolerance = 500 * time.Millisecond // seek and container rounding slack
			if want := 3 * time.Second; media.Duration < want-tolerance || media.Duration > want+tolerance {
				t.Errorf("output duration = %s, want %s ± %s", media.Duration, want, tolerance)
			}
			if len(media.VideoStreams) != 1 || len(media.AudioStreams) != 1 {
				t.Fatalf("expected one video and one audio stream, got %d/%d",
					len(media.VideoStreams), len(media.AudioStreams))
			}
			if tc.container != model.ContainerMP4 {
				return // Matroska reports per-stream duration as N/A
			}

			// Stream-level durations: video and audio must both cover
			// roughly the selected range and stay near each other (A/V
			// sync sanity).
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
		})
	}
}

// writeTrimFixture generates the shared integration fixture: a
// 6-second testsrc2 pattern with a continuous 440 Hz sine audio track
// and a subtitle event spanning the whole input. The 3s GOP (-g 90 at
// 30fps) forces the demuxer to seek to the keyframe at 0 for a mid-file
// start, so copied cuts snap to keyframes and copied audio carries
// preroll — the behaviors the trim and join tests exercise. The sine is
// continuous across the whole duration, so every join boundary sits in
// audible signal: a gap or dropout there is measurable PCM silence.
func writeTrimFixture(t *testing.T, ffmpegPath, input, subs string) {
	t.Helper()
	// A subtitle event spanning the whole input: copied subtitle events
	// cannot be cut at trim boundaries, so a trimmed conversion must
	// skip the stream rather than let the event set the output
	// duration.
	if err := os.WriteFile(subs, []byte("1\n00:00:00,000 --> 00:00:06,000\nSpanning event\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	generate := exec.CommandContext(context.Background(), ffmpegPath, []string{ //nolint:gosec // G204: integration test drives the locally discovered FFmpeg with t.TempDir paths
		"-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc2=duration=6:size=320x240:rate=30",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=6",
		"-i", subs,
		"-map", "0:v:0", "-map", "1:a:0", "-map", "2:0",
		"-c:v", "libx264", "-preset", "ultrafast", "-g", "90",
		"-c:a", "aac", "-c:s", "mov_text", "-shortest",
		input,
	}...)
	if out, err := generate.CombinedOutput(); err != nil {
		t.Fatalf("generating test media: %v: %s", err, out)
	}
}

// requireEncoders skips the test when the installed FFmpeg cannot run
// it. The -encoders listing is matched by exact field, so libx264rgb
// never satisfies a libx264 requirement by substring.
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
