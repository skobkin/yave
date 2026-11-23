package ui

import (
	"fmt"
	"fyne.io/fyne/v2/storage"
	"slices"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"

	"git.skobk.in/skobkin/yave/internal/encoder"
	"git.skobk.in/skobkin/yave/internal/model"
	"git.skobk.in/skobkin/yave/internal/profiles"
	"git.skobk.in/skobkin/yave/internal/validation"
)

func (c *Controller) loadProfile(p model.Profile) {
	c.loading = true
	defer func() {
		c.loading = false
		c.updateControlState()
		c.reconcileDevices(true)
		// The widgets normalize loaded values (defaults, dropdown
		// fallbacks), so the dirty baseline is what they show now, not
		// the raw profile — otherwise every load would look edited.
		c.markProfileSaved()
		c.refresh()
	}()
	c.profile.SetSelected(p.Name)
	c.container.SetSelected(p.Container)
	c.output.SetText(p.OutputTemplate)
	c.videoEncoder.SetSelected(p.Video.Encoder)
	// Seed rate-control options for the just-selected encoder before
	// SetSelected. Fyne silently drops a SetSelected call whose value
	// isn't in Options yet, which would cause the deferred
	// updateControlState to see an empty selection, treat it as
	// unsupported, and fall back to the encoder default (e.g. VAAPI
	// "Auto"). Built-in VAAPI profiles saved as CQP/VBR would then
	// silently reload as Auto and emit different FFmpeg args.
	// populateRateControlOptions also performs the SetSelected when
	// preferred is non-empty, so the explicit SetSelected below is
	// redundant and would re-fire OnChanged.
	// populateRateControlOptions keeps the current selection when
	// preferred is empty — the right behavior for user-driven encoder
	// switches, but wrong for a fresh load: an empty saved value must
	// land on the encoder default instead of inheriting the previous
	// profile's mode. ClearSelected empties the selection so the
	// populate call falls back (OnChanged is swallowed by c.loading).
	if p.Video.RateControl == "" && c.rateControl.Selected != "" {
		c.rateControl.ClearSelected()
	}
	c.populateRateControlOptions(p.Video.RateControl)
	c.updateIntegerRanges()
	c.qp.SetValue(p.Video.QP)
	c.crf.SetValue(p.Video.CRF)
	c.bitrate.SetText(p.Video.Bitrate)
	c.maxrate.SetText(p.Video.Maxrate)
	c.bufsize.SetText(p.Video.Bufsize)
	c.preset.SetText(p.Video.Preset)
	c.tune.SetText(p.Video.Tune)
	// Seed the preset/tune dropdown options for the just-selected
	// encoder before anything reads the selection — same Fyne ordering
	// rule as the rate-control dropdown above. Same empty-value rule
	// too: a profile saved without a preset/tune must land on
	// "(default)", not keep the previously loaded profile's selection
	// (which would be emitted for this profile and persisted by Save).
	// An encoder without a curated preset/tune list keeps its value in
	// the free-text entry, so clear the dropdown there as well: the
	// selection left by a previous profile is not this profile's state,
	// and updateControlState treats a non-empty selection as current —
	// switching to a curated encoder would re-select it (populate from
	// text only happens when the selection is empty), overriding the
	// loaded value (load x265 "slow", load VAAPI "medium", switch to
	// libx264: the stale dropdown regenerated "slow").
	ve := c.currentVideoEncoder()
	if (p.Video.Preset == "" || ve == nil || len(ve.Presets()) == 0) && c.presetSelect.Selected != "" {
		c.presetSelect.ClearSelected()
	}
	c.populatePresetOptions(p.Video.Preset)
	if (p.Video.Tune == "" || ve == nil || len(ve.Tunes()) == 0) && c.tuneSelect.Selected != "" {
		c.tuneSelect.ClearSelected()
	}
	c.populateTuneOptions(p.Video.Tune)
	// Seed the profile options for the just-selected encoder before
	// anything reads the selection: same Fyne ordering rule as the
	// rate-control dropdown above (SetSelected silently drops values
	// missing from Options). populateProfileOptions also performs the
	// SetSelected when preferred is non-empty.
	// Same empty-value rule as the rate-control dropdown above: a
	// profile saved without -profile:v must land on "(default)", not
	// keep the previously loaded profile's selection (which would be
	// emitted for this profile and persisted by Save).
	if p.Video.Profile == "" && c.videoProfile.Selected != "" {
		c.videoProfile.ClearSelected()
	}
	c.populateProfileOptions(p.Video.Profile)
	c.frameMode.SetSelected(p.Video.FrameRateMode)
	c.frameRate.SetText(p.Video.FrameRate)
	c.maxWidth.SetText(intText(p.Video.MaxWidth))
	c.maxHeight.SetText(intText(p.Video.MaxHeight))
	c.keepAspect.SetChecked(p.Video.KeepAspectRatio)
	c.noUpscale.SetChecked(p.Video.PreventUpscaling)
	// Record the loaded device paths as seeds, then rebuild the device
	// dropdowns from the detected nodes (empty before the first
	// detection). reconcileDevices in the deferred block performs the
	// actual selections once loading is done — including the
	// saved-device-not-detected replacement + notification.
	c.vaapiDeviceSeed = p.Video.VAAPIDevice
	c.qsvDeviceSeed = p.Video.QSVDevice
	c.updateDeviceOptions()
	c.applyGPUIndexSeed(p.Video.GPUIndex)
	c.audioMode.SetSelected(p.Audio.Mode)
	// Copy-mode profiles carry Encoder: "copy" in YAML for validation
	// compatibility; the dropdown only lists real encoders, so the
	// value is not selected (the encoder row is hidden in copy mode
	// and profileFromWidgets re-normalizes to "copy").
	if p.Audio.Encoder != "copy" {
		c.audioEncoder.SetSelected(p.Audio.Encoder)
	}
	// Seed the bitrate options for the just-loaded encoder before
	// anything reads the selection (Fyne drops SetSelected values
	// missing from Options). The seed keeps the loaded bitrate
	// round-tripping while the row is hidden in copy mode. The row is
	// also hidden or meaningless for a copy-mode / bitrate-less load,
	// so clear the dropdown first — populateAudioBitrateOptions keeps
	// the current selection when preferred is empty, which would
	// inherit the previously loaded profile's bitrate.
	c.audioBitrateSeed = p.Audio.Bitrate
	if p.Audio.Bitrate == "" || p.Audio.Encoder == "copy" {
		c.audioBitrate.ClearSelected()
	}
	c.populateAudioBitrateOptions(p.Audio.Encoder, p.Audio.Bitrate)
	c.compression.SetValue(p.Audio.Compression)
	c.allAudio.SetChecked(p.Audio.CopyAllStreams)
	c.subtitles.SetSelected(p.Subtitles.Mode)
	c.userIncludeVideo, c.userIncludeAudio = !p.ExcludeVideo, !p.ExcludeAudio
	c.updateStreamIncludeControls()
}

