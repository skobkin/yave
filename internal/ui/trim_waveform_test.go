package ui

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"git.skobk.in/skobkin/yave/internal/analysis"
	"git.skobk.in/skobkin/yave/internal/model"
	"git.skobk.in/skobkin/yave/internal/transcode"
)

// fakeWave stands in for analysis.ComputeWaveform.
type fakeWave struct {
	mu      sync.Mutex
	calls   int
	opts    []analysis.WaveformOptions
	result  analysis.Waveform
	err     error
	partial []analysis.Waveform // delivered to onUpdate before the end
	gate    chan struct{}       // when set, the run waits for it (or its context)
}

func (f *fakeWave) compute(ctx context.Context, o analysis.WaveformOptions, onUpdate func(analysis.Waveform)) (analysis.Waveform, error) {
	f.mu.Lock()
	f.calls++
	f.opts = append(f.opts, o)
	result, err, gate, partial := f.result, f.err, f.gate, f.partial
	f.mu.Unlock()
	for _, p := range partial {
		onUpdate(p)
	}
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return analysis.Waveform{}, ctx.Err()
		}
	}
	return result, err
}

func (f *fakeWave) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func wave(n int) analysis.Waveform {
	peaks := make([]analysis.Peak, n)
	for i := range peaks {
		peaks[i] = analysis.Peak{Min: -1000, Max: 1000}
	}
	return analysis.Waveform{Bucket: time.Second, Peaks: peaks}
}

// waveController is a trim controller over a 100s input with audio (a
// real file, so it can be fingerprinted), with trimming enabled.
func waveController(t *testing.T, f *fakeWave) (*Controller, *uiQueue) {
	t.Helper()
	c, q := stripController(t, okThumb)
	input := filepath.Join(t.TempDir(), "song.mkv")
	if err := os.WriteFile(input, []byte("audio"), 0o600); err != nil {
		t.Fatal(err)
	}
	c.inputPath = input
	c.media = model.MediaInfo{
		Duration:     100 * time.Second,
		VideoStreams: []model.VideoStream{{Index: 0}},
		AudioStreams: []model.AudioStream{{Index: 1}},
	}
	c.config.FFmpeg.FFmpegPath = "/fake/ffmpeg"
	c.waves.compute = f.compute
	c.trimTimeline.SetDuration(c.media.Duration)
	c.trimEnabled = true
	return c, q
}

func drain(q *uiQueue) {
	for {
		select {
		case fn := <-q.ch:
			fn()
		default:
			return
		}
	}
}

func TestWaveformStartsLazilyWhenTrimmingIsEnabled(t *testing.T) {
	f := &fakeWave{result: wave(100)}
	c, q := waveController(t, f)
	c.trimEnabled = false
	c.updateWaveform()
	if f.callCount() != 0 || c.trimTimeline.WaveformLane() {
		t.Fatal("no analysis and no lane while trimming is off")
	}

	c.trimEnabled = true
	c.updateWaveform()
	if !c.waves.running || !c.trimTimeline.WaveformLane() {
		t.Fatalf("running=%v lane=%v after enabling trimming", c.waves.running, c.trimTimeline.WaveformLane())
	}
	q.pump(t, "the waveform", func() bool { return c.waves.have })
	if got := len(c.trimTimeline.Waveform().Peaks); got != 100 {
		t.Errorf("timeline has %d peaks, want 100", got)
	}
	if c.waveStatus.Text != "" || !c.waveStatus.Hidden {
		t.Errorf("status %q must clear when the waveform completes", c.waveStatus.Text)
	}
	if o := f.opts[0]; o.Input != c.inputPath || o.Duration != 100*time.Second || o.FFmpegPath != "/fake/ffmpeg" {
		t.Errorf("options = %+v", o)
	}

	// Further refreshes never recompute.
	for range 5 {
		c.updateWaveform()
	}
	if f.callCount() != 1 {
		t.Errorf("computed %d times, want once", f.callCount())
	}
}

