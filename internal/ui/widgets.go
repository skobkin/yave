package ui

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"

	"image/color"

	"git.skobk.in/skobkin/yave/internal/encoder"
	"git.skobk.in/skobkin/yave/internal/model"
	"git.skobk.in/skobkin/yave/internal/resources"
)

func (c *Controller) createWidgets() {
	c.status = widget.NewLabel("Idle")
	// A long error must not widen the window; the log keeps the full text.
	c.status.Truncation = fyne.TextTruncateEllipsis
	c.detection = widget.NewLabel("Detecting FFmpeg...")
	c.detection.Wrapping = fyne.TextWrapWord
	c.retryDetect = widget.NewButton("Retry detection", c.detect)
	c.detectActivity = widget.NewActivity()
	c.detectActivity.Hide()
	c.inputEntry = widget.NewEntry()
	c.inputEntry.SetPlaceHolder("Select or drop a video file")
	c.inputEntry.OnSubmitted = c.setInput
	// An edited-but-unsubmitted path gates Start (see startBlockReason).
	c.inputEntry.OnChanged = func(string) { c.refresh() }
	c.mediaInfo = widget.NewLabel("No input selected")
	c.mediaInfo.Wrapping = fyne.TextWrapWord

	c.profile = widget.NewSelect(c.profileNames(), func(name string) {
		c.switchProfile(name)
	})
	c.container = widget.NewSelect([]string{model.ContainerMKV, model.ContainerMP4, model.ContainerWebM}, c.changed)
	c.output = widget.NewEntry()
	c.output.OnChanged = c.changed
	c.outputPreview = widget.NewLabel("")
	c.outputPreview.Wrapping = fyne.TextWrapWord

	videoOpts := append([]string{"copy"}, encoder.DefaultRegistry().VideoNames()...)
	c.videoEncoder = widget.NewSelect(videoOpts, c.changed)
	// rateControl options are populated per-encoder by updateControlState.
	c.rateControl = widget.NewSelect([]string{}, c.changed)
	c.rateControlHint = widget.NewLabel("")
	c.rateControlHint.Wrapping = fyne.TextWrapWord
	c.qp = newIntegerSlider(mustQPRange("hevc_vaapi"), 30, c.changed)
	c.crf = newIntegerSlider(mustCRFRange("libx265"), 28, c.changed)
	c.bitrate = entry(c.changed)
	c.bitrate.SetPlaceHolder("e.g. 6M")
	c.maxrate = entry(c.changed)
	c.maxrate.SetPlaceHolder("e.g. 8M")
	c.bufsize = entry(c.changed)
	c.bufsize.SetPlaceHolder("e.g. 12M")
	c.preset = entry(c.changed)
	c.presetSlider = newIntegerSlider(mustPresetRange("libsvtav1"), 6, c.changed)
	c.presetSlider.Hide()
	// Encoders with a curated preset list (Presets()) show the dropdown
	// instead of the free-text entry; exactly one stack child is visible
	// at a time (updateControlState owns the toggle).
	c.presetSelect = widget.NewSelect([]string{}, c.changed)
	c.presetSelect.Hide()
	c.presetControl = container.NewStack(c.preset, c.presetSlider.Object(), c.presetSelect)
	c.tune = entry(c.changed)
	// Same stack pattern as the preset control: encoders with a curated
	// tune list (Tunes()) show the dropdown, everyone else the entry.
	c.tuneSelect = widget.NewSelect([]string{}, c.changed)
	c.tuneSelect.Hide()
	c.tuneControl = container.NewStack(c.tune, c.tuneSelect)
	// Profile options are populated per-encoder by populateProfileOptions.
	c.videoProfile = widget.NewSelect([]string{}, c.changed)
	c.frameMode = widget.NewSelect([]string{"keep", "convert"}, c.changed)
	c.frameRate = entry(c.changed)
	c.frameRate.SetPlaceHolder("e.g. 30 or 30000/1001")
	c.maxWidth = numericEntry(c.changed)
	c.maxWidth.SetPlaceHolder("source size")
	c.maxHeight = numericEntry(c.changed)
	c.maxHeight.SetPlaceHolder("source size")
	c.keepAspect = widget.NewCheck("Keep aspect ratio", func(bool) { c.changed("") })
	c.noUpscale = widget.NewCheck("Prevent upscaling", func(bool) { c.changed("") })
	c.vaapiDevice = widget.NewSelect([]string{}, c.changed)
	c.qsvDevice = widget.NewSelect([]string{}, c.changed)
	c.vaapiDeviceHint = widget.NewLabel("")
	c.vaapiDeviceHint.Wrapping = fyne.TextWrapWord
	c.qsvDeviceHint = widget.NewLabel("")
	c.qsvDeviceHint.Wrapping = fyne.TextWrapWord
	c.gpuIndexEntry = numericEntry(c.changed)
	c.gpuIndexSelect = widget.NewSelect([]string{}, c.changed)
	c.gpuIndexSelect.Hide()
	c.gpuIndexControl = container.NewStack(c.gpuIndexEntry, c.gpuIndexSelect)

	c.audioMode = widget.NewSelect([]string{model.AudioCopy, model.AudioEncode}, c.changed)
	// "copy" is deliberately absent: copy mode has no encoder settings
	// (see updateAudioControls), so the dropdown only ever lists real
	// encoders.
	c.audioEncoder = widget.NewSelect(encoder.DefaultRegistry().AudioNames(), c.changed)
	c.audioBitrate = widget.NewSelect([]string{}, c.changed)
	c.compression = newIntegerSlider(mustAudioCompressionRange("flac"), 8, c.changed)
	c.allAudio = widget.NewCheck("Process all audio streams", func(bool) { c.changed("") })
	c.newStreamIncludeChecks()
	c.subtitles = widget.NewSelect([]string{model.SubtitleNone, model.SubtitleCopy}, c.changed)
	// The audio tab is one dynamic form rebuilt behind a signature; build
	// the initial (copy-mode) row set here so tests and buildLayout see
	// coherent state before the first updateControlState.
	c.audioFormBox = container.NewVBox()
	c.rebuildAudioForm()
	// Same for the video tab (initially just the Encoder row).
	c.videoFormBox = container.NewVBox()
	// The aspect checks live in a stable HBox so the video form's
	// pointer-based signature does not change on every videoFormItems
	// call.
	c.aspectFlags = container.NewHBox(c.keepAspect, c.noUpscale)
	c.rebuildVideoForm()

	c.command = widget.NewMultiLineEntry()
	c.command.Disable()
	c.command.Wrapping = fyne.TextWrapWord
	c.command.SetMinRowsVisible(3)

	// Trim tab: everything except the enable check lives in
	// trimControls and stays disabled until trimming is turned on.
	c.trimEnable = widget.NewCheck("Enable trimming", c.setTrimEnabled)
	c.trimTimeline = newTimeline()
	c.trimTimeline.stepFrame = func(at time.Duration, dir int) (time.Duration, bool) {
		// Manual frame seeking takes over from playback: stop it and let
		// the step show the exact frame it lands on.
		c.cancelPlayback()
		next, state := c.previews.index.step(at, dir, c.media.Duration)
		c.applyFrameStep(next, state)
		return next, state == frameStepMoved
	}
	c.tips = newTooltipLayer()
	c.loadVolume()
	c.transport = c.newTransportBar(false)
	c.trimPlay, c.trimPlaySelection = c.transport.play, c.transport.playSel
	c.trimZoomOut = newTipButton(c.tips, resources.Icon(resources.IconZoomOut),
		"Zoom out (Ctrl+wheel)", func() { c.trimTimeline.ZoomStep(1 / buttonZoomStepFactor) })
	c.trimZoomIn = newTipButton(c.tips, resources.Icon(resources.IconZoomIn),
		"Zoom in (Ctrl+wheel)", func() { c.trimTimeline.ZoomStep(buttonZoomStepFactor) })
	c.trimZoomFit = newTipButton(c.tips, resources.Icon(resources.IconFit),
		"Fit the whole video", c.trimTimeline.Fit)
	c.trimZoomSel = newTipButton(c.tips, resources.Icon(resources.IconZoomToSelection),
		"Zoom to the selected segment", c.trimTimeline.ZoomToSelection)
	c.trimSnap = widget.NewCheck("Snap", func(on bool) { c.trimTimeline.snapEnabled = on })
	c.trimSnap.SetChecked(true)
	c.trimPrevMarker = newTipButton(c.tips, resources.Icon(resources.IconPrevMarker),
		"Previous marker (chapter, scene or keyframe)", func() { c.jumpToMarker(-1) })
	c.trimNextMarker = newTipButton(c.tips, resources.Icon(resources.IconNextMarker),
		"Next marker (chapter, scene or keyframe)", func() { c.jumpToMarker(1) })
	c.sceneDetect = newTipButtonFunc(c.tips, nil, func() string {
		if c.scenes.running {
			return "Stop looking for scene changes"
		}
		if reason := c.sceneBlockReason(); reason != "" {
			return reason
		}
		return "Find scene changes and mark them on the timeline"
	}, c.toggleSceneDetection)
	c.sceneDetect.Text = "Detect scenes"
	c.sceneDetect.Importance = widget.MediumImportance
	c.sceneDetect.Disable()
	c.sceneThresholdLabel = widget.NewLabel("")
	c.sceneThreshold = widget.NewSlider(sceneThresholdMin, sceneThresholdMax)
	c.sceneThreshold.Step = 0.01
	c.sceneThreshold.SetValue(c.sceneThresholdValue())
	c.sceneThreshold.OnChanged = c.onSceneThreshold
	// Fyne fires OnChangeEnded for programmatic SetValue too; only a
	// release by the user may write the config.
	c.sceneThreshold.OnChangeEnded = func(float64) {
		if !c.scenes.syncing {
			c.saveSceneThreshold()
		}
	}
	c.sceneStatus = widget.NewLabel("")
	setStatusLabel(c.sceneStatus, "")
	c.waveStatus = widget.NewLabel("")
	setStatusLabel(c.waveStatus, "")
	c.trimChapters = widget.NewButton("Segments from chapters…", c.segmentsFromChapters)
	c.trimChapters.Hide() // shown while the input has chapters
	c.trimTimeline.onTogglePlay = c.togglePlayback
	c.trimTimeline.onPlayhead = c.onTimelinePlayhead
	c.trimTimeline.onSegments = c.onTimelineSegments
	c.trimTimeline.onSelected = c.onTimelineSelected
	c.trimTimeline.onHover = c.onTimelineHover
	c.trimTimeline.onView = c.onTimelineView
	// Ctrl+[ / Ctrl+] go through the same single-boundary handlers as
	// the Set In/Out buttons, so both entry points keep the opposite
	// entry's validity flag and in-progress text untouched.
	c.trimTimeline.onSetIn = c.setInFromPlayhead
	c.trimTimeline.onSetOut = c.setOutFromPlayhead
	timestampValidator := func(s string) error {
		if s == "" {
			return nil
		}
		_, err := parseTimestamp(s)
		return err
	}
	c.trimIn = entry(func(text string) {
		// A display write (setBoundaryDisplay) mirrors a committed value;
		// re-entering the editor would parse the truncated text back over
		// the exact selection.
		if c.displayingBoundaries {
			return
		}
		c.setTrimBoundary("in", text)
	})
	c.trimIn.Validator = timestampValidator
	c.trimIn.SetPlaceHolder("MM:SS.mmm or seconds")
	c.trimOut = entry(func(text string) {
		if c.displayingBoundaries {
			return
		}
		c.setTrimBoundary("out", text)
	})
	c.trimOut.Validator = timestampValidator
	c.trimOut.SetPlaceHolder("MM:SS.mmm or seconds")
	c.trimSetIn = widget.NewButton("Set In", c.setInFromPlayhead)
	c.trimSetOut = widget.NewButton("Set Out", c.setOutFromPlayhead)
	c.trimReset = widget.NewButton("Reset", func() {
		c.resetTrimSelection()
		c.refresh()
	})
	// Export mode + segment management share the trimSegmentsRow row.
	// The dropdown starts on the default (separate files) with its
	// callback attached only afterwards: SetSelected at creation must
	// mirror the field without counting as a user edit.
	c.trimExport = widget.NewSelect([]string{
		model.TrimExportSeparate.String(),
		model.TrimExportJoin.String(),
	}, nil)
	c.trimExportMode = model.TrimExportSeparate
	c.trimExport.SetSelected(c.trimExportMode.String())
	c.trimExport.OnChanged = func(option string) {
		c.trimExportMode = trimExportModeFromOption(option)
		c.changed(option)
	}
	c.trimHint = widget.NewLabel("")
	c.trimHint.TextStyle = fyne.TextStyle{Italic: true}
	// The reason trimming is unavailable gets its own line above the
	// enable row, hidden while empty: beside the enable check it forced the
	// whole row (and with it the window) several hundred pixels wider.
	setStatusLabel(c.trimHint, "")
	c.trimAdd = widget.NewButton("Add segment", c.addTrimSegment)
	c.trimDelete = widget.NewButton("Delete segment", c.deleteTrimSegment)
	c.trimDuration = widget.NewLabel("")
	c.trimMessage = widget.NewLabel("")
	c.trimMessage.Wrapping = fyne.TextWrapWord
	// Both status lines start (and stay) hidden while empty, so they
	// cost no vertical space between the trim rows.
	c.setTrimMessage("")
	c.previewStatus = widget.NewLabel("")
	c.previewStatus.Wrapping = fyne.TextWrapWord
	c.setPreviewStatus("")
	c.previewTimeLabel = widget.NewLabel("")
	c.hoverTimeLabel = widget.NewLabel("")
	c.previewImage = canvas.NewImageFromImage(nil)
	c.previewImage.FillMode = canvas.ImageFillContain
	c.previewVeil = canvas.NewRectangle(color.Transparent)
	// Hidden until a pending extraction tints the stale frame.
	c.previewVeil.Hide()
	c.previewSpinner = newSpinner()
	// Hidden from birth: Show and Hide start and stop the rotation
	// animation together with the widget's visibility.
	c.previewSpinner.Hide()
	c.trimControls = []fyne.CanvasObject{
		c.trimTimeline, c.trimIn, c.trimOut, c.trimSetIn, c.trimSetOut, c.trimReset,
		c.trimExport, c.trimAdd, c.trimDelete,
		c.trimZoomOut, c.trimZoomIn, c.trimZoomFit, c.trimZoomSel,
		c.trimSnap, c.trimPrevMarker, c.trimNextMarker, c.trimChapters,
	}
	for _, control := range c.trimControls {
		if d, ok := control.(fyne.Disableable); ok {
			d.Disable()
		}
	}

	c.progress = widget.NewProgressBar()
	c.progressInfo = widget.NewLabel("")
	c.progressInfo.Truncation = fyne.TextTruncateEllipsis
	c.progress.Hide()
	c.progressInfo.Hide()
	c.log = widget.NewMultiLineEntry()
	c.log.Disable()
	// Word wrapping a rapidly changing FFmpeg log makes Entry repeatedly lay
	// out thousands of fragments on the UI thread. Horizontal scrolling is a
	// much better tradeoff for machine-generated output.
	c.log.Wrapping = fyne.TextWrapOff
	c.log.SetMinRowsVisible(12)
	c.logFollow = widget.NewCheck("Follow output", nil)
	c.logFollow.SetChecked(true)
	c.logFollow.OnChanged = func(on bool) {
		if on {
			c.flushPendingLog()
		}
	}
	c.openFolder = widget.NewButton("Open folder", c.openOutputFolder)
	c.playOutput = widget.NewButton("Play", c.playOutputFile)
	c.runResult = widget.NewLabel("")
	c.runResult.Wrapping = fyne.TextWrapWord
	c.hideRunActions()
	c.openLog = widget.NewButton("Open log file", c.openLogFile)
	c.openLog.Disable()
	c.copyCommand = widget.NewButton("Copy command", c.copyCommandText)
	c.copyCommand.Disable()
	c.command.OnChanged = func(text string) {
		if strings.TrimSpace(text) == "" {
			c.copyCommand.Disable()
		} else {
			c.copyCommand.Enable()
		}
	}
	c.startHint = widget.NewLabel("")
	c.startHint.Truncation = fyne.TextTruncateEllipsis
	c.startHint.TextStyle = fyne.TextStyle{Italic: true}
	c.start = widget.NewButton("Start", c.startConversion)
	c.cancel = widget.NewButton("Cancel", c.cancelConversion)
	c.cancel.Disable()
}

