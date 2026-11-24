package ui

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/driver/desktop"
)

// shortcutHelp lists the window-level shortcuts (shown in About).
const shortcutHelp = "Ctrl+O open input · Ctrl+Enter start · Ctrl+S save profile · Ctrl+1…6 switch tabs · Space play/pause on the Trim tab"

// modalOpen reports whether a dialog or popup is on top of the window.
func (c *Controller) modalOpen() bool {
	return c.window != nil && c.window.Canvas().Overlays().Top() != nil
}

// registerShortcuts installs the window-level shortcuts. Fyne gives a
// focused text entry (or the timeline) first refusal on every key
// combination, so these never steal keys from text editing or the
// timeline's Ctrl+[ / Ctrl+] handling; click outside a text field to use
// them.
func (c *Controller) registerShortcuts() {
	c.shortcutHandlers = map[fyne.KeyName]func(){}
	add := func(key fyne.KeyName, action func()) {
		handler := func() { c.runShortcut(action) }
		c.shortcutHandlers[key] = handler
		c.window.Canvas().AddShortcut(
			&desktop.CustomShortcut{KeyName: key, Modifier: fyne.KeyModifierControl},
			func(fyne.Shortcut) { handler() },
		)
	}
	add(fyne.KeyO, c.chooseInput)
	add(fyne.KeyReturn, c.startFromShortcut)
	add(fyne.KeyEnter, c.startFromShortcut)
	add(fyne.KeyS, c.saveCurrentProfile)
	for i, key := range []fyne.KeyName{fyne.Key1, fyne.Key2, fyne.Key3, fyne.Key4, fyne.Key5, fyne.Key6} {
		add(key, func() { c.selectTabIndex(i) })
	}
	// Space plays and pauses on the Trim tab when no widget has the focus
	// (see play_key.go). The full-screen preview swaps in its own handler
	// and restores this one on exit.
	c.window.Canvas().SetOnTypedKey(c.windowKey)
}

// runShortcut runs action unless a modal is open.
func (c *Controller) runShortcut(action func()) {
	if c.modalOpen() {
		return
	}
	action()
}

// startFromShortcut behaves exactly like pressing Start: nothing happens
// while the button is disabled (no input, running, invalid field…).
func (c *Controller) startFromShortcut() {
	if c.start == nil || c.start.Disabled() {
		return
	}
	c.startConversion()
}

// selectTabIndex switches to the tab at index, ignoring out-of-range ones.
func (c *Controller) selectTabIndex(index int) {
	if c.tabs == nil || index < 0 || index >= len(c.tabs.Items) {
		return
	}
	c.tabs.SelectIndex(index)
}
