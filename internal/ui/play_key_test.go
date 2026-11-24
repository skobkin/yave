package ui

import (
	"testing"

	"fyne.io/fyne/v2"
	fynetest "fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

func TestSpaceWithoutFocusTogglesPlaybackOnTheTrimTab(t *testing.T) {
	h := newFullscreenHarness(t)
	c := h.c
	c.window.Canvas().Unfocus()
	space := &fyne.KeyEvent{Name: fyne.KeySpace}

	c.windowKey(space)
	if !c.playbackActive() {
		t.Fatal("Space with nothing focused must start playback on the Trim tab")
	}
	c.windowKey(space)
	if c.playbackActive() {
		t.Fatal("Space must pause running playback")
	}

	c.tabs.SelectIndex(0)
	c.windowKey(space)
	if c.playbackActive() {
		t.Fatal("Space must not play while another tab is shown")
	}

	c.tabs.SelectIndex(1)
	c.window.Canvas().Overlays().Add(widget.NewLabel("modal"))
	c.windowKey(space)
	if c.playbackActive() {
		t.Fatal("Space must not play while a dialog is open")
	}
}

func TestRegisterShortcutsInstallsTheWindowKeyHandler(t *testing.T) {
	c := shortcutController(t)
	if c.window.Canvas().OnTypedKey() == nil {
		t.Fatal("registerShortcuts must route unfocused keys (Space) to the controller")
	}
}

func TestTrimControlsHandSpaceToPlayback(t *testing.T) {
	h := newFullscreenHarness(t)
	c := h.c
	space := &fyne.KeyEvent{Name: fyne.KeySpace}

	c.trimExport.TypedKey(space)
	if !c.playbackActive() {
		t.Fatal("Space on the export dropdown must start playback")
	}
	c.transport.volume.TypedKey(space)
	if c.playbackActive() {
		t.Fatal("Space on the volume slider must pause playback")
	}

	// Other keys keep their usual meaning.
	before := c.transport.volume.Value
	c.transport.volume.SetValue(50)
	c.transport.volume.TypedKey(&fyne.KeyEvent{Name: fyne.KeyRight})
	if c.transport.volume.Value != 51 {
		t.Errorf("Right on the volume slider = %v, want 51 (was %v)", c.transport.volume.Value, before)
	}
	c.trimExport.TypedKey(&fyne.KeyEvent{Name: fyne.KeyRight})
	if c.trimExport.Selected != c.trimExport.Options[1] {
		t.Errorf("Right on the export dropdown selected %q, want the next option", c.trimExport.Selected)
	}
}

func TestClickCheckGivesUpFocusAfterAClick(t *testing.T) {
	var toggled bool
	check := newClickCheck("Snap", func(on bool) { toggled = on })
	w := fynetest.NewWindow(check)
	defer w.Close()

	fynetest.Tap(check)
	if !toggled {
		t.Fatal("a click must still toggle the check")
	}
	if focused := w.Canvas().Focused(); focused != nil {
		t.Fatalf("a clicked check box kept the focus (%T): Space would toggle it", focused)
	}

	// Focused from the keyboard it keeps working with Space.
	w.Canvas().Focus(check)
	check.TypedRune(' ') // a Check toggles on the typed space character
	if toggled {
		t.Fatal("Space on a keyboard-focused check must still toggle it")
	}
}

func TestFitSelectToOptionsUsesTheWidestOption(t *testing.T) {
	s := widget.NewSelect([]string{"Join", "Separate files", "Mid"}, nil)
	fynetest.NewTempWindow(t, s)
	narrow := s.MinSize().Width
	fitSelectToOptions(s)
	if s.PlaceHolder != "Separate files" {
		t.Fatalf("placeholder = %q, want the widest option", s.PlaceHolder)
	}
	s.Refresh()
	text := fyne.MeasureText("Separate files", theme.TextSize(), fyne.TextStyle{}).Width
	if got := s.MinSize().Width; got <= narrow || got < text {
		t.Fatalf("min width %v must grow from %v to fit the %v-wide option", got, narrow, text)
	}
}
