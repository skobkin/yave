package ui

import (
	"strings"
	"time"
	"unicode/utf8"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"git.skobk.in/skobkin/yave/internal/templates"
)

// placeholderOrder is the picker's row order: where the file goes, what it
// is called, what the profile adds, then the clock. Every key of
// templates.Placeholders appears exactly once (pinned by a test).
var placeholderOrder = []string{
	"dir", "name", "input_ext", "ext",
	"profile", "codec", "width", "height", "video_settings", "audio_settings",
	"datetime", "date", "time",
}

// placeholderMatches reports whether a placeholder fits the picker's
// filter, matching its key or its description, ignoring case.
func placeholderMatches(key, filter string) bool {
	filter = strings.ToLower(strings.TrimSpace(filter))
	if filter == "" {
		return true
	}
	return strings.Contains(key, filter) ||
		strings.Contains(strings.ToLower(templates.Placeholders[key]), filter)
}

// insertPlaceholder adds token to template at the cursor column and
// returns the new text and cursor. A cursor at 0 in a non-empty template
// usually means the field was never clicked rather than a deliberate
// choice, and the start of a template is rarely the place for a name
// part, so the token then goes before a trailing ".{ext}", or at the end.
func insertPlaceholder(template string, cursor int, token string) (string, int) {
	runes := []rune(template)
	at := min(max(cursor, 0), len(runes))
	if at == 0 && len(runes) > 0 {
		at = len(runes)
		if before, ok := strings.CutSuffix(template, ".{ext}"); ok {
			at = utf8.RuneCountInString(before)
		}
	}
	text := string(runes[:at]) + token + string(runes[at:])
	return text, at + utf8.RuneCountInString(token)
}

// showOutputTemplateHelp opens the placeholder picker: every placeholder
// with its description and what it expands to for the current input and
// profile. Insert puts it at the template's cursor, Copy puts it on the
// clipboard, and the resolved path below follows each insertion.
func (c *Controller) showOutputTemplateHelp() {
	values := templates.PlaceholderValues(c.templateRequest(c.profileFromWidgets(), time.Now()))
	// Monospace only: a bold monospace face is not guaranteed by every theme.
	keyStyle := fyne.TextStyle{Monospace: true}
	keyWidth := float32(0)
	for _, key := range placeholderOrder {
		keyWidth = max(keyWidth, fyne.MeasureText("{"+key+"}", theme.TextSize(), keyStyle).Width)
	}
	keyWidth += 2*theme.InnerPadding() + 1

	result := widget.NewLabel("")
	result.Wrapping = fyne.TextWrapBreak
	template := widget.NewLabelWithStyle("", fyne.TextAlignLeading, fyne.TextStyle{Monospace: true})
	template.Wrapping = fyne.TextWrapBreak
	showResult := func() {
		template.SetText(c.output.Text)
		result.SetText(c.outputPreview.Text)
	}
	showResult()

	rows := make(map[string]fyne.CanvasObject, len(placeholderOrder))
	list := container.NewVBox()
	for _, key := range placeholderOrder {
		token := "{" + key + "}"
		value, known := values[key]
		if !known {
			value = "— select an input to see it"
		}
		insert := widget.NewButton("Insert", func() {
			text, cursor := insertPlaceholder(c.output.Text, c.output.CursorColumn, token)
			c.output.SetText(text) // OnChanged re-resolves the preview
			c.output.CursorRow, c.output.CursorColumn = 0, cursor
			showResult()
		})
		insert.Importance = widget.LowImportance
		copyButton := widget.NewButtonWithIcon("", theme.ContentCopyIcon(), func() {
			fyne.CurrentApp().Clipboard().SetContent(token)
		})
		copyButton.Importance = widget.LowImportance
		name := widget.NewLabelWithStyle(token, fyne.TextAlignLeading, keyStyle)
		valueLabel := widget.NewLabel(value)
		valueLabel.Wrapping = fyne.TextWrapBreak
		row := container.NewBorder(nil, nil,
			container.NewGridWrap(fyne.NewSize(keyWidth, name.MinSize().Height), name),
			container.NewHBox(insert, copyButton),
			compact(container.NewVBox(valueLabel, mutedText(templates.Placeholders[key], true))),
		)
		rows[key] = row
		list.Add(row)
		list.Add(widget.NewSeparator())
	}

	filter := widget.NewEntry()
	filter.SetPlaceHolder("Filter placeholders")
	filter.OnChanged = func(text string) {
		for i, key := range placeholderOrder {
			shown := placeholderMatches(key, text)
			setShown(rows[key], shown)
			setShown(list.Objects[2*i+1], shown) // the separator below it
		}
		list.Refresh()
	}

	footer := compact(container.NewVBox(
		widget.NewSeparator(),
		container.NewBorder(nil, nil, titleLabel("Template"), nil, template),
		container.NewBorder(nil, nil, titleLabel("Result"), nil, result),
	))
	content := container.NewBorder(filter, footer, nil, nil, container.NewVScroll(list))
	d := dialog.NewCustom("Output template placeholders", "Close", content, c.window)
	d.Resize(fyne.NewSize(780, 600))
	d.Show()
	c.window.Canvas().Focus(filter)
}

// outputTemplateHelpButton opens the placeholder picker.
func (c *Controller) outputTemplateHelpButton() *widget.Button {
	button := widget.NewButton("{…}", c.showOutputTemplateHelp)
	button.Importance = widget.LowImportance
	return button
}
