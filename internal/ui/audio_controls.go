package ui

import (
	"slices"

	"fyne.io/fyne/v2/widget"

	"git.skobk.in/skobkin/yave/internal/encoder"
	"git.skobk.in/skobkin/yave/internal/model"
	"git.skobk.in/skobkin/yave/internal/validation"
)

// updateAudioControls settles the audio mode/encoder state before the
// form rebuild: encode mode always has a real encoder selected (the
// dropdown never contains "copy"), with per-encoder bitrate options in
// place. A build with no audio encoders at all reverts the mode to
// copy visibly rather than leaving an encode selection that cannot be
// satisfied.
func (c *Controller) updateAudioControls() {
	if c.audioMode.Selected != model.AudioEncode {
		return
	}
	if len(c.audioEncoder.Options) == 0 {
		// Guarded revert: a visible state change beats a silent
		// profile swap (and validation failing at Start).
		if c.audioMode.Selected != model.AudioCopy {
			c.audioMode.SetSelected(model.AudioCopy)
		}
		return
	}
	if !slices.Contains(c.audioEncoder.Options, c.audioEncoder.Selected) {
		c.audioEncoder.SetSelected(c.audioEncoder.Options[0])
	}
	c.populateAudioBitrateOptions(c.audioEncoder.Selected, "")
}

// audioEncoderForName resolves the curated AudioEncoder for a dropdown
// name, or nil for unknown names (e.g. "copy").
func audioEncoderForName(name string) encoder.AudioEncoder {
	if e, ok := encoder.DefaultRegistry().Get(name); ok {
		ae, _ := e.(encoder.AudioEncoder)
		return ae
	}
	return nil
}

// rebuildAudioForm rebuilds the audio tab's dynamic form when the
// applicable row set changed (copy mode shows only Mode/all-audio/
// Subtitles; encode mode adds Encoder plus Bitrate or Compression).
func (c *Controller) rebuildAudioForm() {
	items := c.audioFormItems()
	rebuildFormOnSignature(c.audioFormBox, &c.audioFormSignature, items)
	// Visible rows are never disabled: hiding replaces greying out.
	c.audioMode.Enable()
	c.allAudio.Enable()
	c.subtitles.Enable()
	if c.audioMode.Selected != model.AudioEncode {
		return
	}
	c.audioEncoder.Enable()
	for _, item := range items {
		switch item.Text {
		case "Bitrate":
			c.audioBitrate.Enable()
		case "Compression":
			c.compression.Enable()
		}
	}
}

// audioFormItems computes the audio tab's row set from the current
// mode and encoder capabilities. The same widget pointers are reused
// across rebuilds, so no widget state is copied — only the row set
// moves between Form instances.
func (c *Controller) audioFormItems() []*widget.FormItem {
	if c.includeAudio != nil && !c.includeAudio.Checked {
		// Audio is not written: only the subtitle selector stays, and
		// only while video is (subtitles are not processed without it).
		if c.includeVideo != nil && !c.includeVideo.Checked {
			return nil
		}
		return []*widget.FormItem{widget.NewFormItem("Subtitles", c.subtitles)}
	}
	items := []*widget.FormItem{
		widget.NewFormItem("Mode", c.audioMode),
	}
	if c.audioMode.Selected != model.AudioEncode {
		return append(items,
			widget.NewFormItem("", c.allAudio),
			widget.NewFormItem("Subtitles", c.subtitles),
		)
	}
	items = append(items, widget.NewFormItem("Encoder", c.audioEncoder))
	ae := audioEncoderForName(c.audioEncoder.Selected)
	switch {
	case ae != nil && len(ae.BitrateChoices()) > 0:
		items = append(items, widget.NewFormItem("Bitrate", c.audioBitrate))
	case ae != nil:
		if _, ok := ae.AudioCompressionRange(); ok {
			items = append(items, widget.NewFormItem("Compression", c.compression.Object()))
		}
	}
	return append(items,
		widget.NewFormItem("", c.allAudio),
		widget.NewFormItem("Subtitles", c.subtitles),
	)
}

// populateAudioBitrateOptions rebuilds the bitrate dropdown from the
// named encoder's choices. preferred (when non-empty) is the value the
// dropdown should land on; the current selection is used when
// preferred is empty, falling back to 160k (the historical default)
// or the median choice. Out-of-list preferred/current values are
// unioned into the options so hand-edited profiles stay visible and
// validation reports them. Unknown encoders ("copy", pre-selection)
// leave the dropdown untouched — it is hidden in those states and the
// audioBitrateSeed keeps the loaded value round-tripping.
func (c *Controller) populateAudioBitrateOptions(encoderName, preferred string) {
	ae := audioEncoderForName(encoderName)
	if ae == nil {
		return
	}
	options := slices.Clone(ae.BitrateChoices())
	target := preferred
	if target == "" {
		target = c.audioBitrate.Selected
	}
	for _, extra := range []string{target, c.audioBitrate.Selected} {
		if extra != "" && !slices.Contains(options, extra) {
			options = append(options, extra)
		}
	}
	c.audioBitrate.Options = options
	if target == "" {
		switch {
		case slices.Contains(options, "160k"):
			target = "160k"
		case len(options) > 0:
			target = options[len(options)/2]
		}
	}
	if target != "" && c.audioBitrate.Selected != target {
		c.audioBitrate.SetSelected(target)
	}
}

func mustAudioCompressionRange(encoder string) validation.IntegerRange {
	r, _ := validation.AudioCompressionRange(encoder)
	return r
}
