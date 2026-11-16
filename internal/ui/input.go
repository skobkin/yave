package ui

import (
	"context"
	"fmt"
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
	d.SetFilter(storage.NewExtensionFileFilter([]string{".mkv", ".mp4", ".webm", ".mov", ".avi", ".m4v"}))
	d.Show()
}

func (c *Controller) setInput(path string) {
	path = strings.TrimSpace(path)
	if path == "" {
		return
	}
	c.inputGeneration++
	gen := c.inputGeneration
	// Reset every per-input piece of state synchronously: until the new
	// file inspects, no stale metadata, trim selection, thumbnail, or
	// preview may feed refresh, validation, or conversion.
	c.media = model.MediaInfo{}
	c.inputPath = path
	c.inputEntry.SetText(path)
	c.mediaInfo.SetText("Inspecting input...")
	c.cancelTrimWork()
	c.resetTrimForNewInput()
	c.refresh()
	go func() {
		info, err := ffprobe.Inspect(context.Background(), c.config.FFmpeg.FFprobePath, path)
		c.dispatchUI(func() {
			// A late result from an older input must not overwrite the
			// newer input's state.
			if c.inputGeneration != gen {
				return
			}
			if err != nil {
				c.media = model.MediaInfo{}
				c.mediaInfo.SetText("Inspection failed: " + err.Error())
			} else {
				c.media = info
				c.mediaInfo.SetText(formatMediaInfo(info))
				// Thumbnails and previews stay lazy: they start when
				// trimming is enabled (see setTrimEnabled).
				c.trimTimeline.SetDuration(info.Duration)
			}
			c.refresh()
		})
	}()
}

func (c *Controller) detect() {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		caps, err := ffmpeg.Discover(ctx, c.config.FFmpeg)
		fyne.Do(func() {
			if err != nil {
				c.caps = model.Capabilities{}
				c.detection.SetText("Detection failed: " + err.Error())
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
	c.caps = caps
	c.config.FFmpeg.FFmpegPath = caps.FFmpegPath
	c.config.FFmpeg.FFprobePath = caps.FFprobePath
	summary := fmt.Sprintf("%s\n%s\nHardware acceleration: %s\nHardware devices: %s",
		caps.FFmpegVersion, caps.FFprobeVersion, strings.Join(caps.HWAccels, ", "), hardwareDevicesSummary(caps))
	if devices := vaapiDevicesSummary(caps); devices != "" {
		summary += "\nVAAPI devices: " + devices
	}
	c.detection.SetText(summary)
	c.updateEncoderOptions()
	c.updateDeviceOptions()
	c.reconcileDevices(false)
	c.updateControlState()
	c.selectRunnableProfile()
}

func formatMediaInfo(info model.MediaInfo) string {
	lines := []string{
		fmt.Sprintf("Format: %s | Duration: %s | Bitrate: %s", info.Format, info.Duration.Round(time.Second), formatBitrate(info.Bitrate)),
	}
	if v, ok := info.PrimaryVideo(); ok {
		lines = append(lines, fmt.Sprintf("Video: %s, %dx%d, %s fps, %s, range %s",
			v.Codec, v.Width, v.Height, v.FrameRate, v.PixelFormat, v.ColorRange))
	}
	for i, a := range info.AudioStreams {
		lines = append(lines, fmt.Sprintf("Audio %d: %s, %d Hz, %d channels", i+1, a.Codec, a.SampleRate, a.Channels))
	}
	lines = append(lines, fmt.Sprintf("Subtitles: %d | Chapters: %d", len(info.SubtitleStreams), info.ChapterCount))
	return strings.Join(lines, "\n")
}

func formatBitrate(value int64) string {
	if value <= 0 {
		return "unknown"
	}
	return fmt.Sprintf("%.1f Mbps", float64(value)/1_000_000)
}
