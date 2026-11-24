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

	"git.skobk.in/skobkin/yave/internal/profiles"
	"git.skobk.in/skobkin/yave/internal/templates"
)

func (c *Controller) buildLayout() fyne.CanvasObject {
	browse := widget.NewButton("Browse...", c.chooseInput)
	c.showAllFiles = widget.NewCheck("All files", nil)
	recent := widget.NewButton("Recent", nil)
	recent.OnTapped = func() {
		menu := widget.NewPopUpMenu(c.recentMenu(), c.window.Canvas())
		pos := fyne.CurrentApp().Driver().AbsolutePositionForObject(recent)
		menu.ShowAtPosition(pos.Add(fyne.NewPos(0, recent.Size().Height)))
	}
	copyInfo := widget.NewButton("Copy", func() { fyne.CurrentApp().Clipboard().SetContent(c.mediaInfo.Text) })
	inputBox := container.NewVBox(
		widget.NewCard("FFmpeg", "", container.NewVBox(c.detection, container.NewHBox(c.retryDetect, c.detectActivity))),
		widget.NewCard("Input", "", container.NewBorder(nil, nil, nil, container.NewHBox(c.showAllFiles, recent, browse), c.inputEntry)),
		widget.NewCard("Media information", "", container.NewBorder(nil, container.NewHBox(layout.NewSpacer(), copyInfo), nil, nil, c.mediaInfo)),
	)
	inputTab := container.NewVScroll(inputBox)

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
		// Transport buttons share the enable row so playback costs no
		// extra vertical space.
		c.trimHint,
		container.NewHBox(c.trimEnable, layout.NewSpacer(),
			c.trimZoomOut, c.trimZoomIn, c.trimZoomFit, c.trimZoomSel,
			c.trimSnap, c.trimPrevMarker, c.trimNextMarker,
			c.transport.box),
		c.trimTimeline,
		c.trimMessage,
		c.buildSegmentPanel(),
		trimSegmentsRow(c),
		trimScenesRow(c),
		c.waveStatus,
		trimBoundariesRow(c),
	)
	trimTab := container.NewBorder(nil, controls, nil, nil, trimPreviewSection(c))
	c.trimControlsBox, c.trimTabBox = controls, trimTab

	outputTab := container.NewVBox(
		widget.NewForm(
			widget.NewFormItem("Container", c.container),
			widget.NewFormItem("Output template", container.NewBorder(
				nil, nil, nil, c.outputTemplateHelpButton(), c.output,
			)),
		),
		widget.NewCard("Resolved output", "", c.outputPreview),
	)

	videoTab := container.NewVScroll(container.NewVBox(streamIncludeRow(c.includeVideo, c.includeVideoHint), c.videoFormBox))

	audioTab := container.NewVScroll(container.NewVBox(streamIncludeRow(c.includeAudio, c.includeAudioHint), c.audioFormBox))

	runTab := container.NewBorder(
		container.NewVBox(
			container.NewHBox(widget.NewLabel("Command"), layout.NewSpacer(), c.copyCommand),
			c.command,
			container.NewHBox(widget.NewLabel("Output"), layout.NewSpacer(), c.openFolder, c.playOutput, c.logFollow, c.openLog),
			c.runResult,
		),
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

// trimPreviewSection composes the frame preview for the Trim tab: the
// preview layers with a transparent double-tap layer on top, which opens
// the full-screen preview.
func trimPreviewSection(c *Controller) fyne.CanvasObject {
	return c.previewLayers(newPreviewSurface(c.toggleFullscreen, nil))
}

// previewLayers stacks the frame preview: the image fills the whole cell —
// ImageFillContain letterboxes the frame inside it and centers it, so the
// cell can follow the window size — with the rendered-frame and hover
// timestamps drawn on a translucent chip pinned to the image's top edge
// instead of on their own row below it. extra objects (the double-tap
// layer, the full-screen strip) are stacked on top. The Trim tab and the
// full-screen view both build their stack from the same widgets.
func (c *Controller) previewLayers(extra ...fyne.CanvasObject) *fyne.Container {
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
	layers := []fyne.CanvasObject{
		c.previewImage,
		c.previewVeil,
		container.NewVBox(chip),
		container.NewCenter(c.previewSpinner),
	}
	return container.NewStack(append(layers, extra...)...)
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
		c.trimAdd, c.trimDelete, c.trimChapters,
	)
}