// videoProfileValue maps the Profile dropdown to the profile value,
// emitting "" for encoders that do not accept -profile:v — their row
// is hidden, so a stale invisible selection must never reach the
// saved profile.
func (c *Controller) videoProfileValue() string {
	if ve := c.currentVideoEncoder(); ve == nil || !ve.SupportsProfile() {
		return ""
	}
	return profileOptionToValue(c.videoProfile.Selected)
}

// commandProfile returns the widget state as the profile shape
// ffmpeg.BuildCommand consumes: an automatic VAAPI device ("") is
// materialized into the concrete path resolveAutoVAAPI picks from the
// current capabilities. profileFromWidgets stays the persisted shape
// where "" means automatic — Save must keep the Auto intent.
// resolveAutoVAAPI is pure and both call sites (the refresh preview
// and the startConversion run) see identical inputs, so preview,
// validation (which resolves the same way internally), and the actual
// run always agree.
func (c *Controller) commandProfile() model.Profile {
	p := c.withStreamOverrides(c.profileFromWidgets())
	if strings.TrimSpace(p.Video.VAAPIDevice) != "" {
		return p
	}
	choice, ok := c.resolveAutoVAAPI(p.Video.Encoder, p.Video.Profile)
	if ok && choice.Path != "" {
		p.Video.VAAPIDevice = choice.Path
	}
	return p
}

