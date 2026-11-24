package ui

import (
	"context"
	"fmt"
	"os"
	"time"

	"fyne.io/fyne/v2"

	"git.skobk.in/skobkin/yave/internal/analysis"
	"git.skobk.in/skobkin/yave/internal/profiles"
)

// Scene detection is an opt-in background analysis: one FFmpeg pass over
// the input scores every frame, the cuts above a low floor are kept, and
// the threshold slider filters them live — moving it never reruns FFmpeg.
// The result is cached per input (path, size, modification time), the run
// is cancellable, and an export cancels it.
const (
	sceneDefaultThreshold = 0.3
	sceneThresholdMin     = analysis.DefaultSceneFloor
	sceneThresholdMax     = 0.9
	sceneCacheSize        = 4
)

type sceneCacheEntry struct {
	key    string
	scenes []analysis.Scene
}

// sceneState is the scene-detection bookkeeping. UI thread only.
type sceneState struct {
	job     bgJob
	running bool
	scenes  []analysis.Scene // every cut at or above the floor, for the current input
	have    bool             // a detection (or cache hit) filled scenes
	cache   []sceneCacheEntry
	// syncing suppresses the slider's change callbacks while the
	// controller sets its value.
	syncing bool

	// detect runs one detection; seam over analysis.DetectScenes so tests
	// can intercept it.
	detect func(ctx context.Context, o analysis.SceneOptions, progress func(time.Duration)) ([]analysis.Scene, error)
}

func (s *sceneState) detector() func(context.Context, analysis.SceneOptions, func(time.Duration)) ([]analysis.Scene, error) {
	if s.detect != nil {
		return s.detect
	}
	return analysis.DetectScenes
}

// sceneDetectionAvailable reports whether the FFmpeg build lists every
// filter the detection needs. A build whose filter listing could not be
// read (nil) offers nothing, per the capability rule: never assume a
// feature exists.
func (c *Controller) sceneDetectionAvailable() bool {
	for _, name := range analysis.RequiredSceneFilters {
		if !c.caps.Filters[name] {
			return false
		}
	}
	return true
}

// sceneBlockReason explains why detection cannot start now, or "".
func (c *Controller) sceneBlockReason() string {
	_, hasVideo := c.media.PrimaryVideo()
	switch {
	case c.inputPath == "" || c.media.Duration <= 0:
		return "Open a video on the Input tab first"
	case !c.trimEnabled:
		return "Enable trimming to detect scenes"
	case !hasVideo:
		return "The input has no video stream"
	case c.runControl != nil:
		return "Not available while an export runs"
	case c.config.FFmpeg.FFmpegPath == "":
		return "FFmpeg is not available"
	}
	return ""
}

// sceneThresholdValue is the configured threshold, clamped to the
// slider's range.
func (c *Controller) sceneThresholdValue() float64 {
	v := sceneDefaultThreshold
	if c.config.UI.SceneThreshold != nil {
		v = *c.config.UI.SceneThreshold
	}
	return min(max(v, sceneThresholdMin), sceneThresholdMax)
}

// updateSceneControls reconciles the scene row with the current state. It
// runs from refresh().
func (c *Controller) updateSceneControls() {
	if c.sceneDetect == nil {
		return
	}
	available := c.sceneDetectionAvailable()
	setVisible(c.trimScenesBox, available)
	reason := c.sceneBlockReason()
	switch {
	case c.scenes.running:
		c.sceneDetect.SetText("Cancel scene detection")
		c.sceneDetect.Enable()
	default:
		c.sceneDetect.SetText("Detect scenes")
		setEnabled(c.sceneDetect, available && reason == "")
	}
	showThreshold := available && c.scenes.have
	setVisible(c.sceneThresholdBox, showThreshold)
}

// setVisible shows or hides an object; a nil container is ignored (tests
// that never built the layout).
func setVisible(o *fyne.Container, visible bool) {
	if o == nil {
		return
	}
	if visible {
		o.Show()
	} else {
		o.Hide()
	}
}

func (c *Controller) setSceneStatus(text string) {
	if c.sceneStatus != nil {
		setStatusLabel(c.sceneStatus, text)
	}
}

// toggleSceneDetection is the Detect / Cancel button.
func (c *Controller) toggleSceneDetection() {
	if c.scenes.running {
		c.cancelSceneDetection("Scene detection cancelled")
		return
	}
	c.startSceneDetection()
}

