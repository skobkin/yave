package ui

import (
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/widget"
)

func shortcutController(t *testing.T) *Controller {
	t.Helper()
	c := dirtyTestController(t)
	c.window.SetContent(c.buildLayout())
	c.registerShortcuts()
	return c
}

// fire runs the Ctrl+<key> handler registered on the canvas.
func fire(c *Controller, key fyne.KeyName) {
	handler, ok := c.shortcutHandlers[key]
	if !ok {
		panic("no shortcut registered for " + string(key))
	}
	handler()
}

func TestShortcutsSwitchTabs(t *testing.T) {
	c := shortcutController(t)
	for i, key := range []fyne.KeyName{fyne.Key1, fyne.Key2, fyne.Key3, fyne.Key4, fyne.Key5} {
		fire(c, key)
		if got := c.tabs.SelectedIndex(); got != i {
			t.Fatalf("Ctrl+%d selected tab %d, want %d", i+1, got, i)
		}
	}
}

func TestShortcutSaveProfile(t *testing.T) {
	c := shortcutController(t)
	c.bitrate.SetText("4M")
	fire(c, fyne.KeyS)
	if c.profileDirty() || c.config.Profiles[0].Video.Bitrate != "4M" {
		t.Fatal("Ctrl+S must save the current profile")
	}
}

func TestShortcutsIgnoredWhileModalOpen(t *testing.T) {
	c := shortcutController(t)
	c.window.Canvas().Overlays().Add(widget.NewLabel("modal"))
	fire(c, fyne.Key3)
	if c.tabs.SelectedIndex() != 0 {
		t.Fatal("no shortcut may act while a dialog or popup is open")
	}
}

func TestStartShortcutRespectsDisabledStart(t *testing.T) {
	c := shortcutController(t)
	c.start.Disable()
	c.status.SetText("Idle")
	fire(c, fyne.KeyReturn)
	if c.status.Text != "Idle" {
		t.Fatal("Ctrl+Enter must do nothing while Start is disabled")
	}
	// With Start enabled it runs the Start path, which validates first:
	// an empty input path is refused with the validation status.
	c.inputPath = ""
	c.start.Enable()
	fire(c, fyne.KeyReturn)
	if c.status.Text != "Validation failed" {
		t.Fatalf("status = %q, want the Start-button validation path", c.status.Text)
	}
}
