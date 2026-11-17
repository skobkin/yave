package ffprobe

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// writeFakeFFprobe installs a shell script that records its arguments
// and answers the origin probe (format=start_time) with formatStart;
// every other invocation prints the given frame document.
//
// Tests using this helper must not run in parallel: while WriteFile
// holds the script's write fd open, a fork in any concurrent test
// inherits it, and the child holds it past exec — the kernel then
// rejects the sibling's exec of its own fresh script with
// "text file busy" (golang/go#22315). The spawns are milliseconds, so
// serializing them costs nothing measurable; rename-based writes do
// not help, because the inherited fd tracks the inode, not the path.
func writeFakeFFprobe(t *testing.T, dir, document, formatStart string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("test helper is a POSIX shell script")
	}
	argsFile := filepath.Join(dir, "ffprobe.args")
	script := "#!/bin/sh\n" +
		`printf '%s\n' "$@" >> ` + strconv.Quote(argsFile) + "\n" +
		"case \"$*\" in\n" +
		"*format=start_time*)\n" +
		`  printf '{"format":{"start_time":"%s"}}\n' ` + strconv.Quote(formatStart) + "\n" +
		"  ;;\n" +
		"*)\n" +
		"  cat <<'EOF'\n" + document + "\nEOF\n" +
		"  ;;\n" +
		"esac\n"
	path := filepath.Join(dir, "ffprobe")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil { //nolint:gosec // G306: test helper script must be executable
		t.Fatal(err)
	}
	return path
}