// startSceneDetection runs one detection pass in the background.
func (c *Controller) startSceneDetection() {
	if c.scenes.running || !c.sceneDetectionAvailable() || c.sceneBlockReason() != "" {
		return
	}
	ctx, job := c.scenes.job.start(context.Background())
	c.scenes.running = true
	c.setSceneStatus("Detecting scenes… 0%")
	c.updateSceneControls()

	opts := analysis.SceneOptions{
		FFmpegPath: c.config.FFmpeg.FFmpegPath,
		Input:      c.inputPath,
		Floor:      analysis.DefaultSceneFloor,
		Duration:   c.media.Duration,
	}
	gen := c.inputGeneration
	detect := c.scenes.detector()
	key := inputFingerprint(c.inputPath)
	duration := c.media.Duration
	lastPct := -1 // touched only by the progress callback's goroutine
	go func() {
		scenes, err := detect(ctx, opts, func(done time.Duration) {
			pct := 0
			if duration > 0 {
				pct = int(min(max(done, 0), duration) * 100 / duration)
			}
			if pct == lastPct {
				return
			}
			lastPct = pct
			c.dispatchUI(func() {
				if c.inputGeneration == gen && c.scenes.job.current(job) {
					c.setSceneStatus(fmt.Sprintf("Detecting scenes… %d%%", pct))
				}
			})
		})
		c.dispatchUI(func() {
			if c.inputGeneration != gen || !c.scenes.job.current(job) {
				return // cancelled, or a newer input owns the state
			}
			c.sceneDetectionFinished(key, scenes, err)
		})
	}()
}

func (c *Controller) sceneDetectionFinished(key string, scenes []analysis.Scene, err error) {
	c.scenes.running = false
	if err != nil {
		c.setSceneStatus("Scene detection failed: " + err.Error())
		c.updateSceneControls()
		return
	}
	c.scenes.scenes, c.scenes.have = scenes, true
	c.storeSceneCache(key, scenes)
	c.syncSceneMarkers()
	c.updateSceneControls()
}

// cancelSceneDetection stops a running detection and reports why.
func (c *Controller) cancelSceneDetection(status string) {
	wasRunning := c.scenes.running
	c.scenes.job.stop()
	c.scenes.running = false
	if wasRunning || status == "" {
		c.setSceneStatus(status)
	}
	c.updateSceneControls()
}

// cancelAnalysisWork stops the background analyses that compete with an
// export for CPU and disk. Scene detection is left cancelled for the user
// to restart; later analyses (the waveform) restart themselves.
func (c *Controller) cancelAnalysisWork() {
	if c.scenes.running {
		c.cancelSceneDetection("Scene detection cancelled: an export started")
	}
}

// clearScenes drops the current input's results for a new input.
func (c *Controller) clearScenes() {
	c.scenes.job.stop()
	c.scenes.running = false
	c.scenes.scenes, c.scenes.have = nil, false
	c.trimTimeline.SetMarkers(markerScene, nil)
	c.setSceneStatus("")
	c.updateSceneControls()
}

// loadCachedScenes restores the results of an earlier detection of this
// very file (same path, size and modification time).
func (c *Controller) loadCachedScenes() {
	key := inputFingerprint(c.inputPath)
	if key == "" {
		return
	}
	for _, entry := range c.scenes.cache {
		if entry.key == key {
			c.scenes.scenes, c.scenes.have = entry.scenes, true
			c.syncSceneMarkers()
			c.updateSceneControls()
			return
		}
	}
}

// storeSceneCache remembers a result, most recent first, bounded.
func (c *Controller) storeSceneCache(key string, scenes []analysis.Scene) {
	if key == "" {
		return
	}
	kept := make([]sceneCacheEntry, 0, sceneCacheSize)
	kept = append(kept, sceneCacheEntry{key: key, scenes: scenes})
	for _, entry := range c.scenes.cache {
		if entry.key != key && len(kept) < sceneCacheSize {
			kept = append(kept, entry)
		}
	}
	c.scenes.cache = kept
}

// inputFingerprint identifies a file's content cheaply: path, size and
// modification time. A file that cannot be stat'ed has none and is never
// cached.
func inputFingerprint(path string) string {
	info, err := os.Stat(path)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%s|%d|%d", path, info.Size(), info.ModTime().UnixNano())
}

// syncSceneMarkers draws the cuts at or above the threshold and updates
// the slider's label and the count.
func (c *Controller) syncSceneMarkers() {
	threshold := c.sceneThresholdValue()
	shown := analysis.FilterScenes(c.scenes.scenes, threshold)
	markers := make([]timelineMarker, len(shown))
	for i, s := range shown {
		markers[i] = timelineMarker{At: s.At, Label: fmt.Sprintf("Scene cut (score %.2f)", s.Score)}
	}
	c.trimTimeline.SetMarkers(markerScene, markers)
	if c.sceneThresholdLabel != nil {
		c.sceneThresholdLabel.SetText(fmt.Sprintf("Threshold %.2f", threshold))
	}
	if c.sceneThreshold != nil {
		c.scenes.syncing = true
		c.sceneThreshold.SetValue(threshold)
		c.scenes.syncing = false
	}
	c.setSceneStatus(fmt.Sprintf("%d scene cut(s)", len(shown)))
}

// onSceneThreshold follows the slider live: only the markers change.
func (c *Controller) onSceneThreshold(v float64) {
	if c.scenes.syncing {
		return
	}
	v = min(max(v, sceneThresholdMin), sceneThresholdMax)
	c.config.UI.SceneThreshold = &v
	c.syncSceneMarkers()
}

// saveSceneThreshold persists the threshold when the slider is released.
func (c *Controller) saveSceneThreshold() {
	if c.config.UI.SceneThreshold == nil || c.configPath == "" {
		return
	}
	_ = profiles.Save(c.configPath, c.config) //nolint:errcheck // a UI preference; the next save retries
}
