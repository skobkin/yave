package ui

import (
	"sort"
	"strconv"
	"strings"

	"fyne.io/fyne/v2/widget"

	"git.skobk.in/skobkin/yave/internal/encoder"
	"git.skobk.in/skobkin/yave/internal/ffmpeg"
	"git.skobk.in/skobkin/yave/internal/model"
	"git.skobk.in/skobkin/yave/internal/validation"
)

// populateRateControlOptions rebuilds the rate-control dropdown from
// the currently-selected encoder's advertised modes. preferred (when
// non-empty) is the value we want the select to land on after the
// options exist; the current selection is used when preferred is empty.
// If the preferred/current value isn't in the new options, we fall
// back to the encoder's default so a stale mode never lingers across
// encoder switches.
//
// Callers that are about to call SetSelected (loadProfile in
// particular) must invoke this first: Fyne silently drops a
// SetSelected whose value isn't already in Options, so the saved
// profile's rate control would otherwise be discarded and replaced by
// the encoder default during the deferred updateControlState.
func (c *Controller) populateRateControlOptions(preferred string) {
	encoderName := c.videoEncoder.Selected
	var ve encoder.VideoEncoder
	if encoderName != "copy" {
		if e, ok := encoder.DefaultRegistry().Get(encoderName); ok {
			ve, _ = e.(encoder.VideoEncoder)
		}
	}
	if ve == nil {
		c.rateControl.Options = []string{}
		return
	}
	modes := ve.RateControlModes()
	options := make([]string, len(modes))
	supported := false
	target := preferred
	if target == "" {
		target = c.rateControl.Selected
	}
	for i, m := range modes {
		label := rateControlLabel(m)
		options[i] = label
		if label == target {
			supported = true
		}
	}
	c.rateControl.Options = options
	// Only SetSelected when the value actually changes; firing
	// OnChanged from inside updateControlState would re-enter
	// updateControlState via changed() and recurse until the
	// goroutine stack overflows.
	if supported {
		if c.rateControl.Selected != target {
			c.rateControl.SetSelected(target)
		}
	} else {
		fallback := rateControlLabel(ve.DefaultRateControl())
		if c.rateControl.Selected != fallback {
			c.rateControl.SetSelected(fallback)
		}
	}
}

