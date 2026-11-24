package analysis

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestSceneParser(t *testing.T) {
	t.Parallel()
	s := time.Second
	tests := []struct {
		name  string
		lines []string
		want  []Scene
	}{
		{
			name: "frame metadata pairs",
			lines: []string{
				"frame:0    pts:3000    pts_time:3", "lavfi.scene_score=0.400000",
				"frame:1    pts:6500    pts_time:6.5", "lavfi.scene_score=0.912345",
			},
			want: []Scene{{At: 3 * s, Score: 0.4}, {At: 6500 * time.Millisecond, Score: 0.912345}},
		},
		{
			name:  "a score without a preceding time is dropped",
			lines: []string{"lavfi.scene_score=0.5", "frame:0 pts:1 pts_time:2", "lavfi.scene_score=0.6"},
			want:  []Scene{{At: 2 * s, Score: 0.6}},
		},
		{
			name:  "a time without a score is not carried to the next pair",
			lines: []string{"frame:0 pts:1 pts_time:2", "frame:1 pts:2 pts_time:4", "lavfi.scene_score=0.7"},
			want:  []Scene{{At: 4 * s, Score: 0.7}},
		},
		{
			name:  "a malformed time invalidates its score",
			lines: []string{"frame:0 pts:1 pts_time:abc", "lavfi.scene_score=0.7"},
			want:  nil,
		},
		{
			name:  "a negative time is rejected",
			lines: []string{"frame:0 pts:1 pts_time:-1", "lavfi.scene_score=0.7"},
			want:  nil,
		},
		{
			name:  "a malformed score is skipped",
			lines: []string{"frame:0 pts:1 pts_time:2", "lavfi.scene_score=nope"},
			want:  nil,
		},
		{
			name:  "unrelated lines are ignored",
			lines: []string{"", "random noise", "frame:0 pts:1 pts_time:2", "lavfi.something_else=1", "lavfi.scene_score=0.3"},
			want:  []Scene{{At: 2 * s, Score: 0.3}},
		},
		{
			name: "frames below the floor are not recorded",
			lines: []string{
				"frame:0 pts:0 pts_time:0", "lavfi.scene_score=0.000000",
				"frame:1 pts:1 pts_time:0.1", "lavfi.scene_score=0.099999",
				"frame:2 pts:2 pts_time:0.2", "lavfi.scene_score=0.100000",
				"frame:3 pts:3 pts_time:0.3", "lavfi.scene_score=0.850000",
			},
			want: []Scene{{At: 200 * time.Millisecond, Score: 0.1}, {At: 300 * time.Millisecond, Score: 0.85}},
		},
		{name: "no output", lines: nil, want: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			p := sceneParser{floor: 0.1}
			for _, line := range tt.lines {
				p.line(line)
			}
			if !reflect.DeepEqual(p.scenes, tt.want) {
				t.Errorf("scenes = %v, want %v", p.scenes, tt.want)
			}
		})
	}
}

func TestParseProgressLine(t *testing.T) {
	t.Parallel()
	tests := []struct {
		line   string
		want   time.Duration
		wantOK bool
	}{
		{"out_time_us=6100000", 6100 * time.Millisecond, true},
		{"out_time_us=0", 0, true},
		{"out_time_us=N/A", 0, false},
		{"out_time_us=-5", 0, false},
		{"out_time_ms=6100000", 0, false}, // only the µs field is read
		{"progress=continue", 0, false},
		{"garbage", 0, false},
	}
	for _, tt := range tests {
		got, ok := parseProgressLine(tt.line)
		if ok != tt.wantOK || got != tt.want {
			t.Errorf("parseProgressLine(%q) = (%s, %v), want (%s, %v)", tt.line, got, ok, tt.want, tt.wantOK)
		}
	}
	for _, noise := range []string{"frame=2", "fps=0.00", "stream_0_0_q=-0.0", "speed=1.17e+03x", "progress=end", "bitrate=N/A"} {
		if !isProgressNoise(noise) {
			t.Errorf("%q must count as progress noise", noise)
		}
	}
	for _, real := range []string{"Error opening input file x.mkv", "Invalid data found when processing input", "a=b"} {
		if isProgressNoise(real) {
			t.Errorf("%q is a real diagnostic, not progress noise", real)
		}
	}
}