func TestFrameTimesNonZeroStart(t *testing.T) {
	dir := t.TempDir()
	// The container starts at 1.043 s and so do the frames: normalized
	// values must begin at 0 so they sit on the same timeline as
	// ffmpeg's input-side -ss. The window [0, 3s] contains the frames.
	document := `{
	  "frames": [
	    {"best_effort_timestamp_time": "1.043000"},
	    {"best_effort_timestamp_time": "1.076333"},
	    {"best_effort_timestamp_time": "1.109667"}
	  ]
	}`
	fake := writeFakeFFprobe(t, dir, document, "1.043000")
	times, start, err := FrameTimes(context.Background(), fake, "/tmp/in.mkv", 0, 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if !durationClose(start, 1043*time.Millisecond, time.Microsecond) {
		t.Errorf("start = %s, want 1.043s", start)
	}
	// Values come from float seconds strings; compare within 1 µs.
	want := []time.Duration{0, 33333 * time.Microsecond, 66667 * time.Microsecond}
	if len(times) != len(want) {
		t.Fatalf("times = %v, want %v", times, want)
	}
	for i := range want {
		if !durationClose(times[i], want[i], time.Microsecond) {
			t.Errorf("times[%d] = %s, want %s", i, times[i], want[i])
		}
	}
	args, err := os.ReadFile(filepath.Join(dir, "ffprobe.args")) //nolint:gosec // G304: path comes from t.TempDir
	if err != nil {
		t.Fatal(err)
	}
	fields := strings.Fields(string(args))
	for _, want := range []string{"-select_streams", "v:0", "-show_frames", "-read_intervals", "1.043%5.043"} {
		if !containsField(fields, want) {
			t.Errorf("args missing %q: %v", want, fields)
		}
	}
}

// Regression: frame timestamps were rejected when negative, but with a
// negative container origin the absolute timestamps before zero are
// real positions: raw -0.52s with origin -1s normalizes to 0.48s on the
// UI timeline. Rejecting them before normalization made frame stepping
// miss every early frame (a negative-origin MPEG-TS reported its first
// step target as 1s instead of 0.48s).
func TestFrameTimesNegativeOriginNormalizesNegativeRawTimes(t *testing.T) {
	dir := t.TempDir()
	document := `{
	  "frames": [
	    {"best_effort_timestamp_time": "-0.520000"},
	    {"best_effort_timestamp_time": "-0.486667"},
	    {"best_effort_timestamp_time": "0.480000"}
	  ]
	}`
	fake := writeFakeFFprobe(t, dir, document, "-1.000000")
	times, origin, err := FrameTimes(context.Background(), fake, "/tmp/in.mkv", 0, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if !durationClose(origin, -time.Second, time.Microsecond) {
		t.Errorf("origin = %s, want -1s", origin)
	}
	want := []time.Duration{
		480 * time.Millisecond,
		513333 * time.Microsecond,
		1480 * time.Millisecond,
	}
	if len(times) != len(want) {
		t.Fatalf("times = %v, want %v", times, want)
	}
	for i := range want {
		if !durationClose(times[i], want[i], time.Microsecond) {
			t.Errorf("times[%d] = %s, want %s", i, times[i], want[i])
		}
	}
}

// -read_intervals expects absolute container timestamps; the requested
// window is input-relative and must be shifted by the container's
// format start_time in both directions (positive and negative origin).
func TestFrameTimesTranslatesProbeWindow(t *testing.T) {
	cases := []struct {
		name       string
		origin     string
		from, to   time.Duration
		wantWindow string
	}{
		{"positive origin", "10.000000", 2 * time.Second, 5 * time.Second, "12.000%16.000"},
		{"negative origin", "-1.500000", 2 * time.Second, 5 * time.Second, "0.500%4.500"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			document := `{"frames": []}`
			fake := writeFakeFFprobe(t, dir, document, tc.origin)
			if _, _, err := FrameTimes(context.Background(), fake, "/tmp/in.mkv", tc.from, tc.to); err != nil {
				t.Fatal(err)
			}
			args, err := os.ReadFile(filepath.Join(dir, "ffprobe.args")) //nolint:gosec // G304: path comes from t.TempDir
			if err != nil {
				t.Fatal(err)
			}
			if !containsField(strings.Fields(string(args)), tc.wantWindow) {
				t.Errorf("window %q not translated by origin %s: %s", tc.wantWindow, tc.origin, args)
			}
		})
	}
}

func durationClose(got, want, tolerance time.Duration) bool {
	diff := got - want
	if diff < 0 {
		diff = -diff
	}
	return diff <= tolerance
}

func TestFrameTimesReorderAndDedup(t *testing.T) {
	dir := t.TempDir()
	// best_effort values may arrive out of presentation order; "N/A"
	// frames are skipped; duplicates collapse.
	document := `{
	  "frames": [
	    {"best_effort_timestamp_time": "0.200000"},
	    {"best_effort_timestamp_time": "N/A"},
	    {"best_effort_timestamp_time": "0.100000"},
	    {"best_effort_timestamp_time": "0.100000"},
	    {"best_effort_timestamp_time": "N/A"},
	    {"best_effort_timestamp_time": "0.300000"}
	  ]
	}`
	fake := writeFakeFFprobe(t, dir, document, "0.000000")
	times, _, err := FrameTimes(context.Background(), fake, "/tmp/in.mkv", 0, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	want := []time.Duration{100 * time.Millisecond, 200 * time.Millisecond, 300 * time.Millisecond}
	if len(times) != len(want) {
		t.Fatalf("times = %v, want %v", times, want)
	}
	for i := range want {
		if times[i] != want[i] {
			t.Errorf("times[%d] = %s, want %s", i, times[i], want[i])
		}
	}
}

// Regression: a video stream delayed relative to the container (audio
// at zero, video starting one second in) used to be rebased against
// the video stream's own start_time, indexing its first frame at zero.
// ffmpeg's input-side -ss seeks against the container start, so the
// indexed position and the extracted frame disagreed by one second.
// Normalization now uses the container origin only.
func TestFrameTimesVideoOffsetFromContainer(t *testing.T) {
	dir := t.TempDir()
	document := `{
  "frames": [
    {"best_effort_timestamp_time": "1.000000"},
    {"best_effort_timestamp_time": "1.033333"}
  ]
}`
	fake := writeFakeFFprobe(t, dir, document, "0.000000")
	times, _, err := FrameTimes(context.Background(), fake, "/tmp/in.mkv", 0, 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	want := []time.Duration{time.Second, time.Second + 33333*time.Microsecond}
	if len(times) != len(want) {
		t.Fatalf("times = %v, want %v", times, want)
	}
	for i := range want {
		if !durationClose(times[i], want[i], time.Microsecond) {
			t.Errorf("times[%d] = %s, want %s", i, times[i], want[i])
		}
	}
}

func TestFrameTimesWindowFiltering(t *testing.T) {
	dir := t.TempDir()
	// -read_intervals seeks to a keyframe before the requested start,
	// so the raw document can contain earlier frames; they must be
	// filtered to [from, to].
	document := `{
	  "frames": [
	    {"best_effort_timestamp_time": "0.000000"},
	    {"best_effort_timestamp_time": "9.000000"},
	    {"best_effort_timestamp_time": "10.500000"},
	    {"best_effort_timestamp_time": "10.750000"},
	    {"best_effort_timestamp_time": "13.000000"}
	  ]
	}`
	fake := writeFakeFFprobe(t, dir, document, "0.000000")
	times, _, err := FrameTimes(context.Background(), fake, "/tmp/in.mkv", 10*time.Second, 12*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	want := []time.Duration{10*time.Second + 500*time.Millisecond, 10*time.Second + 750*time.Millisecond}
	if len(times) != len(want) {
		t.Fatalf("times = %v, want %v", times, want)
	}
	for i := range want {
		if times[i] != want[i] {
			t.Errorf("times[%d] = %s, want %s", i, times[i], want[i])
		}
	}
}

func TestFrameTimesGuards(t *testing.T) {
	dir := t.TempDir()
	fake := writeFakeFFprobe(t, dir, `{"frames": []}`, "0.000000")
	if _, _, err := FrameTimes(context.Background(), fake, "", 0, time.Second); err == nil {
		t.Error("empty input path must be rejected")
	}
	if _, _, err := FrameTimes(context.Background(), fake, "/tmp/in.mkv", time.Second, time.Second); err == nil {
		t.Error("empty window must be rejected")
	}
	if _, _, err := FrameTimes(context.Background(), fake, "/tmp/in.mkv", 2*time.Second, time.Second); err == nil {
		t.Error("inverted window must be rejected")
	}
	if _, _, err := FrameTimes(context.Background(), "", "/tmp/in.mkv", 0, time.Second); err == nil {
		t.Error("missing ffprobe must be reported")
	}
}

// Regression: real ffprobe emits a single "format" object, not a
// "formats" array (decoded as an empty document — origin silently 0 and
// frame windows probed in the wrong domain). Lock the real document
// shape, including keys beyond start_time, as the decoding contract.
func TestInputOriginRealDocumentShape(t *testing.T) {
	dir := t.TempDir()
	document := `{"frames": []}`
	fake := writeFakeFFprobe(t, dir, document, "10.000000")
	times, origin, err := FrameTimes(context.Background(), fake, "/tmp/in.mkv", 0, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if !durationClose(origin, 10*time.Second, time.Microsecond) {
		t.Errorf("origin = %s, want 10s", origin)
	}
	if len(times) != 0 {
		t.Errorf("times = %v, want none (empty frame document)", times)
	}
}

// Regression: -read_intervals stops reading at the window end, so
// frames buffered behind the decoder's B-frame reorder delay were
// never flushed: a 30fps B-frame fixture reported its [0,2s] window
// only through 1.9s (58 frames of 61), and stepping backward from a
// 2s playhead landed on 1.9s instead of the immediately preceding
// frame. The read now extends by frameProbeMargin and filters to the
// requested window. Uses the real toolchain — the buffering is
// decoder behavior no fake executable can reproduce; mpeg4 with -bf 2
// is a native encoder, so no codec gating is needed.
func TestFrameTimesFlushesBufferedFrames(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test (real ffmpeg + ffprobe)")
	}
	ffmpegPath, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not installed")
	}
	ffprobePath, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("ffprobe not installed")
	}

	dir := t.TempDir()
	input := dir + "/input.mp4"
	generate := exec.CommandContext(context.Background(), ffmpegPath, //nolint:gosec // G204: locally discovered FFmpeg on a t.TempDir file
		"-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc2=duration=4:size=160x120:rate=30",
		"-c:v", "mpeg4", "-bf", "2", "-q:v", "3", input)
	if out, err := generate.CombinedOutput(); err != nil {
		t.Fatalf("generating test media: %v: %s", err, out)
	}

	times, _, err := FrameTimes(context.Background(), ffprobePath, input, 0, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	// 30fps over [0,2s] inclusive = 61 frames (0 through 2.0): the
	// buffered tail must be present, not clipped by the read end.
	if len(times) != 61 {
		t.Fatalf("frame count = %d, want 61 (last %v)", len(times), times[len(times)-1])
	}
	if last := times[len(times)-1]; last < 2*time.Second-time.Millisecond {
		t.Errorf("last frame in window = %s, want the 2.0s tail", last)
	}
}

// Regression: the fixed one-second read extension could not cover
// every decoder reorder delay — a 1fps mpeg4 source with -bf 2 holds
// two frames = two seconds in the decoder, and FrameTimes(0, 2s)
// returned only frame 0 while the caller marked the window complete,
// so stepping backward from the 2s playhead jumped to 0s instead of
// 1s. The read now extends until a frame lands beyond the window end,
// so all three frames are reported. Uses the real toolchain — the
// buffering is decoder behavior no fake executable can reproduce;
// mpeg4 is a native encoder, so no codec gating is needed.
func TestFrameTimesExtendsReadPastDecoderBuffering(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test (real ffmpeg + ffprobe)")
	}
	ffmpegPath, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not installed")
	}
	ffprobePath, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("ffprobe not installed")
	}

	dir := t.TempDir()
	input := dir + "/input.mp4"
	generate := exec.CommandContext(context.Background(), ffmpegPath, //nolint:gosec // G204: locally discovered FFmpeg on a t.TempDir file
		"-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc2=duration=4:size=160x120:rate=1",
		"-c:v", "mpeg4", "-bf", "2", "-q:v", "3", input)
	if out, err := generate.CombinedOutput(); err != nil {
		t.Fatalf("generating test media: %v: %s", err, out)
	}

	times, _, err := FrameTimes(context.Background(), ffprobePath, input, 0, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	want := []time.Duration{0, time.Second, 2 * time.Second}
	if len(times) != len(want) {
		t.Fatalf("frame times = %v, want %v", times, want)
	}
	for i := range want {
		if !durationClose(times[i], want[i], time.Millisecond) {
			t.Errorf("times[%d] = %s, want %s", i, times[i], want[i])
		}
	}
}

func containsField(fields []string, want string) bool {
	for _, field := range fields {
		if field == want {
			return true
		}
	}
	return false
}