func (c *Controller) updateControlState() {
	encoderName := c.videoEncoder.Selected
	copyVideo := encoderName == "copy" || encoderName == ""

	// Look up the encoder from the registry. Unknown names (e.g. before
	// detection completes, or a name stripped by ParseEncoders) leave ve
	// nil and the video tab collapses to the Encoder row — the dropdown
	// still updates so the user can pick another.
	var ve encoder.VideoEncoder
	if !copyVideo {
		if e, ok := encoder.DefaultRegistry().Get(encoderName); ok {
			ve, _ = e.(encoder.VideoEncoder)
		}
	}

	c.updateIntegerRanges()

	c.populateRateControlOptions("")

	mode := c.rateControl.Selected
	var presetOK, supportsPreset, supportsProfile bool
	var presetR encoder.IntegerRange
	if ve != nil {
		presetR, presetOK = ve.PresetRange()
		supportsProfile = ve.SupportsProfile()
		supportsPreset = ve.SupportsPreset()
	}
	integerPreset := presetOK && presetR.Max >= presetR.Min

	// Preset: toggle between curated dropdown, text entry and integer
	// slider. When switching to an integer-preset encoder, sync the
	// slider from the text field if the user had a numeric value there.
	// Entering a curated encoder seeds the dropdown from the text field
	// while the dropdown has no selection yet, so a preset picked for
	// the previous categorical encoder carries over when the new encoder
	// knows the same name.
	//
	// All three controls are gated on SupportsPreset (i.e. the encoder
	// actually emits a preset flag). Encoders like VAAPI that have no
	// preset flag must not expose a writable field here, because the
	// saved value would never reach ffmpeg.
	switch {
	case !supportsPreset:
		c.presetSlider.Hide()
		c.preset.Hide()
		c.presetSelect.Hide()
	case integerPreset:
		if value, err := strconv.Atoi(strings.TrimSpace(c.preset.Text)); err == nil {
			c.presetSlider.SetValue(value)
		}
		c.preset.Hide()
		c.presetSelect.Hide()
		c.presetSlider.Show()
	case len(ve.Presets()) > 0:
		// Seed from the text field only on first entry: once the
		// dropdown holds a selection (including "(default)") that
		// selection is the state — re-seeding from the stale hidden
		// entry would snap the value back on every updateControlState.
		preferred := ""
		if c.presetSelect.Selected == "" {
			preferred = c.preset.Text
		}
		populateCuratedOptions(c.presetSelect, ve.Presets(), preferred)
		c.preset.Hide()
		c.presetSlider.Hide()
		c.presetSelect.Show()
	default:
		c.presetSlider.Hide()
		c.presetSelect.Hide()
		c.preset.Show()
	}

	// Tune mirrors the preset toggle: a curated list shows the dropdown,
	// everyone else the free-text entry. Row visibility itself is
	// expressed by the form rebuild; only the stack children need the
	// explicit toggle.
	if ve != nil && ve.SupportsTune() && len(ve.Tunes()) > 0 {
		preferred := ""
		if c.tuneSelect.Selected == "" {
			preferred = c.tune.Text
		}
		populateCuratedOptions(c.tuneSelect, ve.Tunes(), preferred)
		c.tune.Hide()
		c.tuneSelect.Show()
	} else {
		c.tuneSelect.Hide()
	}

	if supportsProfile {
		// The options must exist before any SetSelected; keep them in
		// sync with the encoder here so encoder switches relist values.
		c.populateProfileOptions("")
	} else {
		// The Profile row is hidden for these encoders (see
		// videoFormItems); clear the invisible selection so switching
		// back to a supporting encoder never resurrects a stale value.
		// The Selected != "" guard prevents ClearSelected from
		// re-firing OnChanged and re-entering updateControlState.
		c.videoProfile.Options = []string{}
		if c.videoProfile.Selected != "" {
			c.videoProfile.ClearSelected()
		}
	}

	// The rate-control hint explains the selected mode — this is what
	// makes Auto and VBR legible. The hint lives in its own label so
	// mode switches that keep the same row set update text without a
	// form rebuild.
	if hint := rateControlHintText(mode); c.rateControlHint.Text != hint {
		c.rateControlHint.SetText(hint)
	}

	c.rebuildVideoForm()
	c.updateVAAPIDeviceHint()
	c.updateQSVDeviceHint()

	// Everything visible is enabled; the form rebuild is what expresses
	// applicability. The preset control keeps its internal entry/slider
	// toggle (both live in one form row).
	c.rateControl.Enable()
	c.qp.Enable()
	c.crf.Enable()
	c.bitrate.Enable()
	c.maxrate.Enable()
	c.bufsize.Enable()
	c.preset.Enable()
	c.presetSlider.Enable()
	c.presetSelect.Enable()
	c.tune.Enable()
	c.tuneSelect.Enable()
	c.videoProfile.Enable()
	c.frameMode.Enable()
	c.frameRate.Enable()
	c.maxWidth.Enable()
	c.maxHeight.Enable()
	c.keepAspect.Enable()
	c.noUpscale.Enable()
	// The device dropdowns list "(Auto)" / "(default)" even without
	// detected nodes; the seed fallback in profileFromWidgets still
	// round-trips the saved value through an empty selection.
	if len(c.vaapiDevice.Options) > 0 {
		c.vaapiDevice.Enable()
	}
	if len(c.qsvDevice.Options) > 0 {
		c.qsvDevice.Enable()
	}
	c.gpuIndex.Enable()

	// Audio rows: visibility instead of grey-out. updateAudioControls
	// first settles the encoder selection and bitrate options, then the
	// form rebuild expresses the applicable row set; visible rows are
	// always enabled.
	c.updateAudioControls()
	c.rebuildAudioForm()
}

