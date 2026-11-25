package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"

	"git.skobk.in/skobkin/yave/internal/ffmpeg"
)

// maxAutoRenameAttempts bounds the search for a free "_N" name.
const maxAutoRenameAttempts = 9999

// freeOutputBase finds the smallest "_N" suffix (name_1.mkv, name_2.mkv…)
// for which none of the targets derived from the renamed base exists and
// none is the input file. targetsFor maps a base to every file the export
// would write, so a multi-part export (…_1_part01.mkv, …) is renamed
// consistently as one unit. ok is false when no free name is found.
func freeOutputBase(base, input string, targetsFor func(base string) []string) (string, bool) {
	ext := filepath.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	for n := 1; n <= maxAutoRenameAttempts; n++ {
		candidate := fmt.Sprintf("%s_%d%s", stem, n, ext)
		free := true
		for _, target := range targetsFor(candidate) {
			if _, err := os.Lstat(target); err == nil || ffmpeg.SameFilePath(target, input) {
				free = false
				break
			}
		}
		if free {
			return candidate, true
		}
	}
	return "", false
}

// showOverwriteDialog asks what to do about existing outputs: replace
// them, write next to them under free names, or cancel. The list scrolls,
// so many conflicting parts cannot make the dialog taller than the window.
func (c *Controller) showOverwriteDialog(existing []string, onOverwrite, onRename func()) {
	label := widget.NewLabel(overwritePromptMessage(existing))
	label.Wrapping = fyne.TextWrapWord
	scroll := container.NewVScroll(label)
	scroll.SetMinSize(fyne.NewSize(520, 160))

	// All three choices share the dialog's own button row, dismiss first
	// as in every Fyne dialog, then the destructive choice, then the safe
	// highlighted one.
	d := dialog.NewCustomWithoutButtons("Overwrite output?", scroll, c.window)
	cancel := widget.NewButton("Cancel", d.Hide)
	overwrite := widget.NewButton("Overwrite", func() {
		d.Hide()
		onOverwrite()
	})
	overwrite.Importance = widget.DangerImportance
	rename := widget.NewButton("Auto-rename", func() {
		d.Hide()
		onRename()
	})
	rename.Importance = widget.HighImportance
	d.SetButtons([]fyne.CanvasObject{cancel, overwrite, rename})
	// Only the width is set: the height follows the content, so there is
	// no empty band between the message and the buttons.
	d.Resize(fyne.NewSize(600, d.MinSize().Height))
	d.Show()
}