func TestSceneArgs(t *testing.T) {
	t.Parallel()
	got := SceneArgs(SceneOptions{Input: "/media/in put.mkv", Floor: 0.15})
	want := []string{
		"-hide_banner", "-nostdin", "-loglevel", "error", "-nostats",
		"-progress", "pipe:2",
		"-i", "/media/in put.mkv",
		"-map", "0:v:0", "-an", "-sn", "-dn",
		"-vf", "scale=160:-2,select='gte(scene,0)',metadata=mode=print:key=lavfi.scene_score:file=-",
		"-f", "null", "-",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("args =\n%q\nwant\n%q", got, want)
	}
	// The floor is applied while parsing, not in the filter: selecting
	// cuts in FFmpeg would stall its progress clock between them.
	if a, b := SceneArgs(SceneOptions{Input: "x", Floor: 0.15}), SceneArgs(SceneOptions{Input: "x", Floor: 0.5}); !reflect.DeepEqual(a, b) {
		t.Error("the floor must not change the FFmpeg command")
	}
	for _, floor := range []float64{0, -1, 1, 2} {
		if got := (SceneOptions{Floor: floor}).floor(); got != DefaultSceneFloor {
			t.Errorf("floor %v = %v, want the default", floor, got)
		}
	}
}

func TestFilterScenes(t *testing.T) {
	t.Parallel()
	scenes := []Scene{{At: 1, Score: 0.1}, {At: 2, Score: 0.3}, {At: 3, Score: 0.5}, {At: 4, Score: 0.9}}
	tests := []struct {
		threshold float64
		want      int
	}{{0.1, 4}, {0.3, 3}, {0.31, 2}, {0.9, 1}, {0.95, 0}}
	for _, tt := range tests {
		if got := FilterScenes(scenes, tt.threshold); len(got) != tt.want {
			t.Errorf("threshold %v keeps %d, want %d", tt.threshold, len(got), tt.want)
		}
	}
	if got := FilterScenes(nil, 0.3); got == nil || len(got) != 0 {
		t.Errorf("filtering nothing = %v, want an empty non-nil slice", got)
	}
}

// writeFakeFFmpeg writes a shell script that records its arguments, then
// runs body.
func writeFakeFFmpeg(t *testing.T, body string) (path, argsFile string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("test helper is a POSIX shell script")
	}
	dir := t.TempDir()
	argsFile = filepath.Join(dir, "args")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > '" + argsFile + "'\n" + body + "\n"
	path = filepath.Join(dir, "ffmpeg")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil { //nolint:gosec // G306: test helper script must be executable
		t.Fatal(err)
	}
	return path, argsFile
}

func TestDetectScenesReadsResultsAndProgress(t *testing.T) {
	path, argsFile := writeFakeFFmpeg(t, `
printf 'out_time_us=1000000\nout_time_us=N/A\nfps=0.00\nout_time_us=2500000\nprogress=end\n' >&2
printf 'frame:0 pts:0 pts_time:0\nlavfi.scene_score=0.000000\nframe:1    pts:3000    pts_time:3\nlavfi.scene_score=0.400000\nframe:2 pts:4000 pts_time:4\nlavfi.scene_score=0.100000\nframe:3    pts:6000    pts_time:6\nlavfi.scene_score=0.600000\n'
`)
	var seen []time.Duration
	got, err := DetectScenes(context.Background(),
		SceneOptions{FFmpegPath: path, Input: "movie.mkv", Floor: 0.2, Duration: 10 * time.Second},
		func(done time.Duration) { seen = append(seen, done) })
	if err != nil {
		t.Fatal(err)
	}
	want := []Scene{{At: 3 * time.Second, Score: 0.4}, {At: 6 * time.Second, Score: 0.6}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("scenes = %v, want %v", got, want)
	}
	if wantProgress := []time.Duration{time.Second, 2500 * time.Millisecond}; !reflect.DeepEqual(seen, wantProgress) {
		t.Errorf("progress = %v, want %v", seen, wantProgress)
	}
	recorded, err := os.ReadFile(argsFile) //nolint:gosec // G304: a file the test just had its fake FFmpeg write in t.TempDir
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(recorded), "gte(scene,0)") || !strings.Contains(string(recorded), "movie.mkv") {
		t.Errorf("FFmpeg ran with %q", recorded)
	}
}