func (c *Controller) changed(string) {
	if c.loading {
		return
	}
	c.updateControlState()
	c.refresh()
}

// trimExportModeFromOption maps a trimExport dropdown option back to its
// export mode. Unknown text falls back to the default (separate files) —
// the dropdown only ever offers the two known options, so this guards
// programmatic selections like a reset to "".
func trimExportModeFromOption(option string) model.TrimExportMode {
	if option == model.TrimExportJoin.String() {
		return model.TrimExportJoin
	}
	return model.TrimExportSeparate
}

// rebuildFormOnSignature swaps the form inside box when signature
// changed. Fyne's widget.Form cannot hide rows (FormLayout skips a row
// only when both cells are invisible) and mutating Form.Items is
// unsafe (the render pass pairs items and labels positionally), so a
// changed row set is expressed by building a fresh Form from the same
// widget pointers. The signature guard keeps identical states from
// rebuilding on every changed() tick. None of the widgets placed in
// these forms implement fyne.Validatable, so a rebuild cannot leak
// stale validation state.
func rebuildFormOnSignature(box *fyne.Container, signature *string, items []*widget.FormItem) {
	newSig := formItemsSignature(items)
	if *signature == newSig {
		return
	}
	*signature = newSig
	box.RemoveAll()
	box.Add(widget.NewForm(items...))
	box.Refresh()
}

// formItemsSignature builds a comparable key from a form item list so
// a stale signature is detected even when the caller's cached key and
// the actual row set drift apart.
func formItemsSignature(items []*widget.FormItem) string {
	var b strings.Builder
	for _, item := range items {
		b.WriteString(item.Text)
		b.WriteString("\x00")
		fmt.Fprintf(&b, "%p", item.Widget)
		b.WriteString("\x00")
	}
	return b.String()
}

func entry(changed func(string)) *widget.Entry {
	e := widget.NewEntry()
	e.OnChanged = changed
	return e
}

func numericEntry(changed func(string)) *widget.Entry {
	e := entry(changed)
	e.Validator = func(s string) error {
		if s == "" {
			return nil
		}
		_, err := strconv.Atoi(s)
		return err
	}
	return e
}
