package ui

import (
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	fynetest "fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"

	"git.skobk.in/skobkin/yave/internal/encoder"
	"git.skobk.in/skobkin/yave/internal/ffmpeg"
	"git.skobk.in/skobkin/yave/internal/model"
	"git.skobk.in/skobkin/yave/internal/templates"
	"git.skobk.in/skobkin/yave/internal/vaapi"
	"git.skobk.in/skobkin/yave/internal/validation"
)

func TestFormatProgressInfoIncludesOnlyRoundedTimeAndSpeed(t *testing.T) {
	progress := model.Progress{
		Frame:      1234,
		OutTime:    80*time.Second + 400*time.Millisecond,
		Speed:      "1.5x",
		Percentage: 0.625,
	}

	got := formatProgressInfo(progress)

	if got != "1m20s | 1.5x" {
		t.Fatalf("progress info = %q, want %q", got, "1m20s | 1.5x")
	}
	if strings.Contains(got, "1234") || strings.Contains(got, "62.5") {
		t.Fatalf("progress info includes frame count or percentage: %q", got)
	}
}

func TestProgressInfoHasFixedWidthAndDoesNotResizeProgressBar(t *testing.T) {
	app := fynetest.NewApp()
	defer app.Quit()

	c := testController()
	c.createWidgets()
	// createWidgets hides the progress widgets by default (issue #16);
	// show them explicitly so the bar takes part in layout measurement.
	c.progress.Show()
	c.progressInfo.Show()
	statusBar := c.buildStatusBar()
	statusBar.Resize(fyne.NewSize(1050, statusBar.MinSize().Height))
	initialWidth := c.progress.Size().Width

	c.progressInfo.SetText(strings.Repeat("unexpectedly long progress text ", 5))
	statusBar.Layout.Layout(statusBar.Objects, statusBar.Size())

	if c.progressInfo.Truncation != fyne.TextTruncateEllipsis {
		t.Fatalf("progress info truncation = %v, want ellipsis", c.progressInfo.Truncation)
	}
	if c.progress.Size().Width != initialWidth {
		t.Fatalf("progress width = %v after update, want %v", c.progress.Size().Width, initialWidth)
	}
}

func TestProgressAndInfoAreHiddenAfterCreateWidgets(t *testing.T) {
	app := fynetest.NewApp()
	defer app.Quit()

	c := testController()
	c.createWidgets()

	if c.progress.Visible() {
		t.Fatal("progress bar should be hidden after createWidgets")
	}
	if c.progressInfo.Visible() {
		t.Fatal("progress info should be hidden after createWidgets")
	}
}

func TestProgressVisibilityFlipsAcrossRunLifecycle(t *testing.T) {
	app := fynetest.NewApp()
	defer app.Quit()

	c := &Controller{
		status:       widget.NewLabel("Idle"),
		progress:     widget.NewProgressBar(),
		progressInfo: widget.NewLabel(""),
		log:          widget.NewMultiLineEntry(),
	}
	c.createWidgets()

	if c.progress.Visible() || c.progressInfo.Visible() {
		t.Fatal("progress widgets should be hidden at idle")
	}

	c.setProgressVisible(true)
	if !c.progress.Visible() || !c.progressInfo.Visible() {
		t.Fatal("progress widgets should be visible after Show")
	}

	c.setProgressVisible(false)
	if c.progress.Visible() || c.progressInfo.Visible() {
		t.Fatal("progress widgets should be hidden after Hide")
	}
}

func TestRunFieldsUseAppropriateWrapping(t *testing.T) {
	app := fynetest.NewApp()
	defer app.Quit()

	c := &Controller{
		config: model.Config{
			Profiles: []model.Profile{{Name: "test"}},
		},
	}

	c.createWidgets()

	if c.command.Wrapping != fyne.TextWrapWord {
		t.Fatalf("command wrapping = %v, want word wrapping", c.command.Wrapping)
	}
	if c.log.Wrapping != fyne.TextWrapOff {
		t.Fatalf("output wrapping = %v, want wrapping disabled", c.log.Wrapping)
	}
}

func TestProfileBarExpandsSelectorAndRightAlignsActions(t *testing.T) {
	app := fynetest.NewApp()
	defer app.Quit()

	c := &Controller{
		config: model.Config{
			Profiles: []model.Profile{{Name: "test"}},
		},
	}
	c.createWidgets()

	bar := c.profileBar()
	bar.Resize(fyne.NewSize(1050, bar.MinSize().Height))

	if c.profile.Size().Width <= c.profile.MinSize().Width {
		t.Fatalf("profile selector width = %v, want wider than minimum %v", c.profile.Size().Width, c.profile.MinSize().Width)
	}

	barContainer, ok := bar.(*fyne.Container)
	if !ok {
		t.Fatalf("profile bar type = %T, want *fyne.Container", bar)
	}
	var actions *fyne.Container
	for _, object := range barContainer.Objects {
		candidate, ok := object.(*fyne.Container)
		if ok {
			actions = candidate
			break
		}
	}
	if actions == nil {
		t.Fatal("profile action container not found")
	}
	if got := actions.Position().X + actions.Size().Width; got != bar.Size().Width {
		t.Fatalf("profile actions right edge = %v, want %v", got, bar.Size().Width)
	}
}

func TestShowRunErrorSelectsRunTab(t *testing.T) {
	app := fynetest.NewApp()
	defer app.Quit()

	c := &Controller{
		status:       widget.NewLabel("Idle"),
		progressInfo: widget.NewLabel("old progress"),
		progress:     widget.NewProgressBar(),
		log:          widget.NewMultiLineEntry(),
	}
	otherTab := container.NewTabItem("Input", widget.NewLabel("input"))
	c.runTab = container.NewTabItem("Run", widget.NewLabel("run"))
	c.tabs = container.NewAppTabs(otherTab, c.runTab)
	c.progress.SetValue(0.5)

	c.showRunError("Validation failed", "Error: input path is required")

	if c.tabs.Selected() != c.runTab {
		t.Fatal("Run tab was not selected")
	}
	if c.status.Text != "Validation failed" {
		t.Fatalf("status = %q", c.status.Text)
	}
	if c.progressInfo.Text != "" {
		t.Fatalf("progress info = %q, want empty", c.progressInfo.Text)
	}
	if c.progress.Value != 0 {
		t.Fatalf("progress = %v, want 0", c.progress.Value)
	}
	if c.log.Text != "Error: input path is required" {
		t.Fatalf("output = %q", c.log.Text)
	}
}

func TestSetRunResultSelectsRunTabOnFailure(t *testing.T) {
	app := fynetest.NewApp()
	defer app.Quit()

	c := &Controller{
		status:       widget.NewLabel("Running"),
		progress:     widget.NewProgressBar(),
		progressInfo: widget.NewLabel(""),
	}
	otherTab := container.NewTabItem("Input", widget.NewLabel("input"))
	c.runTab = container.NewTabItem("Run", widget.NewLabel("run"))
	c.tabs = container.NewAppTabs(otherTab, c.runTab)

	c.setRunResult(errors.New("FFmpeg failed: exit status 1"), 0, 1)

	if c.tabs.Selected() != c.runTab {
		t.Fatal("Run tab was not selected")
	}
	if c.status.Text != "Failed: FFmpeg failed: exit status 1" {
		t.Fatalf("status = %q", c.status.Text)
	}
}

func TestEncoderSwitchUpdatesIntegerRangesAndClamps(t *testing.T) {
	app := fynetest.NewApp()
	defer app.Quit()

	c := testController()
	c.createWidgets()

	c.videoEncoder.SetSelected("av1_vaapi")
	c.rateControl.SetSelected("CQP")
	c.qp.SetValue(255)
	c.videoEncoder.SetSelected("h264_vaapi")

	if c.qp.Value() != 51 {
		t.Fatalf("QP = %d, want clamped value 51", c.qp.Value())
	}
	if c.qp.slider.Min != 1 || c.qp.slider.Max != 51 {
		t.Fatalf("QP range = %.0f-%.0f, want 1-51", c.qp.slider.Min, c.qp.slider.Max)
	}

	c.videoEncoder.SetSelected("libsvtav1")
	c.rateControl.SetSelected("CRF")
	c.crf.SetValue(63)
	c.videoEncoder.SetSelected("libx264")

	if c.crf.Value() != 51 {
		t.Fatalf("CRF = %d, want clamped value 51", c.crf.Value())
	}
	if c.crf.slider.Min != 0 || c.crf.slider.Max != 51 {
		t.Fatalf("CRF range = %.0f-%.0f, want 0-51", c.crf.slider.Min, c.crf.slider.Max)
	}
}

func TestEncoderSwitchProportionallyRemapsQPLargerRange(t *testing.T) {
	app := fynetest.NewApp()
	defer app.Quit()

	c := testController()
	c.createWidgets()

	// h264_vaapi QP is 1-51; switching to av1_vaapi (0-255) should remap
	// value 30 to ~148 instead of leaving it at 30.
	c.videoEncoder.SetSelected("h264_vaapi")
	c.rateControl.SetSelected("CQP")
	c.qp.SetValue(30)

	c.videoEncoder.SetSelected("av1_vaapi")

	if c.qp.Value() != 148 {
		t.Fatalf("QP = %d, want proportional remap 148", c.qp.Value())
	}
	if c.qp.slider.Min != 0 || c.qp.slider.Max != 255 {
		t.Fatalf("QP range = %.0f-%.0f, want 0-255", c.qp.slider.Min, c.qp.slider.Max)
	}
}

func TestEncoderSwitchProportionallyRemapsCRFLargerRange(t *testing.T) {
	app := fynetest.NewApp()
	defer app.Quit()

	c := testController()
	c.createWidgets()

	// libx264 CRF is 0-51; switching to libsvtav1 (0-63) should remap
	// value 30 to 37 (30/51 * 63 = 37.06 -> 37).
	c.videoEncoder.SetSelected("libx264")
	c.rateControl.SetSelected("CRF")
	c.crf.SetValue(30)

	c.videoEncoder.SetSelected("libsvtav1")

	if c.crf.Value() != 37 {
		t.Fatalf("CRF = %d, want proportional remap 37", c.crf.Value())
	}
	if c.crf.slider.Min != 0 || c.crf.slider.Max != 63 {
		t.Fatalf("CRF range = %.0f-%.0f, want 0-63", c.crf.slider.Min, c.crf.slider.Max)
	}
}

func TestEncoderSwitchSameRangeKeepsValue(t *testing.T) {
	app := fynetest.NewApp()
	defer app.Quit()

	c := testController()
	c.createWidgets()

	// h264_vaapi and hevc_vaapi share QP 1-51, so the value should be
	// preserved exactly on a switch.
	c.videoEncoder.SetSelected("h264_vaapi")
	c.rateControl.SetSelected("CQP")
	c.qp.SetValue(28)

	c.videoEncoder.SetSelected("hevc_vaapi")

	if c.qp.Value() != 28 {
		t.Fatalf("QP = %d, want unchanged 28 on same-range switch", c.qp.Value())
	}
}

func TestIntegerSliderDisplaysRangeValueAndEnabledState(t *testing.T) {
	app := fynetest.NewApp()
	defer app.Quit()

	changes := 0
	slider := newIntegerSlider(validation.IntegerRange{Min: 0, Max: 63}, 28, func(string) {
		changes++
	})

	if _, ok := slider.Object().(*fyne.Container); !ok {
		t.Fatalf("slider object type = %T, want *fyne.Container", slider.Object())
	}
	if slider.minLabel.Text != "0" || slider.value.Text != "28" || slider.maxLabel.Text != "63" {
		t.Fatalf("labels = %q %q %q, want 0 28 63", slider.minLabel.Text, slider.value.Text, slider.maxLabel.Text)
	}
	slider.SetValue(64)
	if slider.Value() != 63 || slider.value.Text != "63" {
		t.Fatalf("value = %d (%q), want clamped 63", slider.Value(), slider.value.Text)
	}
	if changes != 1 {
		t.Fatalf("change callbacks = %d, want 1", changes)
	}

	slider.Disable()
	if !slider.slider.Disabled() {
		t.Fatal("slider should be disabled")
	}
	slider.Enable()
	if slider.slider.Disabled() {
		t.Fatal("slider should be enabled")
	}
}

func TestSliderValuesRoundTripThroughProfile(t *testing.T) {
	app := fynetest.NewApp()
	defer app.Quit()

	c := testController()
	c.createWidgets()
	profile := c.config.Profiles[0]
	profile.Video.Encoder = "av1_vaapi"
	profile.Video.RateControl = "CQP"
	profile.Video.QP = 255
	profile.Audio.Mode = model.AudioEncode
	profile.Audio.Encoder = "flac"
	profile.Audio.Compression = 12

	c.loadProfile(profile)
	got := c.profileFromWidgets()

	if got.Video.QP != 255 {
		t.Fatalf("QP = %d, want 255", got.Video.QP)
	}
	if got.Audio.Compression != 12 {
		t.Fatalf("compression = %d, want 12", got.Audio.Compression)
	}
}