func (c *Controller) profileFromWidgets() model.Profile {
	audioMode := c.audioMode.Selected
	audioEncoder := c.audioEncoder.Selected
	if audioMode != model.AudioEncode {
		// Copy mode has no encoder settings; YAML/validation expect
		// the literal "copy" encoder alongside the copy mode.
		audioEncoder = "copy"
	}
	// In copy mode the bitrate row is hidden, so the invisible
	// dropdown selection must never win: only a visible encode-mode
	// selection takes precedence over the loaded value.
	audioBitrate := c.audioBitrateSeed
	if audioMode == model.AudioEncode {
		audioBitrate = firstNonEmpty(c.audioBitrate.Selected, c.audioBitrateSeed)
	}
	return model.Profile{
		Name: c.profile.Selected, Container: c.container.Selected, OutputTemplate: c.output.Text,
		Video: model.VideoSettings{
			Encoder: c.videoEncoder.Selected, RateControl: c.rateControl.Selected,
			QP: c.qp.Value(), CRF: c.crf.Value(), Bitrate: c.bitrate.Text,
			Maxrate: c.maxrate.Text, Bufsize: c.bufsize.Text, Preset: c.presetValue(),
			Tune: c.tuneValue(), Profile: c.videoProfileValue(),
			FrameRateMode: c.frameMode.Selected, FrameRate: c.frameRate.Text,
			MaxWidth: atoi(c.maxWidth.Text), MaxHeight: atoi(c.maxHeight.Text),
			KeepAspectRatio: c.keepAspect.Checked, PreventUpscaling: c.noUpscale.Checked,
			// When no nodes were detected (detection failure,
			// non-Linux, pre-detect window) the dropdown selection is
			// empty; fall back to the loaded profile's saved value so a
			// Save in that state keeps the configured device instead of
			// wiping it from config.yaml. The seed only backfills an
			// empty selection: an explicit "(Auto)" pick maps to the
			// empty (automatic) value and must win over the seed, or
			// the model would silently keep the pre-pick device while
			// the dropdown shows "(Auto)".
			VAAPIDevice: vaapiDeviceFromWidgets(c),
			QSVDevice:   qsvDeviceFromWidgets(c),
			GPUIndex:    c.gpuIndexValue(),
		},
		Audio: model.AudioSettings{
			Mode: audioMode, Encoder: audioEncoder,
			Bitrate:        audioBitrate,
			Compression:    c.compression.Value(),
			CopyAllStreams: c.allAudio.Checked,
		},
		Subtitles:    model.SubtitleSettings{Mode: c.subtitles.Selected},
		ExcludeVideo: !c.userIncludeVideo,
		ExcludeAudio: !c.userIncludeAudio,
	}
}

// defaultOption is the dropdown label standing in for "emit no
// -profile:v" (Profile) and "use ffmpeg's default device" (QSV
// device) — both map to the empty string in the profile model.
const defaultOption = "(default)"

func profileValueToOption(value string) string {
	if strings.TrimSpace(value) == "" {
		return defaultOption
	}
	return value
}

func profileOptionToValue(option string) string {
	if option == defaultOption {
		return ""
	}
	return option
}

// populateCuratedOptions fills a dropdown with a curated value list
// prefixed by "(default)". preferred (when non-empty) is the value the
// dropdown should land on after the options exist; the current
// selection is used when preferred is empty. The preferred and current
// values are unioned into the options even when outside the curated
// list, so a hand-edited profile value stays visible instead of being
// silently dropped by SetSelected.
//
// Callers about to read the selection (loadProfile in particular) must
// invoke this first: Fyne silently drops a SetSelected whose value
// isn't already in Options.
func populateCuratedOptions(sel *widget.Select, curated []string, preferred string) {
	options := append([]string{defaultOption}, curated...)
	for _, extra := range []string{profileValueToOption(preferred), sel.Selected} {
		if extra != "" && extra != defaultOption && !slices.Contains(options, extra) {
			options = append(options, extra)
		}
	}
	sel.Options = options

	target := profileValueToOption(preferred)
	if preferred == "" {
		target = sel.Selected
	}
	// An empty selection is represented visibly by "(default)" so the
	// dropdown never shows the generic placeholder where a real state
	// belongs.
	if target == "" {
		target = defaultOption
	}
	// Only SetSelected when the value actually changes; firing OnChanged
	// from inside updateControlState would re-enter updateControlState
	// via changed() (see populateRateControlOptions).
	if sel.Selected != target {
		sel.SetSelected(target)
	}
}

// profileChoices returns the profile value list for the encoder: the
// detected build's narrowed list when available, else the curated
// list (before detection completes, or when the build enumerates
// nothing — absence of an enumeration is not evidence of non-support).
func (c *Controller) profileChoices(ve encoder.VideoEncoder) []string {
	if m, ok := c.caps.Encoders[ve.Name()]; ok && len(m.Profiles) > 0 {
		return m.Profiles
	}
	return ve.Profiles()
}