func TestDetectScenesWithoutProgressCallbackOrScenes(t *testing.T) {
	path, _ := writeFakeFFmpeg(t, "printf 'out_time_us=100\\n' >&2")
	got, err := DetectScenes(context.Background(), SceneOptions{FFmpegPath: path, Input: "x.mkv"}, nil)
	if err != nil || len(got) != 0 {
		t.Fatalf("got %v, %v; want no scenes and no error", got, err)
	}
}

func TestDetectScenesFailureCarriesDiagnosticsButNotProgress(t *testing.T) {
	path, _ := writeFakeFFmpeg(t, `
printf 'out_time_us=1000\nspeed=2x\nprogress=continue\nx.mkv: Invalid data found when processing input\n' >&2
exit 1
`)
	_, err := DetectScenes(context.Background(), SceneOptions{FFmpegPath: path, Input: "x.mkv"}, nil)
	if err == nil {
		t.Fatal("a failing FFmpeg must be an error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "Invalid data found when processing input") {
		t.Errorf("error %q lacks FFmpeg's diagnostic", msg)
	}
	if strings.Contains(msg, "out_time_us") || strings.Contains(msg, "speed=") {
		t.Errorf("error %q is polluted with progress records", msg)
	}
}

func TestDetectScenesCancellationStopsTheProcess(t *testing.T) {
	path, _ := writeFakeFFmpeg(t, "sleep 30")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := DetectScenes(ctx, SceneOptions{FFmpegPath: path, Input: "x.mkv"}, nil)
		done <- err
	}()
	time.Sleep(150 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v, want a cancellation", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("cancellation did not stop the detection")
	}
}

func TestDetectScenesRejectsBadSetups(t *testing.T) {
	t.Parallel()
	if _, err := DetectScenes(context.Background(), SceneOptions{FFmpegPath: "ffmpeg"}, nil); err == nil {
		t.Error("an empty input must fail")
	}
	if _, err := DetectScenes(context.Background(), SceneOptions{Input: "x.mkv"}, nil); err == nil {
		t.Error("an empty FFmpeg path must fail")
	}
	if _, err := DetectScenes(context.Background(), SceneOptions{FFmpegPath: "/no/such/ffmpeg", Input: "x.mkv"}, nil); err == nil {
		t.Error("a missing executable must fail")
	}
}

// TestDetectScenesIntegration runs the real filter chain over a fixture
// with known cuts (3s red, 3s blue, 3s green): the cuts must be found at
// 3s and 6s, and stay there when the container carries a timestamp offset
// (FFmpeg rebases the start_time away, matching -ss).
func TestDetectScenesIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test (real ffmpeg)")
	}
	ffmpegPath, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not installed")
	}
	for _, offset := range []string{"0", "2"} {
		t.Run("offset "+offset, func(t *testing.T) {
			input := t.TempDir() + "/scenes.mkv"
			generate := exec.CommandContext(context.Background(), ffmpegPath, //nolint:gosec // G204: locally discovered FFmpeg on a t.TempDir file
				"-hide_banner", "-loglevel", "error",
				"-f", "lavfi", "-i", "color=c=red:s=160x120:d=3:r=10",
				"-f", "lavfi", "-i", "color=c=blue:s=160x120:d=3:r=10",
				"-f", "lavfi", "-i", "color=c=green:s=160x120:d=3:r=10",
				"-filter_complex", "[0:v][1:v][2:v]concat=n=3:v=1:a=0",
				"-c:v", "mpeg4", "-output_ts_offset", offset, input)
			if out, err := generate.CombinedOutput(); err != nil {
				t.Fatalf("generating test media: %v: %s", err, out)
			}
			var last time.Duration
			got, err := DetectScenes(context.Background(),
				SceneOptions{FFmpegPath: ffmpegPath, Input: input, Duration: 9 * time.Second},
				func(done time.Duration) { last = done })
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != 2 {
				t.Fatalf("scenes = %v, want 2 cuts", got)
			}
			for i, want := range []time.Duration{3 * time.Second, 6 * time.Second} {
				if d := got[i].At - want; d < -50*time.Millisecond || d > 50*time.Millisecond {
					t.Errorf("cut %d at %s, want ~%s", i, got[i].At, want)
				}
				if got[i].Score < DefaultSceneFloor {
					t.Errorf("cut %d score %v below the floor", i, got[i].Score)
				}
			}
			if last < 8*time.Second {
				t.Errorf("progress reached only %s of 9s", last)
			}
		})
	}
}
