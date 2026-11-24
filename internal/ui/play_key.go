package ui

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/widget"
)

// Space plays and pauses on the Trim tab wherever the keyboard focus is,
// except in a text field. Fyne hands every key to the focused widget, and
// to the canvas only when nothing is focused, so each case is covered
// separately:
//
//   - nothing focused (after clicking a button, the preview or empty
//     space): windowKey, the canvas key handler;
//   - the timeline: its own TypedKey;
//   - a check box: a click no longer leaves it focused (clickCheck), so
//     Space cannot toggle "Enable trimming" or "Snap" by surprise. Focused
//     with Tab it still toggles on Space, as keyboard users expect;
//   - a dropdown or a slider: Space plays (playKeySelect, playKeySlider).
//     A dropdown still opens with Up/Down; a slider ignores Space anyway.

// trimTabShown reports whether the Trim tab is the one on screen.
func (c *Controller) trimTabShown() bool {
	return c.tabs != nil && c.trimTab != nil && c.tabs.Selected() == c.trimTab
}

// playFromKey is Space outside the timeline: it toggles playback while
// the Trim tab or the full-screen preview is showing and no dialog is open.
func (c *Controller) playFromKey() {
	if c.modalOpen() {
		return
	}
	switch {
	case c.fs.active:
		c.fullscreenActivity()
	case !c.trimTabShown():
		return
	}
	c.togglePlayback()
}

// windowKey receives the keys no focused widget took.
func (c *Controller) windowKey(ev *fyne.KeyEvent) {
	if ev.Name == fyne.KeySpace {
		c.playFromKey()
	}
}

// clickCheck is a check box that gives the keyboard focus up after a
// click, like a button: a focused check box toggles on Space.
type clickCheck struct {
	widget.Check
}

func newClickCheck(label string, changed func(bool)) *clickCheck {
	c := &clickCheck{}
	c.Text, c.OnChanged = label, changed
	c.ExtendBaseWidget(c)
	return c
}

// Tapped toggles the check, then drops the focus the tap gave it.
func (c *clickCheck) Tapped(e *fyne.PointEvent) {
	c.Check.Tapped(e)
	if app := fyne.CurrentApp(); app != nil {
		if canvas := app.Driver().CanvasForObject(c); canvas != nil && canvas.Focused() == fyne.Focusable(c) {
			canvas.Unfocus()
		}
	}
}

// playKeySelect is a dropdown that hands Space to onSpace instead of
// opening its list.
type playKeySelect struct {
	widget.Select
	onSpace func()
}

func newPlayKeySelect(options []string, onSpace func()) *playKeySelect {
	s := &playKeySelect{onSpace: onSpace}
	s.Options = options
	s.ExtendBaseWidget(s)
	fitSelectToOptions(&s.Select)
	return s
}

func (s *playKeySelect) TypedKey(ev *fyne.KeyEvent) {
	if ev.Name == fyne.KeySpace && s.onSpace != nil {
		s.onSpace()
		return
	}
	s.Select.TypedKey(ev)
}

// playKeySlider is a slider that hands Space to onSpace.
type playKeySlider struct {
	widget.Slider
	onSpace func()
}

func newPlayKeySlider(minimum, maximum float64, onSpace func()) *playKeySlider {
	s := &playKeySlider{onSpace: onSpace}
	s.Min, s.Max, s.Step, s.Orientation = minimum, maximum, 1, widget.Horizontal
	s.ExtendBaseWidget(s)
	return s
}

func (s *playKeySlider) TypedKey(ev *fyne.KeyEvent) {
	if ev.Name == fyne.KeySpace && s.onSpace != nil {
		s.onSpace()
		return
	}
	s.Slider.TypedKey(ev)
}
