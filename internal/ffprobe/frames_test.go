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
// and prints the given JSON document.
func writeFakeFFprobe(t *testing.T, dir, name, document string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("test helper is a POSIX shell script")
	}
	argsFile := filepath.Join(dir, name+".args")
	script := "#!/bin/sh\n" +
		`printf '%s\n' "$@" >> ` + strconv.Quote(argsFile) + "\n" +
		"cat <<'EOF'\n" + document + "\nEOF\n"
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil { //nolint:gosec // G306: test helper script must be executable
		t.Fatal(err)
	}
	return path
}

func TestFrameTimesNonZeroStart(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// Frames start at 1.043 s: normalized values must begin at 0 so
	// they sit on the same timeline as ffmpeg's input-side -ss. The
	// window [0, 3s] contains the normalized frames.
	document := `{
	  "frames": [
	    {"best_effort_timestamp_time": "1.043000"},
	    {"best_effort_timestamp_time": "1.076333"},
	    {"best_effort_timestamp_time": "1.109667"}
	  ],
	  "streams": [{"start_time": "1.043000"}]
	}`
	fake := writeFakeFFprobe(t, dir, "ffprobe", document)
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
	args, err := os.ReadFile(filepath.Join(dir, "ffprobe.args"))
	if err != nil {
		t.Fatal(err)
	}
	fields := strings.Fields(string(args))
	for _, want := range []string{"-select_streams", "v:0", "-show_frames", "-read_intervals", "0.000%+3.000"} {
		if !containsField(fields, want) {
			t.Errorf("args missing %q: %v", want, fields)
		}
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
	  ],
	  "streams": [{"start_time": "0.000000"}]
	}`
	fake := writeFakeFFprobe(t, dir, "ffprobe", document)
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
	  ],
	  "streams": [{"start_time": "0.000000"}]
	}`
	fake := writeFakeFFprobe(t, dir, "ffprobe", document)
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
	fake := writeFakeFFprobe(t, dir, "ffprobe", `{"frames": [], "streams": []}`)
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

func containsField(fields []string, want string) bool {
	for _, field := range fields {
		if field == want {
			return true
		}
	}
	return false
}