// rateControlHintText explains the selected rate-control mode: what
// the user is expected to set, and what the encoder does with it. An
// unknown/empty mode returns the empty string.
func rateControlHintText(mode string) string {
	switch {
	case strings.EqualFold(mode, "Auto"):
		return "Encoder default rate control. A target bitrate is optional."
	case strings.EqualFold(mode, "CQP"):
		return "Constant quality: every frame gets the same quantization parameter. Set QP (lower is better quality)."
	case strings.EqualFold(mode, "CRF"):
		return "Constant quality: set CRF (lower is better quality)."
	case strings.EqualFold(mode, "VBR"):
		return "Set a target bitrate; maximum bitrate and buffer size cap the peaks."
	case strings.EqualFold(mode, "CBR"):
		return "Constant bitrate: set the target bitrate (the maximum is forced to match)."
	case strings.EqualFold(mode, "bitrate"):
		return "Set the target bitrate."
	}
	return ""
}

// currentVideoEncoder resolves the selected video encoder, or nil for
// "copy", an empty selection, or an unknown name.
func (c *Controller) currentVideoEncoder() encoder.VideoEncoder {
	name := c.videoEncoder.Selected
	if name == "" || name == "copy" {
		return nil
	}
	if e, ok := encoder.DefaultRegistry().Get(name); ok {
		ve, _ := e.(encoder.VideoEncoder)
		return ve
	}
	return nil
}

// rebuildVideoForm rebuilds the Video tab's dynamic form when the
// applicable row set changed; see videoFormItems for the matrix.
func (c *Controller) rebuildVideoForm() {
	rebuildFormOnSignature(c.videoFormBox, &c.videoFormSignature, c.videoFormItems())
}

// videoFormItems computes the Video tab's row set from the selected
// encoder and rate-control mode. Rows that cannot affect the command
// are left out entirely (hide over grey-out): copy mode collapses the
// tab to the Encoder row, and each rate-control mode shows only the
// controls its BuildVideoArgs consumes.
func (c *Controller) videoFormItems() []*widget.FormItem {
	items := []*widget.FormItem{
		widget.NewFormItem("Encoder", c.videoEncoder),
	}
	ve := c.currentVideoEncoder()
	if ve == nil {
		return items
	}

	if len(ve.RateControlModes()) > 0 {
		items = append(items,
			widget.NewFormItem("Rate control", c.rateControl),
			widget.NewFormItem("", c.rateControlHint),
		)
	}
	mode := c.rateControl.Selected
	rateCapped := strings.EqualFold(mode, "VBR") || strings.EqualFold(mode, "CBR")
	bitrateShown := rateCapped || strings.EqualFold(mode, "bitrate") ||
		(strings.EqualFold(mode, "Auto") && ve.HWBackend() == encoder.HWVAAPI)
	switch {
	case strings.EqualFold(mode, "CQP"):
		items = append(items, widget.NewFormItem("QP", c.qp.Object()))
	case strings.EqualFold(mode, "CRF"):
		items = append(items, widget.NewFormItem("CRF", c.crf.Object()))
	case bitrateShown:
		items = append(items, widget.NewFormItem("Target bitrate", c.bitrate))
	}
	if rateCapped {
		items = append(items,
			widget.NewFormItem("Maximum bitrate", c.maxrate),
			widget.NewFormItem("Buffer size", c.bufsize),
		)
	}
	if ve.SupportsPreset() {
		items = append(items, widget.NewFormItem("Preset", c.presetControl))
	}
	if ve.SupportsTune() {
		items = append(items, widget.NewFormItem("Tune", c.tuneControl))
	}
	if ve.SupportsProfile() {
		items = append(items, widget.NewFormItem("Profile", c.videoProfile))
	}
	items = append(items, widget.NewFormItem("Frame rate mode", c.frameMode))
	if c.frameMode.Selected == "convert" {
		items = append(items, widget.NewFormItem("Frame rate", c.frameRate))
	}
	items = append(items,
		widget.NewFormItem("Maximum width", c.maxWidth),
		widget.NewFormItem("Maximum height", c.maxHeight),
		// c.aspectFlags is a Controller field: a fresh container here
		// would change the signature on every call and rebuild the
		// form on each keystroke.
		widget.NewFormItem("", c.aspectFlags),
	)
	switch ve.HWBackend() {
	case encoder.HWVAAPI:
		items = append(items,
			widget.NewFormItem("VAAPI device", c.vaapiDevice),
			// The hint is a stable-pointer caption row (like the
			// rate-control hint): it is added unconditionally within
			// this branch, so the form signature does not churn.
			widget.NewFormItem("", c.vaapiDeviceHint),
		)
	case encoder.HWQSV:
		items = append(items,
			widget.NewFormItem("QSV device", c.qsvDevice),
			// Same stable-pointer caption pattern as the VAAPI
			// hint: added unconditionally in this branch, so the
			// form signature does not churn.
			widget.NewFormItem("", c.qsvDeviceHint),
		)
	case encoder.HWNVENC:
		gpuItem := widget.NewFormItem("GPU index", c.gpuIndex)
		gpuItem.HintText = "NVIDIA GPU to use when several are present"
		items = append(items, gpuItem)
	}
	return items
}