// populateProfileOptions rebuilds the Profile dropdown from the
// currently-selected encoder's curated (or build-narrowed) list,
// prefixed with the "(default)" option. preferred (when non-empty) is
// the value the dropdown should land on after the options exist; the
// current selection is used when preferred is empty. The preferred and
// current values are unioned into the options even when outside the
// curated list, so a hand-edited profile value stays visible and
// validation — not a silently emptied dropdown — reports it.
//
// Callers about to read the selection (loadProfile in particular)
// must invoke this first: Fyne silently drops a SetSelected whose
// value isn't already in Options.
func (c *Controller) populateProfileOptions(preferred string) {
	encoderName := c.videoEncoder.Selected
	var ve encoder.VideoEncoder
	if encoderName != "copy" {
		if e, ok := encoder.DefaultRegistry().Get(encoderName); ok {
			ve, _ = e.(encoder.VideoEncoder)
		}
	}
	if ve == nil || !ve.SupportsProfile() {
		c.videoProfile.Options = []string{}
		return
	}
	options := append([]string{defaultOption}, c.profileChoices(ve)...)
	// Union the preferred and current values so out-of-list values
	// stay visible instead of being silently dropped by SetSelected.
	for _, extra := range []string{profileValueToOption(preferred), c.videoProfile.Selected} {
		if extra != "" && extra != defaultOption && !slices.Contains(options, extra) {
			options = append(options, extra)
		}
	}
	c.videoProfile.Options = options

	target := profileValueToOption(preferred)
	if preferred == "" {
		target = c.videoProfile.Selected
	}
	// An empty selection is represented visibly by "(default)" so the
	// dropdown never shows the generic placeholder where a real state
	// belongs.
	if target == "" {
		target = defaultOption
	}
	// Only SetSelected when the value actually changes; firing
	// OnChanged from inside updateControlState would re-enter
	// updateControlState via changed() (see populateRateControlOptions).
	if c.videoProfile.Selected != target {
		c.videoProfile.SetSelected(target)
	}
}

func (c *Controller) saveCurrentProfile() {
	p := c.profileFromWidgets()
	for i := range c.config.Profiles {
		if c.config.Profiles[i].Name == p.Name {
			c.config.Profiles[i] = p
			c.saveConfig()
			c.markProfileSaved()
			c.status.SetText(fmt.Sprintf("Profile %q saved", p.Name))
			c.refresh()
			return
		}
	}
}

// markProfileSaved records the editor's current state as the saved one.
func (c *Controller) markProfileSaved() {
	if c.profile == nil || c.audioMode == nil {
		return
	}
	c.savedProfile = c.profileFromWidgets()
	c.hasSavedProfile = true
}

// profileDirty reports whether the editor differs from the saved profile.
func (c *Controller) profileDirty() bool {
	if !c.hasSavedProfile || c.profile == nil || c.audioMode == nil {
		return false
	}
	return c.profileFromWidgets() != c.savedProfile
}

// updateProfileDirtyMarker shows an asterisk in the profile bar and the
// window title while the editor has unsaved changes.
func (c *Controller) updateProfileDirtyMarker() {
	if c.profileLabel == nil {
		return
	}
	if c.profileDirty() {
		c.profileLabel.SetText("Profile *:")
	} else {
		c.profileLabel.SetText("Profile:")
	}
	c.updateWindowTitle()
}

// guardUnsavedProfile runs proceed immediately when the editor is clean;
// otherwise it asks whether to save, discard or cancel first.
func (c *Controller) guardUnsavedProfile(proceed func()) {
	if !c.profileDirty() {
		proceed()
		return
	}
	var d dialog.Dialog
	save := widget.NewButton("Save", func() {
		d.Hide()
		c.saveCurrentProfile()
		proceed()
	})
	save.Importance = widget.HighImportance
	discard := widget.NewButton("Discard", func() {
		d.Hide()
		proceed()
	})
	message := widget.NewLabel(fmt.Sprintf("Profile %q has unsaved changes.", c.profile.Selected))
	message.Wrapping = fyne.TextWrapWord
	d = dialog.NewCustom("Unsaved changes", "Cancel", container.NewVBox(message, container.NewHBox(save, discard)), c.window)
	d.Show()
}

