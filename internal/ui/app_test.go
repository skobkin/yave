package ui

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	fynetest "fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"

	"git.skobk.in/skobkin/yave/internal/encoder"
	"git.skobk.in/skobkin/yave/internal/model"
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

func TestRunFieldsUseWordWrapping(t *testing.T) {
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
	if c.log.Wrapping != fyne.TextWrapWord {
		t.Fatalf("output wrapping = %v, want word wrapping", c.log.Wrapping)
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
		status:   widget.NewLabel("Running"),
		progress: widget.NewProgressBar(),
	}
	otherTab := container.NewTabItem("Input", widget.NewLabel("input"))
	c.runTab = container.NewTabItem("Run", widget.NewLabel("run"))
	c.tabs = container.NewAppTabs(otherTab, c.runTab)

	c.setRunResult(errors.New("FFmpeg failed: exit status 1"))

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

func TestSVTPresetSwitchesControlsAndPreservesCategoricalValue(t *testing.T) {
	app := fynetest.NewApp()
	defer app.Quit()

	c := testController()
	c.createWidgets()
	c.videoEncoder.SetSelected("libx264")
	c.preset.SetText("slow")

	c.videoEncoder.SetSelected("libsvtav1")
	if c.preset.Visible() || !c.presetSlider.Visible() {
		t.Fatal("SVT-AV1 should show only the integer preset slider")
	}
	c.presetSlider.SetValue(13)
	if got := c.profileFromWidgets().Video.Preset; got != "13" {
		t.Fatalf("SVT preset = %q, want 13", got)
	}

	c.videoEncoder.SetSelected("libx265")
	if !c.preset.Visible() || c.presetSlider.Visible() {
		t.Fatal("x265 should show only the categorical preset entry")
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
	// Either hidden (our path) or disabled is acceptable. The bug
	// we're guarding against is the previous behaviour: enabled and
	// visible despite the encoder doing nothing with the value.
	if !c.preset.Hidden && c.preset.Disabled() == false {
		t.Errorf("hevc_vaapi preset must be hidden or disabled, not enabled")
	}

	// Switching to a categorical-preset encoder (libx264) must
	// re-show the field.
	c.videoEncoder.SetSelected("libx264")
	if !c.preset.Visible() || c.presetSlider.Visible() {
		t.Errorf("libx264 must show the categorical preset entry after VAAPI")
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

// TestEncoderSwitchToNVENCHidesVAAPIDevice verifies the dispatch gates
// the hardware-device widgets by HWBackend: NVENC enables gpuIndex,
// disables vaapiDevice and qsvDevice.
func TestEncoderSwitchToNVENCHidesVAAPIDevice(t *testing.T) {
	app := fynetest.NewApp()
	defer app.Quit()

	c := testController()
	c.createWidgets()

	// VAAPI on first: vaapiDevice enabled, qsvDevice/gpuIndex disabled.
	c.videoEncoder.SetSelected("hevc_vaapi")
	if c.vaapiDevice.Disabled() {
		t.Errorf("vaapiDevice should be enabled for hevc_vaapi")
	}
	if !c.qsvDevice.Disabled() {
		t.Errorf("qsvDevice should be disabled for hevc_vaapi")
	}
	if !c.gpuIndex.Disabled() {
		t.Errorf("gpuIndex should be disabled for hevc_vaapi")
	}

	// Switch to NVENC: gpuIndex enabled, the other two disabled.
	c.videoEncoder.SetSelected("hevc_nvenc")
	if !c.vaapiDevice.Disabled() {
		t.Errorf("vaapiDevice should be disabled for hevc_nvenc")
	}
	if !c.qsvDevice.Disabled() {
		t.Errorf("qsvDevice should be disabled for hevc_nvenc")
	}
	if c.gpuIndex.Disabled() {
		t.Errorf("gpuIndex should be enabled for hevc_nvenc")
	}
}

// TestEncoderSwitchToQSVEnablesQSVDevice verifies QSV enables only the
// qsvDevice widget.
func TestEncoderSwitchToQSVEnablesQSVDevice(t *testing.T) {
	app := fynetest.NewApp()
	defer app.Quit()

	c := testController()
	c.createWidgets()

	c.videoEncoder.SetSelected("hevc_qsv")
	if !c.vaapiDevice.Disabled() {
		t.Errorf("vaapiDevice should be disabled for hevc_qsv")
	}
	if c.qsvDevice.Disabled() {
		t.Errorf("qsvDevice should be enabled for hevc_qsv")
	}
	if !c.gpuIndex.Disabled() {
		t.Errorf("gpuIndex should be disabled for hevc_qsv")
	}

	// Software encoder: every device widget disabled.
	c.videoEncoder.SetSelected("libx265")
	if !c.vaapiDevice.Disabled() {
		t.Errorf("vaapiDevice should be disabled for libx265")
	}
	if !c.qsvDevice.Disabled() {
		t.Errorf("qsvDevice should be disabled for libx265")
	}
	if !c.gpuIndex.Disabled() {
		t.Errorf("gpuIndex should be disabled for libx265")
	}

	// Copy: every device widget disabled.
	c.videoEncoder.SetSelected("copy")
	if !c.vaapiDevice.Disabled() {
		t.Errorf("vaapiDevice should be disabled for copy")
	}
	if !c.qsvDevice.Disabled() {
		t.Errorf("qsvDevice should be disabled for copy")
	}
	if !c.gpuIndex.Disabled() {
		t.Errorf("gpuIndex should be disabled for copy")
	}
}

// TestRateControlOptionsFollowEncoder verifies the rate-control dropdown
// is repopulated per encoder from RateControlModes() and the existing
// selection is preserved when supported (or falls back to default).
func TestRateControlOptionsFollowEncoder(t *testing.T) {
	app := fynetest.NewApp()
	defer app.Quit()

	c := testController()
	c.createWidgets()

	// libx264 only supports CRF — matches the prior buildVideoArgs
	// behaviour which never emitted -b:v for libx264.
	c.videoEncoder.SetSelected("libx264")
	want := []string{"CRF"}
	if !slices.Equal(c.rateControl.Options, want) {
		t.Errorf("libx264 rate-control options = %v, want %v", c.rateControl.Options, want)
	}

	// libsvtav1 advertises {CRF, bitrate}.
	c.videoEncoder.SetSelected("libsvtav1")
	want = []string{"CRF", "bitrate"}
	if !slices.Equal(c.rateControl.Options, want) {
		t.Errorf("libsvtav1 rate-control options = %v, want %v", c.rateControl.Options, want)
	}

	// libaom-av1 advertises {CRF, bitrate}.
	c.videoEncoder.SetSelected("libaom-av1")
	want = []string{"CRF", "bitrate"}
	if !slices.Equal(c.rateControl.Options, want) {
		t.Errorf("libaom-av1 rate-control options = %v, want %v", c.rateControl.Options, want)
	}

	// hevc_vaapi advertises {AUTO, CQP, VBR, CBR}.
	c.videoEncoder.SetSelected("hevc_vaapi")
	want = []string{"Auto", "CQP", "VBR", "CBR"}
	if !slices.Equal(c.rateControl.Options, want) {
		t.Errorf("hevc_vaapi rate-control options = %v, want %v", c.rateControl.Options, want)
	}

	// hevc_nvenc advertises {CQP, VBR, CBR}.
	c.videoEncoder.SetSelected("hevc_nvenc")
	want = []string{"CQP", "VBR", "CBR"}
	if !slices.Equal(c.rateControl.Options, want) {
		t.Errorf("hevc_nvenc rate-control options = %v, want %v", c.rateControl.Options, want)
	}

	// hevc_qsv advertises {CQP, VBR, CBR}.
	c.videoEncoder.SetSelected("hevc_qsv")
	want = []string{"CQP", "VBR", "CBR"}
	if !slices.Equal(c.rateControl.Options, want) {
		t.Errorf("hevc_qsv rate-control options = %v, want %v", c.rateControl.Options, want)
	}
}

// TestRateControlFallsBackToDefaultWhenUnsupported verifies the
// dispatch resets the rate-control selection to the encoder's default
// when the prior selection is no longer supported.
func TestRateControlFallsBackToDefaultWhenUnsupported(t *testing.T) {
	app := fynetest.NewApp()
	defer app.Quit()

	c := testController()
	c.createWidgets()

	// VAAPI has AUTO mode; setting it explicitly so we can verify the
	// fallback path.
	c.videoEncoder.SetSelected("hevc_vaapi")
	c.rateControl.SetSelected("Auto")
	if c.rateControl.Selected != "Auto" {
		t.Fatalf("setup: rate control = %q, want Auto", c.rateControl.Selected)
	}

	// NVENC does not support AUTO; selecting it must fall back to CQP
	// (NVENC's default) so the user never sees a stale mode that the
	// encoder cannot accept.
	c.videoEncoder.SetSelected("hevc_nvenc")
	if c.rateControl.Selected != "CQP" {
		t.Errorf("rate control = %q, want CQP (NVENC default)", c.rateControl.Selected)
	}
}

// TestEncoderSwitchFromLibsvtav1ToLibaomPreservesCPUUsed verifies the
// integer preset slider remaps proportionally between two integer-preset
// encoders (libsvtav1 {0,13} -> libaom-av1 {0,8}). 6 on SVT-AV1 maps to
// roughly 4 on libaom-av1 (mid-quality in both scales).
func TestEncoderSwitchFromLibsvtav1ToLibaomPreservesCPUUsed(t *testing.T) {
	app := fynetest.NewApp()
	defer app.Quit()

	c := testController()
	c.createWidgets()

	c.videoEncoder.SetSelected("libsvtav1")
	c.presetSlider.SetValue(6)
	if got := c.profileFromWidgets().Video.Preset; got != "6" {
		t.Fatalf("SVT preset = %q, want 6", got)
	}

	c.videoEncoder.SetSelected("libaom-av1")
	if got := c.presetSlider.Value(); got != 4 {
		t.Errorf("libaom-av1 preset = %d, want 4 (proportional remap of SVT 6/13 -> libaom 4/8)", got)
	}
	if got := c.profileFromWidgets().Video.Preset; got != "4" {
		t.Errorf("profile preset = %q, want 4", got)
	}
}

// TestInitialEncoderListIncludesNVENCAndQSV verifies the createWidgets
// initial list is seeded from the registry so new encoders appear even
// before ffmpeg detection completes.
func TestInitialEncoderListIncludesNVENCAndQSV(t *testing.T) {
	app := fynetest.NewApp()
	defer app.Quit()

	c := testController()
	c.createWidgets()

	want := []string{"hevc_nvenc", "hevc_qsv", "libaom-av1"}
	for _, name := range want {
		if !slices.Contains(c.videoEncoder.Options, name) {
			t.Errorf("initial video encoder list missing %q (got %v)", name, c.videoEncoder.Options)
		}
	}
	// "copy" must always be the first option so it is the default selection.
	if len(c.videoEncoder.Options) == 0 || c.videoEncoder.Options[0] != "copy" {
		t.Errorf("initial video encoder list must start with 'copy' (got %v)", c.videoEncoder.Options)
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
// the user is stuck. Switching encoder must clear the field so the
// round-trip through loadProfile / profileFromWidgets does not
// contain a value the encoder refuses.
func TestProfileClearedWhenEncoderDoesNotSupportProfile(t *testing.T) {
	app := fynetest.NewApp()
	defer app.Quit()

	c := testController()
	c.createWidgets()

	// Two paths leave the Profile field populated: (a) the user
	// typed/saved a profile on a supporting encoder (e.g. h264_nvenc
	// "main"), then (b) the user picks av1_nvenc. Both paths must
	// drop the value.
	t.Run("user switch to av1_nvenc", func(t *testing.T) {
		c.videoEncoder.SetSelected("h264_nvenc")
		c.videoProfile.SetText("main")
		c.videoEncoder.SetSelected("av1_nvenc")

		if got := c.videoProfile.Text; got != "" {
			t.Errorf("videoProfile.Text after switch to av1_nvenc = %q, want \"\"", got)
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

		if got := c.videoProfile.Text; got != "" {
			t.Errorf("videoProfile.Text after load = %q, want \"\"", got)
		}
		if got := c.profileFromWidgets().Video.Profile; got != "" {
			t.Errorf("profileFromWidgets().Video.Profile after load = %q, want \"\"", got)
		}
	})

	// Sanity check: switching back to a profile-supporting encoder
	// must not invent a value where the user had none. An empty
	// field stays empty so the encoder sees the same null state it
	// would on first launch.
	t.Run("switching back does not repopulate", func(t *testing.T) {
		c.videoEncoder.SetSelected("av1_nvenc")
		c.videoProfile.SetText("") // already empty; ensures SetText("") does not re-fire changed

		c.videoEncoder.SetSelected("h264_nvenc")
		if got := c.videoProfile.Text; got != "" {
			t.Errorf("videoProfile.Text after switch back to h264_nvenc = %q, want \"\"", got)
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