func (c *Controller) updateIntegerRanges() {
	if r, ok := validation.QPRange(c.videoEncoder.Selected); ok {
		// Only remap when the slider's range was actually set by a previous
		// encoder that supported QP. If qpRangeEncoder is empty (initial state
		// or the prior encoder did not expose QP) or matches the current
		// encoder, the range is either unset or already in sync — just set
		// it and let SetRange's Clamp keep the stored value sensible.
		if c.qpRangeEncoder != "" && c.qpRangeEncoder != c.videoEncoder.Selected {
			// Capture the value before mutating the slider. SetRange
			// internally clamps the value, so reading c.qp.Value()
			// after SetRange would remap the already-clamped value
			// rather than the original.
			old := c.qp.Range()
			oldValue := c.qp.Value()
			c.qp.SetRange(r)
			c.qp.SetValue(validation.ProportionalConvert(oldValue, old, r))
		} else {
			c.qp.SetRange(r)
		}
		c.qpRangeEncoder = c.videoEncoder.Selected
	} else {
		c.qpRangeEncoder = ""
	}
	if r, ok := validation.CRFRange(c.videoEncoder.Selected); ok {
		if c.crfRangeEncoder != "" && c.crfRangeEncoder != c.videoEncoder.Selected {
			old := c.crf.Range()
			oldValue := c.crf.Value()
			c.crf.SetRange(r)
			c.crf.SetValue(validation.ProportionalConvert(oldValue, old, r))
		} else {
			c.crf.SetRange(r)
		}
		c.crfRangeEncoder = c.videoEncoder.Selected
	} else {
		c.crfRangeEncoder = ""
	}
	// Preset slider: only integer-preset encoders own this range.
	// libsvtav1 {0,13} -> libaom-av1 {0,8} should remap 6 -> 4, while
	// categorical->integer (e.g. libx264 "slow" -> libsvtav1) must NOT
	// remap a stale "6" left over from a default; an empty
	// presetRangeEncoder signals the slider has never been owned by an
	// integer encoder and we should just clamp without remap.
	if r, ok := validation.PresetRange(c.videoEncoder.Selected); ok && r.Max >= r.Min {
		if c.presetRangeEncoder != "" && c.presetRangeEncoder != c.videoEncoder.Selected {
			old := c.presetSlider.Range()
			oldValue := c.presetSlider.Value()
			c.presetSlider.SetRange(r)
			c.presetSlider.SetValue(validation.ProportionalConvert(oldValue, old, r))
		} else {
			c.presetSlider.SetRange(r)
		}
		c.presetRangeEncoder = c.videoEncoder.Selected
	} else {
		c.presetRangeEncoder = ""
	}
}

func (c *Controller) presetValue() string {
	if ve := c.currentVideoEncoder(); ve != nil && len(ve.Presets()) > 0 {
		return profileOptionToValue(c.presetSelect.Selected)
	}
	if r, ok := validation.PresetRange(c.videoEncoder.Selected); ok && r.Max >= r.Min {
		return strconv.Itoa(c.presetSlider.Value())
	}
	return c.preset.Text
}