// switchProfile handles a profile dropdown change: the selection has
// already moved, so a dirty editor snaps it back and asks first.
func (c *Controller) switchProfile(name string) {
	if c.loading || name == c.savedProfile.Name {
		return
	}
	target, ok := profiles.Find(c.config.Profiles, name)
	if !ok {
		return
	}
	if !c.profileDirty() {
		c.loadProfile(target)
		return
	}
	c.loading = true
	c.profile.SetSelected(c.savedProfile.Name)
	c.loading = false
	c.guardUnsavedProfile(func() { c.loadProfile(target) })
}

func (c *Controller) saveAs(name string) {
	p := c.profileFromWidgets()
	p.Name = name
	if err := profiles.Add(&c.config, p); err != nil {
		dialog.ShowError(err, c.window)
		return
	}
	c.profile.Options = c.profileNames()
	c.profile.Refresh()
	c.loadProfile(p)
	c.saveConfig()
}

func (c *Controller) renameProfile(name string) {
	old := c.profile.Selected
	if err := profiles.Rename(&c.config, old, name); err != nil {
		dialog.ShowError(err, c.window)
		return
	}
	c.profile.Options = c.profileNames()
	c.profile.Refresh()
	// Select the new name without reloading the saved copy, which would
	// discard unsaved edits; the edits move to the renamed profile.
	c.loading = true
	c.profile.SetSelected(name)
	c.loading = false
	c.savedProfile.Name = name
	c.saveConfig()
	c.refresh()
}

func (c *Controller) deleteProfile() {
	name := c.profile.Selected
	message := "Delete profile " + name + "?"
	if c.profileDirty() {
		message += "\nIts unsaved changes will be lost."
	}
	dialog.NewConfirm("Delete profile", message, func(ok bool) {
		if !ok {
			return
		}
		if err := profiles.Delete(&c.config, name); err != nil {
			dialog.ShowError(err, c.window)
			return
		}
		c.profile.Options = c.profileNames()
		c.profile.Refresh()
		c.loadProfile(c.startupProfile())
		c.saveConfig()
	}, c.window).Show()
}

func (c *Controller) setDefault() {
	c.config.DefaultProfile = c.profile.Selected
	c.saveConfig()
	c.status.SetText(fmt.Sprintf("Profile %q set as default", c.profile.Selected))
	c.updateDefaultMarker()
}

// updateDefaultMarker shows a star beside the profile selector while the
// selected profile is the startup default.
func (c *Controller) updateDefaultMarker() {
	if c.defaultMarker == nil {
		return
	}
	if c.profile.Selected != "" && c.profile.Selected == c.config.DefaultProfile {
		c.defaultMarker.Show()
	} else {
		c.defaultMarker.Hide()
	}
}

// profileNameTaken reports whether a profile with that name exists.
func (c *Controller) profileNameTaken(name string) bool {
	_, ok := profiles.Find(c.config.Profiles, name)
	return ok
}

// askProfileName shows the name dialog with inline validation: OK stays
// disabled and the reason is shown until the name is acceptable. taken
// decides which existing names clash (Rename lets the current name pass).
func (c *Controller) askProfileName(title, initial string, taken func(string) bool, callback func(string)) {
	entry := widget.NewEntry()
	entry.Validator = func(name string) error { return profiles.ValidateName(name, taken) }
	entry.SetText(initial)
	items := []*widget.FormItem{
		widget.NewFormItem("Profile name", entry),
	}
	d := dialog.NewForm(title, "OK", "Cancel", items, func(ok bool) {
		if !ok {
			return
		}
		callback(strings.TrimSpace(entry.Text))
	}, c.window)
	d.Resize(fyne.NewSize(560, 180))
	d.Show()
}

// exportProfile writes the profile as shown in the editor to a YAML file.
func (c *Controller) exportProfile() {
	p := c.profileFromWidgets()
	data, err := profiles.ExportProfile(p)
	if err != nil {
		dialog.ShowError(err, c.window)
		return
	}
	d := dialog.NewFileSave(func(w fyne.URIWriteCloser, err error) {
		if err != nil {
			dialog.ShowError(err, c.window)
			return
		}
		if w == nil {
			return
		}
		defer func() { _ = w.Close() }()
		if _, err := w.Write(data); err != nil {
			dialog.ShowError(fmt.Errorf("write profile: %w", err), c.window)
			return
		}
		c.status.SetText(fmt.Sprintf("Profile %q exported", p.Name))
	}, c.window)
	d.SetFileName(sanitizeFileName(p.Name) + ".yaml")
	d.Resize(openFileDialogSize(c.window.Canvas().Size()))
	d.Show()
}

