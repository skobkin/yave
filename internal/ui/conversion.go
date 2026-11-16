package ui

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/dialog"

	"git.skobk.in/skobkin/yave/internal/ffmpeg"
	"git.skobk.in/skobkin/yave/internal/model"
	"git.skobk.in/skobkin/yave/internal/templates"
	"git.skobk.in/skobkin/yave/internal/transcode"
	"git.skobk.in/skobkin/yave/internal/validation"
)

const (
	progressInfoWidthSample = "999h59m59s | 999.9x"
	cancelGracePeriod       = 5 * time.Second
	runLogFlushInterval     = 100 * time.Millisecond
)

type cancellationStage uint8

type conversionControl interface {
	RequestCancel()
	Terminate()
}

const (
	cancellationRunning cancellationStage = iota
	cancellationRequested
	cancellationTerminateAvailable
	cancellationTerminating
)

func (c *Controller) refresh() {
	// Materialize the automatic VAAPI device so the previewed command
	// shows the resolved -vaapi_device, not the empty auto value.
	p := c.commandProfile()
	c.outputPath = ""
	c.command.SetText("")
	if c.inputPath != "" {
		width, height := dimensions(p, c.media)
		resolved, err := templates.Resolve(p.OutputTemplate, templates.Request{
			InputPath: c.inputPath,
			Extension: p.Container,
			Profile:   p.Name,
			Codec:     p.Video.Encoder,
			Width:     width,
			Height:    height,
			Time:      time.Now(),
		})
		if err != nil {
			c.outputPreview.SetText("Invalid template: " + err.Error())
		} else {
			c.outputPath = resolved
			c.outputPreview.SetText(resolved)
			plan, buildErr := ffmpeg.BuildCommand(ffmpeg.BuildRequest{
				FFmpegPath: c.config.FFmpeg.FFmpegPath,
				InputPath:  c.inputPath,
				OutputPath: c.outputPath,
				Profile:    p,
				Media:      c.media,
				Trim:       c.trimRequest(),
			})
			// Explain a blank preview instead of silently keeping the
			// stale text: automatic device selection makes unreachable
			// builds (no compatible VAAPI device) a normal preview state.
			if buildErr != nil {
				c.command.SetText("Command error: " + buildErr.Error())
			} else {
				c.command.SetText(plan.Preview)
			}
		}
	}
	// Trim availability follows the input: no file or an unknown
	// duration leaves trimming off until inspection completes.
	if c.inputPath != "" && c.media.Duration > 0 {
		c.trimEnable.Enable()
	} else {
		c.trimEnable.Disable()
		if c.trimEnabled {
			c.setTrimEnabled(false)
		}
	}
	// The Trim tab's validation message (boundary errors + stream-copy
	// warning) recomputes here so it stays synchronized with every
	// state change that flows through refresh().
	c.updateTrimMessages(p.Video.Encoder)
	if c.runControl == nil {
		c.start.Enable()
	} else {
		c.start.Disable()
	}
}

func (c *Controller) startConversion() {
	p := c.profileFromWidgets()
	resolved, resolveErr := c.resolveOutput(p, time.Now())
	result := validation.Validate(p, &c.media, c.caps, c.inputPath, resolved)
	if c.trimEnabled {
		result.Merge(validation.ValidateTrim(c.trimSelection, &c.media, p.Video.Encoder))
		// Invalid timecode entries keep the last valid selection for
		// display, but Start must never silently use those stale
		// boundaries.
		result.Errors = append(result.Errors, c.trimEntryIssues()...)
	}
	messages := issueMessages(result)
	if resolveErr != nil {
		messages = append([]string{"Error: " + resolveErr.Error()}, messages...)
	}
	if resolveErr != nil || !result.Valid() {
		c.showRunError("Validation failed", strings.Join(messages, "\n"))
		return
	}
	// The run uses the materialized profile (automatic VAAPI device
	// resolved to a concrete path); validation above deliberately saw
	// the raw profile so its auto semantics could apply.
	cp := c.commandProfile()
	start := func(overwrite bool) {
		plan, buildErr := ffmpeg.BuildCommand(ffmpeg.BuildRequest{
			FFmpegPath: c.config.FFmpeg.FFmpegPath,
			InputPath:  c.inputPath, OutputPath: resolved,
			Profile: cp, Media: c.media, Overwrite: overwrite,
			Trim: c.trimRequest(),
		})
		if buildErr != nil {
			c.showRunError("Command generation failed", buildErr.Error())
			return
		}
		c.run(plan, messages)
	}
	if _, statErr := os.Stat(resolved); statErr == nil {
		dialog.NewConfirm("Overwrite output?",
			"The output file already exists:\n"+resolved+"\n\nOverwrite it?",
			func(ok bool) {
				if ok {
					start(true)
				}
			}, c.window).Show()
		return
	}
	start(false)
}