// trimScenesRow holds the scene-detection controls: the detect/cancel
// button, a status line, and the threshold slider once results exist.
// The whole row is hidden unless the FFmpeg build has the filters the
// detection needs, so it costs no space otherwise.
func trimScenesRow(c *Controller) fyne.CanvasObject {
	sliderCell := container.NewGridWrap(fyne.NewSize(200, c.sceneThreshold.MinSize().Height), c.sceneThreshold)
	c.sceneThresholdBox = container.NewHBox(c.sceneThresholdLabel, sliderCell)
	c.sceneThresholdBox.Hide()
	c.trimScenesBox = container.NewHBox(c.sceneDetect, c.sceneThresholdBox, c.sceneStatus)
	c.trimScenesBox.Hide()
	return c.trimScenesBox
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
	actions := container.NewHBox(c.startHint, c.start, c.cancel)

	return container.NewBorder(nil, nil, status, actions, c.progress)
}

// moreMenu lists the rarely used profile actions and the application
// entries behind the profile bar's "⋯" button.
func (c *Controller) moreMenu() *fyne.Menu {
	return fyne.NewMenu("",
		fyne.NewMenuItem("Duplicate profile", func() {
			c.askProfileName("Duplicate profile",
				profiles.UniqueName(c.profile.Selected+" copy", c.profileNameTaken), c.profileNameTaken, c.saveAs)
		}),
		fyne.NewMenuItem("Rename profile", func() {
			current := c.profile.Selected
			c.askProfileName("Rename profile", current, func(name string) bool {
				return name != current && c.profileNameTaken(name)
			}, c.renameProfile)
		}),
		fyne.NewMenuItem("Delete profile", c.deleteProfile),
		fyne.NewMenuItem("Set as default", c.setDefault),
		fyne.NewMenuItemSeparator(),
		fyne.NewMenuItem("Export profile…", c.exportProfile),
		fyne.NewMenuItem("Import profile…", c.importProfile),
		fyne.NewMenuItem("Restore built-in profiles…", c.restoreBuiltins),
		fyne.NewMenuItemSeparator(),
		fyne.NewMenuItem("Settings…", c.showSettings),
		fyne.NewMenuItem("Check for updates…", c.checkForUpdates),
		fyne.NewMenuItem("About", c.showAbout),
	)
}

func (c *Controller) showAbout() {
	dialog.ShowInformation("About YAVE", windowTitle()+"\nA focused desktop FFmpeg transcoding frontend.\n\nShortcuts: "+shortcutHelp, c.window)
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
	more := widget.NewButtonWithIcon("", theme.MoreHorizontalIcon(), nil)
	more.OnTapped = func() {
		menu := widget.NewPopUpMenu(c.moreMenu(), c.window.Canvas())
		pos := fyne.CurrentApp().Driver().AbsolutePositionForObject(more)
		menu.ShowAtPosition(pos.Add(fyne.NewPos(0, more.Size().Height)))
	}
	actions := container.NewHBox(
		widget.NewButton("Save", c.saveCurrentProfile),
		widget.NewButton("Save as", func() { c.askProfileName("Save profile as", "", c.profileNameTaken, c.saveAs) }),
		more,
	)
	c.profileLabel = widget.NewLabel("Profile:")
	c.defaultMarker = widget.NewLabel("★ default")
	c.defaultMarker.Hide()
	return container.NewBorder(nil, nil, c.profileLabel, container.NewHBox(c.defaultMarker, actions), c.profile)
}
