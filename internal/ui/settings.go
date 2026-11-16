package ui

import (
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"

	"git.skobk.in/skobkin/yave/internal/profiles"
)

func (c *Controller) showSettings() {
	ffmpegPath := widget.NewEntry()
	ffmpegPath.SetText(c.config.FFmpeg.FFmpegPath)
	ffprobePath := widget.NewEntry()
	ffprobePath.SetText(c.config.FFmpeg.FFprobePath)
	checkUpdates := widget.NewCheck("", nil)
	checkUpdates.SetChecked(c.config.Updates.CheckAutomatically)
	form := dialog.NewForm("Settings", "Save and detect", "Cancel", []*widget.FormItem{
		widget.NewFormItem("FFmpeg path", ffmpegPath),
		widget.NewFormItem("ffprobe path", ffprobePath),
		widget.NewFormItem("Check for updates automatically", checkUpdates),
	}, func(ok bool) {
		if !ok {
			return
		}
		c.config.FFmpeg.FFmpegPath = strings.TrimSpace(ffmpegPath.Text)
		c.config.FFmpeg.FFprobePath = strings.TrimSpace(ffprobePath.Text)
		c.config.Updates.CheckAutomatically = checkUpdates.Checked
		c.saveConfig()
		c.applyUpdateCheckSetting(checkUpdates.Checked)
		c.detect()
	}, c.window)
	// Without an explicit size the dialog sits at its minimum, where the
	// wide "Check for updates automatically" label column leaves the two
	// path entries only a sliver of width; 560px matches the profile-name
	// dialog and gives the entries room for full executable paths.
	form.Resize(fyne.NewSize(560, 260))
	form.Show()
}

func (c *Controller) saveConfig() {
	if err := profiles.Save(c.configPath, c.config); err != nil {
		dialog.ShowError(err, c.window)
	}
}
