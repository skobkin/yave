package ui

import (
	"image/color"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

// readOnlyTheme makes a disabled entry read like text rather than like a
// switched-off control. Fyne has no read-only Entry; a disabled one still
// allows selecting text and copying it from the context menu, but paints
// the text and the border in the dim disabled colour. Under this theme the
// disabled colour is the regular foreground and the border is dropped, so
// the entry becomes a plain text panel on the input background. Every
// other lookup goes to the app theme at call time.
type readOnlyTheme struct{}

func (readOnlyTheme) base() fyne.Theme { return fyne.CurrentApp().Settings().Theme() }

func (t readOnlyTheme) Color(n fyne.ThemeColorName, v fyne.ThemeVariant) color.Color {
	if n == theme.ColorNameDisabled {
		n = theme.ColorNameForeground
	}
	return t.base().Color(n, v)
}

func (t readOnlyTheme) Font(s fyne.TextStyle) fyne.Resource     { return t.base().Font(s) }
func (t readOnlyTheme) Icon(n fyne.ThemeIconName) fyne.Resource { return t.base().Icon(n) }

func (t readOnlyTheme) Size(n fyne.ThemeSizeName) float32 {
	if n == theme.SizeNameInputBorder {
		return 0
	}
	return t.base().Size(n)
}

// newReadOnlyText is a disabled monospace multi-line entry for
// machine-generated text (the FFmpeg command and its log). Wrap it with
// readOnlyText when placing it.
func newReadOnlyText() *widget.Entry {
	e := widget.NewMultiLineEntry()
	e.TextStyle = fyne.TextStyle{Monospace: true}
	e.Disable()
	return e
}

// readOnlyText places a newReadOnlyText entry under readOnlyTheme.
func readOnlyText(e *widget.Entry) fyne.CanvasObject {
	return container.NewThemeOverride(e, readOnlyTheme{})
}
