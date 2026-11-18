package ui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"fyne.io/fyne/v2/dialog"

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
		req := templates.Request{
			InputPath: c.inputPath,
			Extension: p.Container,
			Profile:   p.Name,
			Codec:     p.Video.Encoder,
			Width:     width,
			Height:    height,
			Time:      time.Now(),
		}
		// A multi-part separate export only seeds its part names from
		// the base and never writes it: resolving with the
		// input-collision check would mark a template like
		// {dir}/{name}.{ext} invalid whenever the base equals the
		// input, even though every actual target is safe.
		var resolved string
		var err error
		if c.trimEnabled && c.trimExportMode == model.TrimExportSeparate && len(c.trimSelection.Segments) > 1 {
			resolved, err = templates.ResolveParts(p.OutputTemplate, req)
		} else {
			resolved, err = templates.Resolve(p.OutputTemplate, req)
		}
		if err != nil {
			c.outputPreview.SetText("Invalid template: " + err.Error())
		} else {
			c.outputPath = resolved
			c.renderExportPreview(p, resolved)
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
	// warning + join-export checks) recomputes here so it stays
	// synchronized with every state change that flows through refresh().
	c.updateTrimMessages(p.Video.Encoder, p.Audio)
	c.updateSegmentButtons()
	if c.runControl == nil {
		c.start.Enable()
	} else {
		c.start.Disable()
	}
}