func TestWaveformAppearsProgressivelyWithProgress(t *testing.T) {
	f := &fakeWave{result: wave(100), partial: []analysis.Waveform{wave(25), wave(50)}, gate: make(chan struct{})}
	c, q := waveController(t, f)
	c.updateWaveform()
	q.pump(t, "the partial waveform", func() bool { return len(c.trimTimeline.Waveform().Peaks) == 50 })
	if !strings.Contains(c.waveStatus.Text, "50%") || c.waveStatus.Hidden {
		t.Errorf("status = %q (hidden %v), want the progress", c.waveStatus.Text, c.waveStatus.Hidden)
	}
	if c.waves.have {
		t.Error("a partial waveform is not complete")
	}
	close(f.gate)
	q.pump(t, "completion", func() bool { return c.waves.have })
	if got := len(c.trimTimeline.Waveform().Peaks); got != 100 {
		t.Errorf("final peaks = %d", got)
	}
}

func TestWaveformIsHiddenForInputsWithoutAudio(t *testing.T) {
	f := &fakeWave{result: wave(10)}
	c, _ := waveController(t, f)
	c.media.AudioStreams = nil
	c.updateWaveform()
	if f.callCount() != 0 || c.trimTimeline.WaveformLane() {
		t.Error("an input without audio gets no lane and no analysis")
	}
	// Audio that appears (a new input) is picked up.
	c.media.AudioStreams = []model.AudioStream{{Index: 1}}
	c.updateWaveform()
	if !c.waves.running {
		t.Error("audio present: the analysis must start")
	}
}

func TestWaveformIgnoresProfileSettings(t *testing.T) {
	f := &fakeWave{result: wave(10)}
	c, q := waveController(t, f)
	// Excluding audio from the output (or any encoder choice) must not
	// affect the analysis of the source.
	c.includeAudio.SetChecked(false)
	c.videoEncoder.Selected = "copy"
	c.audioEncoder.Selected = "flac"
	c.updateWaveform()
	q.pump(t, "the waveform", func() bool { return c.waves.have })
	if f.callCount() != 1 {
		t.Errorf("computed %d times", f.callCount())
	}
}

func TestExportCancelsTheWaveformAndItRestartsAfterwards(t *testing.T) {
	f := &fakeWave{result: wave(100), partial: []analysis.Waveform{wave(30)}, gate: make(chan struct{})}
	c, q := waveController(t, f)
	c.updateWaveform()
	q.pump(t, "the partial waveform", func() bool { return len(c.trimTimeline.Waveform().Peaks) == 30 })

	// An export starts: the analysis stops and its partial result goes.
	c.runControl = transcode.NewRunControl()
	c.cancelAnalysisWork()
	if c.waves.running {
		t.Fatal("an export must cancel the waveform analysis")
	}
	if len(c.trimTimeline.Waveform().Peaks) != 0 {
		t.Error("the partial waveform of a cancelled run must not linger")
	}
	// While the export runs, refreshes must not restart it.
	c.updateWaveform()
	if c.waves.running {
		t.Fatal("the analysis must not restart during an export")
	}

	// The export ends with trimming still on: the analysis restarts by itself.
	c.runControl = nil
	f.mu.Lock()
	f.gate, f.partial = nil, nil
	f.mu.Unlock()
	c.updateWaveform()
	q.pump(t, "the restarted analysis", func() bool { return c.waves.have })
	if f.callCount() != 2 {
		t.Errorf("computed %d times, want a fresh run after the export", f.callCount())
	}
}

