package ui

import (
	"context"
	"path/filepath"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/storage"

	"git.skobk.in/skobkin/yave/internal/ffmpeg"
	"git.skobk.in/skobkin/yave/internal/ffprobe"
	"git.skobk.in/skobkin/yave/internal/model"
)

func (c *Controller) chooseInput() {
	d := dialog.NewFileOpen(func(reader fyne.URIReadCloser, err error) {
		if err != nil {
			dialog.ShowError(err, c.window)
			return
		}
		if reader == nil {
			return
		}
		_ = reader.Close()
		c.setInput(reader.URI().Path())
	}, c.window)
	if c.showAllFiles == nil || !c.showAllFiles.Checked {
		d.SetFilter(storage.NewExtensionFileFilter(mediaExtensions))
	}
	if loc := locationFor(c.dialogStartDir()); loc != nil {
		d.SetLocation(loc)
	}
	d.Resize(openFileDialogSize(c.window.Canvas().Size()))
	d.Show()
}

// dialogStartDir is the folder of the current input, else the last one.
func (c *Controller) dialogStartDir() string {
	if c.inputPath != "" {
		return filepath.Dir(c.inputPath)
	}
	return c.config.UI.LastInputDir
}

func (c *Controller) setInput(path string) {
	path = strings.TrimSpace(path)
	if path == "" {
		return
	}
	c.rememberInput(path)
	c.inputGeneration++
	gen := c.inputGeneration
	// Reset every per-input piece of state synchronously: until the new
	// file inspects, no stale metadata, trim selection, thumbnail, or
	// preview may feed refresh, validation, or conversion.
	c.media = model.MediaInfo{}
	c.inputPath = path
	c.inputEntry.SetText(path)
	c.inspecting = true
	if c.runControl == nil {
		// A finished run's status and output actions belong to the
		// previous input.
		c.status.SetText("Idle")
		c.hideRunActions()
	}
	c.cancelPoster()
	c.setMediaInspecting()
	c.cancelTrimWork()
	c.resetTrimForNewInput()
	c.refresh()
	ffprobePath := c.config.FFmpeg.FFprobePath
	go func() {
		info, err := ffprobe.Inspect(context.Background(), ffprobePath, path)
		c.dispatchUI(func() {
			// A late result from an older input must not overwrite the
			// newer input's state.
			if c.inputGeneration != gen {
				return
			}
			c.inspecting = false
			if err != nil {
				c.media = model.MediaInfo{}
				c.setMediaError("Inspection failed: " + err.Error())
			} else {
				c.media = info
				c.setMediaResult(info)
				c.startPoster(info)
				// Thumbnails and previews stay lazy: they start when
				// trimming is enabled (see setTrimEnabled).
				c.trimTimeline.SetDuration(info.Duration)
				c.syncChapterMarkers()
				c.loadCachedScenes()
				c.loadCachedWaveform()
			}
			c.refresh()
		})
	}()
}

func (c *Controller) detect() {
	// Snapshot the config and bump the generation on the UI thread: the
	// goroutine must not read c.config, which Settings and applyDetection
	// write, and a slow earlier detection must not overwrite a newer one.
	c.detectGeneration++
	gen := c.detectGeneration
	c.setDetecting(true)
	cfg := c.config.FFmpeg
	discover := c.discover
	if discover == nil {
		discover = ffmpeg.Discover
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		caps, err := discover(ctx, cfg)
		c.dispatchUI(func() {
			if c.detectGeneration != gen {
				return
			}
			c.setDetecting(false)
			if err != nil {
				c.caps = model.Capabilities{}
				c.setDetectionError("Detection failed: " + err.Error())
				// Re-derive the derived labels (the automatic-device
				// hint in particular): with caps emptied, a hint that
				// still names the previous detection's resolved device
				// would contradict the "no device detected" validation
				// error the next Start shows.
				c.updateVAAPIDeviceHint()
				c.updateQSVDeviceHint()
			} else {
				c.applyDetection(caps)
			}
			c.refresh()
		})
	}()
}

// applyDetection applies freshly discovered capabilities on the UI
// thread. The ordering is behavior: dropdown options are refreshed
// before anything selects from them (Fyne's Select.SetSelected
// silently drops values not in Options), devices are reconciled
// before a runnable profile is auto-selected because
// selectRunnableProfile may loadProfile a fallback candidate whose
// load-time reconciliation must win, and updateControlState runs
// before that selection so "encode + Encoder=copy" can never exist
// for it to trip over (the A3 silent-revert bug).
func (c *Controller) applyDetection(caps model.Capabilities) {
	// Detection reshapes dropdown options and may auto-select another
	// profile; a clean editor must stay clean through that.
	wasDirty := c.profileDirty()
	defer func() {
		if !wasDirty {
			c.markProfileSaved()
		}
	}()
	c.caps = caps
	prevFFmpeg := c.config.FFmpeg.FFmpegPath
	c.config.FFmpeg.FFmpegPath = caps.FFmpegPath
	c.config.FFmpeg.FFprobePath = caps.FFprobePath
	if c.config.FFmpeg.FFmpegPath != prevFFmpeg {
		// detect() resolves the configured path (a bare "ffmpeg" becomes
		// absolute) after the settings hook already ran; if resolution
		// moved the path again, rebind the preview extractor under it.
		c.onFFmpegExecutableChanged()
	}
	c.setDetectionResult(caps)
	c.updateEncoderOptions()
	c.updateDeviceOptions()
	c.updateGPUIndexOptions()
	c.reconcileDevices(false)
	c.updateControlState()
	c.selectRunnableProfile()
}

// setDetecting shows or hides the detection spinner and keeps the Retry
// button off while a detection or a conversion is running.
func (c *Controller) setDetecting(on bool) {
	c.detecting = on
	if c.detectActivity == nil {
		return
	}
	if on {
		c.detectActivity.Show()
		c.detectActivity.Start()
	} else {
		c.detectActivity.Stop()
		c.detectActivity.Hide()
	}
	c.updateRetryDetect()
}

// updateRetryDetect enables Retry detection only when nothing is running.
func (c *Controller) updateRetryDetect() {
	if c.retryDetect == nil {
		return
	}
	if c.detecting || c.runControl != nil {
		c.retryDetect.Disable()
	} else {
		c.retryDetect.Enable()
	}
}
