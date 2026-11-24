package analysis

import (
	"context"
	"encoding/binary"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func pcm(samples ...int16) []byte {
	out := make([]byte, 2*len(samples))
	for i, s := range samples {
		binary.LittleEndian.PutUint16(out[2*i:], uint16(s)) //nolint:gosec // G115: encoding a test sample
	}
	return out
}

func TestPeakReducer(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		perBucket int
		writes    [][]byte
		want      []Peak
	}{
		{
			name: "two full buckets", perBucket: 3,
			writes: [][]byte{pcm(1, -5, 3, 100, 20, -7)},
			want:   []Peak{{Min: -5, Max: 3}, {Min: -7, Max: 100}},
		},
		{
			name: "a partial last bucket is kept on flush", perBucket: 4,
			writes: [][]byte{pcm(10, 20, 30, 40, -3, 9)},
			want:   []Peak{{Min: 10, Max: 40}, {Min: -3, Max: 9}},
		},
		{
			name: "extremes of the sample range", perBucket: 2,
			writes: [][]byte{pcm(-32768, 32767)},
			want:   []Peak{{Min: -32768, Max: 32767}},
		},
		{
			name: "silence", perBucket: 2,
			writes: [][]byte{pcm(0, 0, 0, 0)},
			want:   []Peak{{}, {}},
		},
		{
			name: "writes may split a sample", perBucket: 2,
			writes: [][]byte{pcm(1, -2, 300, 4)[:3], pcm(1, -2, 300, 4)[3:5], pcm(1, -2, 300, 4)[5:]},
			want:   []Peak{{Min: -2, Max: 1}, {Min: 4, Max: 300}},
		},
		{
			name: "a single-byte write", perBucket: 1,
			writes: [][]byte{{0x34}, {0x12}},
			want:   []Peak{{Min: 0x1234, Max: 0x1234}},
		},
		{name: "empty stream", perBucket: 4, writes: nil, want: nil},
		{
			name: "a trailing odd byte is dropped", perBucket: 2,
			writes: [][]byte{append(pcm(5, 6), 0x7f)},
			want:   []Peak{{Min: 5, Max: 6}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := peakReducer{perBucket: tt.perBucket}
			for _, w := range tt.writes {
				r.write(w)
			}
			r.flush()
			got := r.snapshot().Peaks
			if len(got) == 0 && len(tt.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("peaks = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSnapshotsAreStableWhileTheReducerKeepsGrowing(t *testing.T) {
	t.Parallel()
	r := peakReducer{perBucket: 1}
	r.write(pcm(1, 2, 3))
	snap := r.snapshot()
	for i := range 5000 { // force reallocations
		r.write(pcm(int16(i))) //nolint:gosec // G115: bounded test values
	}
	if len(snap.Peaks) != 3 || snap.Peaks[0].Max != 1 || snap.Peaks[2].Max != 3 {
		t.Errorf("an earlier snapshot changed: %v", snap.Peaks)
	}
	if cap(snap.Peaks) != len(snap.Peaks) {
		t.Error("a snapshot must not be appendable into the reducer's storage")
	}
}

func TestBucketSizingBoundsMemory(t *testing.T) {
	t.Parallel()
	tests := []struct {
		duration    time.Duration
		wantBucket  time.Duration
		maxBuckets  int
		description string
	}{
		{0, 10 * time.Millisecond, 0, "unknown duration uses the finest bucket"},
		{time.Minute, 10 * time.Millisecond, 6000, "short input keeps the finest bucket"},
		{30 * time.Minute, 10 * time.Millisecond, 180_000, "30 minutes still fits the finest bucket"},
		{2 * time.Hour, 36 * time.Millisecond, waveformMaxBuckets, "two hours coarsen the bucket"},
		{10 * time.Hour, 180 * time.Millisecond, waveformMaxBuckets, "ten hours stay bounded"},
	}
	for _, tt := range tests {
		per := samplesPerBucket(tt.duration)
		got := bucketDuration(per)
		if got < tt.wantBucket-time.Millisecond || got > tt.wantBucket+time.Millisecond {
			t.Errorf("%s: bucket = %s, want ~%s", tt.description, got, tt.wantBucket)
		}
		if tt.maxBuckets > 0 {
			if n := int(tt.duration / got); n > tt.maxBuckets+1 {
				t.Errorf("%s: %d buckets exceeds the bound %d", tt.description, n, tt.maxBuckets)
			}
		}
	}
	if per := samplesPerBucket(0); per != 80 {
		t.Errorf("10ms at 8kHz = %d samples, want 80", per)
	}
}

func TestWaveformArgs(t *testing.T) {
	t.Parallel()
	got := WaveformArgs(WaveformOptions{Input: "/media/a b.mkv"})
	want := []string{
		"-hide_banner", "-nostdin", "-loglevel", "error",
		"-i", "/media/a b.mkv",
		"-map", "0:a:0", "-vn", "-sn", "-dn",
		"-af", "aresample=async=1:first_pts=0",
		"-ac", "1", "-ar", "8000",
		"-f", "s16le", "pipe:1",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("args =\n%q\nwant\n%q", got, want)
	}
}

func TestWaveformDuration(t *testing.T) {
	t.Parallel()
	w := Waveform{Bucket: 10 * time.Millisecond, Peaks: make([]Peak, 250)}
	if got := w.Duration(); got != 2500*time.Millisecond {
		t.Errorf("Duration = %s", got)
	}
}

func TestComputeWaveformFromFakeFFmpeg(t *testing.T) {
	// 80 samples are one 10ms bucket at 8 kHz: a ramp, then a loud bucket,
	// then a half-filled one.
	dir := t.TempDir()
	data := filepath.Join(dir, "pcm")
	samples := make([]int16, 0, 200)
	for i := range 80 {
		samples = append(samples, int16(i%50)) //nolint:gosec // G115: small test values
	}
	for range 80 {
		samples = append(samples, -9000)
	}
	for range 40 {
		samples = append(samples, 4000) // a partial third bucket
	}
	if err := os.WriteFile(data, pcm(samples...), 0o600); err != nil {
		t.Fatal(err)
	}
	path, argsFile := writeFakeFFmpeg(t, "cat '"+data+"'")

	got, err := ComputeWaveform(context.Background(), WaveformOptions{FFmpegPath: path, Input: "song.mkv", Duration: time.Minute}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.Bucket != 10*time.Millisecond {
		t.Fatalf("bucket = %s", got.Bucket)
	}
	want := []Peak{{Min: 0, Max: 49}, {Min: -9000, Max: -9000}, {Min: 4000, Max: 4000}}
	if !reflect.DeepEqual(got.Peaks, want) {
		t.Errorf("peaks = %v, want %v", got.Peaks, want)
	}
	recorded, err := os.ReadFile(argsFile) //nolint:gosec // G304: a file the test just had its fake FFmpeg write in t.TempDir
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(recorded), "song.mkv") || !strings.Contains(string(recorded), "0:a:0") {
		t.Errorf("FFmpeg ran with %q", recorded)
	}
}

func TestComputeWaveformReportsProgressiveUpdates(t *testing.T) {
	// A slow producer: the throttled updates must show growing waveforms
	// before the final result.
	path, _ := writeFakeFFmpeg(t, `
i=0
while [ $i -lt 4 ]; do
  head -c 16000 /dev/zero
  sleep 0.35
  i=$((i+1))
done
`)
	var lengths []int
	got, err := ComputeWaveform(context.Background(),
		WaveformOptions{FFmpegPath: path, Input: "x.mkv", Duration: time.Minute},
		func(w Waveform) { lengths = append(lengths, len(w.Peaks)) })
	if err != nil {
		t.Fatal(err)
	}
	if len(lengths) < 2 {
		t.Fatalf("got %d updates, want several: %v", len(lengths), lengths)
	}
	for i := 1; i < len(lengths); i++ {
		if lengths[i] < lengths[i-1] {
			t.Errorf("updates must grow: %v", lengths)
		}
	}
	if final := len(got.Peaks); final != 4*16000/2/80 { // 4 writes of 8000 samples, 80 per bucket
		t.Errorf("final peaks = %d, want 400", final)
	}
	if last := lengths[len(lengths)-1]; last > len(got.Peaks) {
		t.Errorf("an update (%d) is longer than the result (%d)", last, len(got.Peaks))
	}
}

func TestComputeWaveformFailureAndCancellation(t *testing.T) {
	failing, _ := writeFakeFFmpeg(t, "echo 'Stream map 0:a:0 matches no streams.' >&2; exit 1")
	_, err := ComputeWaveform(context.Background(), WaveformOptions{FFmpegPath: failing, Input: "x.mkv"}, nil)
	if err == nil || !strings.Contains(err.Error(), "matches no streams") {
		t.Fatalf("error = %v, want FFmpeg's diagnostic", err)
	}

	slow, _ := writeFakeFFmpeg(t, "sleep 30")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := ComputeWaveform(ctx, WaveformOptions{FFmpegPath: slow, Input: "x.mkv"}, nil)
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
		t.Fatal("cancellation did not stop the computation")
	}

	if _, err := ComputeWaveform(context.Background(), WaveformOptions{FFmpegPath: "ffmpeg"}, nil); err == nil {
		t.Error("an empty input must fail")
	}
}

// TestComputeWaveformIntegration decodes a real file whose audio is a
// loud tone from 1s to 3s (the audio stream starts 1s after the video):
// the envelope must be loud exactly there, i.e. the late audio start must
// not shift the waveform to the front of the timeline.
func TestComputeWaveformIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test (real ffmpeg)")
	}
	ffmpegPath, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not installed")
	}
	input := t.TempDir() + "/delayed.mkv"
	generate := exec.CommandContext(context.Background(), ffmpegPath, //nolint:gosec // G204: locally discovered FFmpeg on a t.TempDir file
		"-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "color=c=black:s=64x64:d=5:r=10",
		"-itsoffset", "1", "-f", "lavfi", "-i", "sine=frequency=440:duration=2:sample_rate=44100",
		"-map", "0:v", "-map", "1:a", "-c:v", "mpeg4", "-c:a", "aac", input)
	if out, err := generate.CombinedOutput(); err != nil {
		t.Fatalf("generating test media: %v: %s", err, out)
	}
	got, err := ComputeWaveform(context.Background(), WaveformOptions{FFmpegPath: ffmpegPath, Input: input, Duration: 5 * time.Second}, nil)
	if err != nil {
		t.Fatal(err)
	}
	loud := func(at time.Duration) bool {
		i := int(at / got.Bucket)
		return i < len(got.Peaks) && got.Peaks[i].Max > 2000
	}
	for _, at := range []time.Duration{200 * time.Millisecond, 800 * time.Millisecond} {
		if loud(at) {
			t.Errorf("sound at %s, before the audio starts at 1s", at)
		}
	}
	for _, at := range []time.Duration{1300 * time.Millisecond, 2 * time.Second, 2700 * time.Millisecond} {
		if !loud(at) {
			t.Errorf("silence at %s, inside the 1s-3s tone", at)
		}
	}
	if loud(3500 * time.Millisecond) {
		t.Error("sound at 3.5s, after the tone ends")
	}
}
