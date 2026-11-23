package ui

import (
	"path/filepath"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"

	"git.skobk.in/skobkin/yave/internal/model"
	"git.skobk.in/skobkin/yave/internal/profiles"
)

func (c *Controller) showSettings() {
	ffmpegPath := widget.NewEntry()
	ffmpegPath.SetText(c.config.FFmpeg.FFmpegPath)
	ffprobePath := widget.NewEntry()
	ffprobePath.SetText(c.config.FFmpeg.FFprobePath)
	resetPaths := widget.NewButton("Reset to auto-detect", func() {
		defaults := model.DefaultConfig().FFmpeg
		ffmpegPath.SetText(defaults.FFmpegPath)
		ffprobePath.SetText(defaults.FFprobePath)
	})
	checkUpdates := widget.NewCheck("", nil)
	checkUpdates.SetChecked(c.config.Updates.CheckAutomatically)
	closeToTray := widget.NewCheck("", nil)
	closeToTray.SetChecked(!c.config.UI.QuitOnClose)
	n := c.config.Notifications
	notifyCompletedCheck := widget.NewCheck("Conversion finished", nil)
	notifyCompletedCheck.SetChecked(!n.DisableCompleted)
	notifyFailedCheck := widget.NewCheck("Conversion failed", nil)
	notifyFailedCheck.SetChecked(!n.DisableFailed)
	notifyCanceledCheck := widget.NewCheck("Conversion canceled", nil)
	notifyCanceledCheck.SetChecked(!n.DisableCanceled)
	typeChecks := []*widget.Check{notifyCompletedCheck, notifyFailedCheck, notifyCanceledCheck}
	notifyAll := widget.NewCheck("", func(on bool) {
		for _, check := range typeChecks {
			if on {
				check.Enable()
			} else {
				check.Disable()
			}
		}
	})
	notifyAll.SetChecked(!n.DisableAll)
	form := dialog.NewForm("Settings", "Save and detect", "Cancel", []*widget.FormItem{
		widget.NewFormItem("FFmpeg path", c.pathWithBrowse(ffmpegPath)),
		widget.NewFormItem("ffprobe path", c.pathWithBrowse(ffprobePath)),
		widget.NewFormItem("", resetPaths),
		widget.NewFormItem("Check for updates automatically", checkUpdates),
		widget.NewFormItem("Close to tray", closeToTray),
		widget.NewFormItem("Desktop notifications", notifyAll),
		widget.NewFormItem("   Notify when", container.NewVBox(typeChecks[0], typeChecks[1], typeChecks[2])),
	}, func(ok bool) {
		if !ok {
			return
		}
		prevFFmpeg := c.config.FFmpeg.FFmpegPath
		c.config.FFmpeg.FFmpegPath = strings.TrimSpace(ffmpegPath.Text)
		c.config.FFmpeg.FFprobePath = strings.TrimSpace(ffprobePath.Text)
		c.config.Updates.CheckAutomatically = checkUpdates.Checked
		c.config.UI.QuitOnClose = !closeToTray.Checked
		c.config.Notifications = model.NotificationsConfig{
			DisableAll:       !notifyAll.Checked,
			DisableCompleted: !notifyCompletedCheck.Checked,
			DisableFailed:    !notifyFailedCheck.Checked,
			DisableCanceled:  !notifyCanceledCheck.Checked,
		}
		if c.config.FFmpeg.FFmpegPath != prevFFmpeg {
			// Invalidate previews immediately — do not wait for detect()
			// to resolve the new path, or a hung old extraction could
			// keep the pending indication stuck until it returns.
			c.onFFmpegExecutableChanged()
		}
		c.saveConfig()
		c.applyUpdateCheckSetting(checkUpdates.Checked)
		c.detect()
	}, c.window)
	// Without an explicit size the dialog sits at its minimum, where the
	// wide "Check for updates automatically" label column leaves the two
	// path entries only a sliver of width; 560px matches the profile-name
	// dialog and gives the entries room for full executable paths.
	form.Resize(fyne.NewSize(620, 420))
	form.Show()
}

func (c *Controller) saveConfig() {
	if err := profiles.Save(c.configPath, c.config); err != nil {
		dialog.ShowError(err, c.window)
	}
}

// pathWithBrowse puts a Browse… button beside an executable path entry.
// The chosen file only fills the entry: an invalid executable is reported
// by the detection that follows "Save and detect", not silently accepted.
func (c *Controller) pathWithBrowse(entry *widget.Entry) fyne.CanvasObject {
	browse := widget.NewButton("Browse…", func() {
		d := dialog.NewFileOpen(func(r fyne.URIReadCloser, err error) {
			if err != nil {
				dialog.ShowError(err, c.window)
				return
			}
			if r == nil {
				return
			}
			_ = r.Close()
			entry.SetText(r.URI().Path())
		}, c.window)
		if dir := filepath.Dir(strings.TrimSpace(entry.Text)); strings.ContainsRune(entry.Text, filepath.Separator) {
			if loc := locationFor(dir); loc != nil {
				d.SetLocation(loc)
			}
		}
		d.Resize(openFileDialogSize(c.window.Canvas().Size()))
		d.Show()
	})
	return container.NewBorder(nil, nil, nil, browse, entry)
}
