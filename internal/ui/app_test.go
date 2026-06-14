package ui

import (
	"errors"
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	fynetest "fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"

	"git.skobk.in/skobkin/yave/internal/model"
	"git.skobk.in/skobkin/yave/internal/validation"
)

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