// tuneValue reads the tune state: the curated dropdown when the encoder
// curates one, the free-text entry otherwise. "(default)" maps to the
// empty string — the builder omits -tune when it is empty.
func (c *Controller) tuneValue() string {
	if ve := c.currentVideoEncoder(); ve != nil && len(ve.Tunes()) > 0 {
		return profileOptionToValue(c.tuneSelect.Selected)
	}
	return c.tune.Text
}

// populatePresetOptions seeds the preset dropdown from the encoder's
// curated list. preferred (when non-empty) is the value the dropdown
// should land on after the options exist.
func (c *Controller) populatePresetOptions(preferred string) {
	if ve := c.currentVideoEncoder(); ve != nil && len(ve.Presets()) > 0 {
		populateCuratedOptions(c.presetSelect, ve.Presets(), preferred)
		return
	}
	c.presetSelect.Options = []string{}
}

// populateTuneOptions seeds the tune dropdown from the encoder's
// curated list; see populatePresetOptions.
func (c *Controller) populateTuneOptions(preferred string) {
	if ve := c.currentVideoEncoder(); ve != nil && len(ve.Tunes()) > 0 {
		populateCuratedOptions(c.tuneSelect, ve.Tunes(), preferred)
		return
	}
	c.tuneSelect.Options = []string{}
}

func (c *Controller) updateEncoderOptions() {
	// "copy" stays a video option (video passthrough is a real mode)
	// but is never an audio encoder option: audio copy mode is
	// expressed by Mode alone.
	video, audio := []string{"copy"}, []string{}
	detected := make(map[string]model.Encoder, len(c.caps.Encoders))
	for name, enc := range c.caps.Encoders {
		if name == "copy" {
			continue
		}
		detected[name] = enc
	}
	registry := encoder.DefaultRegistry()
	video = append(video, orderedEncoders(detected, registry.VideoNames(), ffmpeg.MediaVideo)...)
	audio = append(audio, orderedEncoders(detected, registry.AudioNames(), ffmpeg.MediaAudio)...)
	c.videoEncoder.Options = video
	c.videoEncoder.Refresh()
	c.audioEncoder.Options = audio
	c.audioEncoder.Refresh()
}

// orderedEncoders orders one media kind's detected encoder names in
// DefaultRegistry order (priority descending, name ascending) so the
// post-detection dropdowns keep the same ordering the pre-detection
// seeding uses. Detected names the registry does not know about are
// appended alphabetically at the end so nothing disappears when the
// registry and the detected build disagree.
func orderedEncoders(detected map[string]model.Encoder, registryNames []string, media string) []string {
	seen := make(map[string]bool, len(registryNames))
	ordered := make([]string, 0, len(detected))
	for _, name := range registryNames {
		enc, ok := detected[name]
		if !ok || enc.Media != media {
			continue
		}
		ordered = append(ordered, name)
		seen[name] = true
	}
	var extras []string
	for name, enc := range detected {
		if !seen[name] && enc.Media == media {
			extras = append(extras, name)
		}
	}
	sort.Strings(extras)
	return append(ordered, extras...)
}

func mustQPRange(encoder string) validation.IntegerRange {
	r, _ := validation.QPRange(encoder)
	return r
}

// rateControlLabel maps the encoder package's RateControl enum to the
// display strings the dropdown uses. Kept lower-case for "bitrate" to
// preserve profile-file compatibility — existing profile YAML stores
// the value verbatim as the selected dropdown label.
func rateControlLabel(rc encoder.RateControl) string {
	switch rc {
	case encoder.RCAuto:
		return "Auto"
	case encoder.RCCQP:
		return "CQP"
	case encoder.RCVBR:
		return "VBR"
	case encoder.RCCBR:
		return "CBR"
	case encoder.RCCRF:
		return "CRF"
	case encoder.RCBitrate:
		return "bitrate"
	}
	return string(rc)
}

func mustCRFRange(encoder string) validation.IntegerRange {
	r, _ := validation.CRFRange(encoder)
	return r
}

func mustPresetRange(encoder string) validation.IntegerRange {
	r, _ := validation.PresetRange(encoder)
	return r
}
