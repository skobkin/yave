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
	"git.skobk.in/skobkin/yave/internal/profiles"
	"git.skobk.in/skobkin/yave/internal/transcode"
)

// fakeScenes stands in for analysis.DetectScenes.
type fakeScenes struct {
	mu       sync.Mutex
	calls    int
	opts     []analysis.SceneOptions
	scenes   []analysis.Scene
	err      error
	progress []time.Duration
	gate     chan struct{} // when set, the run waits for it (or its context)
}

func (f *fakeScenes) detect(ctx context.Context, o analysis.SceneOptions, progress func(time.Duration)) ([]analysis.Scene, error) {
	f.mu.Lock()
	f.calls++
	f.opts = append(f.opts, o)
	scenes, err, gate, steps := f.scenes, f.err, f.gate, f.progress
	f.mu.Unlock()
	for _, step := range steps {
		progress(step)
	}
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return scenes, err
}

func (f *fakeScenes) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

var sceneFilters = map[string]bool{"scale": true, "select": true, "metadata": true}

// sceneController is a trim controller over a 100s input (a real file, so
// it can be fingerprinted) whose FFmpeg build has the scene filters.
func sceneController(t *testing.T, f *fakeScenes) (*Controller, *uiQueue) {
	t.Helper()
	c, q := stripController(t, okThumb)
	input := filepath.Join(t.TempDir(), "movie.mkv")
	if err := os.WriteFile(input, []byte("frames"), 0o600); err != nil {
		t.Fatal(err)
	}
	c.inputPath = input
	c.media = model.MediaInfo{Duration: 100 * time.Second, VideoStreams: []model.VideoStream{{Index: 0}}}
	c.config.FFmpeg.FFmpegPath = "/fake/ffmpeg"
	c.caps.Filters = sceneFilters
	c.scenes.detect = f.detect
	c.trimTimeline.SetDuration(c.media.Duration)
	c.trimEnabled = true
	trimScenesRow(c) // builds the containers the real layout owns
	c.updateSceneControls()
	return c, q
}

func threeScenes() []analysis.Scene {
	return []analysis.Scene{
		{At: 10 * time.Second, Score: 0.15},
		{At: 40 * time.Second, Score: 0.35},
		{At: 70 * time.Second, Score: 0.80},
	}
}

func TestSceneRowIsHiddenUnlessTheBuildHasTheFilters(t *testing.T) {
	c, _ := sceneController(t, &fakeScenes{})
	if c.trimScenesBox.Hidden {
		t.Fatal("a build with select, metadata and scale offers scene detection")
	}
	for name, filters := range map[string]map[string]bool{
		"no listing":       nil,
		"empty listing":    {},
		"no select":        {"scale": true, "metadata": true},
		"no metadata":      {"scale": true, "select": true},
		"no scale":         {"select": true, "metadata": true},
		"unrelated filter": {"scdet": true},
	} {
		c.caps.Filters = filters
		c.updateSceneControls()
		if !c.trimScenesBox.Hidden {
			t.Errorf("%s: the scene row must be hidden", name)
		}
		c.startSceneDetection() // a hidden feature must not be startable either
		if c.scenes.running {
			t.Errorf("%s: detection started although the filters are missing", name)
		}
	}
}

func TestSceneDetectionBlockReasons(t *testing.T) {
	f := &fakeScenes{}
	c, _ := sceneController(t, f)
	if got := c.sceneBlockReason(); got != "" {
		t.Fatalf("a ready controller is blocked: %q", got)
	}
	if c.sceneDetect.Disabled() {
		t.Fatal("the Detect button must be enabled when nothing blocks it")
	}

	cases := []struct {
		name   string
		mutate func()
		undo   func()
		want   string
	}{
		{"no input", func() { c.inputPath = "" }, func() {}, "Open a video"},
		{"trimming off", func() { c.trimEnabled = false }, func() { c.trimEnabled = true }, "Enable trimming"},
		{"no video", func() { c.media.VideoStreams = nil }, func() { c.media.VideoStreams = []model.VideoStream{{}} }, "no video stream"},
		{"export running", func() { c.runControl = transcode.NewRunControl() }, func() { c.runControl = nil }, "export runs"},
		{"no ffmpeg", func() { c.config.FFmpeg.FFmpegPath = "" }, func() { c.config.FFmpeg.FFmpegPath = "/fake/ffmpeg" }, "FFmpeg is not available"},
	}
	input := c.inputPath
	for _, tc := range cases {
		tc.mutate()
		if got := c.sceneBlockReason(); !strings.Contains(got, tc.want) {
			t.Errorf("%s: reason = %q, want it to mention %q", tc.name, got, tc.want)
		}
		c.updateSceneControls()
		if !c.sceneDetect.Disabled() {
			t.Errorf("%s: the Detect button must be disabled", tc.name)
		}
		if tip := c.sceneDetect.tip(); !strings.Contains(tip, tc.want) {
			t.Errorf("%s: tooltip %q must explain the block", tc.name, tip)
		}
		c.startSceneDetection()
		if f.callCount() != 0 {
			t.Errorf("%s: detection ran while blocked", tc.name)
		}
		tc.undo()
		c.inputPath = input
	}
}