func (c *Controller) startConversion() {
	p := c.profileFromWidgets()
	// A multi-part separate export never writes the resolved base —
	// every target carries a _partNN suffix — so its base resolves
	// without the input-collision check and validation sees the
	// per-part targets (the base equaling the input is not a defect:
	// nothing writes it). Every other mode writes the base itself.
	resolve := c.resolveOutput
	trim := c.trimRequest()
	multiPart := trim.Active() && c.trimExportMode == model.TrimExportSeparate && len(trim.Segments) > 1
	if multiPart {
		resolve = c.resolveOutputParts
	}
	resolved, resolveErr := resolve(p, time.Now())
	var result validation.Result
	switch {
	case resolveErr != nil:
		// The resolution error alone is reported below; deriving
		// targets from an unresolved base would only add noise.
	case multiPart:
		result = validation.ValidateTargets(p, &c.media, c.caps, c.inputPath,
			exportTargetPaths(model.TrimExportSeparate, trim, resolved))
	default:
		result = validation.Validate(p, &c.media, c.caps, c.inputPath, resolved)
	}
	if c.trimEnabled {
		result.Merge(validation.ValidateTrim(c.trimSelection, &c.media, p.Video.Encoder))
		// Join-export checks (video-copy rejection, audio-copy warning)
		// gate BEFORE any dialog or staging directory is created.
		result.Merge(validation.ValidateTrimExport(c.trimSelection, c.trimExportMode, &c.media, p.Video.Encoder, p.Audio))
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

	// Stat every target before the dialog: exactly the files that exist
	// now are named, and only those may later be published with replace
	// semantics. The base template is resolved once here so every part
	// of a multi-segment export shares one {datetime}.
	targets := exportTargetPaths(c.trimExportMode, c.trimRequest(), resolved)
	// A target that exists but is not a regular file (typically a
	// directory) can never be published over — reject the run before
	// the overwrite dialog instead of failing halfway through a
	// multi-segment export after every part encoded successfully.
	if problem := unpublishableTarget(targets); problem != "" {
		c.showRunError("Validation failed", "Error: "+problem)
		return
	}
	existing := existingPaths(targets)
	start := func(approved map[string]bool) {
		job, err := c.buildTrimExportJob(cp, resolved, approved)
		if err != nil {
			c.showRunError("Export planning failed", err.Error())
			return
		}
		c.runJob(job, messages)
	}
	if len(existing) > 0 {
		dialog.NewConfirm("Overwrite output?", overwritePromptMessage(existing), func(ok bool) {
			if ok {
				approved := make(map[string]bool, len(existing))
				for _, path := range existing {
					approved[path] = true
				}
				start(approved)
			}
		}, c.window).Show()
		return
	}
	start(map[string]bool{})
}

func (c *Controller) showRunError(status, message string) {
	c.status.SetText(status)
	c.progressInfo.SetText("")
	c.progress.SetValue(0)
	c.log.SetText(message)
	c.tabs.Select(c.runTab)
	c.setProgressVisible(false)
}

// runJob starts a staged export job. The job carries every stage's
// command, duration, and publication closure plus the config snapshot,
// so nothing below this point touches mutable controller state. The
// published-output tally counts stages whose OutputPath the runner
// reports as successfully published — join-mode intermediates (empty
// OutputPath) are never counted and never described as published.
func (c *Controller) runJob(job *trimExportJob, messages []string) {
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
	c.setProgressVisible(true)

	stages := job.stages
	preview := stages[0].Plan.Preview
	if len(stages) > 1 {
		preview += "\n\n" + stagedExportNote(len(stages))
	}
	c.command.SetText(preview)

	// Warnings deduplicate across plans: every segment of a
	// subtitle-carrying input repeats the skip warning, and once is
	// enough.
	seen := make(map[string]bool)
	var warnings []string
	for _, stage := range stages {
		for _, warning := range stage.Plan.Warnings {
			if !seen[warning] {
				seen[warning] = true
				warnings = append(warnings, warning)
			}
		}
	}

	runLog, logErr := openLastRunLog()
	c.activeRunLog = runLog
	if logErr != nil {
		runLog.AppendLine("Warning: complete conversion log is unavailable: " + logErr.Error())
	}
	if len(stages) == 1 {
		runLog.AppendLine("Command: " + stages[0].Plan.Preview)
	} else {
		for i, stage := range stages {
			runLog.AppendLine(fmt.Sprintf("Command (%d of %d): %s", i+1, len(stages), stage.Plan.Preview))
		}
	}
	for _, warning := range warnings {
		runLog.AppendLine("Warning: " + warning)
	}
	for _, message := range messages {
		runLog.AppendLine(message)
	}
	runLog.AppendLine("")
	initialLog, _ := runLog.LiveSnapshot()
	c.log.SetText(initialLog)
	stopLogUpdates := c.startRunLogUpdates(runLog, control)

	published := 0
	runDone := make(chan struct{})
	c.runDone = runDone
	go func() {
		// quitApplication waits on this after Terminate: cleanup below
		// must run before the process exits (see quitApplication).
		defer close(runDone)
		err := c.runStages(control, job.stages, transcode.Callbacks{
			OnLog: func(line string) {
				runLog.AppendLine(line)
			},
			OnProgress: func(p model.Progress) {
				c.dispatchUI(func() {
					c.progress.SetValue(p.Percentage)
					c.setTrayProgress(p.Percentage)
					c.progressInfo.SetText(formatProgressInfo(p))
				})
			},
			OnStageDone: func(_ int, _ int, stage transcode.Stage, err error) {
				if err != nil {
					// The failure is reported by the returned error and
					// the status line; no per-stage line needed.
					return
				}
				if stage.OutputPath != "" {
					runLog.AppendLine(stage.Label + " published → " + stage.OutputPath)
					published++
					return
				}
				// Processing-only stage (join intermediates): staging is
				// deliberately not described as published.
				runLog.AppendLine(stage.Label + " encoded → " + stage.Plan.OutputPath)
			},
		})
		close(stopLogUpdates)
		job.cleanup()
		for _, warning := range job.warnings.drain() {
			runLog.AppendLine("Warning: " + warning)
		}
		_ = runLog.Close()
		finalLog, _ := runLog.LiveSnapshot()
		c.dispatchUI(func() {
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
			c.setRunResult(err, published, len(job.outputs))
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

func (c *Controller) setRunResult(err error, published, total int) {
	c.resetTrayProgress()
	switch err {
	case nil:
		c.status.SetText("Completed")
		c.progress.SetValue(1)
	case context.Canceled: //nolint:errorlint // context.Canceled is a sentinel; == comparison is intentional
		c.status.SetText(partialStatus("Canceled", published, total))
	default:
		c.status.SetText(partialStatus("Failed: "+err.Error(), published, total))
		c.tabs.Select(c.runTab)
	}
	c.setProgressVisible(false)
}

// partialStatus augments a canceled/failed status with the number of
// published user outputs. A zero tally keeps today's plain texts — in
// join mode the intermediates were temporary processing and must not be
// claimed as outputs; a partial tally can only arise in separate mode,
// where every published part survives the failure.
func partialStatus(base string, published, total int) string {
	if published == 0 || published >= total {
		return base
	}
	return fmt.Sprintf("%s (%d of %d parts written)", base, published, total)
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

// resolveOutputParts resolves the naming base for a multi-part separate
// export. The base is only a seed there — every written target carries
// a _partNN suffix — so the input-collision check that guards
// single-target runs is skipped: a template like {dir}/{name}.{ext}
// stays usable even though the base equals the input, and the per-part
// targets are validated when the export is planned.
func (c *Controller) resolveOutputParts(p model.Profile, at time.Time) (string, error) {
	width, height := dimensions(p, c.media)
	return templates.ResolveParts(p.OutputTemplate, templates.Request{
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
