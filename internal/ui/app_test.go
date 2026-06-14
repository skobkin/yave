package ui

import (
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	fynetest "fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"

	"git.skobk.in/skobkin/yave/internal/model"
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