func TestSceneDetectionShowsProgressThenMarkers(t *testing.T) {
	f := &fakeScenes{scenes: threeScenes(), progress: []time.Duration{25 * time.Second, 50 * time.Second}, gate: make(chan struct{})}
	c, q := sceneController(t, f)

	c.startSceneDetection()
	if !c.scenes.running || c.sceneDetect.Text != "Cancel scene detection" {
		t.Fatalf("running=%v button=%q", c.scenes.running, c.sceneDetect.Text)
	}
	q.pump(t, "50% progress", func() bool { return strings.Contains(c.sceneStatus.Text, "50%") })
	if got := c.sceneStatus.Text; !strings.HasPrefix(got, "Detecting scenes") {
		t.Errorf("status = %q", got)
	}

	close(f.gate)
	q.pump(t, "the results", func() bool { return !c.scenes.running })
	if f.opts[0].Input != c.inputPath || f.opts[0].Duration != 100*time.Second || f.opts[0].Floor != analysis.DefaultSceneFloor {
		t.Errorf("detection options = %+v", f.opts[0])
	}
	// Default threshold 0.3: the 0.15 cut is below it.
	got := c.trimTimeline.Markers(markerScene)
	if len(got) != 2 || got[0].At != 40*time.Second || got[1].At != 70*time.Second {
		t.Fatalf("markers = %v, want the cuts at 40s and 70s", got)
	}
	if c.sceneDetect.Text != "Detect scenes" || c.sceneThresholdBox.Hidden {
		t.Errorf("after a run: button %q, threshold hidden=%v", c.sceneDetect.Text, c.sceneThresholdBox.Hidden)
	}
	if !strings.Contains(c.sceneStatus.Text, "2 scene cut") {
		t.Errorf("status = %q", c.sceneStatus.Text)
	}
}

func TestSceneThresholdFiltersLiveWithoutRerunningAndPersists(t *testing.T) {
	f := &fakeScenes{scenes: threeScenes()}
	c, q := sceneController(t, f)
	c.configPath = filepath.Join(t.TempDir(), "config.yaml")
	c.config = model.Config{FFmpeg: c.config.FFmpeg, Version: 1}
	if err := profiles.Normalize(&c.config); err != nil {
		t.Fatal(err)
	}
	c.startSceneDetection()
	q.pump(t, "the results", func() bool { return c.scenes.have })

	count := func() int { return len(c.trimTimeline.Markers(markerScene)) }
	if count() != 2 {
		t.Fatalf("markers at the default threshold = %d, want 2", count())
	}
	c.sceneThreshold.SetValue(0.1) // lowest: every recorded cut
	if count() != 3 {
		t.Errorf("markers at 0.1 = %d, want 3", count())
	}
	c.sceneThreshold.SetValue(0.5)
	if count() != 1 {
		t.Errorf("markers at 0.5 = %d, want 1", count())
	}
	c.onSceneThreshold(0.9) // a live drag tick: markers follow, nothing is saved
	if count() != 0 {
		t.Errorf("markers at 0.9 = %d, want 0", count())
	}
	if f.callCount() != 1 {
		t.Fatalf("moving the slider ran FFmpeg %d times", f.callCount())
	}
	if !strings.Contains(c.sceneThresholdLabel.Text, "0.90") {
		t.Errorf("label = %q", c.sceneThresholdLabel.Text)
	}

	// Releasing the slider writes the threshold to the config file.
	c.sceneThreshold.OnChangeEnded(0.9)
	loaded, err := profiles.Load(c.configPath)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.UI.SceneThreshold == nil || *loaded.UI.SceneThreshold != 0.9 {
		t.Fatalf("persisted threshold = %v, want 0.9", loaded.UI.SceneThreshold)
	}
}

