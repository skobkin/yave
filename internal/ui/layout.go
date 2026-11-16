package ui

import (
	"sort"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"

	"git.skobk.in/skobkin/yave/internal/templates"
)

func (c *Controller) buildLayout() fyne.CanvasObject {
	browse := widget.NewButton("Browse...", c.chooseInput)
	inputTab := container.NewVBox(
		widget.NewCard("FFmpeg", "", c.detection),
		widget.NewCard("Input", "", container.NewBorder(nil, nil, nil, browse, c.inputEntry)),
		widget.NewCard("Media information", "", c.mediaInfo),
	)

	outputTab := container.NewVBox(
		widget.NewForm(
			widget.NewFormItem("Container", c.container),
			widget.NewFormItem("Output template", container.NewBorder(
				nil, nil, nil, c.outputTemplateHelpButton(), c.output,
			)),
		),
		widget.NewCard("Resolved output", "", c.outputPreview),
	)

	videoTab := container.NewVScroll(container.NewVBox(c.videoFormBox))

	audioTab := container.NewVBox(c.audioFormBox)

	runTab := container.NewBorder(
		container.NewVBox(widget.NewLabel("Command"), c.command, widget.NewLabel("Output")),
		nil, nil, nil,
		c.log,
	)

	c.runTab = container.NewTabItem("Run", runTab)
	c.tabs = container.NewAppTabs(
		container.NewTabItem("Input", inputTab),
		container.NewTabItem("Output", outputTab),
		container.NewTabItem("Video", videoTab),
		container.NewTabItem("Audio & Subtitles", audioTab),
		c.runTab,
	)
	return container.NewBorder(c.profileBar(), c.buildStatusBar(), nil, nil, c.tabs)
}

func (c *Controller) buildStatusBar() *fyne.Container {
	progressInfoSize := widget.NewLabel(progressInfoWidthSample).MinSize()
	progressInfo := container.NewGridWrap(progressInfoSize, c.progressInfo)
	status := container.NewHBox(c.status, progressInfo)
	actions := container.NewHBox(c.start, c.cancel)

	return container.NewBorder(nil, nil, status, actions, c.progress)
}

func (c *Controller) outputTemplateHelpButton() *widget.Button {
	button := widget.NewButton("?", c.showOutputTemplateHelp)
	button.Importance = widget.LowImportance
	return button
}

// outputTemplateHelpText renders the template-help dialog body from
// templates.Placeholders — the single source of truth — so the help
// cannot drift from what Resolve actually accepts. Keys are listed
// sorted, space-padded to a common width; the example block below is
// hand-written prose the map cannot express.
func outputTemplateHelpText() string {
	keys := make([]string, 0, len(templates.Placeholders))
	width := 0
	for key := range templates.Placeholders {
		keys = append(keys, key)
		if len(key) > width {
			width = len(key)
		}
	}
	sort.Strings(keys)

	var b strings.Builder
	for _, key := range keys {
		b.WriteString("{" + key + "}")
		b.WriteString(strings.Repeat(" ", width-len(key)+2))
		b.WriteString(templates.Placeholders[key])
		b.WriteString("\n")
	}
	b.WriteString(`
Template:
{dir}/{name}_{profile}.{ext}

Result:
/home/user/Videos/movie_HEVC_720p.mkv`)
	return b.String()
}

func (c *Controller) showOutputTemplateHelp() {
	help := outputTemplateHelpText()
	text := widget.NewMultiLineEntry()
	text.SetText(help)
	text.SetMinRowsVisible(16)
	text.Disable()

	helpDialog := dialog.NewCustom("Output template placeholders", "Close", text, c.window)
	helpDialog.Resize(fyne.NewSize(760, 600))
	helpDialog.Show()
}

func (c *Controller) profileBar() fyne.CanvasObject {
	actions := container.NewHBox(
		widget.NewButton("Save", c.saveCurrentProfile),
		widget.NewButton("Save as", func() { c.askProfileName("Save profile as", "", c.saveAs) }),
		widget.NewButton("Duplicate", func() { c.askProfileName("Duplicate profile", c.profile.Selected+" copy", c.saveAs) }),
		widget.NewButton("Rename", func() { c.askProfileName("Rename profile", c.profile.Selected, c.renameProfile) }),
		widget.NewButton("Delete", c.deleteProfile),
		widget.NewButton("Set default", c.setDefault),
		widget.NewButton("Settings", c.showSettings),
	)
	return container.NewBorder(nil, nil, widget.NewLabel("Profile:"), actions, c.profile)
}
