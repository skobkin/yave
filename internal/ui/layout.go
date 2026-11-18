package ui

import (
	"image/color"
	"sort"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
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

	// The trim tab deliberately uses no heading cards: the section
	// titles ("Trim", "Frame preview", "Boundaries") cost vertical
	// space without saying anything the tab name doesn't. The frame
	// preview leads and expands: a Border layout glues every control
	// (status, enable check, timeline, message, boundary row) to the
	// bottom edge and gives all remaining space to the preview cell,
	// so resizing the window only resizes the preview image. The
	// boundary fields share one row with the duration and Reset so the
	// whole tab fits next to the timeline without scrolling.
	controls := container.NewVBox(
		c.previewStatus,
		c.trimEnable,
		c.trimTimeline,
		c.trimMessage,
		trimSegmentsRow(c),
		trimBoundariesRow(c),
	)
	trimTab := container.NewBorder(nil, controls, nil, nil, trimPreviewSection(c))

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
		container.NewTabItem("Trim", trimTab),
		container.NewTabItem("Output", outputTab),
		container.NewTabItem("Video", videoTab),
		container.NewTabItem("Audio & Subtitles", audioTab),
		c.runTab,
	)
	return container.NewBorder(c.profileBar(), c.buildStatusBar(), nil, nil, c.tabs)
}

// trimPreviewSection composes the frame preview: the image fills the
// whole cell — ImageFillContain letterboxes the frame inside it and
// centers it, so the cell can follow the window size — with the
// rendered-frame and hover timestamps drawn on a translucent chip
// pinned to the image's top edge instead of on their own row below it.
func trimPreviewSection(c *Controller) fyne.CanvasObject {
	chip := container.NewStack(
		overlayChipRectangle(),
		container.NewPadded(container.NewHBox(
			c.previewTimeLabel, layout.NewSpacer(), c.hoverTimeLabel,
		)),
	)
	// The VBox pins the chip to the top edge: box layouts stack from
	// the top, so the chip keeps its own (label-sized) height at the
	// top of the cell while the Stack stretches the image underneath.
	// The tint veil sits directly over the image (under the chip), and
	// the spinner floats over the image's center as further layers, so
	// showing them never reflows the image.
	return container.NewStack(
		c.previewImage,
		c.previewVeil,
		container.NewVBox(chip),
		container.NewCenter(c.previewSpinner),
	)
}

// overlayChipRectangle is the translucent background behind the preview
// timestamps: the theme's overlay-background hue with forced
// transparency, so the chip stays legible in both theme variants
// (matching overlay-on-foreground contrast) without hiding the frame
// underneath.
func overlayChipRectangle() *canvas.Rectangle {
	settings := fyne.CurrentApp().Settings()
	chip := color.NRGBAModel.Convert(
		settings.Theme().Color(theme.ColorNameOverlayBackground, settings.ThemeVariant()),
	).(color.NRGBA)
	chip.A = 0xc0
	return canvas.NewRectangle(chip)
}

// trimEntryWidth fits a full HH:MM:SS.mmm timestamp with room to
// spare; the natural Entry width is a couple of characters.
const trimEntryWidth = 140

// trimSegmentsRow holds the export-mode dropdown and the segment
// add/delete buttons on one line above the boundary fields: the mode
// governs the whole export job, while Add/Delete manage the segment
// list the boundary entries edit.
func trimSegmentsRow(c *Controller) fyne.CanvasObject {
	return container.NewHBox(
		widget.NewLabel("Export"), c.trimExport,
		c.trimAdd, c.trimDelete,
	)
}

// trimBoundariesRow lays the boundary fields out on a single line: In
// and Out entries with their Set buttons, the selected duration, and
// Reset. The entries sit in fixed-width GridWrap cells — an HBox would
// otherwise shrink them to their natural width and make them unusable.
func trimBoundariesRow(c *Controller) fyne.CanvasObject {
	// The cell height is the taller of the entry and the button, so
	// both controls stand at the same height however the theme sizes
	// them.
	cellHeight := fyne.Max(c.trimIn.MinSize().Height, c.trimSetIn.MinSize().Height)
	entryCell := fyne.NewSize(trimEntryWidth, cellHeight)
	return container.NewHBox(
		widget.NewLabel("In"), container.NewGridWrap(entryCell, c.trimIn), c.trimSetIn,
		widget.NewLabel("Out"), container.NewGridWrap(entryCell, c.trimOut), c.trimSetOut,
		widget.NewLabel("Duration"), c.trimDuration,
		c.trimReset,
	)
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

Result (profile "HEVC VAAPI 720p CQ"):
/home/user/Videos/movie_HEVC_VAAPI_720p_CQ.mkv`)
	return b.String()
}

func (c *Controller) showOutputTemplateHelp() {
	help := outputTemplateHelpText()
	text := widget.NewMultiLineEntry()
	text.SetText(help)
	text.SetMinRowsVisible(16)
	text.Disable()

	helpDialog := dialog.NewCustom("Output template placeholders", "Close", text, c.window)
	text.Wrapping = fyne.TextWrapWord
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