func TestSceneThresholdIsClampedAndDefaulted(t *testing.T) {
	c, _ := sceneController(t, &fakeScenes{})
	if got := c.sceneThresholdValue(); got != sceneDefaultThreshold {
		t.Errorf("unset threshold = %v, want the default", got)
	}
	for in, want := range map[float64]float64{0.01: sceneThresholdMin, 5: sceneThresholdMax, 0.42: 0.42} {
		v := in
		c.config.UI.SceneThreshold = &v
		if got := c.sceneThresholdValue(); got != want {
			t.Errorf("threshold %v = %v, want %v", in, got, want)
		}
	}
}

func TestCancellingSceneDetectionIgnoresTheLateResult(t *testing.T) {
	f := &fakeScenes{scenes: threeScenes(), gate: make(chan struct{})}
	c, q := sceneController(t, f)
	c.startSceneDetection()
	c.sceneDetect.OnTapped() // the same button cancels
	if c.scenes.running {
		t.Fatal("cancel did not stop the run")
	}
	if got := c.sceneStatus.Text; got != "Scene detection cancelled" {
		t.Errorf("status = %q", got)
	}
	if c.sceneDetect.Text != "Detect scenes" {
		t.Errorf("button = %q after cancelling", c.sceneDetect.Text)
	}
	close(f.gate)
	time.Sleep(50 * time.Millisecond)
	for drained := false; !drained; {
		select {
		case fn := <-q.ch:
			fn()
		default:
			drained = true
		}
	}
	if c.scenes.have || len(c.trimTimeline.Markers(markerScene)) != 0 {
		t.Error("a cancelled detection must not publish results")
	}
	// It can be started again.
	f.mu.Lock()
	f.gate = nil
	f.mu.Unlock()
	c.startSceneDetection()
	q.pump(t, "the restart", func() bool { return c.scenes.have })
}

func TestExportStartCancelsSceneDetection(t *testing.T) {
	f := &fakeScenes{scenes: threeScenes(), gate: make(chan struct{})}
	c, _ := sceneController(t, f)
	c.startSceneDetection()
	c.cancelAnalysisWork()
	if c.scenes.running {
		t.Fatal("an export must cancel scene detection")
	}
	if got := c.sceneStatus.Text; !strings.Contains(got, "export") {
		t.Errorf("status = %q, want the reason", got)
	}
	c.cancelAnalysisWork() // idempotent when nothing runs
	if got := c.sceneStatus.Text; !strings.Contains(got, "export") {
		t.Errorf("a second call must not disturb the status: %q", got)
	}
}

func TestSceneDetectionFailureIsReportedAndRetryable(t *testing.T) {
	f := &fakeScenes{err: errors.New("boom")}
	c, q := sceneController(t, f)
	c.startSceneDetection()
	q.pump(t, "the failure", func() bool { return !c.scenes.running })
	if got := c.sceneStatus.Text; got != "Scene detection failed: boom" {
		t.Errorf("status = %q", got)
	}
	if c.scenes.have || len(c.trimTimeline.Markers(markerScene)) != 0 {
		t.Error("a failed run must not leave results")
	}
	if c.sceneDetect.Disabled() {
		t.Error("the user must be able to retry")
	}
}

