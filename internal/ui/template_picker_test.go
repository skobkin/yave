package ui

import (
	"testing"

	"fyne.io/fyne/v2"
	fynetest "fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"

	"git.skobk.in/skobkin/yave/internal/templates"
)

func TestPlaceholderOrderListsEveryPlaceholderOnce(t *testing.T) {
	seen := map[string]bool{}
	for _, key := range placeholderOrder {
		if _, ok := templates.Placeholders[key]; !ok {
			t.Errorf("picker lists unknown placeholder %q", key)
		}
		if seen[key] {
			t.Errorf("picker lists %q twice", key)
		}
		seen[key] = true
	}
	for key := range templates.Placeholders {
		if !seen[key] {
			t.Errorf("picker misses placeholder %q", key)
		}
	}
}

func TestInsertPlaceholder(t *testing.T) {
	for _, tt := range []struct {
		name, template string
		cursor         int
		want           string
		wantCursor     int
	}{
		{"at the cursor", "{dir}/{name}.{ext}", 12, "{dir}/{name}{codec}.{ext}", 19},
		{"never-clicked field goes before the extension", "{dir}/{name}.{ext}", 0, "{dir}/{name}{codec}.{ext}", 19},
		{"never-clicked field without extension appends", "{dir}/{name}", 0, "{dir}/{name}{codec}", 19},
		{"empty template", "", 0, "{codec}", 7},
		{"cursor past the end clamps", "ab", 99, "ab{codec}", 9},
		{"non-ASCII text counts runes", "фильм_", 6, "фильм_{codec}", 13},
	} {
		got, cursor := insertPlaceholder(tt.template, tt.cursor, "{codec}")
		if got != tt.want || cursor != tt.wantCursor {
			t.Errorf("%s: got %q cursor %d, want %q cursor %d", tt.name, got, cursor, tt.want, tt.wantCursor)
		}
	}
}

func TestPlaceholderMatches(t *testing.T) {
	if !placeholderMatches("video_settings", "") || !placeholderMatches("video_settings", " VIDEO ") {
		t.Fatal("empty filter and key matches must pass, ignoring case and spaces")
	}
	if !placeholderMatches("name", "filename") {
		t.Fatal("the description must match too")
	}
	if placeholderMatches("date", "encoder") {
		t.Fatal("an unrelated placeholder must be filtered out")
	}
}

func TestTemplatePickerInsertsIntoTheTemplate(t *testing.T) {
	if placeholderOrder[0] != "dir" {
		t.Fatal("the test assumes {dir} is the first row")
	}
	c := dirtyTestController(t)
	c.window.SetContent(c.buildLayout())
	c.output.SetText("{dir}/{name}.{ext}")
	c.showOutputTemplateHelp()
	overlay := c.window.Canvas().Overlays().Top()
	if overlay == nil {
		t.Fatal("the picker must open as a dialog")
	}
	insert := findButtonDeep(t, overlay, "Insert") // the first row: {dir}
	if insert == nil {
		t.Fatal("no Insert button in the picker")
	}
	insert.OnTapped()
	if want := "{dir}/{name}{dir}.{ext}"; c.output.Text != want {
		t.Fatalf("template = %q, want %q", c.output.Text, want)
	}
}

// findButtonDeep finds a button by label anywhere below object, walking
// into widgets (pop-ups, dialogs, scrolls) through their renderers.
func findButtonDeep(t *testing.T, object fyne.CanvasObject, label string) *widget.Button {
	t.Helper()
	switch o := object.(type) {
	case *widget.Button:
		if o.Text == label {
			return o
		}
		return nil
	case *fyne.Container:
		for _, child := range o.Objects {
			if b := findButtonDeep(t, child, label); b != nil {
				return b
			}
		}
	case fyne.Widget:
		for _, child := range fynetest.TempWidgetRenderer(t, o).Objects() {
			if b := findButtonDeep(t, child, label); b != nil {
				return b
			}
		}
	}
	return nil
}