func (c *Controller) showRunError(status, message string) {
	c.status.SetText(status)
	c.progressInfo.SetText("")
	c.progress.SetValue(0)
	c.log.SetText(message)
	c.tabs.Select(c.runTab)
	c.setProgressVisible(false)
}

func (c *Controller) run(plan model.RunPlan, messages []string) {
	control := transcode.NewRunControl()
	c.runControl = control
	c.cancelStage = cancellationRunning
	c.cancelSince = time.Time{}
	c.start.Disable()
	c.cancel.SetText("Cancel")
	c.cancel.Enable()
	c.status.SetText("Running")
	c.progress.SetValue(0)
	c.setTrayProgress(0)
	c.progressInfo.SetText("")
	c.command.SetText(plan.Preview)
	c.setProgressVisible(true)
	for _, warning := range plan.Warnings {
		messages = append(messages, "Warning: "+warning)
	}
	runLog, logErr := openLastRunLog()
	c.activeRunLog = runLog
	if logErr != nil {
		runLog.AppendLine("Warning: complete conversion log is unavailable: " + logErr.Error())
	}
	runLog.AppendLine("Command: " + plan.Preview)
	for _, message := range messages {
		runLog.AppendLine(message)
	}
	runLog.AppendLine("")
	initialLog, _ := runLog.LiveSnapshot()
	c.log.SetText(initialLog)
	stopLogUpdates := c.startRunLogUpdates(runLog, control)

	go func() {
		// With an active trim, ffmpeg's input-side -ss makes
		// out_time segment-relative, so progress must be measured
		// against the selected duration, not the whole input.
		duration := c.media.Duration
		if seg, ok := c.trimRequest().First(); ok {
			duration = seg.End - seg.Start
		}
		err := (transcode.Runner{}).Run(control, plan, duration, transcode.Callbacks{
			OnLog: func(line string) {
				runLog.AppendLine(line)
			},
			OnProgress: func(p model.Progress) {
				fyne.Do(func() {
					c.progress.SetValue(p.Percentage)
					c.setTrayProgress(p.Percentage)
					c.progressInfo.SetText(formatProgressInfo(p))
				})
			},
		})
		close(stopLogUpdates)
		_ = runLog.Close()
		finalLog, _ := runLog.LiveSnapshot()
		fyne.Do(func() {
			if c.runControl != control {
				return
			}
			c.stopCancelTimer()
			c.runControl = nil
			c.activeRunLog = nil
			c.cancelStage = cancellationRunning
			c.cancelSince = time.Time{}
			c.cancel.SetText("Cancel")
			c.cancel.Disable()
			c.log.SetText(finalLog)
			c.setRunResult(err)
			c.refresh()
		})
	}()
}

