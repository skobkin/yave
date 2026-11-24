package ui

import (
	"context"
	"fmt"

	"git.skobk.in/skobkin/yave/internal/analysis"
)

// The waveform strip is a background analysis of the input's first audio
// stream: one FFmpeg pass reduces it to a min/max envelope while it
// streams (bounded memory), drawn progressively under the thumbnails. It
// starts when trimming is enabled, is independent of the profile, is
// cached per input (path, size, modification time), and is cancelled when
// a new input is loaded or an export starts — and restarted after the
// export while trimming is still on.

const waveCacheSize = 4

type waveCacheEntry struct {
	key  string
	wave analysis.Waveform
}

// waveState is the waveform bookkeeping. UI thread only.
type waveState struct {
	job     bgJob
	running bool
	have    bool // a complete waveform is installed
	failed  bool
	cache   []waveCacheEntry

	// compute runs one analysis; seam over analysis.ComputeWaveform so
	// tests can intercept it.
	compute func(ctx context.Context, o analysis.WaveformOptions, onUpdate func(analysis.Waveform)) (analysis.Waveform, error)
}

func (w *waveState) computer() func(context.Context, analysis.WaveformOptions, func(analysis.Waveform)) (analysis.Waveform, error) {
	if w.compute != nil {
		return w.compute
	}
	return analysis.ComputeWaveform
}

// hasAudioInput reports whether the loaded input has an audio stream to
// draw: the lane is hidden otherwise.
func (c *Controller) hasAudioInput() bool {
	return c.inputPath != "" && c.media.Duration > 0 && len(c.media.AudioStreams) > 0
}

// updateWaveform reconciles the lane and the analysis with the current
// state. It runs from refresh(), so everything below is a cheap no-op
// when nothing changed.
func (c *Controller) updateWaveform() {
	w := &c.waves
	if !c.hasAudioInput() {
		c.cancelWaveform("")
		c.setWaveformLane(false)
		return
	}
	if w.have {
		c.setWaveformLane(true)
		return
	}
	if !c.trimEnabled {
		// A failed analysis may be retried the next time trimming is
		// enabled.
		w.failed = false
		if !w.running {
			c.setWaveformLane(false)
		}
		return
	}
	if w.running || w.failed || c.runControl != nil || c.config.FFmpeg.FFmpegPath == "" {
		return
	}
	c.startWaveform()
}

// setWaveformLane shows or hides the lane and re-lays-out the trim tab,
// whose minimum height changes with it.
func (c *Controller) setWaveformLane(show bool) {
	if c.trimTimeline.WaveformLane() == show {
		return
	}
	c.trimTimeline.SetWaveformLane(show)
	c.relayoutTrimTab()
}

// relayoutTrimTab re-lays-out the containers around the timeline after
// its minimum size changed (Fyne does not propagate that by itself).
func (c *Controller) relayoutTrimTab() {
	if c.trimControlsBox != nil {
		c.trimControlsBox.Refresh()
	}
	if c.trimTabBox != nil {
		c.trimTabBox.Refresh()
	}
}

func (c *Controller) setWaveStatus(text string) {
	if c.waveStatus != nil {
		setStatusLabel(c.waveStatus, text)
	}
}

// startWaveform runs the analysis in the background.
func (c *Controller) startWaveform() {
	w := &c.waves
	ctx, job := w.job.start(context.Background())
	w.running = true
	c.setWaveformLane(true)
	c.setWaveStatus("Computing waveform… 0%")

	opts := analysis.WaveformOptions{
		FFmpegPath: c.config.FFmpeg.FFmpegPath,
		Input:      c.inputPath,
		Duration:   c.media.Duration,
	}
	gen := c.inputGeneration
	compute := w.computer()
	key := inputFingerprint(c.inputPath)
	duration := c.media.Duration
	go func() {
		wave, err := compute(ctx, opts, func(partial analysis.Waveform) {
			c.dispatchUI(func() {
				if c.inputGeneration != gen || !w.job.current(job) {
					return
				}
				c.trimTimeline.SetWaveform(partial)
				pct := 0
				if duration > 0 {
					pct = int(min(partial.Duration(), duration) * 100 / duration)
				}
				c.setWaveStatus(fmt.Sprintf("Computing waveform… %d%%", pct))
			})
		})
		c.dispatchUI(func() {
			if c.inputGeneration != gen || !w.job.current(job) {
				return // cancelled, or a newer input owns the lane
			}
			c.waveformFinished(key, wave, err)
		})
	}()
}

func (c *Controller) waveformFinished(key string, wave analysis.Waveform, err error) {
	w := &c.waves
	w.running = false
	if err != nil {
		w.failed = true
		c.trimTimeline.SetWaveform(analysis.Waveform{})
		c.setWaveformLane(false)
		c.setWaveStatus("Waveform unavailable: " + err.Error())
		return
	}
	w.have = true
	c.trimTimeline.SetWaveform(wave)
	c.storeWaveCache(key, wave)
	c.setWaveStatus("")
}

// cancelWaveform stops a running analysis and drops its partial result;
// status is shown in its place ("" clears the line).
func (c *Controller) cancelWaveform(status string) {
	w := &c.waves
	if !w.running {
		return
	}
	w.job.stop()
	w.running = false
	c.trimTimeline.SetWaveform(analysis.Waveform{})
	c.setWaveStatus(status)
}

// clearWaveform drops the current input's waveform for a new input.
func (c *Controller) clearWaveform() {
	w := &c.waves
	w.job.stop()
	w.running, w.have, w.failed = false, false, false
	c.trimTimeline.SetWaveform(analysis.Waveform{})
	c.setWaveformLane(false)
	c.setWaveStatus("")
}

// loadCachedWaveform restores the waveform of an earlier analysis of this
// very file.
func (c *Controller) loadCachedWaveform() {
	key := inputFingerprint(c.inputPath)
	if key == "" || !c.hasAudioInput() {
		return
	}
	for _, entry := range c.waves.cache {
		if entry.key == key {
			c.waves.have = true
			c.trimTimeline.SetWaveform(entry.wave)
			c.setWaveformLane(true)
			return
		}
	}
}

// storeWaveCache remembers a result, most recent first, bounded.
func (c *Controller) storeWaveCache(key string, wave analysis.Waveform) {
	if key == "" {
		return
	}
	kept := make([]waveCacheEntry, 0, waveCacheSize)
	kept = append(kept, waveCacheEntry{key: key, wave: wave})
	for _, entry := range c.waves.cache {
		if entry.key != key && len(kept) < waveCacheSize {
			kept = append(kept, entry)
		}
	}
	c.waves.cache = kept
}