func TestNewInputClearsTheWaveformAndTheCacheRestoresTheSameFile(t *testing.T) {
	f := &fakeWave{result: wave(100)}
	c, q := waveController(t, f)
	c.updateWaveform()
	q.pump(t, "the waveform", func() bool { return c.waves.have })

	c.resetTrimForNewInput()
	if c.waves.have || len(c.trimTimeline.Waveform().Peaks) != 0 || c.trimTimeline.WaveformLane() {
		t.Fatal("a new input must clear the waveform and hide the lane")
	}

	// The same unchanged file comes back from the cache without FFmpeg.
	c.loadCachedWaveform()
	if !c.waves.have || len(c.trimTimeline.Waveform().Peaks) != 100 || !c.trimTimeline.WaveformLane() {
		t.Fatalf("cache miss for an unchanged file: have=%v", c.waves.have)
	}
	if f.callCount() != 1 {
		t.Errorf("the cache hit ran FFmpeg again (%d)", f.callCount())
	}

	// A modified file invalidates it, and a trimming-enabled refresh recomputes.
	c.resetTrimForNewInput()
	if err := os.WriteFile(c.inputPath, []byte("different, longer audio"), 0o600); err != nil {
		t.Fatal(err)
	}
	c.loadCachedWaveform()
	if c.waves.have {
		t.Error("a changed file must not reuse the cached waveform")
	}
	c.trimEnabled = true
	c.updateWaveform()
	q.pump(t, "the recomputation", func() bool { return c.waves.have })
	if f.callCount() != 2 {
		t.Errorf("computed %d times, want a recomputation for the changed file", f.callCount())
	}
}

func TestWaveformCacheIsBoundedAndMostRecentFirst(t *testing.T) {
	c, _ := waveController(t, &fakeWave{})
	for i := range 6 {
		c.storeWaveCache(string(rune('a'+i)), wave(i))
	}
	if len(c.waves.cache) != waveCacheSize || c.waves.cache[0].key != "f" || c.waves.cache[waveCacheSize-1].key != "c" {
		t.Errorf("cache = %v", c.waves.cache)
	}
	c.storeWaveCache("d", wave(1))
	if c.waves.cache[0].key != "d" || len(c.waves.cache) != waveCacheSize {
		t.Errorf("re-stored key: %v", c.waves.cache)
	}
	c.storeWaveCache("", wave(1))
	if c.waves.cache[0].key == "" {
		t.Error("an unfingerprintable file must not be cached")
	}
}

func TestWaveformFailureHidesTheLaneAndDoesNotLoop(t *testing.T) {
	f := &fakeWave{err: errors.New("no audio stream")}
	c, q := waveController(t, f)
	c.updateWaveform()
	q.pump(t, "the failure", func() bool { return c.waves.failed })

	if !strings.Contains(c.waveStatus.Text, "Waveform unavailable: no audio stream") || c.waveStatus.Hidden {
		t.Errorf("status = %q", c.waveStatus.Text)
	}
	if c.trimTimeline.WaveformLane() || len(c.trimTimeline.Waveform().Peaks) != 0 {
		t.Error("a failed analysis must leave no lane")
	}
	for range 5 {
		c.updateWaveform()
	}
	if f.callCount() != 1 {
		t.Errorf("computed %d times; a failed input must not be retried on every refresh", f.callCount())
	}

	// Trimming off and on again retries.
	f.mu.Lock()
	f.err, f.result = nil, wave(10)
	f.mu.Unlock()
	c.trimEnabled = false
	c.updateWaveform()
	c.trimEnabled = true
	c.updateWaveform()
	q.pump(t, "the retry", func() bool { return c.waves.have })
}

func TestStaleWaveformResultsAreDropped(t *testing.T) {
	f := &fakeWave{result: wave(100), partial: []analysis.Waveform{wave(10)}, gate: make(chan struct{})}
	c, q := waveController(t, f)
	c.updateWaveform()
	q.pump(t, "a partial update", func() bool { return len(c.trimTimeline.Waveform().Peaks) == 10 })
	c.inputGeneration++ // another input was loaded
	c.trimTimeline.SetWaveform(analysis.Waveform{})
	close(f.gate)
	time.Sleep(50 * time.Millisecond)
	drain(q)
	if c.waves.have || len(c.trimTimeline.Waveform().Peaks) != 0 {
		t.Error("a result for an older input must not be shown")
	}
}

func TestWaveformNeedsFFmpeg(t *testing.T) {
	f := &fakeWave{result: wave(10)}
	c, _ := waveController(t, f)
	c.config.FFmpeg.FFmpegPath = ""
	c.updateWaveform()
	if f.callCount() != 0 || c.waves.running {
		t.Error("no FFmpeg, no analysis")
	}
}
