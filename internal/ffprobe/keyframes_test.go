package ffprobe

import (
	"context"
	"os/exec"
	"reflect"
	"testing"
	"time"
)

func TestParseKeyframeTimes(t *testing.T) {
	t.Parallel()
	s := time.Second
	tests := []struct {
		name   string
		json   string
		origin time.Duration
		to     time.Duration
		want   []time.Duration
	}{
		{
			name: "only keyframe packets count",
			json: `{"packets": [
				{"pts_time": "0.000000", "dts_time": "-0.066", "flags": "K__"},
				{"pts_time": "0.100000", "flags": "___"},
				{"pts_time": "3.000000", "flags": "KD_"},
				{"pts_time": "3.033000", "flags": "_D_"}]}`,
			to:   10 * s,
			want: []time.Duration{0, 3 * s},
		},
		{
			name:   "the origin is subtracted",
			json:   `{"packets": [{"pts_time": "2.500000", "flags": "K__"}, {"pts_time": "5.500000", "flags": "K__"}]}`,
			origin: 1500 * time.Millisecond,
			to:     10 * s,
			want:   []time.Duration{1 * s, 4 * s},
		},
		{
			name:   "positions before the origin and past the end are dropped",
			json:   `{"packets": [{"pts_time": "0.500000", "flags": "K__"}, {"pts_time": "2.000000", "flags": "K__"}, {"pts_time": "9.000000", "flags": "K__"}]}`,
			origin: 1 * s, to: 5 * s,
			want: []time.Duration{1 * s},
		},
		{
			name: "dts stands in for a missing pts",
			json: `{"packets": [{"pts_time": "N/A", "dts_time": "4.000000", "flags": "K__"}, {"dts_time": "7.000000", "flags": "K__"}]}`,
			to:   10 * s,
			want: []time.Duration{4 * s, 7 * s},
		},
		{
			name: "packets without any timestamp are skipped",
			json: `{"packets": [{"pts_time": "N/A", "dts_time": "N/A", "flags": "K__"}, {"flags": "K__"}]}`,
			to:   10 * s,
			want: []time.Duration{},
		},
		{
			name: "unsorted and duplicate positions are normalized",
			json: `{"packets": [{"pts_time": "6.000000", "flags": "K__"}, {"pts_time": "3.000000", "flags": "K__"}, {"pts_time": "3.000000", "flags": "K__"}]}`,
			to:   10 * s,
			want: []time.Duration{3 * s, 6 * s},
		},
		{name: "no packets", json: `{"packets": []}`, to: 10 * s, want: []time.Duration{}},
		{name: "no packets key", json: `{}`, to: 10 * s, want: []time.Duration{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := parseKeyframeTimes([]byte(tt.json), tt.origin, tt.to)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
	if _, err := parseKeyframeTimes([]byte("not json"), 0, time.Second); err == nil {
		t.Error("malformed JSON must be an error")
	}
}

func TestKeyframeTimesValidatesArguments(t *testing.T) {
	t.Parallel()
	if _, err := KeyframeTimes(context.Background(), "definitely-not-an-ffprobe", "x.mkv", 0, time.Second); err == nil {
		t.Error("a missing executable must fail")
	}
	ffprobe, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("ffprobe not installed")
	}
	if _, err := KeyframeTimes(context.Background(), ffprobe, "", 0, time.Second); err == nil {
		t.Error("an empty input path must fail")
	}
	if _, err := KeyframeTimes(context.Background(), ffprobe, "x.mkv", time.Second, time.Second); err == nil {
		t.Error("an empty window must fail")
	}
}

// TestKeyframeTimesIntegration checks the scan against a real file whose
// keyframes are known (a 3s GOP at 30fps): the window result must start
// with the keyframe the seek lands on — the one a stream-copy cut at from
// begins at — and must stay correct with a timestamp offset.
func TestKeyframeTimesIntegration(t *testing.T) {
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
	s := time.Second
	for _, offset := range []string{"0", "2"} {
		t.Run("offset "+offset, func(t *testing.T) {
			input := t.TempDir() + "/gop.mkv"
			generate := exec.CommandContext(context.Background(), ffmpegPath, //nolint:gosec // G204: locally discovered FFmpeg on a t.TempDir file
				"-hide_banner", "-loglevel", "error",
				"-f", "lavfi", "-i", "testsrc2=duration=20:size=160x120:rate=30",
				"-c:v", "mpeg4", "-g", "90", "-bf", "0", "-q:v", "3",
				"-output_ts_offset", offset, input)
			if out, err := generate.CombinedOutput(); err != nil {
				t.Fatalf("generating test media: %v: %s", err, out)
			}

			// Keyframes every 3s: 0, 3, 6, ..., 18.
			all, err := KeyframeTimes(context.Background(), ffprobePath, input, 0, 20*s)
			if err != nil {
				t.Fatal(err)
			}
			if len(all) != 7 {
				t.Fatalf("whole-file keyframes = %v, want 7 (every 3s)", all)
			}
			for i, at := range all {
				want := time.Duration(i) * 3 * s
				if at < want-20*time.Millisecond || at > want+20*time.Millisecond {
					t.Errorf("keyframe %d at %s, want ~%s", i, at, want)
				}
			}

			// A mid-file window starts at the keyframe preceding from.
			win, err := KeyframeTimes(context.Background(), ffprobePath, input, 7500*time.Millisecond, 13*s)
			if err != nil {
				t.Fatal(err)
			}
			if len(win) == 0 {
				t.Fatal("no keyframes in the window")
			}
			if first := win[0]; first < 6*s-20*time.Millisecond || first > 6*s+20*time.Millisecond {
				t.Errorf("window starts at %s, want the keyframe before 7.5s (~6s): %v", first, win)
			}
			if last := win[len(win)-1]; last < 12*s-20*time.Millisecond || last > 13*s {
				t.Errorf("window ends at %s, want the keyframe ~12s: %v", last, win)
			}
		})
	}
}
