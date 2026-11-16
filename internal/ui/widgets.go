package ui

import (
	"fmt"
	"strconv"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"

	"git.skobk.in/skobkin/yave/internal/encoder"
	"git.skobk.in/skobkin/yave/internal/model"
	"git.skobk.in/skobkin/yave/internal/profiles"
)

func (c *Controller) createWidgets() {
	c.status = widget.NewLabel("Idle")
	c.detection = widget.NewLabel("Detecting FFmpeg...")
	c.detection.Wrapping = fyne.TextWrapWord
	c.inputEntry = widget.NewEntry()
	c.inputEntry.SetPlaceHolder("Select or drop a video file")
	c.inputEntry.OnSubmitted = c.setInput
	c.mediaInfo = widget.NewLabel("No input selected")
	c.mediaInfo.Wrapping = fyne.TextWrapWord

	c.profile = widget.NewSelect(c.profileNames(), func(name string) {
		if c.loading {
			return
		}
		if p, ok := profiles.Find(c.config.Profiles, name); ok {
			c.loadProfile(p)
		}
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
	c.maxrate = entry(c.changed)
	c.bufsize = entry(c.changed)
	c.preset = entry(c.changed)
	c.presetSlider = newIntegerSlider(mustPresetRange("libsvtav1"), 6, c.changed)
	c.presetSlider.Hide()
	c.presetControl = container.NewStack(c.preset, c.presetSlider.Object())
	c.tune = entry(c.changed)
	// Profile options are populated per-encoder by populateProfileOptions.
	c.videoProfile = widget.NewSelect([]string{}, c.changed)
	c.frameMode = widget.NewSelect([]string{"keep", "convert"}, c.changed)
	c.frameRate = entry(c.changed)
	c.maxWidth = numericEntry(c.changed)
	c.maxHeight = numericEntry(c.changed)
	c.keepAspect = widget.NewCheck("Keep aspect ratio", func(bool) { c.changed("") })
	c.noUpscale = widget.NewCheck("Prevent upscaling", func(bool) { c.changed("") })
	c.vaapiDevice = widget.NewSelect([]string{}, c.changed)
	c.qsvDevice = widget.NewSelect([]string{}, c.changed)
	c.vaapiDeviceHint = widget.NewLabel("")
	c.vaapiDeviceHint.Wrapping = fyne.TextWrapWord
	c.qsvDeviceHint = widget.NewLabel("")
	c.qsvDeviceHint.Wrapping = fyne.TextWrapWord
	c.gpuIndex = numericEntry(c.changed)

	c.audioMode = widget.NewSelect([]string{model.AudioCopy, model.AudioEncode}, c.changed)
	// "copy" is deliberately absent: copy mode has no encoder settings
	// (see updateAudioControls), so the dropdown only ever lists real
	// encoders.
	c.audioEncoder = widget.NewSelect(encoder.DefaultRegistry().AudioNames(), c.changed)
	c.audioBitrate = widget.NewSelect([]string{}, c.changed)
	c.compression = newIntegerSlider(mustAudioCompressionRange("flac"), 8, c.changed)
	c.allAudio = widget.NewCheck("Process all audio streams", func(bool) { c.changed("") })
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