func TestNewInputClearsScenesAndCacheRestoresTheSameFile(t *testing.T) {
	f := &fakeScenes{scenes: threeScenes()}
	c, q := sceneController(t, f)
	c.startSceneDetection()
	q.pump(t, "the results", func() bool { return c.scenes.have })

	c.resetTrimForNewInput()
	if c.scenes.have || len(c.trimTimeline.Markers(markerScene)) != 0 || c.sceneStatus.Text != "" {
		t.Fatal("a new input must clear the scene results")
	}

	// Re-opening the very same file restores them without FFmpeg.
	c.loadCachedScenes()
	if !c.scenes.have || len(c.trimTimeline.Markers(markerScene)) != 2 {
		t.Fatalf("cache miss for an unchanged file: have=%v markers=%d", c.scenes.have, len(c.trimTimeline.Markers(markerScene)))
	}
	if f.callCount() != 1 {
		t.Errorf("the cache hit ran FFmpeg again (%d calls)", f.callCount())
	}

	// A modified file invalidates it.
	c.resetTrimForNewInput()
	if err := os.WriteFile(c.inputPath, []byte("different, longer contents"), 0o600); err != nil {
		t.Fatal(err)
	}
	c.loadCachedScenes()
	if c.scenes.have {
		t.Error("a changed file must not reuse the cached scenes")
	}

	// A different file has no entry either.
	other := filepath.Join(t.TempDir(), "other.mkv")
	if err := os.WriteFile(other, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	c.inputPath = other
	c.loadCachedScenes()
	if c.scenes.have {
		t.Error("a different file must not reuse the cached scenes")
	}
}

func TestSceneCacheIsBoundedAndMostRecentFirst(t *testing.T) {
	c, _ := sceneController(t, &fakeScenes{})
	for i := range 6 {
		c.storeSceneCache(string(rune('a'+i)), []analysis.Scene{{At: time.Duration(i)}})
	}
	if len(c.scenes.cache) != sceneCacheSize {
		t.Fatalf("cache holds %d entries, want %d", len(c.scenes.cache), sceneCacheSize)
	}
	if c.scenes.cache[0].key != "f" || c.scenes.cache[sceneCacheSize-1].key != "c" {
		t.Errorf("cache order = %v", c.scenes.cache)
	}
	c.storeSceneCache("d", nil) // a re-stored key moves to the front, no duplicate
	if c.scenes.cache[0].key != "d" || len(c.scenes.cache) != sceneCacheSize {
		t.Errorf("re-stored key: %v", c.scenes.cache)
	}
	c.storeSceneCache("", nil) // an unfingerprintable file is never cached
	if c.scenes.cache[0].key == "" {
		t.Error("an empty key must not be cached")
	}
}

func TestStaleSceneResultsFromAnOlderInputAreDropped(t *testing.T) {
	f := &fakeScenes{scenes: threeScenes(), gate: make(chan struct{})}
	c, q := sceneController(t, f)
	c.startSceneDetection()
	c.inputGeneration++ // another input was loaded meanwhile
	close(f.gate)
	time.Sleep(50 * time.Millisecond)
	for drained := false; !drained; {
		select {
		case fn := <-q.ch:
			fn()
		default:
			drained = true
		}
	}
	if c.scenes.have || len(c.trimTimeline.Markers(markerScene)) != 0 {
		t.Error("a result for an older input must not be shown")
	}
}

func TestSceneMarkersJoinMarkerNavigation(t *testing.T) {
	f := &fakeScenes{scenes: threeScenes()}
	c, q := sceneController(t, f)
	c.startSceneDetection()
	q.pump(t, "the results", func() bool { return c.scenes.have })
	c.trimTimeline.SetPlayhead(0)
	c.jumpToMarker(1)
	if got := c.trimTimeline.Playhead(); got != 40*time.Second {
		t.Errorf("next marker = %s, want the first visible scene cut at 40s", got)
	}
}

func TestProgrammaticSceneSliderSyncDoesNotWriteTheConfig(t *testing.T) {
	// Fyne fires OnChangeEnded for SetValue too: restoring cached scenes or
	// finishing a detection moves the slider and must not save the config.
	f := &fakeScenes{scenes: threeScenes()}
	c, _ := sceneController(t, f)
	c.configPath = filepath.Join(t.TempDir(), "config.yaml")
	threshold := 0.55
	c.config.UI.SceneThreshold = &threshold
	c.scenes.scenes, c.scenes.have = threeScenes(), true
	c.syncSceneMarkers()
	if _, err := os.Stat(c.configPath); err == nil {
		t.Fatal("syncing the slider wrote the config file")
	}
	if got := c.sceneThreshold.Value; got != 0.55 {
		t.Errorf("slider = %v, want the configured 0.55", got)
	}
}