func (c *Controller) startRunLogUpdates(runLog *runLog, control conversionControl) chan struct{} {
	stop := make(chan struct{})
	go func() {
		ticker := time.NewTicker(runLogFlushInterval)
		defer ticker.Stop()
		completed := make(chan struct{}, 1)
		var displayedVersion uint64
		updatePending := false
		for {
			select {
			case <-ticker.C:
				if updatePending {
					continue
				}
				text, version := runLog.LiveSnapshot()
				if version == displayedVersion {
					continue
				}
				displayedVersion = version
				updatePending = true
				c.dispatchUI(func() {
					if c.runControl == control {
						c.log.SetText(text)
					}
					completed <- struct{}{}
				})
			case <-completed:
				updatePending = false
			case <-stop:
				return
			}
		}
	}()
	return stop
}

func formatProgressInfo(progress model.Progress) string {
	return fmt.Sprintf("%s | %s", progress.OutTime.Round(time.Second), progress.Speed)
}

func (c *Controller) setRunResult(err error) {
	c.resetTrayProgress()
	switch err {
	case nil:
		c.status.SetText("Completed")
		c.progress.SetValue(1)
	case context.Canceled: //nolint:errorlint // context.Canceled is a sentinel; == comparison is intentional
		c.status.SetText("Canceled")
	default:
		c.status.SetText("Failed: " + err.Error())
		c.tabs.Select(c.runTab)
	}
	c.setProgressVisible(false)
}

// setProgressVisible shows or hides the progress bar and its paired
// progressInfo label together. They are visually coupled in buildStatusBar,
// so the bar must never appear without its sibling.
func (c *Controller) setProgressVisible(visible bool) {
	if visible {
		c.progress.Show()
		c.progressInfo.Show()
		return
	}
	c.progress.Hide()
	c.progressInfo.Hide()
}

func (c *Controller) cancelConversion() {
	control := c.runControl
	if control == nil {
		return
	}
	switch c.cancelStage {
	case cancellationRunning:
		c.cancelStage = cancellationRequested
		c.cancelSince = time.Now()
		c.status.SetText("Canceling…")
		// Keep the control clickable. Under normal conditions the timer changes
		// its label to Terminate; if the UI callback is delayed, a click after
		// the grace period can still force termination based on wall time.
		c.cancel.Enable()
		control.RequestCancel()
		delay := c.cancellationDelay()
		c.cancelTimer = time.AfterFunc(delay, func() {
			c.dispatchUI(func() {
				if c.runControl != control || c.cancelStage != cancellationRequested {
					return
				}
				c.cancelStage = cancellationTerminateAvailable
				c.status.SetText("FFmpeg is not responding")
				c.cancel.SetText("Terminate")
				c.cancel.Enable()
			})
		})
	case cancellationRequested:
		if time.Since(c.cancelSince) < c.cancellationDelay() {
			return
		}
		c.terminateConversion(control)
	case cancellationTerminateAvailable:
		c.terminateConversion(control)
	}
}

func (c *Controller) cancellationDelay() time.Duration {
	if c.cancelDelay > 0 {
		return c.cancelDelay
	}
	return cancelGracePeriod
}

func (c *Controller) terminateConversion(control conversionControl) {
	c.cancelStage = cancellationTerminating
	c.status.SetText("Terminating…")
	c.cancel.SetText("Terminate")
	c.cancel.Disable()
	control.Terminate()
}

func (c *Controller) stopCancelTimer() {
	if c.cancelTimer != nil {
		c.cancelTimer.Stop()
		c.cancelTimer = nil
	}
}

func (c *Controller) resolveOutput(p model.Profile, at time.Time) (string, error) {
	width, height := dimensions(p, c.media)
	return templates.Resolve(p.OutputTemplate, templates.Request{
		InputPath: c.inputPath, Extension: p.Container, Profile: p.Name,
		Codec: p.Video.Encoder, Width: width, Height: height, Time: at,
	})
}

func issueMessages(result validation.Result) []string {
	items := make([]string, 0, len(result.Errors)+len(result.Warnings))
	for _, issue := range result.Errors {
		items = append(items, "Error: "+issue.Message)
	}
	for _, issue := range result.Warnings {
		items = append(items, "Warning: "+issue.Message)
	}
	return items
}