// Regression: loading a profile whose encoder has no curated preset or
// tune list (the VAAPI encoders) kept the previous profile's dropdown
// selection alive — populate empties the options but not the selection,
// and updateControlState treats a non-empty selection as current state.
// Load x265 "slow", then VAAPI "medium", then switch to libx264: the
// stale dropdown re-selected "slow" and the generated profile emitted
// it instead of the loaded "medium" (which only lived in the hidden
// text entry). The load clears the dropdown when the loaded encoder has
// no curated list, so the next curated switch seeds from the loaded
// value.
func TestProfileLoadClearsPresetSelectionForPresetlessEncoders(t *testing.T) {
	app := fynetest.NewApp()
	defer app.Quit()

	c := testController()
	c.createWidgets()

	c.loadProfile(model.Profile{
		Name:      "x265-slow",
		Container: "mkv",
		Video:     model.VideoSettings{Encoder: "libx265", Preset: "slow", Tune: "fast"},
	})
	if got := c.presetSelect.Selected; got != "slow" {
		t.Fatalf("preset selection after x265 load = %q, want slow", got)
	}

	// A preset-less encoder's saved value lives only in the free-text
	// entry; the dropdown must not carry the previous profile's state.
	c.loadProfile(model.Profile{
		Name:      "vaapi",
		Container: "mkv",
		Video:     model.VideoSettings{Encoder: "hevc_vaapi", Preset: "medium", Tune: "medium"},
	})
	if got := c.presetSelect.Selected; got != "" {
		t.Fatalf("preset selection after VAAPI load = %q, want cleared", got)
	}
	if got := c.preset.Text; got != "medium" {
		t.Fatalf("preset text entry = %q, want medium", got)
	}
	if got := c.tuneSelect.Selected; got != "" {
		t.Fatalf("tune selection after VAAPI load = %q, want cleared", got)
	}
	if got := c.tune.Text; got != "medium" {
		t.Fatalf("tune text entry = %q, want medium", got)
	}

	// Switching to a curated encoder seeds the dropdown from the loaded
	// value instead of resurrecting the previous profile's selection.
	c.videoEncoder.SetSelected("libx264")
	if got := c.presetSelect.Selected; got != "medium" {
		t.Fatalf("preset after libx264 switch = %q, want the loaded medium", got)
	}
	if got := c.profileFromWidgets().Video.Preset; got != "medium" {
		t.Fatalf("generated preset = %q, want medium", got)
	}
	if got := c.profileFromWidgets().Video.Tune; got != "medium" {
		t.Fatalf("generated tune = %q, want medium", got)
	}
}

func TestSVTPresetSwitchesControlsAndPreservesCategoricalValue(t *testing.T) {
	app := fynetest.NewApp()
	defer app.Quit()

	c := testController()
	c.createWidgets()
	c.videoEncoder.SetSelected("libx264")
	c.presetSelect.SetSelected("slow")

	c.videoEncoder.SetSelected("libsvtav1")
	if c.presetSelect.Visible() || !c.presetSlider.Visible() {
		t.Fatal("SVT-AV1 should show only the integer preset slider")
	}
	c.presetSlider.SetValue(13)
	if got := c.profileFromWidgets().Video.Preset; got != "13" {
		t.Fatalf("SVT preset = %q, want 13", got)
	}

	c.videoEncoder.SetSelected("libx265")
	// Back on a categorical encoder: the dropdown selection carries
	// over — "slow" is a shared x264/x265 ladder name.
	if !c.presetSelect.Visible() || c.preset.Visible() || c.presetSlider.Visible() {
		t.Fatal("x265 should show only the curated preset dropdown")
	}
	if got := c.presetSelect.Selected; got != "slow" {
		t.Fatalf("carried-over preset selection = %q, want slow", got)
	}
	if got := c.profileFromWidgets().Video.Preset; got != "slow" {
		t.Fatalf("categorical preset = %q, want slow", got)
	}
}

// TestVAAPIPresetFieldHidden verifies that encoders without a preset
// flag in ffmpeg (the VAAPI encoders) do not expose the preset widget.
// An enabled-but-ignored field would let users save a value that has
// no effect on the generated command, which is a silent footgun.
func TestVAAPIPresetFieldHidden(t *testing.T) {
	app := fynetest.NewApp()
	defer app.Quit()

	c := testController()
	c.createWidgets()

	c.videoEncoder.SetSelected("hevc_vaapi")
	if c.preset.Visible() {
		t.Errorf("hevc_vaapi must not show the categorical preset entry (it has no preset flag)")
	}
	if c.presetSlider.Visible() {
		t.Errorf("hevc_vaapi must not show the integer preset slider")
	}
	if c.presetSelect.Visible() {
		t.Errorf("hevc_vaapi must not show the curated preset dropdown (it has no preset flag)")
	}
	// Either hidden (our path) or disabled is acceptable. The bug
	// we're guarding against is the previous behaviour: enabled and
	// visible despite the encoder doing nothing with the value.
	if !c.preset.Hidden && c.preset.Disabled() == false {
		t.Errorf("hevc_vaapi preset must be hidden or disabled, not enabled")
	}

	// Switching to a categorical-preset encoder (libx264) must
	// re-show a preset control: libx264 curates its list, so the
	// dropdown appears.
	c.videoEncoder.SetSelected("libx264")
	if !c.presetSelect.Visible() || c.preset.Visible() || c.presetSlider.Visible() {
		t.Errorf("libx264 must show the curated preset dropdown after VAAPI")
	}
	if !c.tuneSelect.Visible() {
		t.Errorf("libx264 must show the curated tune dropdown after VAAPI")
	}
}

// TestLibx264CuratedPresetAndTuneDropdowns pins the libx264 dropdown
// options and the carryover between the two curated encoders: the
// shared preset names survive an x265 -> x264 encoder switch because
// populateCuratedOptions keeps the current selection when the new list
// knows it.
func TestLibx264CuratedPresetAndTuneDropdowns(t *testing.T) {
	app := fynetest.NewApp()
	defer app.Quit()

	c := testController()
	c.createWidgets()
	c.videoEncoder.SetSelected("libx264")

	wantPresets := []string{"(default)", "ultrafast", "superfast", "veryfast", "faster", "fast", "medium", "slow", "slower", "veryslow", "placebo"}
	if !slices.Equal(c.presetSelect.Options, wantPresets) {
		t.Fatalf("preset options = %v, want %v", c.presetSelect.Options, wantPresets)
	}
	wantTunes := []string{"(default)", "film", "animation", "grain", "stillimage", "psnr", "ssim", "fastdecode", "zerolatency"}
	if !slices.Equal(c.tuneSelect.Options, wantTunes) {
		t.Fatalf("tune options = %v, want %v", c.tuneSelect.Options, wantTunes)
	}

	c.presetSelect.SetSelected("veryslow")
	c.tuneSelect.SetSelected("film")
	c.videoEncoder.SetSelected("libx265")
	if got := c.profileFromWidgets().Video.Preset; got != "veryslow" {
		t.Fatalf("preset after x264->x265 switch = %q, want veryslow (shared ladder name)", got)
	}
	// "film" is x264-only: the selection must not silently persist as a
	// value libx265 would reject — it stays visible via the union, and
	// the saved profile keeps it (the user set it deliberately).
	if got := c.presetSelect.Selected; got != "veryslow" {
		t.Fatalf("preset selection after switch = %q, want veryslow", got)
	}
	if got := c.tuneSelect.Selected; got != "film" {
		t.Fatalf("tune selection after switch = %q, want film (kept visible by the union)", got)
	}
	if got := c.profileFromWidgets().Video.Tune; got != "film" {
		t.Fatalf("tune after switch = %q, want film", got)
	}
}

// TestLibvpxVp9CuratedDeadlineDropdown pins the VP9 deadline dropdown:
// the row is labeled Preset but drives -deadline, and "(default)" must
// leave the flag out entirely (the builder omits it for an empty
// value).
func TestLibvpxVp9CuratedDeadlineDropdown(t *testing.T) {
	app := fynetest.NewApp()
	defer app.Quit()

	c := testController()
	c.createWidgets()
	c.videoEncoder.SetSelected("libvpx-vp9")

	want := []string{"(default)", "best", "good", "realtime"}
	if !slices.Equal(c.presetSelect.Options, want) {
		t.Fatalf("deadline options = %v, want %v", c.presetSelect.Options, want)
	}

	c.presetSelect.SetSelected("realtime")
	if got := c.profileFromWidgets().Video.Preset; got != "realtime" {
		t.Fatalf("deadline selection = %q, want realtime", got)
	}
	c.presetSelect.SetSelected("(default)")
	if got := c.profileFromWidgets().Video.Preset; got != "" {
		t.Fatalf("(default) deadline = %q, want empty", got)
	}

	// An out-of-list saved value stays visible and round-trips.
	profile := c.config.Profiles[0]
	profile.Video.Encoder = "libvpx-vp9"
	profile.Video.Preset = "good"
	c.loadProfile(profile)
	if c.presetSelect.Selected != "good" {
		t.Fatalf("loaded deadline selection = %q, want good", c.presetSelect.Selected)
	}
}

// TestNVENCCuratedPresetDropdown pins the NVENC preset dropdown: the
// validated p1..p7 (+legacy) list is offered instead of free text, and
// the selection lands in the saved profile verbatim (the builder
// canonicalizes case variants, as before).
func TestNVENCCuratedPresetDropdown(t *testing.T) {
	app := fynetest.NewApp()
	defer app.Quit()

	c := testController()
	c.createWidgets()
	c.videoEncoder.SetSelected("h264_nvenc")

	want := []string{"(default)", "p1", "p2", "p3", "p4", "p5", "p6", "p7", "slow", "medium", "fast", "hp", "hq", "bd"}
	if !slices.Equal(c.presetSelect.Options, want) {
		t.Fatalf("preset options = %v, want %v", c.presetSelect.Options, want)
	}
	if c.preset.Visible() || c.presetSlider.Visible() {
		t.Fatal("h264_nvenc must show only the curated preset dropdown")
	}

	c.presetSelect.SetSelected("p7")
	if got := c.profileFromWidgets().Video.Preset; got != "p7" {
		t.Fatalf("preset selection = %q, want p7", got)
	}
	c.videoEncoder.SetSelected("av1_nvenc")
	// av1_nvenc curates only p1..p7; the legacy names must not surface
	// as selectable options for it.
	for _, legacy := range []string{"slow", "hp", "bd"} {
		if slices.Contains(c.presetSelect.Options, legacy) {
			t.Errorf("av1_nvenc options must not contain legacy preset %q: %v", legacy, c.presetSelect.Options)
		}
	}
	if got := c.profileFromWidgets().Video.Preset; got != "p7" {
		t.Fatalf("preset after nvenc switch = %q, want p7 (shared p1..p7 name)", got)
	}
}

// TestQSVCuratedPresetDropdown pins the QSV preset dropdown: the
// validated veryfast..veryslow list is offered for all four QSV
// encoders instead of free text.
func TestQSVCuratedPresetDropdown(t *testing.T) {
	app := fynetest.NewApp()
	defer app.Quit()

	c := testController()
	c.createWidgets()
	c.videoEncoder.SetSelected("hevc_qsv")

	want := []string{"(default)", "veryfast", "faster", "fast", "medium", "slow", "slower", "veryslow"}
	if !slices.Equal(c.presetSelect.Options, want) {
		t.Fatalf("preset options = %v, want %v", c.presetSelect.Options, want)
	}
	if c.preset.Visible() || c.presetSlider.Visible() {
		t.Fatal("hevc_qsv must show only the curated preset dropdown")
	}

	c.presetSelect.SetSelected("veryslow")
	if got := c.profileFromWidgets().Video.Preset; got != "veryslow" {
		t.Fatalf("preset selection = %q, want veryslow", got)
	}
	// The same list applies to the other QSV encoders.
	c.videoEncoder.SetSelected("vp9_qsv")
	if !slices.Equal(c.presetSelect.Options, want) {
		t.Fatalf("vp9_qsv preset options = %v, want %v", c.presetSelect.Options, want)
	}
}

// TestLibx265CuratedPresetAndTuneDropdowns pins the libx265 dropdown
// behavior: curated options prefixed by "(default)", "(default)"
// emitting no flag value, round-trips through loadProfile, and
// out-of-list profile values staying visible instead of being silently
// dropped.
func TestLibx265CuratedPresetAndTuneDropdowns(t *testing.T) {
	app := fynetest.NewApp()
	defer app.Quit()

	c := testController()
	c.createWidgets()
	c.videoEncoder.SetSelected("libx265")

	wantPresets := []string{"(default)", "ultrafast", "superfast", "veryfast", "faster", "fast", "medium", "slow", "slower", "veryslow", "placebo"}
	if !slices.Equal(c.presetSelect.Options, wantPresets) {
		t.Fatalf("preset options = %v, want %v", c.presetSelect.Options, wantPresets)
	}
	wantTunes := []string{"(default)", "psnr", "ssim", "grain", "fastdecode", "animation", "zerolatency"}
	if !slices.Equal(c.tuneSelect.Options, wantTunes) {
		t.Fatalf("tune options = %v, want %v", c.tuneSelect.Options, wantTunes)
	}
	if !c.tuneSelect.Visible() || c.tune.Visible() {
		t.Fatal("x265 should show the curated tune dropdown")
	}

	c.presetSelect.SetSelected("slow")
	c.tuneSelect.SetSelected("grain")
	got := c.profileFromWidgets().Video
	if got.Preset != "slow" || got.Tune != "grain" {
		t.Fatalf("profile preset/tune = %q/%q, want slow/grain", got.Preset, got.Tune)
	}

	// "(default)" stands for the empty string: the builder omits both
	// flags when they are empty.
	c.presetSelect.SetSelected("(default)")
	c.tuneSelect.SetSelected("(default)")
	got = c.profileFromWidgets().Video
	if got.Preset != "" || got.Tune != "" {
		t.Fatalf("(default) preset/tune = %q/%q, want empty", got.Preset, got.Tune)
	}

	// Round-trip: a saved profile's values land in the dropdowns.
	profile := c.config.Profiles[0]
	profile.Video.Encoder = "libx265"
	profile.Video.Preset = "veryslow"
	profile.Video.Tune = "zerolatency"
	c.loadProfile(profile)
	if c.presetSelect.Selected != "veryslow" || c.tuneSelect.Selected != "zerolatency" {
		t.Fatalf("loaded selections = %q/%q, want veryslow/zerolatency", c.presetSelect.Selected, c.tuneSelect.Selected)
	}
	if got := c.profileFromWidgets().Video; got.Preset != "veryslow" || got.Tune != "zerolatency" {
		t.Fatalf("round-tripped preset/tune = %q/%q", got.Preset, got.Tune)
	}

	// An empty saved value lands on "(default)", not on the previous
	// profile's selection.
	profile.Video.Preset = ""
	profile.Video.Tune = ""
	c.loadProfile(profile)
	if c.presetSelect.Selected != "(default)" || c.tuneSelect.Selected != "(default)" {
		t.Fatalf("empty saved preset/tune selections = %q/%q, want (default)", c.presetSelect.Selected, c.tuneSelect.Selected)
	}

	// A hand-edited out-of-list value must stay visible so it keeps
	// round-tripping rather than being silently emptied.
	profile.Video.Preset = "exotic"
	c.loadProfile(profile)
	if c.presetSelect.Selected != "exotic" {
		t.Fatalf("out-of-list preset selection = %q, want exotic", c.presetSelect.Selected)
	}
	if got := c.profileFromWidgets().Video.Preset; got != "exotic" {
		t.Fatalf("out-of-list preset round-trip = %q, want exotic", got)
	}
}