// sanitizeFileName makes a profile name safe as a file name.
func sanitizeFileName(name string) string {
	out := strings.Map(func(r rune) rune {
		if strings.ContainsRune(`/\:*?"<>|`, r) || r < 0x20 {
			return '_'
		}
		return r
	}, strings.TrimSpace(name))
	if out == "" {
		return "profile"
	}
	return out
}

// importProfile reads a profile YAML file; a name clash is resolved by
// asking for another name.
func (c *Controller) importProfile() {
	d := dialog.NewFileOpen(func(r fyne.URIReadCloser, err error) {
		if err != nil {
			dialog.ShowError(err, c.window)
			return
		}
		if r == nil {
			return
		}
		defer func() { _ = r.Close() }()
		p, err := profiles.ImportProfile(r)
		if err != nil {
			dialog.ShowError(err, c.window)
			return
		}
		if !c.profileNameTaken(p.Name) {
			c.addImported(p)
			return
		}
		c.askProfileName("Profile name already exists — choose another",
			profiles.UniqueName(p.Name, c.profileNameTaken), c.profileNameTaken,
			func(name string) {
				p.Name = name
				c.addImported(p)
			})
	}, c.window)
	d.SetFilter(storage.NewExtensionFileFilter([]string{".yaml", ".yml"}))
	d.Resize(openFileDialogSize(c.window.Canvas().Size()))
	d.Show()
}

// addImported adds an imported profile, persists it and selects it.
func (c *Controller) addImported(p model.Profile) {
	if err := profiles.Add(&c.config, p); err != nil {
		dialog.ShowError(err, c.window)
		return
	}
	c.saveConfig()
	c.profile.Options = c.profileNames()
	c.profile.Refresh()
	c.guardUnsavedProfile(func() {
		c.loadProfile(p)
		c.status.SetText(fmt.Sprintf("Profile %q imported", p.Name))
	})
}

// restoreBuiltins adds missing built-in profiles and, after asking, resets
// modified ones; user profiles are never touched.
func (c *Controller) restoreBuiltins() {
	status := profiles.CheckBuiltins(c.config)
	if len(status.Missing)+len(status.Modified) == 0 {
		dialog.ShowInformation("Built-in profiles", "All built-in profiles are present and unmodified.", c.window)
		return
	}
	apply := func(reset bool) {
		done := profiles.RestoreBuiltins(&c.config, reset)
		c.saveConfig()
		c.profile.Options = c.profileNames()
		c.profile.Refresh()
		if reset && slices.Contains(done.Modified, c.profile.Selected) {
			c.loadProfile(c.profileByName(c.profile.Selected))
		}
		c.status.SetText(fmt.Sprintf("Built-in profiles restored: %d added, %d reset", len(done.Missing), len(done.Modified)))
	}
	if len(status.Modified) == 0 {
		apply(false)
		return
	}
	message := fmt.Sprintf("Modified built-in profiles: %s.\n\nReset them to their defaults too?", strings.Join(status.Modified, ", "))
	d := dialog.NewCustomConfirm("Restore built-in profiles", "Reset modified", "Only add missing", widget.NewLabel(message),
		apply, c.window)
	d.Show()
}

func (c *Controller) profileByName(name string) model.Profile {
	p, _ := profiles.Find(c.config.Profiles, name)
	return p
}

func (c *Controller) startupProfile() model.Profile {
	if p, ok := profiles.Find(c.config.Profiles, c.config.DefaultProfile); ok {
		return p
	}
	return c.config.Profiles[0]
}

func (c *Controller) selectRunnableProfile() {
	current := c.profileFromWidgets()
	if validation.ValidateProfile(current, c.caps, nil).Valid() {
		return
	}
	for _, candidate := range c.config.Profiles {
		if validation.ValidateProfile(candidate, c.caps, nil).Valid() {
			c.loadProfile(candidate)
			return
		}
	}
}

func (c *Controller) profileNames() []string {
	names := make([]string, 0, len(c.config.Profiles))
	for _, p := range c.config.Profiles {
		names = append(names, p.Name)
	}
	return names
}
