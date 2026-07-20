package ui

import (
	"context"
	"errors"
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	fynetest "fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"
)

func TestTrayProgressIndicatorDeduplicatesBucketsAndReset(t *testing.T) {
	var names []string
	indicator := newTrayProgressIndicator(func(icon fyne.Resource) {
		names = append(names, icon.Name())
	})

	indicator.SetProgress(0.101)
	indicator.SetProgress(0.119)
	indicator.SetProgress(0.12)
	indicator.Reset()
	indicator.Reset()

	want := []string{
		"app_icon_tray_progress_010.svg",
		"app_icon_tray_progress_012.svg",
		"app_icon.svg",
	}
	if len(names) != len(want) {
		t.Fatalf("icon updates = %v, want %v", names, want)
	}
	for index := range want {
		if names[index] != want[index] {
			t.Fatalf("icon updates = %v, want %v", names, want)
		}
	}
}

func TestSetRunResultResetsTrayProgressForEveryOutcome(t *testing.T) {
	app := fynetest.NewApp()
	defer app.Quit()

	tests := []struct {
		name string
		err  error
	}{
		{name: "success"},
		{name: "canceled", err: context.Canceled},
		{name: "failure", err: errors.New("FFmpeg failed")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var names []string
			indicator := newTrayProgressIndicator(func(icon fyne.Resource) {
				names = append(names, icon.Name())
			})
			indicator.SetProgress(0.5)

			progress := widget.NewProgressBar()
			progressInfo := widget.NewLabel("")
			runTab := container.NewTabItem("Run", widget.NewLabel("run"))
			controller := &Controller{
				status:       widget.NewLabel("Running"),
				progress:     progress,
				progressInfo: progressInfo,
				runTab:       runTab,
				tabs: container.NewAppTabs(
					container.NewTabItem("Input", widget.NewLabel("input")),
					runTab,
				),
				trayProgress: indicator,
			}

			controller.setRunResult(tt.err)

			if got := names[len(names)-1]; got != "app_icon.svg" {
				t.Fatalf("last tray icon = %q, want base app icon", got)
			}
		})
	}
}