func TestCRFAndQPRemapOnlyWhenPriorEncoderSupportedTheControl(t *testing.T) {
	app := fynetest.NewApp()
	defer app.Quit()

	// 1. Starting on hevc_vaapi (does not expose CRF) and then switching to
	//    libsvtav1 must not rewrite the stored CRF: the prior encoder never
	//    owned a CRF range, so its {0, 51} default would silently remap 28
	//    to ~35 even though the user never touched CRF.
	c := testController()
	c.createWidgets()
	c.videoEncoder.SetSelected("hevc_vaapi")
	c.updateIntegerRanges()
	if got := c.crf.Value(); got != 28 {
		t.Fatalf("CRF after hevc_vaapi = %d, want 28", got)
	}
	c.videoEncoder.SetSelected("libsvtav1")
	if got := c.crf.Value(); got != 28 {
		t.Fatalf("CRF after hevc_vaapi->libsvtav1 = %d, want 28 (no remap from stale range)", got)
	}

	// 2. Symmetric case: libx264 owns CRF {0,51}, switching to libsvtav1
	//    {0,63} with CRF=23 should remap, not just clamp.
	c = testController()
	c.createWidgets()
	c.videoEncoder.SetSelected("libx264")
	c.updateIntegerRanges()
	c.crf.SetValue(23)
	c.videoEncoder.SetSelected("libsvtav1")
	if got := c.crf.Value(); got == 23 {
		t.Fatalf("CRF after libx264(23)->libsvtav1 = %d, want remapped (not 23)", got)
	}

	// 3. QP: starting on libx265 (does not expose QP) and switching to
	//    av1_vaapi must keep the stored QP value instead of remapping from
	//    the stale {1, 51} initial range.
	c = testController()
	c.createWidgets()
	c.videoEncoder.SetSelected("libx265")
	c.updateIntegerRanges()
	if got := c.qp.Value(); got != 30 {
		t.Fatalf("QP after libx265 = %d, want 30", got)
	}
	c.videoEncoder.SetSelected("av1_vaapi")
	if got := c.qp.Value(); got != 30 {
		t.Fatalf("QP after libx265->av1_vaapi = %d, want 30 (no remap from stale range)", got)
	}

	// 4. QP between two VAAPI encoders that own the range: hevc_vaapi
	//    {1, 51} -> av1_vaapi {0, 255} with QP=30 should remap.
	c = testController()
	c.createWidgets()
	c.videoEncoder.SetSelected("hevc_vaapi")
	c.updateIntegerRanges()
	c.qp.SetValue(30)
	c.videoEncoder.SetSelected("av1_vaapi")
	if got := c.qp.Value(); got == 30 {
		t.Fatalf("QP after hevc_vaapi(30)->av1_vaapi = %d, want remapped (not 30)", got)
	}
}

func testController() *Controller {
	return &Controller{
		config: model.Config{
			Profiles: []model.Profile{{
				Name:           "test",
				Container:      model.ContainerMKV,
				OutputTemplate: "{dir}/{name}.{ext}",
			}},
		},
	}
}

// deviceCapableController returns a controller whose fake detection
// found one render node, so the device dropdowns have options.
func deviceCapableController() *Controller {
	c := testController()
	c.createWidgets()
	c.caps = model.Capabilities{RenderNodes: []string{"/dev/dri/renderD128"}}
	c.updateDeviceOptions()
	return c
}

// videoFormLabels reads the labels of the currently built video form.
func videoFormLabels(c *Controller) []string {
	labels := []string{}
	if len(c.videoFormBox.Objects) == 0 {
		return labels
	}
	form, ok := c.videoFormBox.Objects[0].(*widget.Form)
	if !ok {
		return labels
	}
	for _, item := range form.Items {
		labels = append(labels, item.Text)
	}
	return labels
}

// TestEncoderSwitchShowsOnlyMatchingDeviceRow pins the hide-not-grey
// device dispatch: each hardware backend shows exactly its own device
// row, software and copy encoders show none.
func TestEncoderSwitchShowsOnlyMatchingDeviceRow(t *testing.T) {
	app := fynetest.NewApp()
	defer app.Quit()

	c := deviceCapableController()

	c.videoEncoder.SetSelected("hevc_vaapi")
	labels := videoFormLabels(c)
	if !slices.Contains(labels, "VAAPI device") {
		t.Errorf("hevc_vaapi rows %v miss the VAAPI device row", labels)
	}
	if slices.Contains(labels, "QSV device") || slices.Contains(labels, "GPU index") {
		t.Errorf("hevc_vaapi rows %v must not show QSV/GPU rows", labels)
	}
	if c.vaapiDevice.Disabled() {
		t.Errorf("visible VAAPI device row must be enabled")
	}

	c.videoEncoder.SetSelected("hevc_nvenc")
	labels = videoFormLabels(c)
	if !slices.Contains(labels, "GPU index") {
		t.Errorf("hevc_nvenc rows %v miss the GPU index row", labels)
	}
	if slices.Contains(labels, "VAAPI device") || slices.Contains(labels, "QSV device") {
		t.Errorf("hevc_nvenc rows %v must not show VAAPI/QSV rows", labels)
	}

	c.videoEncoder.SetSelected("hevc_qsv")
	labels = videoFormLabels(c)
	if !slices.Contains(labels, "QSV device") {
		t.Errorf("hevc_qsv rows %v miss the QSV device row", labels)
	}
	if slices.Contains(labels, "VAAPI device") || slices.Contains(labels, "GPU index") {
		t.Errorf("hevc_qsv rows %v must not show VAAPI/GPU rows", labels)
	}
	if c.qsvDevice.Disabled() {
		t.Errorf("visible QSV device row must be enabled")
	}

	// Software encoder: no device rows.
	c.videoEncoder.SetSelected("libx265")
	labels = videoFormLabels(c)
	for _, deviceRow := range []string{"VAAPI device", "QSV device", "GPU index"} {
		if slices.Contains(labels, deviceRow) {
			t.Errorf("libx265 rows %v must not show %q", labels, deviceRow)
		}
	}

	// Copy: the tab collapses to the Encoder row.
	c.videoEncoder.SetSelected("copy")
	labels = videoFormLabels(c)
	if !slices.Equal(labels, []string{"Encoder"}) {
		t.Errorf("copy rows = %v, want [Encoder]", labels)
	}
}

// TestProfileFromWidgetsIncludesQSVDeviceAndGPUIndex verifies the new
// fields round-trip through loadProfile / profileFromWidgets, which is
// what serialization to the profile YAML depends on.
func TestProfileFromWidgetsIncludesQSVDeviceAndGPUIndex(t *testing.T) {
	app := fynetest.NewApp()
	defer app.Quit()

	c := testController()
	c.createWidgets()
	profile := c.config.Profiles[0]
	profile.Video.Encoder = "hevc_qsv"
	profile.Video.QSVDevice = "/dev/dri/renderD128"
	profile.Video.GPUIndex = 2

	c.loadProfile(profile)
	got := c.profileFromWidgets()

	if got.Video.QSVDevice != "/dev/dri/renderD128" {
		t.Errorf("QSVDevice = %q, want /dev/dri/renderD128", got.Video.QSVDevice)
	}
	if got.Video.GPUIndex != 2 {
		t.Errorf("GPUIndex = %d, want 2", got.Video.GPUIndex)
	}
}

// TestProfileClearedWhenEncoderDoesNotSupportProfile pins the fix for
// the av1_nvenc trap: the Profile widget is disabled (because
// av1_nvenc rejects -profile:v), so a stale value (e.g. "main" from
// the previous encoder) cannot be edited away by the user. The
// widget still feeds profileFromWidgets, so profileFromWidgets would
// emit the stale profile and nvencEncoder.Validate would reject it —
// the user is stuck. Switching encoder must clear the selection so the
// round-trip through loadProfile / profileFromWidgets does not
// contain a value the encoder refuses.
func TestProfileClearedWhenEncoderDoesNotSupportProfile(t *testing.T) {
	app := fynetest.NewApp()
	defer app.Quit()

	c := testController()
	c.createWidgets()

	// Two paths leave the Profile widget populated: (a) the user
	// picked/saved a profile on a supporting encoder (e.g. h264_nvenc
	// "main"), then (b) the user picks av1_nvenc. Both paths must
	// drop the value.
	t.Run("user switch to av1_nvenc", func(t *testing.T) {
		c.videoEncoder.SetSelected("h264_nvenc")
		c.videoProfile.SetSelected("main")
		c.videoEncoder.SetSelected("av1_nvenc")

		if got := profileOptionToValue(c.videoProfile.Selected); got != "" {
			t.Errorf("videoProfile selection after switch to av1_nvenc = %q, want cleared", got)
		}
		if got := c.profileFromWidgets().Video.Profile; got != "" {
			t.Errorf("profileFromWidgets().Video.Profile after switch to av1_nvenc = %q, want \"\"", got)
		}
	})

	t.Run("loadProfile directly into av1_nvenc with stale profile", func(t *testing.T) {
		// loadProfile runs with c.loading=true; the clear happens in
		// the deferred updateControlState when c.loading drops.
		profile := c.config.Profiles[0]
		profile.Video.Encoder = "av1_nvenc"
		profile.Video.Profile = "main"
		c.loadProfile(profile)

		if got := profileOptionToValue(c.videoProfile.Selected); got != "" {
			t.Errorf("videoProfile selection after load = %q, want cleared", got)
		}
		if got := c.profileFromWidgets().Video.Profile; got != "" {
			t.Errorf("profileFromWidgets().Video.Profile after load = %q, want \"\"", got)
		}
	})

	// Sanity check: switching back to a profile-supporting encoder
	// must not invent a value where the user had none. The widget
	// lands on "(default)" — the visible null state that emits no
	// -profile:v — rather than keeping a phantom value.
	t.Run("switching back does not repopulate", func(t *testing.T) {
		c.videoEncoder.SetSelected("av1_nvenc")

		c.videoEncoder.SetSelected("h264_nvenc")
		if got := profileOptionToValue(c.videoProfile.Selected); got != "" {
			t.Errorf("videoProfile selection after switch back to h264_nvenc = %q, want \"\"", got)
		}
		if got := c.profileFromWidgets().Video.Profile; got != "" {
			t.Errorf("profileFromWidgets().Video.Profile after switch back = %q, want \"\"", got)
		}
	})
}

// TestLoadProfilePreservesSavedRateControl pins the round-trip from a
// profile with a non-default rate control (CQP, VBR) for encoders
// whose own default differs (VAAPI defaults to Auto, NVENC/QSV to
// CQP). Before the fix, loadProfile called SetSelected before
// updateControlState populated the dropdown options; Fyne dropped the
// selection silently and the deferred update fell back to the
// encoder default, so a built-in VAAPI profile saved as CQP/VBR
// silently reloaded as Auto and produced different FFmpeg args.
func TestLoadProfilePreservesSavedRateControl(t *testing.T) {
	app := fynetest.NewApp()
	defer app.Quit()

	c := testController()
	c.createWidgets()

	cases := []struct {
		encoder  string
		saved    string
		wantOpts []string
	}{
		// VAAPI default is Auto — saved CQP must survive.
		{encoder: "hevc_vaapi", saved: "CQP", wantOpts: []string{"Auto", "CQP", "VBR", "CBR"}},
		{encoder: "hevc_vaapi", saved: "VBR", wantOpts: []string{"Auto", "CQP", "VBR", "CBR"}},
		{encoder: "hevc_vaapi", saved: "CBR", wantOpts: []string{"Auto", "CQP", "VBR", "CBR"}},
		// NVENC default is CQP — saved VBR/CBR must survive.
		{encoder: "hevc_nvenc", saved: "VBR", wantOpts: []string{"CQP", "VBR", "CBR"}},
		{encoder: "hevc_nvenc", saved: "CBR", wantOpts: []string{"CQP", "VBR", "CBR"}},
		// QSV default is CQP — saved VBR/CBR must survive.
		{encoder: "hevc_qsv", saved: "VBR", wantOpts: []string{"CQP", "VBR", "CBR"}},
	}
	for _, tt := range cases {
		t.Run(tt.encoder+"/"+tt.saved, func(t *testing.T) {
			profile := c.config.Profiles[0]
			profile.Video.Encoder = tt.encoder
			profile.Video.RateControl = tt.saved

			c.loadProfile(profile)

			if !slices.Equal(c.rateControl.Options, tt.wantOpts) {
				t.Errorf("rate-control options after load = %v, want %v", c.rateControl.Options, tt.wantOpts)
			}
			if c.rateControl.Selected != tt.saved {
				t.Errorf("rate control after load = %q, want %q (saved value was lost)", c.rateControl.Selected, tt.saved)
			}
			if got := c.profileFromWidgets().Video.RateControl; got != tt.saved {
				t.Errorf("profileFromWidgets rate control = %q, want %q", got, tt.saved)
			}
		})
	}
}

// TestEncoderRegistryInterfacesForCuratedSet sanity-checks the encoder
// registry exposes the full curated set the UI assumes — every name the
// tests above touch must be present. This guards against accidental
// removal during future refactors.
func TestEncoderRegistryInterfacesForCuratedSet(t *testing.T) {
	want := []string{
		"libx264", "libx265", "libsvtav1", "libvpx-vp9", "libaom-av1",
		"h264_vaapi", "hevc_vaapi", "av1_vaapi", "vp9_vaapi",
		"h264_nvenc", "hevc_nvenc", "av1_nvenc",
		"h264_qsv", "hevc_qsv", "av1_qsv", "vp9_qsv",
	}
	reg := encoder.DefaultRegistry()
	for _, name := range want {
		e, ok := reg.Get(name)
		if !ok {
			t.Errorf("registry missing %q", name)
			continue
		}
		if _, ok := e.(encoder.VideoEncoder); !ok {
			t.Errorf("%q is registered but does not implement VideoEncoder", name)
		}
	}
}

