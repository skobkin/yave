package ffprobe

import (
	"context"
	"os"
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
	t.Parallel()
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
	for _, want := range []string{"-select_streams", "v:0", "-show_frames", "-read_intervals", "1.043%4.043"} {
		if !containsField(fields, want) {
			t.Errorf("args missing %q: %v", want, fields)
		}
	}
}

// -read_intervals expects absolute container timestamps; the requested
// window is input-relative and must be shifted by the container's
// format start_time in both directions (positive and negative origin).
func TestFrameTimesTranslatesProbeWindow(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		origin     string
		from, to   time.Duration
		wantWindow string
	}{
		{"positive origin", "10.000000", 2 * time.Second, 5 * time.Second, "12.000%15.000"},
		{"negative origin", "-1.500000", 2 * time.Second, 5 * time.Second, "0.500%3.500"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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

func containsField(fields []string, want string) bool {
	for _, field := range fields {
		if field == want {
			return true
		}
	}
	return false
}