func TestHardwareDevicesSummary(t *testing.T) {
	cases := []struct {
		name string
		caps model.Capabilities
		want string
	}{
		{"none available", model.Capabilities{}, "none"},
		{"vaapi and qsv", model.Capabilities{HWVAAPIAvailable: true, HWQSVAvailable: true}, "vaapi, qsv"},
		{"nvenc single gpu", model.Capabilities{HWNVENCAvailable: true, NVENCGPUCount: 1}, "nvenc"},
		{"all with multi gpu", model.Capabilities{HWVAAPIAvailable: true, HWQSVAvailable: true, HWNVENCAvailable: true, NVENCGPUCount: 3}, "vaapi, qsv, nvenc (3 GPUs)"},
		{"multi gpu only", model.Capabilities{HWNVENCAvailable: true, NVENCGPUCount: 2}, "nvenc (2 GPUs)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := hardwareDevicesSummary(tc.caps); got != tc.want {
				t.Errorf("hardwareDevicesSummary() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestSelectRunnableProfileFallsBackWhenHWFiltered covers GPU removal:
// the default profile references hevc_nvenc, detection finds no NVIDIA
// device, and the app must land on the software profile with the
// encoder dropdown actually showing the fallback encoder.
func TestSelectRunnableProfileFallsBackWhenHWFiltered(t *testing.T) {
	app := fynetest.NewApp()
	defer app.Quit()

	nvencProfile := model.Profile{
		Name:           "NVENC",
		Container:      model.ContainerMKV,
		OutputTemplate: "{dir}/{name}.{ext}",
		Video: model.VideoSettings{
			Encoder:       "hevc_nvenc",
			RateControl:   "CQP",
			QP:            30,
			FrameRateMode: "keep",
		},
		Audio:     model.AudioSettings{Mode: model.AudioCopy, Encoder: "copy"},
		Subtitles: model.SubtitleSettings{Mode: model.SubtitleNone},
	}
	softwareProfile := model.Profile{
		Name:           "Software",
		Container:      model.ContainerMKV,
		OutputTemplate: "{dir}/{name}.{ext}",
		Video: model.VideoSettings{
			Encoder:       "libx265",
			RateControl:   "CRF",
			CRF:           28,
			FrameRateMode: "keep",
		},
		Audio:     model.AudioSettings{Mode: model.AudioCopy, Encoder: "copy"},
		Subtitles: model.SubtitleSettings{Mode: model.SubtitleNone},
	}

	c := &Controller{config: model.Config{
		Profiles:       []model.Profile{nvencProfile, softwareProfile},
		DefaultProfile: "NVENC",
	}}
	c.createWidgets()
	c.loadProfile(nvencProfile)

	// Simulate detection with NVENC filtered out: caps contains only
	// the software encoder, mirroring filterUnavailableHWEncoders.
	c.caps = model.Capabilities{
		Encoders: map[string]model.Encoder{
			"libx265": {Name: "libx265", Media: ffmpeg.MediaVideo},
			"copy":    {Name: "copy", Media: "video,audio"},
		},
	}

	// Mirror the detect() ordering: options refreshed BEFORE profile
	// selection, so Fyne's SetSelected lands on the fallback encoder.
	// (The full production sequence is pinned by TestApplyDetectionSequence.)
	c.updateEncoderOptions()
	c.selectRunnableProfile()

	if got := c.profile.Selected; got != "Software" {
		t.Errorf("selected profile = %q, want %q", got, "Software")
	}
	if got := c.videoEncoder.Selected; got != "libx265" {
		t.Errorf("selected encoder = %q, want %q", got, "libx265")
	}
}

// TestApplyDetectionSequence pins the end state of the real
// detection-application ordering (updateEncoderOptions →
// updateDeviceOptions → reconcileDevices(false) → updateControlState →
// selectRunnableProfile): detect-time reconciliation replaces a
// vanished seed, a fallback candidate loaded by selectRunnableProfile
// wins through its load-time reconciliation, and re-applying the same
// capabilities stays quiet.
func TestApplyDetectionSequence(t *testing.T) {
	app := fynetest.NewApp()
	defer app.Quit()

	stale := model.Profile{
		Name:           "Stale",
		Container:      model.ContainerMKV,
		OutputTemplate: "{dir}/{name}.{ext}",
		Video: model.VideoSettings{
			Encoder:       "hevc_vaapi",
			RateControl:   "VBR",
			VAAPIDevice:   "/dev/dri/renderD99", // not among the detected nodes
			FrameRateMode: "keep",
			// VBR without a target bitrate is invalid, forcing the
			// selectRunnableProfile fallback below.
		},
		Audio:     model.AudioSettings{Mode: model.AudioCopy, Encoder: "copy"},
		Subtitles: model.SubtitleSettings{Mode: model.SubtitleNone},
	}
	good := stale
	good.Name = "Good"
	good.Video.RateControl = "CQP"
	good.Video.QP = 26
	good.Video.VAAPIDevice = "/dev/dri/renderD129" // also not detected

	c := &Controller{config: model.Config{Profiles: []model.Profile{stale, good}, DefaultProfile: "Stale"}}
	c.createWidgets()
	c.loadProfile(stale)

	caps := model.Capabilities{
		RenderNodes: []string{"/dev/dri/renderD128"},
		Encoders: map[string]model.Encoder{
			"hevc_vaapi": {Name: "hevc_vaapi", Media: ffmpeg.MediaVideo},
			"copy":       {Name: "copy", Media: "video,audio"},
		},
	}

	c.applyDetection(caps)

	// The runnable fallback was loaded; its saved device was not
	// detected either, so the load-time reconciliation replaced it
	// with automatic selection and reseeded to empty. (The pinned
	// ordering of the detection sequence is unchanged by the
	// automatic-device feature; only the replacement target moved
	// from the first node to "(Auto)", which lets the capability
	// picker choose a compatible node once devices are probed.)
	if got := c.profile.Selected; got != "Good" {
		t.Errorf("selected profile after detection = %q, want the runnable fallback %q", got, "Good")
	}
	if got := c.vaapiDevice.Selected; got != vaapiAutoOption {
		t.Errorf("device selection after detection = %q, want the replacement %q", got, vaapiAutoOption)
	}
	if c.vaapiDeviceSeed != "" {
		t.Errorf("device seed after detection = %q, want updated to the empty automatic value", c.vaapiDeviceSeed)
	}

	// Re-applying the same capabilities stays quiet: nothing flips
	// back to a saved-but-absent path.
	c.applyDetection(caps)
	if got := c.vaapiDevice.Selected; got != vaapiAutoOption {
		t.Errorf("device selection after re-application = %q, want unchanged", got)
	}
	if got := c.profile.Selected; got != "Good" {
		t.Errorf("selected profile after re-application = %q, want unchanged", got)
	}
}

// TestProfileOptionsFollowEncoder pins the per-encoder profile
// dropdown: options rebuild from the encoder's curated list plus the
// "(default)" prefix, unsupported encoders empty the list, and the
// detected build's narrowed list takes precedence when present.
func TestProfileOptionsFollowEncoder(t *testing.T) {
	app := fynetest.NewApp()
	defer app.Quit()

	c := testController()
	c.createWidgets()

	c.videoEncoder.SetSelected("hevc_vaapi")
	want := []string{"(default)", "main", "main10", "rext"}
	if !slices.Equal(c.videoProfile.Options, want) {
		t.Errorf("hevc_vaapi profile options = %v, want %v", c.videoProfile.Options, want)
	}
	if got := c.videoProfile.Selected; got != "(default)" {
		t.Errorf("hevc_vaapi initial profile selection = %q, want %q", got, "(default)")
	}

	c.videoEncoder.SetSelected("av1_nvenc")
	if len(c.videoProfile.Options) != 0 {
		t.Errorf("av1_nvenc profile options = %v, want empty", c.videoProfile.Options)
	}

	// The detected build's list wins over the curated fallback.
	c.videoEncoder.SetSelected("hevc_vaapi")
	c.caps = model.Capabilities{Encoders: map[string]model.Encoder{
		"hevc_vaapi": {Name: "hevc_vaapi", Media: ffmpeg.MediaVideo, Profiles: []string{"main"}},
	}}
	c.updateControlState()
	if !slices.Equal(c.videoProfile.Options, []string{"(default)", "main"}) {
		t.Errorf("build-narrowed options = %v, want [(default) main]", c.videoProfile.Options)
	}
}

// TestProfileOptionValueMapping covers the "(default)" <-> "" mapping
// in both directions.
func TestProfileOptionValueMapping(t *testing.T) {
	if got := profileValueToOption(""); got != "(default)" {
		t.Errorf("profileValueToOption(\"\") = %q, want %q", got, "(default)")
	}
	if got := profileValueToOption("main"); got != "main" {
		t.Errorf("profileValueToOption(\"main\") = %q, want %q", got, "main")
	}
	if got := profileOptionToValue("(default)"); got != "" {
		t.Errorf("profileOptionToValue(\"(default)\") = %q, want \"\"", got)
	}
	if got := profileOptionToValue("main10"); got != "main10" {
		t.Errorf("profileOptionToValue(\"main10\") = %q, want %q", got, "main10")
	}
}

// TestLoadProfileSeedsProfileOptions pins that loadProfile makes the
// saved profile value selectable before reading it back: an
// out-of-list value stays visible in the dropdown (union seeding) so
// validation — not a silently emptied widget — reports it, and an
// in-list value is selected verbatim.
func TestLoadProfileSeedsProfileOptions(t *testing.T) {
	app := fynetest.NewApp()
	defer app.Quit()

	c := testController()
	c.createWidgets()

	profile := c.config.Profiles[0]
	profile.Video.Encoder = "hevc_vaapi"
	profile.Video.Profile = "rext"
	c.loadProfile(profile)
	if got := c.profileFromWidgets().Video.Profile; got != "rext" {
		t.Errorf("in-list profile round-trip = %q, want rext", got)
	}

	// A value outside the curated list must survive the load instead
	// of being silently dropped by SetSelected.
	profile.Video.Profile = "main444"
	c.loadProfile(profile)
	if got := c.videoProfile.Selected; got != "main444" {
		t.Errorf("out-of-list selection = %q, want main444 kept visible", got)
	}
	if !slices.Contains(c.videoProfile.Options, "main444") {
		t.Errorf("options %v do not contain the out-of-list saved value", c.videoProfile.Options)
	}
	if got := c.profileFromWidgets().Video.Profile; got != "main444" {
		t.Errorf("out-of-list profile round-trip = %q, want main444", got)
	}
}

// TestDeviceOptionsFromDetectedNodes pins that the device dropdowns
// mirror caps.RenderNodes, with the QSV default option prefixed.
func TestDeviceOptionsFromDetectedNodes(t *testing.T) {
	app := fynetest.NewApp()
	defer app.Quit()

	c := testController()
	c.createWidgets()
	c.caps = model.Capabilities{RenderNodes: []string{"/dev/dri/renderD128", "/dev/dri/renderD129"}}
	c.updateDeviceOptions()

	if !slices.Equal(c.vaapiDevice.Options, []string{vaapiAutoOption, "renderD128", "renderD129"}) {
		t.Errorf("vaapiDevice options = %v, want (Auto) plus the detected nodes", c.vaapiDevice.Options)
	}
	if !slices.Equal(c.qsvDevice.Options, []string{"(default)", "/dev/dri/renderD128", "/dev/dri/renderD129"}) {
		t.Errorf("qsvDevice options = %v, want (default) plus the detected nodes", c.qsvDevice.Options)
	}
}

// TestReconcileDevicesReplacesMissingSeed covers the load-time case:
// a saved device that no longer exists among the detected nodes is
// replaced with automatic selection (QSV: the default device) and the
// seed is updated to the empty auto value so later detections stay
// quiet. The window is nil in tests, so the notification path must be
// skipped safely.
func TestReconcileDevicesReplacesMissingSeed(t *testing.T) {
	app := fynetest.NewApp()
	defer app.Quit()

	c := deviceCapableController()
	c.vaapiDeviceSeed = "/dev/dri/renderD99"
	c.qsvDeviceSeed = "/dev/dri/renderD99"
	c.reconcileDevices(true)

	if got := c.vaapiDevice.Selected; got != vaapiAutoOption {
		t.Errorf("vaapiDevice selection = %q, want the replacement %q", got, vaapiAutoOption)
	}
	if c.vaapiDeviceSeed != "" {
		t.Errorf("vaapiDeviceSeed = %q, want updated to the empty automatic value", c.vaapiDeviceSeed)
	}
	if got := c.qsvDevice.Selected; got != "(default)" {
		t.Errorf("qsvDevice selection = %q, want (default)", got)
	}
	if got := c.profileFromWidgets().Video.VAAPIDevice; got != "" {
		t.Errorf("profile VAAPI device = %q, want the empty automatic value", got)
	}

	// A second reconcile must stay quiet about the (now valid) seed:
	// the replacement selection is kept and no further change occurs.
	c.reconcileDevices(false)
	if got := c.vaapiDevice.Selected; got != vaapiAutoOption {
		t.Errorf("vaapiDevice selection after re-reconcile = %q, want unchanged", got)
	}
}

// TestReconcileDevicesKeepsManualChoice pins the detect-time contract:
// reconciliation must not force the loaded profile's seed back over a
// selection the user made manually.
func TestReconcileDevicesKeepsManualChoice(t *testing.T) {
	app := fynetest.NewApp()
	defer app.Quit()

	c := deviceCapableController()
	c.caps = model.Capabilities{RenderNodes: []string{"/dev/dri/renderD128", "/dev/dri/renderD129"}}
	c.updateDeviceOptions()

	c.videoEncoder.SetSelected("hevc_vaapi")
	c.reconcileDevices(true) // applies seed / first node
	first := c.vaapiDevice.Selected
	c.vaapiDevice.SetSelected("renderD129")

	// Detect-time: same nodes, seed still points at the load-time
	// value — the manual pick must survive.
	c.reconcileDevices(false)
	if got := c.vaapiDevice.Selected; got != "renderD129" {
		t.Errorf("vaapiDevice selection after detect-time reconcile = %q, want manual renderD129 (load selection was %q)", got, first)
	}
}

// TestReconcileDevicesAppliesPendingSeed covers the startup order:
// loadProfile runs before detection (no nodes yet), so the seed is
// pending; when detection delivers the nodes the seed — not the first
// node — must be selected.
func TestReconcileDevicesAppliesPendingSeed(t *testing.T) {
	app := fynetest.NewApp()
	defer app.Quit()

	c := testController()
	c.createWidgets()
	profile := c.config.Profiles[0]
	profile.Video.Encoder = "hevc_vaapi"
	profile.Video.VAAPIDevice = "/dev/dri/renderD129"
	c.loadProfile(profile) // no caps yet: options stay empty

	if got := c.vaapiDevice.Selected; got != "" {
		t.Fatalf("pre-detect selection = %q, want empty", got)
	}

	c.caps = model.Capabilities{RenderNodes: []string{"/dev/dri/renderD128", "/dev/dri/renderD129"}}
	c.updateDeviceOptions()
	c.reconcileDevices(false) // the detect() sequence
	if got := c.vaapiDevice.Selected; got != "renderD129" {
		t.Errorf("pending seed selection = %q, want the renderD129 option", got)
	}
}

// TestDeviceSurvivesSaveWithoutDetectedNodes is the data-loss
// regression: with no detected nodes (detection failure, non-Linux,
// pre-detect window), the dropdown selection is empty but a Save must
// keep the loaded profile's device paths instead of writing empty
// strings to config.yaml.
func TestDeviceSurvivesSaveWithoutDetectedNodes(t *testing.T) {
	app := fynetest.NewApp()
	defer app.Quit()

	c := testController()
	c.createWidgets()
	profile := c.config.Profiles[0]
	profile.Video.Encoder = "hevc_vaapi"
	profile.Video.VAAPIDevice = "/dev/dri/renderD128"
	profile.Video.QSVDevice = ""
	c.loadProfile(profile) // c.caps is empty: no nodes detected

	got := c.profileFromWidgets()
	if got.Video.VAAPIDevice != "/dev/dri/renderD128" {
		t.Errorf("VAAPI device after Save-with-empty-caps = %q, want the saved path", got.Video.VAAPIDevice)
	}
	if got.Video.QSVDevice != "" {
		t.Errorf("QSV device after Save-with-empty-caps = %q, want \"\"", got.Video.QSVDevice)
	}
}

// TestQSVDefaultPickOverridesSeed is the UI/model divergence
// regression: after a load applied the saved device, an explicit
// "(default)" pick must map to the empty path. The seed only
// backfills an empty dropdown selection — the model, the command
// preview, and Save must follow what the dropdown shows.
func TestQSVDefaultPickOverridesSeed(t *testing.T) {
	app := fynetest.NewApp()
	defer app.Quit()

	c := deviceCapableController()
	c.caps = model.Capabilities{RenderNodes: []string{"/dev/dri/renderD128", "/dev/dri/renderD129"}}
	c.updateDeviceOptions()
	profile := c.config.Profiles[0]
	profile.Video.Encoder = "hevc_qsv"
	profile.Video.QSVDevice = "/dev/dri/renderD129"
	c.loadProfile(profile)

	if got := c.qsvDevice.Selected; got != "/dev/dri/renderD129" {
		t.Fatalf("loaded QSV device selection = %q, want the saved node", got)
	}
	c.qsvDevice.SetSelected(defaultOption)
	if got := c.profileFromWidgets().Video.QSVDevice; got != "" {
		t.Errorf("QSV device with explicit (default) pick = %q, want \"\"", got)
	}
}

// TestLoadProfileAppliesSavedDeviceOverStaleSelection is the
// profile-switch regression: reconcileDevices at load time must apply
// the freshly loaded profile's device even when a previously loaded
// profile left a valid selection behind — otherwise the stale node is
// previewed, encoded, and Saved into the newly loaded profile.
func TestLoadProfileAppliesSavedDeviceOverStaleSelection(t *testing.T) {
	app := fynetest.NewApp()
	defer app.Quit()

	c := deviceCapableController()
	c.caps = model.Capabilities{RenderNodes: []string{"/dev/dri/renderD128", "/dev/dri/renderD129"}}
	c.updateDeviceOptions()

	first := c.config.Profiles[0]
	first.Video.Encoder = "hevc_vaapi"
	first.Video.VAAPIDevice = "/dev/dri/renderD128"
	first.Video.QSVDevice = "/dev/dri/renderD128"
	c.loadProfile(first)

	second := first
	second.Video.VAAPIDevice = "/dev/dri/renderD129"
	c.loadProfile(second)

	if got := c.vaapiDevice.Selected; got != "renderD129" {
		t.Errorf("VAAPI selection after switching profiles = %q, want the loaded profile's renderD129 option", got)
	}
	if got := c.profileFromWidgets().Video.VAAPIDevice; got != "/dev/dri/renderD129" {
		t.Errorf("profile VAAPI device after switching profiles = %q, want the loaded profile's path", got)
	}

	// QSV variant: a profile saved with the default device must not
	// inherit the previous profile's concrete node.
	third := second
	third.Video.Encoder = "hevc_qsv"
	third.Video.QSVDevice = ""
	c.loadProfile(third)
	if got := c.qsvDevice.Selected; got != defaultOption {
		t.Errorf("QSV selection after switching to a default-device profile = %q, want (default)", got)
	}
	if got := c.profileFromWidgets().Video.QSVDevice; got != "" {
		t.Errorf("profile QSV device after switching profiles = %q, want \"\"", got)
	}
}

// TestLoadProfileEmptyProfileResetsSelection is the stale-profile-
// value regression: populateProfileOptions keeps the current selection
// when the preferred value is empty (the encoder-switch behavior), so
// loading a profile saved without -profile:v after one with a profile
// kept the previous value visible, emitted it for the new profile, and
// Saved it into it. An empty loaded value must land on "(default)".
func TestLoadProfileEmptyProfileResetsSelection(t *testing.T) {
	app := fynetest.NewApp()
	defer app.Quit()

	c := testController()
	c.createWidgets()

	first := c.config.Profiles[0]
	first.Video.Encoder = "libx265"
	first.Video.RateControl = "CRF"
	first.Video.Profile = "main10"
	c.loadProfile(first)
	if got := c.videoProfile.Selected; got != "main10" {
		t.Fatalf("first load profile selection = %q, want main10", got)
	}

	second := first
	second.Video.Profile = ""
	c.loadProfile(second)

	if got := c.videoProfile.Selected; got != defaultOption {
		t.Errorf("profile selection after loading an empty-profile value = %q, want %q", got, defaultOption)
	}
	if got := c.profileFromWidgets().Video.Profile; got != "" {
		t.Errorf("profile value after loading an empty-profile value = %q, want \"\"", got)
	}
}

// TestLoadProfileBitrateNotInherited is the stale-bitrate regression:
// a copy-mode profile (bitrate row hidden) must keep its own saved
// bitrate on Save instead of inheriting the previously loaded encode
// profile's dropdown selection, and an encode profile without a
// bitrate must fall back instead of keeping the previous selection.
func TestLoadProfileBitrateNotInherited(t *testing.T) {
	app := fynetest.NewApp()
	defer app.Quit()

	c := testController()
	c.createWidgets()

	encode := c.config.Profiles[0]
	encode.Audio.Mode = model.AudioEncode
	encode.Audio.Encoder = "libmp3lame"
	encode.Audio.Bitrate = "320k"
	c.loadProfile(encode)
	if got := c.audioBitrate.Selected; got != "320k" {
		t.Fatalf("encode load bitrate selection = %q, want 320k", got)
	}

	copied := encode
	copied.Audio.Mode = model.AudioCopy
	copied.Audio.Encoder = "copy"
	copied.Audio.Bitrate = "160k"
	c.loadProfile(copied)
	if got := c.profileFromWidgets().Audio.Bitrate; got != "160k" {
		t.Errorf("copy-profile bitrate after switching = %q, want the copy profile's 160k", got)
	}

	empty := copied
	empty.Audio.Mode = model.AudioEncode
	empty.Audio.Encoder = "libmp3lame"
	empty.Audio.Bitrate = ""
	c.loadProfile(empty)
	// The cleared selection must fall back to a documented choice of
	// the encoder (160k when offered, else the median) — never to the
	// previously loaded profile's value.
	if got := c.audioBitrate.Selected; got == "320k" {
		t.Errorf("empty-bitrate load kept the previous profile's 320k instead of falling back")
	}
	if got := c.profileFromWidgets().Audio.Bitrate; got == "320k" {
		t.Errorf("empty-bitrate load kept the previous profile's 320k (got %q)", got)
	}
}

// audioFormLabels reads the labels of the currently built audio form.
func audioFormLabels(c *Controller) []string {
	labels := []string{}
	if len(c.audioFormBox.Objects) == 0 {
		return labels
	}
	form, ok := c.audioFormBox.Objects[0].(*widget.Form)
	if !ok {
		return labels
	}
	for _, item := range form.Items {
		labels = append(labels, item.Text)
	}
	return labels
}

// TestAudioFormRowMatrix pins the hide-not-grey row sets: copy mode
// shows only Mode/all-audio/Subtitles; encode mode adds Encoder plus
// Bitrate (bitrate encoders) or Compression (flac), labelled
// "Compression" rather than "FLAC compression".
func TestAudioFormRowMatrix(t *testing.T) {
	app := fynetest.NewApp()
	defer app.Quit()

	c := testController()
	c.createWidgets()

	// Unselected mode starts in the copy layout.
	want := []string{"Mode", "", "Subtitles"}
	if got := audioFormLabels(c); !slices.Equal(got, want) {
		t.Fatalf("initial audio rows = %v, want %v", got, want)
	}

	c.audioMode.SetSelected(model.AudioEncode)
	c.audioEncoder.SetSelected("libopus")
	want = []string{"Mode", "Encoder", "Bitrate", "", "Subtitles"}
	if got := audioFormLabels(c); !slices.Equal(got, want) {
		t.Fatalf("libopus audio rows = %v, want %v", got, want)
	}
	if c.audioBitrate.Disabled() || c.audioEncoder.Disabled() {
		t.Errorf("visible audio rows must be enabled (bitrate=%v encoder=%v)", c.audioBitrate.Disabled(), c.audioEncoder.Disabled())
	}

	c.audioEncoder.SetSelected("flac")
	want = []string{"Mode", "Encoder", "Compression", "", "Subtitles"}
	if got := audioFormLabels(c); !slices.Equal(got, want) {
		t.Fatalf("flac audio rows = %v, want %v", got, want)
	}
	if c.compression.slider.Disabled() {
		t.Errorf("visible compression row must be enabled")
	}

	c.audioMode.SetSelected(model.AudioCopy)
	want = []string{"Mode", "", "Subtitles"}
	if got := audioFormLabels(c); !slices.Equal(got, want) {
		t.Fatalf("copy audio rows = %v, want %v", got, want)
	}
}

// TestAudioFormNoRebuildOnIdenticalState pins the signature guard:
// driving updateControlState without a state change must not rebuild
// the form (the Form instance stays the same pointer).
func TestAudioFormNoRebuildOnIdenticalState(t *testing.T) {
	app := fynetest.NewApp()
	defer app.Quit()

	c := testController()
	c.createWidgets()
	c.audioMode.SetSelected(model.AudioEncode)
	c.audioEncoder.SetSelected("libopus")

	before := c.audioFormBox.Objects[0]
	c.updateControlState()
	c.updateControlState()
	if c.audioFormBox.Objects[0] != before {
		t.Errorf("identical state rebuilt the audio form")
	}

	// A real state change must rebuild.
	c.audioEncoder.SetSelected("flac")
	if c.audioFormBox.Objects[0] == before {
		t.Errorf("mode/encoder change did not rebuild the audio form")
	}
}

// TestVideoFormNoRebuildOnIdenticalState pins the signature guard on
// the video tab: every videoFormItems widget must be a stable pointer
// (the aspect-flags HBox included — a fresh container there changed
// the signature on every call and rebuilt the whole form on each
// keystroke or slider tick).
func TestVideoFormNoRebuildOnIdenticalState(t *testing.T) {
	app := fynetest.NewApp()
	defer app.Quit()

	c := testController()
	c.createWidgets()
	c.videoEncoder.SetSelected("libx264")

	before := c.videoFormBox.Objects[0]
	c.updateControlState()
	c.updateControlState()
	if c.videoFormBox.Objects[0] != before {
		t.Errorf("identical state rebuilt the video form")
	}

	// Copy mode collapses the row set and must rebuild.
	c.videoEncoder.SetSelected("copy")
	if c.videoFormBox.Objects[0] == before {
		t.Errorf("encoder change to copy did not rebuild the video form")
	}

	// Back to a real encoder: rebuild, then a same-row-set state tick
	// (frame rate text is not a row boundary in keep mode) must not.
	c.videoEncoder.SetSelected("libx264")
	afterSwitch := c.videoFormBox.Objects[0]
	if afterSwitch == before {
		t.Fatalf("encoder change back to libx264 did not rebuild the video form")
	}
	c.updateControlState()
	if c.videoFormBox.Objects[0] != afterSwitch {
		t.Errorf("identical state rebuilt the video form after encoder switch")
	}
}

// TestAudioEncoderAutoSelectOnEncodeMode pins that switching to encode
// mode with no usable selection picks the first available encoder, and
// that "copy" never appears among the options.
func TestAudioEncoderAutoSelectOnEncodeMode(t *testing.T) {
	app := fynetest.NewApp()
	defer app.Quit()

	c := testController()
	c.createWidgets()
	if slices.Contains(c.audioEncoder.Options, "copy") {
		t.Fatalf("audio encoder options %v must not contain copy", c.audioEncoder.Options)
	}

	c.audioMode.SetSelected(model.AudioEncode)
	if got := c.audioEncoder.Selected; got == "" || got == "copy" {
		t.Errorf("audio encoder after encode mode = %q, want auto-selected real encoder", got)
	}
}

// TestAudioBitrateOptionsFollowEncoder pins the per-encoder bitrate
// dropdown: libopus offers its choices, flac none, and a hand-edited
// out-of-list value is kept visible.
func TestAudioBitrateOptionsFollowEncoder(t *testing.T) {
	app := fynetest.NewApp()
	defer app.Quit()

	c := testController()
	c.createWidgets()
	c.audioMode.SetSelected(model.AudioEncode)
	c.audioEncoder.SetSelected("libopus")
	if !slices.Equal(c.audioBitrate.Options, []string{"96k", "128k", "160k", "192k", "256k"}) {
		t.Errorf("libopus bitrate options = %v", c.audioBitrate.Options)
	}

	c.audioEncoder.SetSelected("libmp3lame")
	if !slices.Equal(c.audioBitrate.Options, []string{"128k", "192k", "256k", "320k", "160k"}) {
		t.Errorf("libmp3lame bitrate options = %v (previous selection unioned)", c.audioBitrate.Options)
	}

	// A hand-edited/loaded out-of-list value is seeded via the
	// preferred path (the loadProfile route) and stays visible.
	c.populateAudioBitrateOptions("libmp3lame", "96k")
	if got := c.audioBitrate.Selected; got != "96k" {
		t.Errorf("out-of-list bitrate selection = %q, want 96k kept", got)
	}
	if !slices.Contains(c.audioBitrate.Options, "96k") {
		t.Errorf("bitrate options %v do not contain the out-of-list value", c.audioBitrate.Options)
	}
}

// TestCopyModeAudioRoundTrip pins that a copy-mode profile keeps
// encoder:"copy" and its bitrate through loadProfile/profileFromWidgets
// even though the encoder and bitrate rows are hidden.
func TestCopyModeAudioRoundTrip(t *testing.T) {
	app := fynetest.NewApp()
	defer app.Quit()

	c := testController()
	c.createWidgets()
	profile := c.config.Profiles[0]
	profile.Audio = model.AudioSettings{Mode: model.AudioCopy, Encoder: "copy", Bitrate: "160k", Compression: 8}
	c.loadProfile(profile)

	got := c.profileFromWidgets()
	if got.Audio.Encoder != "copy" {
		t.Errorf("copy-mode encoder = %q, want copy", got.Audio.Encoder)
	}
	if got.Audio.Bitrate != "160k" {
		t.Errorf("copy-mode bitrate = %q, want 160k (seed round-trip)", got.Audio.Bitrate)
	}
	if got.Audio.Mode != model.AudioCopy {
		t.Errorf("copy-mode round-trip mode = %q", got.Audio.Mode)
	}
}

// TestEncodeModeSurvivesDetection is the A3 regression: switching to
// encode mode while detection is still running used to let the
// detection callback silently load a copy-mode built-in (encode +
// encoder "copy" failed validation), snapping Mode back to copy and
// greying out Encoder/Bitrate. With "copy" gone from the encoder
// options and auto-selection in place, that state can no longer exist.
func TestEncodeModeSurvivesDetection(t *testing.T) {
	app := fynetest.NewApp()
	defer app.Quit()

	c := testController()
	c.createWidgets()
	c.audioMode.SetSelected(model.AudioEncode) // user acts while detection runs

	// The detect() body: options from caps, control state, runnable
	// profile selection.
	c.caps = model.Capabilities{Encoders: map[string]model.Encoder{
		"libopus":    {Name: "libopus", Media: ffmpeg.MediaAudio},
		"copy":       {Name: "copy", Media: "video,audio"},
		"h264_vaapi": {Name: "h264_vaapi", Media: ffmpeg.MediaVideo},
	}}
	c.updateEncoderOptions()
	c.updateControlState()
	c.selectRunnableProfile()

	if got := c.audioMode.Selected; got != model.AudioEncode {
		t.Errorf("audio mode after detection = %q, want encode (silent revert)", got)
	}
	if c.audioEncoder.Disabled() || c.audioBitrate.Disabled() {
		t.Errorf("encoder=%v bitrate=%v disabled after detection, want enabled", c.audioEncoder.Disabled(), c.audioBitrate.Disabled())
	}
}

// TestEncodeModeWithoutAudioEncodersReverts pins the zero-encoder
// corner: a build with no audio encoders reverts the mode to copy
// visibly instead of leaving an unsatisfiable encode selection.
func TestEncodeModeWithoutAudioEncodersReverts(t *testing.T) {
	app := fynetest.NewApp()
	defer app.Quit()

	c := testController()
	c.createWidgets()
	c.audioMode.SetSelected(model.AudioEncode)

	c.caps = model.Capabilities{Encoders: map[string]model.Encoder{
		"copy": {Name: "copy", Media: "video,audio"},
	}}
	c.updateEncoderOptions() // audio options become empty
	c.updateControlState()

	if got := c.audioMode.Selected; got != model.AudioCopy {
		t.Errorf("audio mode with no audio encoders = %q, want visible revert to copy", got)
	}
}

// TestUpdateEncoderOptionsFollowsRegistryPriority pins the
// post-detection dropdown ordering: registry priority (priority
// descending, name ascending) rather than alphabetical, "copy" first
// for video, and detected names the registry does not know appended
// alphabetically at the end.
func TestUpdateEncoderOptionsFollowsRegistryPriority(t *testing.T) {
	app := fynetest.NewApp()
	defer app.Quit()

	c := testController()
	c.createWidgets()

	// The fixture is a map, so iteration order is random: the
	// resulting option lists must come out registry-ordered anyway.
	c.caps = model.Capabilities{Encoders: map[string]model.Encoder{
		"copy":       {Name: "copy", Media: "video,audio"},
		"mp3":        {Name: "mp3", Media: ffmpeg.MediaAudio},
		"libmp3lame": {Name: "libmp3lame", Media: ffmpeg.MediaAudio},
		"flac":       {Name: "flac", Media: ffmpeg.MediaAudio},
		"opus":       {Name: "opus", Media: ffmpeg.MediaAudio},
		"aac":        {Name: "aac", Media: ffmpeg.MediaAudio},
		"libopus":    {Name: "libopus", Media: ffmpeg.MediaAudio},
		"libx264":    {Name: "libx264", Media: ffmpeg.MediaVideo},
		"libx265":    {Name: "libx265", Media: ffmpeg.MediaVideo},
		"h264_vaapi": {Name: "h264_vaapi", Media: ffmpeg.MediaVideo},
		"hevc_amf":   {Name: "hevc_amf", Media: ffmpeg.MediaVideo}, // not in the registry
	}}
	c.updateEncoderOptions()

	// Priorities: aac/libopus 100 (name asc), opus 90,
	// flac/libmp3lame 70 (name asc), mp3 60.
	wantAudio := []string{"aac", "libopus", "opus", "flac", "libmp3lame", "mp3"}
	if !slices.Equal(c.audioEncoder.Options, wantAudio) {
		t.Errorf("audio options = %v, want %v", c.audioEncoder.Options, wantAudio)
	}

	// Priorities: libx264 100, libx265 90, h264_vaapi 60 — not
	// alphabetical (h264_vaapi would sort first); the unknown
	// hevc_amf lands last.
	wantVideo := []string{"copy", "libx264", "libx265", "h264_vaapi", "hevc_amf"}
	if !slices.Equal(c.videoEncoder.Options, wantVideo) {
		t.Errorf("video options = %v, want %v", c.videoEncoder.Options, wantVideo)
	}

	// A narrow detected build keeps registry order among its names.
	c.caps = model.Capabilities{Encoders: map[string]model.Encoder{
		"copy":    {Name: "copy", Media: "video,audio"},
		"libx265": {Name: "libx265", Media: ffmpeg.MediaVideo},
	}}
	c.updateEncoderOptions()
	if want := []string{"copy", "libx265"}; !slices.Equal(c.videoEncoder.Options, want) {
		t.Errorf("narrow video options = %v, want %v", c.videoEncoder.Options, want)
	}
	if len(c.audioEncoder.Options) != 0 {
		t.Errorf("narrow audio options = %v, want empty", c.audioEncoder.Options)
	}
}

// TestVideoFormRateControlMatrix pins the hide-not-grey rate-control
// rows per family and mode: only the controls the encoder's
// BuildVideoArgs consumes for the selected mode are present, and
// visible rows are enabled.
func TestVideoFormRateControlMatrix(t *testing.T) {
	app := fynetest.NewApp()
	defer app.Quit()

	c := testController()
	c.createWidgets()

	type tc struct {
		name     string
		encoder  string
		mode     string
		wantRows []string // rate-control-related rows that must be present
		notRows  []string // rate-control-related rows that must be absent
	}
	cases := []tc{
		{"vaapi auto shows optional bitrate", "hevc_vaapi", "Auto",
			[]string{"Rate control", "Target bitrate"},
			[]string{"QP", "CRF", "Maximum bitrate", "Buffer size"}},
		{"vaapi cqp shows qp only", "hevc_vaapi", "CQP",
			[]string{"Rate control", "QP"},
			[]string{"CRF", "Target bitrate", "Maximum bitrate", "Buffer size"}},
		{"vaapi vbr shows capped trio", "hevc_vaapi", "VBR",
			[]string{"Rate control", "Target bitrate", "Maximum bitrate", "Buffer size"},
			[]string{"QP", "CRF"}},
		{"vaapi cbr shows capped trio", "hevc_vaapi", "CBR",
			[]string{"Rate control", "Target bitrate", "Maximum bitrate", "Buffer size"},
			[]string{"QP", "CRF"}},
		{"nvenc cqp shows qp", "h264_nvenc", "CQP",
			[]string{"Rate control", "QP"},
			[]string{"CRF", "Target bitrate", "Maximum bitrate"}},
		{"software crf shows crf", "libx264", "CRF",
			[]string{"Rate control", "CRF"},
			[]string{"QP", "Target bitrate", "Maximum bitrate", "Buffer size"}},
		{"software bitrate shows bitrate", "libsvtav1", "bitrate",
			[]string{"Rate control", "Target bitrate"},
			[]string{"QP", "CRF", "Maximum bitrate", "Buffer size"}},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			c.videoEncoder.SetSelected(tt.encoder)
			c.rateControl.SetSelected(tt.mode)
			labels := videoFormLabels(c)
			for _, row := range tt.wantRows {
				if !slices.Contains(labels, row) {
					t.Errorf("rows %v miss %q", labels, row)
				}
			}
			for _, row := range tt.notRows {
				if slices.Contains(labels, row) {
					t.Errorf("rows %v must not contain %q", labels, row)
				}
			}
		})
	}
}

// TestVideoFormTuneProfilePresetVisibility pins the capability-driven
// rows: Tune only on libx264/libx265, Profile only on supporting
// encoders (hidden, not disabled, for av1_nvenc), Preset per encoder.
func TestVideoFormTuneProfilePresetVisibility(t *testing.T) {
	app := fynetest.NewApp()
	defer app.Quit()

	c := testController()
	c.createWidgets()

	c.videoEncoder.SetSelected("libx264")
	labels := videoFormLabels(c)
	for _, row := range []string{"Tune", "Profile", "Preset"} {
		if !slices.Contains(labels, row) {
			t.Errorf("libx264 rows %v miss %q", labels, row)
		}
	}

	c.videoEncoder.SetSelected("hevc_vaapi")
	labels = videoFormLabels(c)
	if slices.Contains(labels, "Tune") {
		t.Errorf("hevc_vaapi rows %v must not show Tune", labels)
	}
	if !slices.Contains(labels, "Profile") {
		t.Errorf("hevc_vaapi rows %v miss Profile", labels)
	}
	if slices.Contains(labels, "Preset") {
		t.Errorf("hevc_vaapi rows %v must not show Preset", labels)
	}

	// av1_nvenc: the Profile row is hidden entirely and the invisible
	// value is dropped from the saved profile.
	c.videoEncoder.SetSelected("h264_nvenc")
	c.videoProfile.SetSelected("main")
	c.videoEncoder.SetSelected("av1_nvenc")
	labels = videoFormLabels(c)
	if slices.Contains(labels, "Profile") {
		t.Errorf("av1_nvenc rows %v must not show Profile", labels)
	}
	if got := c.profileFromWidgets().Video.Profile; got != "" {
		t.Errorf("hidden profile row emitted %q, want \"\"", got)
	}

	// Frame rate: the input row only appears in convert mode.
	c.videoEncoder.SetSelected("hevc_vaapi")
	c.frameMode.SetSelected("convert")
	if !slices.Contains(videoFormLabels(c), "Frame rate") {
		t.Errorf("convert mode must show the Frame rate row")
	}
	c.frameMode.SetSelected("keep")
	if slices.Contains(videoFormLabels(c), "Frame rate") {
		t.Errorf("keep mode must hide the Frame rate row")
	}
}

// TestRateControlHintExplainsMode pins the per-mode hint text that
// makes Auto and VBR legible.
func TestRateControlHintExplainsMode(t *testing.T) {
	app := fynetest.NewApp()
	defer app.Quit()

	c := testController()
	c.createWidgets()
	c.videoEncoder.SetSelected("hevc_vaapi")

	c.rateControl.SetSelected("Auto")
	if c.rateControlHint.Text == "" {
		t.Errorf("Auto mode must show an explanatory hint")
	}
	autoHint := c.rateControlHint.Text

	c.rateControl.SetSelected("VBR")
	if c.rateControlHint.Text == "" || c.rateControlHint.Text == autoHint {
		t.Errorf("VBR mode hint = %q, want distinct non-empty text", c.rateControlHint.Text)
	}
}

// TestOutputTemplateHelpText pins that the generated help lists every
// templates.Placeholders key in sorted order with the example block,
// and uses spaces rather than tabs for alignment.
func TestOutputTemplateHelpText(t *testing.T) {
	help := outputTemplateHelpText()

	keys := make([]string, 0, len(templates.Placeholders))
	for key := range templates.Placeholders {
		keys = append(keys, key)
		if !strings.Contains(help, "{"+key+"}") {
			t.Errorf("help text misses placeholder {%s}:\n%s", key, help)
		}
	}
	sorted := slices.Clone(keys)
	slices.Sort(sorted)

	// The keys appear in sorted order in the text.
	position := -1
	for _, key := range sorted {
		at := strings.Index(help, "{"+key+"}")
		if at < position {
			t.Errorf("placeholder {%s} out of order in help text", key)
		}
		position = at
	}

	for _, want := range []string{
		"Template:",
		"{dir}/{name}_{profile}.{ext}",
		`Result (profile "HEVC VAAPI 720p CQ"):`,
		"/home/user/Videos/movie_HEVC_VAAPI_720p_CQ.mkv",
	} {
		if !strings.Contains(help, want) {
			t.Errorf("help text misses %q:\n%s", want, help)
		}
	}
	if strings.Contains(help, "\t") {
		t.Errorf("help text must align with spaces, not tabs")
	}
	if strings.Contains(help, "movie_HEVC_720p") {
		t.Errorf("help text still shows the fictional profile example:\n%s", help)
	}
}

// TestTemplateHelpExampleResolves pins the help dialog's worked
// example to actual Resolve behavior: the exact template and input the
// example shows must produce the exact result it claims.
func TestTemplateHelpExampleResolves(t *testing.T) {
	got, err := templates.Resolve("{dir}/{name}_{profile}.{ext}", templates.Request{
		InputPath: "/home/user/Videos/movie.mkv",
		Extension: "mkv",
		Profile:   "HEVC VAAPI 720p CQ",
		Time:      time.Date(2026, 3, 12, 14, 30, 5, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("Resolve of the help example failed: %v", err)
	}
	if want := "/home/user/Videos/movie_HEVC_VAAPI_720p_CQ.mkv"; got != want {
		t.Errorf("help example resolves to %q, want %q", got, want)
	}
}

// vaapiDualGPUFixture mirrors the owner's dual-GPU host from issue #23:
// renderD128 is an NVIDIA GPU whose VA-API driver decodes only, while
// renderD129 is an AMD iGPU that encodes H.264 and HEVC.
func vaapiDualGPUFixture() model.Capabilities {
	return model.Capabilities{
		RenderNodes: []string{"/dev/dri/renderD128", "/dev/dri/renderD129"},
		VAAPIDevices: []vaapi.DeviceInfo{
			{
				Path:   "/dev/dri/renderD128",
				Driver: "VA-API NVDEC driver [direct backend]",
				Identity: vaapi.DeviceIdentity{
					Vendor:     "10de",
					Device:     "2191",
					Driver:     "nvidia",
					Name:       "TU116M [GeForce GTX 1660 Ti Mobile]",
					VendorName: "NVIDIA Corporation",
				},
				EntryPoints: map[vaapi.Profile][]vaapi.Entrypoint{
					vaapi.ProfileH264Main: {vaapi.EntrypointVLD},
					vaapi.ProfileHEVCMain: {vaapi.EntrypointVLD},
				},
			},
			{
				Path:   "/dev/dri/renderD129",
				Driver: "Mesa Gallium driver (radeonsi)",
				Identity: vaapi.DeviceIdentity{
					Vendor:     "1002",
					Device:     "1636",
					Driver:     "amdgpu",
					Name:       "Renoir [Radeon Vega Series / Radeon Vega Mobile Series]",
					VendorName: "Advanced Micro Devices, Inc. [AMD/ATI]",
				},
				EntryPoints: map[vaapi.Profile][]vaapi.Entrypoint{
					vaapi.ProfileH264Main: {vaapi.EntrypointVLD, vaapi.EntrypointEncSlice},
					vaapi.ProfileHEVCMain: {vaapi.EntrypointVLD, vaapi.EntrypointEncSlice},
				},
			},
		},
	}
}

// TestVAAPIAutoPickOverridesSeed mirrors TestQSVDefaultPickOverridesSeed
// for the VAAPI side: after a load applied the saved device, an
// explicit "(Auto)" pick must map to the empty value. The seed only
// backfills an empty dropdown selection.
func TestVAAPIAutoPickOverridesSeed(t *testing.T) {
	app := fynetest.NewApp()
	defer app.Quit()

	c := testController()
	c.createWidgets()
	c.caps = vaapiDualGPUFixture()
	c.updateDeviceOptions()
	profile := c.config.Profiles[0]
	profile.Video.Encoder = "hevc_vaapi"
	profile.Video.VAAPIDevice = "/dev/dri/renderD129"
	c.loadProfile(profile)

	if got := c.vaapiDevice.Selected; got != vaapiDeviceOption(c.caps, "/dev/dri/renderD129") {
		t.Fatalf("loaded VAAPI device selection = %q, want the saved node's option", got)
	}
	c.vaapiDevice.SetSelected(vaapiAutoOption)
	if got := c.profileFromWidgets().Video.VAAPIDevice; got != "" {
		t.Errorf("VAAPI device with explicit (Auto) pick = %q, want \"\"", got)
	}
}

func TestVAAPIDeviceHint(t *testing.T) {
	tests := []struct {
		name     string
		caps     func() model.Capabilities
		encoder  string
		profile  string
		device   string // dropdown option to select
		wantHint string
	}{
		{
			name:     "auto resolves to the encode-capable device with its driver",
			caps:     vaapiDualGPUFixture,
			encoder:  "hevc_vaapi",
			device:   vaapiAutoOption,
			wantHint: "Auto: /dev/dri/renderD129 — AMD Renoir [Radeon Vega Series / Radeon Vega Mobile Series] (amdgpu; Mesa Gallium driver (radeonsi))",
		},
		{
			name: "auto without probe data falls back to the first node",
			caps: func() model.Capabilities {
				caps := vaapiDualGPUFixture()
				caps.VAAPIDevices = nil
				return caps
			},
			encoder:  "hevc_vaapi",
			device:   vaapiAutoOption,
			wantHint: "Auto: /dev/dri/renderD128 (capabilities unknown)",
		},
		{
			name: "auto with no compatible device says so",
			caps: func() model.Capabilities {
				fixture := vaapiDualGPUFixture()
				fixture.VAAPIDevices = fixture.VAAPIDevices[:1] // decode-only node only
				return fixture
			},
			encoder:  "hevc_vaapi",
			profile:  "main",
			device:   vaapiAutoOption,
			wantHint: `⚠️ Auto: no compatible device for hevc_vaapi profile "main"`,
		},
		{
			name: "auto without nodes reports the gap",
			caps: func() model.Capabilities {
				return model.Capabilities{}
			},
			encoder:  "hevc_vaapi",
			device:   vaapiAutoOption,
			wantHint: "Auto: no VAAPI render device detected",
		},
		{
			name:     "manual pick describes the device",
			caps:     vaapiDualGPUFixture,
			encoder:  "hevc_vaapi",
			device:   "Renoir (renderD129)",
			wantHint: "/dev/dri/renderD129 — AMD Renoir [Radeon Vega Series / Radeon Vega Mobile Series] (amdgpu; Mesa Gallium driver (radeonsi)) — encodes",
		},
		{
			name:     "manual decode-only pick says so",
			caps:     vaapiDualGPUFixture,
			encoder:  "hevc_vaapi",
			device:   "GeForce GTX 1660 Ti Mobile (renderD128)",
			wantHint: "⚠️ /dev/dri/renderD128 — NVIDIA TU116M [GeForce GTX 1660 Ti Mobile] (nvidia; VA-API NVDEC driver [direct backend]) — does not encode hevc_vaapi profile (automatic)",
		},
		{
			name: "manual pick without a probe record stays hidden",
			caps: func() model.Capabilities {
				caps := vaapiDualGPUFixture()
				caps.VAAPIDevices = nil
				return caps
			},
			encoder:  "hevc_vaapi",
			device:   "renderD129",
			wantHint: "",
		},
		{
			name:     "software encoder hides the hint",
			caps:     vaapiDualGPUFixture,
			encoder:  "libx265",
			device:   vaapiAutoOption,
			wantHint: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app := fynetest.NewApp()
			defer app.Quit()

			c := testController()
			c.createWidgets()
			c.caps = tt.caps()
			c.updateDeviceOptions()

			load := c.config.Profiles[0]
			load.Video.Encoder = tt.encoder
			load.Video.RateControl = "CQP"
			load.Video.QP = 28
			if tt.encoder == "libx265" {
				load.Video.RateControl = "CRF"
				load.Video.CRF = 28
			}
			c.loadProfile(load)
			c.videoProfile.SetSelected(tt.profile)
			c.vaapiDevice.SetSelected(tt.device)

			if got := c.vaapiDeviceHint.Text; got != tt.wantHint {
				t.Errorf("hint = %q, want %q", got, tt.wantHint)
			}
		})
	}
}

func TestCommandProfileMaterializesAuto(t *testing.T) {
	app := fynetest.NewApp()
	defer app.Quit()

	load := func(c *Controller, device string) {
		profile := c.config.Profiles[0]
		profile.Video.Encoder = "hevc_vaapi"
		profile.Video.RateControl = "CQP"
		profile.Video.QP = 28
		profile.Video.VAAPIDevice = device
		c.loadProfile(profile)
	}

	t.Run("auto resolves through the probed devices", func(t *testing.T) {
		c := testController()
		c.createWidgets()
		c.caps = vaapiDualGPUFixture()
		c.updateDeviceOptions()
		load(c, "")

		if got := c.profileFromWidgets().Video.VAAPIDevice; got != "" {
			t.Fatalf("persisted shape = %q, want the empty automatic value", got)
		}
		if got := c.commandProfile().Video.VAAPIDevice; got != "/dev/dri/renderD129" {
			t.Errorf("command shape = %q, want the encode-capable /dev/dri/renderD129", got)
		}
	})

	t.Run("auto without probe data falls back to the first node", func(t *testing.T) {
		c := testController()
		c.createWidgets()
		c.caps = model.Capabilities{RenderNodes: []string{"/dev/dri/renderD128"}}
		c.updateDeviceOptions()
		load(c, "")

		if got := c.commandProfile().Video.VAAPIDevice; got != "/dev/dri/renderD128" {
			t.Errorf("command shape = %q, want the first node", got)
		}
	})

	t.Run("auto without nodes stays empty", func(t *testing.T) {
		c := testController()
		c.createWidgets()
		load(c, "")

		if got := c.commandProfile().Video.VAAPIDevice; got != "" {
			t.Errorf("command shape = %q, want empty without nodes", got)
		}
	})

	t.Run("manual pick passes through untouched", func(t *testing.T) {
		c := testController()
		c.createWidgets()
		c.caps = vaapiDualGPUFixture()
		c.updateDeviceOptions()
		load(c, "/dev/dri/renderD128")

		if got := c.commandProfile().Video.VAAPIDevice; got != "/dev/dri/renderD128" {
			t.Errorf("command shape = %q, want the manual pick", got)
		}
	})
}

func TestVAAPIDevicesSummary(t *testing.T) {
	tests := []struct {
		name string
		caps model.Capabilities
		want string
	}{
		{name: "no devices", caps: model.Capabilities{}, want: ""},
		{
			name: "drivers are rendered per device",
			caps: vaapiDualGPUFixture(),
			want: "/dev/dri/renderD128 (GeForce GTX 1660 Ti Mobile; VA-API NVDEC driver [direct backend]), " +
				"/dev/dri/renderD129 (Renoir; Mesa Gallium driver (radeonsi))",
		},
		{
			name: "failed probes are marked unavailable",
			caps: model.Capabilities{VAAPIDevices: []vaapi.DeviceInfo{
				{Path: "/dev/dri/renderD130", Err: "open: permission denied"},
			}},
			want: "/dev/dri/renderD130 (unavailable: open: permission denied)",
		},
		{
			name: "failed probes keep their identity",
			caps: model.Capabilities{VAAPIDevices: []vaapi.DeviceInfo{
				{
					Path:     "/dev/dri/renderD131",
					Err:      "open: permission denied",
					Identity: vaapi.DeviceIdentity{Vendor: "1002", Device: "1636", Driver: "amdgpu", Name: "Renoir [Radeon Vega Series / Radeon Vega Mobile Series]"},
				},
			}},
			want: "/dev/dri/renderD131 (Renoir; unavailable: open: permission denied)",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := vaapiDevicesSummary(tt.caps); got != tt.want {
				t.Errorf("vaapiDevicesSummary() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestVAAPIDeviceHintRowPinsToVAAPIEncoders pins the caption row: the
// hint label lives in the form only while a VAAPI encoder is selected,
// and the label pointer stays stable so the form does not rebuild on
// every state tick.
func TestVAAPIDeviceHintRowPinsToVAAPIEncoders(t *testing.T) {
	app := fynetest.NewApp()
	defer app.Quit()

	c := deviceCapableController()
	c.videoEncoder.SetSelected("hevc_vaapi")
	if !formContainsWidget(c.videoFormItems(), c.vaapiDeviceHint) {
		t.Error("VAAPI form must carry the vaapiDeviceHint label row")
	}

	c.videoEncoder.SetSelected("libx265")
	if formContainsWidget(c.videoFormItems(), c.vaapiDeviceHint) {
		t.Error("software form must not carry the device hint row")
	}
}

func formContainsWidget(items []*widget.FormItem, w fyne.CanvasObject) bool {
	for _, item := range items {
		if item.Widget == w {
			return true
		}
	}
	return false
}

// TestVAAPIDeviceOptionsShowGPUNames pins the labeled forms: identified
// nodes render name-first with the basename in parentheses, tier-2
// identities fall back to driver and IDs, and unidentified nodes keep
// the bare basename.
func TestVAAPIDeviceOptionsShowGPUNames(t *testing.T) {
	caps := vaapiDualGPUFixture()
	want := []string{vaapiAutoOption, "GeForce GTX 1660 Ti Mobile (renderD128)", "Renoir (renderD129)"}
	options := make([]string, 0, len(caps.RenderNodes)+1)
	options = append(options, vaapiAutoOption)
	for _, node := range caps.RenderNodes {
		options = append(options, vaapiDeviceOption(caps, node))
	}
	if !slices.Equal(options, want) {
		t.Errorf("labeled options = %v, want %v", options, want)
	}

	tier2 := model.Capabilities{
		RenderNodes: []string{"/dev/dri/renderD128"},
		VAAPIDevices: []vaapi.DeviceInfo{
			{Path: "/dev/dri/renderD128", Identity: vaapi.DeviceIdentity{Vendor: "10de", Device: "2191", Driver: "nvidia"}},
		},
	}
	if got := vaapiDeviceOption(tier2, "/dev/dri/renderD128"); got != "nvidia 10de:2191 (renderD128)" {
		t.Errorf("tier-2 option = %q", got)
	}

	mixed := model.Capabilities{
		RenderNodes:  []string{"/dev/dri/renderD128", "/dev/dri/renderD129"},
		VAAPIDevices: vaapiDualGPUFixture().VAAPIDevices[1:],
	}
	if got := vaapiDeviceOption(mixed, "/dev/dri/renderD128"); got != "renderD128" {
		t.Errorf("unidentified option = %q", got)
	}
	if got := vaapiDeviceOption(mixed, "/dev/dri/renderD129"); got != "Renoir (renderD129)" {
		t.Errorf("identified option = %q", got)
	}
}

// TestVAAPIDeviceOptionValues pins the label-to-path inversion the
// profile round-trip depends on: current labels, stale labels, bare
// paths, and bare basenames all resolve to the node.
func TestVAAPIDeviceOptionValues(t *testing.T) {
	fixture := vaapiDualGPUFixture()
	tests := []struct {
		name   string
		caps   model.Capabilities
		option string
		want   string
	}{
		{"auto maps to empty", fixture, vaapiAutoOption, ""},
		{"current label maps to the node", fixture, "Renoir (renderD129)", "/dev/dri/renderD129"},
		{"bare basename maps to the node", fixture, "renderD128", "/dev/dri/renderD128"},
		{"bare path maps to the node", fixture, "/dev/dri/renderD129", "/dev/dri/renderD129"},
		{"stale label resolves via the basename", fixture, "Old Name (renderD129)", "/dev/dri/renderD129"},
		{"unknown option passes through", fixture, "somewhere-else", "somewhere-else"},
		{"unidentified node label maps back", model.Capabilities{RenderNodes: []string{"/dev/dri/renderD128"}}, "renderD128", "/dev/dri/renderD128"},
	}
	for _, tt := range tests {
		if got := vaapiDeviceValue(tt.caps, tt.option); got != tt.want {
			t.Errorf("%s: vaapiDeviceValue(%q) = %q, want %q", tt.name, tt.option, got, tt.want)
		}
	}
}

// TestDeviceSelectionRepinsToCurrentLabel pins the capabilities-
// transition contract: a selection made before detection follows its
// node to the labeled option while the profile value stays the bare
// path, and re-applying identical capabilities stays quiet.
func TestDeviceSelectionRepinsToCurrentLabel(t *testing.T) {
	app := fynetest.NewApp()
	defer app.Quit()
	c := testController()
	c.createWidgets()

	// Before detection the options are bare basenames; select one.
	c.caps = model.Capabilities{RenderNodes: []string{"/dev/dri/renderD129"}}
	c.updateDeviceOptions()
	c.vaapiDevice.SetSelected("renderD129")

	// Detection lands with identity: the same node's option becomes
	// the labeled form, and the selection must follow it while the
	// profile value stays the bare path.
	c.caps = vaapiDualGPUFixture()
	c.updateDeviceOptions()
	if got := c.vaapiDevice.Selected; got != "Renoir (renderD129)" {
		t.Errorf("selection after detection = %q, want the labeled option", got)
	}
	if got := c.profileFromWidgets().Video.VAAPIDevice; got != "/dev/dri/renderD129" {
		t.Errorf("profile value after detection = %q, want the bare path", got)
	}

	// Re-applying identical capabilities must stay quiet: the guarded
	// re-pin is idempotent.
	c.updateDeviceOptions()
	if got := c.vaapiDevice.Selected; got != "Renoir (renderD129)" {
		t.Errorf("selection after re-detection = %q, want it unchanged", got)
	}
}

func TestQSVDeviceHint(t *testing.T) {
	tests := []struct {
		name     string
		caps     func() model.Capabilities
		encoder  string
		device   string // QSV dropdown option to select
		wantHint string
	}{
		{
			name:     "AMD node warns",
			caps:     vaapiDualGPUFixture,
			encoder:  "hevc_qsv",
			device:   "/dev/dri/renderD129",
			wantHint: "⚠️ /dev/dri/renderD129 — AMD Renoir [Radeon Vega Series / Radeon Vega Mobile Series] (amdgpu; Mesa Gallium driver (radeonsi)) — not an Intel GPU (QSV requires Intel graphics)",
		},
		{
			name:     "NVIDIA node warns",
			caps:     vaapiDualGPUFixture,
			encoder:  "hevc_qsv",
			device:   "/dev/dri/renderD128",
			wantHint: "⚠️ /dev/dri/renderD128 — NVIDIA TU116M [GeForce GTX 1660 Ti Mobile] (nvidia; VA-API NVDEC driver [direct backend]) — not an Intel GPU (QSV requires Intel graphics)",
		},
		{
			name: "Intel VA driver stays clean",
			caps: func() model.Capabilities {
				caps := vaapiDualGPUFixture()
				caps.RenderNodes = append(caps.RenderNodes, "/dev/dri/renderD131")
				caps.VAAPIDevices = append(caps.VAAPIDevices, vaapi.DeviceInfo{Path: "/dev/dri/renderD131", Driver: "Intel iHD"})
				return caps
			},
			encoder:  "hevc_qsv",
			device:   "/dev/dri/renderD131",
			wantHint: "/dev/dri/renderD131 (Intel iHD)",
		},
		{
			name: "no evidence stays clean",
			caps: func() model.Capabilities {
				caps := vaapiDualGPUFixture()
				caps.RenderNodes = append(caps.RenderNodes, "/dev/dri/renderD132")
				caps.VAAPIDevices = append(caps.VAAPIDevices, vaapi.DeviceInfo{Path: "/dev/dri/renderD132"})
				return caps
			},
			encoder:  "hevc_qsv",
			device:   "/dev/dri/renderD132",
			wantHint: "/dev/dri/renderD132",
		},
		{
			name:     "default device stays undescribed",
			caps:     vaapiDualGPUFixture,
			encoder:  "hevc_qsv",
			device:   defaultOption,
			wantHint: "",
		},
		{
			name:     "software encoder hides the hint",
			caps:     vaapiDualGPUFixture,
			encoder:  "libx265",
			device:   defaultOption,
			wantHint: "",
		},
		{
			name: "node without a probe record stays hidden",
			caps: func() model.Capabilities {
				return model.Capabilities{RenderNodes: []string{"/dev/dri/renderD199"}}
			},
			encoder:  "hevc_qsv",
			device:   "/dev/dri/renderD199",
			wantHint: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app := fynetest.NewApp()
			defer app.Quit()
			c := testController()
			c.createWidgets()
			c.caps = tt.caps()
			c.updateDeviceOptions()
			load := c.config.Profiles[0]
			load.Video.Encoder = tt.encoder
			load.Video.RateControl = "CQP"
			load.Video.QP = 28
			if tt.encoder == "libx265" {
				load.Video.RateControl = "CRF"
				load.Video.CRF = 28
			}
			c.loadProfile(load)
			c.qsvDevice.SetSelected(tt.device)
			if got := c.qsvDeviceHint.Text; got != tt.wantHint {
				t.Errorf("hint = %q, want %q", got, tt.wantHint)
			}
		})
	}
}

func TestQSVDeviceHintRowPinsToQSVEncoders(t *testing.T) {
	app := fynetest.NewApp()
	defer app.Quit()
	c := deviceCapableController()
	formHasQSVHint := func() bool {
		if len(c.videoFormBox.Objects) == 0 {
			return false
		}
		form, ok := c.videoFormBox.Objects[0].(*widget.Form)
		if !ok {
			return false
		}
		for _, item := range form.Items {
			if item.Widget == c.qsvDeviceHint {
				return true
			}
		}
		return false
	}
	c.videoEncoder.SetSelected("hevc_qsv")
	if !formHasQSVHint() {
		t.Fatalf("hevc_qsv form has no QSV hint caption row")
	}
	c.videoEncoder.SetSelected("libx265")
	if formHasQSVHint() {
		t.Fatalf("libx265 form still carries the QSV hint caption row")
	}
}

// The settings dialog must not be shown at its bare minimum size: the wide
// "Check for updates automatically" label column would otherwise leave the
// FFmpeg and ffprobe path entries a clipped sliver at the panel edge.
func TestShowSettingsDialogKeepsUsableEntryWidth(t *testing.T) {
	application := fynetest.NewApp()
	defer application.Quit()

	c := &Controller{
		window: application.NewWindow("settings"),
		config: model.Config{},
	}
	// The pop-up cannot exceed its canvas; match the production window size
	// so the requested dialog width is not clamped away.
	c.window.Resize(fyne.NewSize(1050, 760))

	c.showSettings()

	top := c.window.Canvas().Overlays().Top()
	if top == nil {
		t.Fatal("expected the settings dialog overlay to be shown")
	}
	// dialog's base wraps the panel in an internal overlay container whose
	// exported Content field holds the dialog's modal pop-up.
	popup, ok := reflect.ValueOf(top).Elem().FieldByName("Content").Interface().(fyne.CanvasObject)
	if !ok {
		t.Fatalf("overlay top = %T, want a dialog container with Content", top)
	}
	if width := popup.Size().Width; width < 560 {
		t.Fatalf("settings dialog width = %.0f, want at least 560", width)
	}
}

// TestGPUIndexOptionsAndRoundTrip pins the GPU index control rules:
// dropdown when detection reported GPUs and the value fits, numeric
// entry otherwise; profile loading authoritative; detection-time
// refresh preserving edits from either widget.
func TestGPUIndexOptionsAndRoundTrip(t *testing.T) {
	app := fynetest.NewApp()
	defer app.Quit()

	c := testController()
	c.createWidgets()

	// No detection yet: entry mode, seed fallback round-trips.
	if !c.gpuIndexEntry.Visible() || c.gpuIndexSelect.Visible() {
		t.Fatalf("pre-detection control: entry visible=%v select visible=%v, want entry only",
			c.gpuIndexEntry.Visible(), c.gpuIndexSelect.Visible())
	}
	if got := c.profileFromWidgets().Video.GPUIndex; got != 0 {
		t.Errorf("pre-detection GPUIndex = %d, want 0 (empty entry, zero seed)", got)
	}

	// Detection with zero GPUs keeps entry mode.
	c.caps = model.Capabilities{}
	c.updateGPUIndexOptions()
	if !c.gpuIndexEntry.Visible() {
		t.Fatalf("count 0: entry must stay visible")
	}

	// A single detected GPU gets a one-entry dropdown (ticket #25).
	c.caps = model.Capabilities{NVENCGPUCount: 1}
	c.updateGPUIndexOptions()
	if !c.gpuIndexSelect.Visible() || c.gpuIndexEntry.Visible() {
		t.Fatalf("count 1: select visible=%v entry visible=%v, want select only",
			c.gpuIndexSelect.Visible(), c.gpuIndexEntry.Visible())
	}
	if want := []string{"0"}; !slices.Equal(c.gpuIndexSelect.Options, want) {
		t.Errorf("count 1 options = %v, want %v", c.gpuIndexSelect.Options, want)
	}
	if got := c.gpuIndexSelect.Selected; got != "0" {
		t.Errorf("count 1 selection = %q, want 0", got)
	}

	// Several GPUs: options 0..N-1.
	c.caps = model.Capabilities{NVENCGPUCount: 3}
	c.updateGPUIndexOptions()
	if want := []string{"0", "1", "2"}; !slices.Equal(c.gpuIndexSelect.Options, want) {
		t.Fatalf("count 3 options = %v, want %v", c.gpuIndexSelect.Options, want)
	}

	// A dropdown edit survives a detection refresh (count unchanged).
	c.gpuIndexSelect.SetSelected("1")
	c.updateGPUIndexOptions()
	if got := c.gpuIndexSelect.Selected; got != "1" {
		t.Errorf("dropdown edit after detection = %q, want 1", got)
	}

	// An entry edit typed before detection survives the switch to
	// dropdown mode.
	c.caps = model.Capabilities{}
	c.updateGPUIndexOptions() // back to entry mode
	c.gpuIndexEntry.SetText("2")
	c.caps = model.Capabilities{NVENCGPUCount: 3}
	c.updateGPUIndexOptions()
	if got := c.gpuIndexSelect.Selected; got != "2" {
		t.Errorf("entry edit carried into dropdown = %q, want 2", got)
	}

	// Detection is repeatable: another refresh keeps the value.
	c.updateGPUIndexOptions()
	if got := c.gpuIndexSelect.Selected; got != "2" {
		t.Errorf("second detection refresh = %q, want 2 (idempotent)", got)
	}

	// Profile loading is authoritative: gpu 2 then gpu 0 must land on 0.
	c.applyGPUIndexSeed(2)
	if got := c.gpuIndexSelect.Selected; got != "2" {
		t.Errorf("load gpu 2 selection = %q, want 2", got)
	}
	c.applyGPUIndexSeed(0)
	if got := c.gpuIndexSelect.Selected; got != "0" {
		t.Errorf("load gpu 0 selection = %q, want 0", got)
	}

	// Round-trip through the profile model.
	if got := c.profileFromWidgets().Video.GPUIndex; got != 0 {
		t.Errorf("profileFromWidgets after load 0 = %d, want 0", got)
	}
	c.gpuIndexSelect.SetSelected("1")
	if got := c.profileFromWidgets().Video.GPUIndex; got != 1 {
		t.Errorf("profileFromWidgets after picking 1 = %d, want 1", got)
	}

	// A loaded index outside the detected range stays in entry mode
	// verbatim — the count is approximate, so no silent clamping.
	c.applyGPUIndexSeed(5)
	if !c.gpuIndexEntry.Visible() || c.gpuIndexSelect.Visible() {
		t.Fatalf("out-of-range seed: want entry mode, entry visible=%v select visible=%v",
			c.gpuIndexEntry.Visible(), c.gpuIndexSelect.Visible())
	}
	if got := c.gpuIndexEntry.Text; got != "5" {
		t.Errorf("out-of-range seed entry text = %q, want 5", got)
	}
	if got := c.profileFromWidgets().Video.GPUIndex; got != 5 {
		t.Errorf("out-of-range seed round-trip = %d, want 5", got)
	}

	// The NVENC form still carries the GPU index row.
	c.caps = model.Capabilities{Encoders: map[string]model.Encoder{
		"hevc_nvenc": {Name: "hevc_nvenc", Media: ffmpeg.MediaVideo},
	}}
	c.videoEncoder.SetSelected("hevc_nvenc")
	c.updateControlState()
	if !slices.Contains(videoFormLabels(c), "GPU index") {
		t.Errorf("hevc_nvenc form rows %v miss GPU index", videoFormLabels(c))
	}
}
