package ui

import (
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

// Fyne has no tooltips, and a widget.PopUp overlay would steal hover
// events from the widgets underneath (the button the pointer rests on
// would see MouseOut the moment the bubble appears). The bubble is
// therefore drawn on a non-interactive layer stacked over the window
// content: a plain container implements no pointer interfaces, so every
// event passes through to the widget below.

const (
	tooltipDelay  = 500 * time.Millisecond
	tooltipGap    = 4 // between the anchor and the bubble
	tooltipMargin = 4 // minimum distance to the window edge
)

// tooltipLayer draws one tooltip bubble at a time.
type tooltipLayer struct {
	layer  *fyne.Container
	bubble *fyne.Container
	label  *widget.Label

	timer *time.Timer
	seq   uint64 // retires timers that fire after a cancel

	// dispatch runs a function on the UI thread; nil uses fyne.Do.
	dispatch func(func())
}

func newTooltipLayer() *tooltipLayer {
	l := &tooltipLayer{label: widget.NewLabel("")}
	background := canvas.NewRectangle(theme.Color(theme.ColorNameOverlayBackground))
	background.StrokeColor = theme.Color(theme.ColorNameSeparator)
	background.StrokeWidth = 1
	background.CornerRadius = theme.InputRadiusSize()
	l.bubble = container.NewStack(background, container.NewPadded(l.label))
	l.bubble.Hide()
	l.layer = container.NewWithoutLayout(l.bubble)
	return l
}

// wrap stacks the layer over content.
func (l *tooltipLayer) wrap(content fyne.CanvasObject) fyne.CanvasObject {
	return container.NewStack(content, l.layer)
}

// schedule shows text(anchor) beside anchor once the pointer has rested
// for the delay. An empty text shows nothing. A later schedule or cancel
// retires an earlier one.
func (l *tooltipLayer) schedule(anchor fyne.CanvasObject, text func() string, delay time.Duration) {
	l.cancel()
	seq := l.seq
	dispatch := l.dispatch
	if dispatch == nil {
		dispatch = fyne.Do
	}
	l.timer = time.AfterFunc(delay, func() {
		dispatch(func() {
			if l.seq == seq {
				l.show(anchor, text())
			}
		})
	})
}

// cancel retires a pending tooltip and hides a visible one.
func (l *tooltipLayer) cancel() {
	l.seq++
	if l.timer != nil {
		l.timer.Stop()
		l.timer = nil
	}
	if !l.bubble.Hidden {
		l.bubble.Hide()
	}
}

func (l *tooltipLayer) show(anchor fyne.CanvasObject, text string) {
	if text == "" || !anchor.Visible() {
		return
	}
	app := fyne.CurrentApp()
	if app == nil {
		return
	}
	driver := app.Driver()
	c := driver.CanvasForObject(l.layer)
	if c == nil {
		return
	}
	l.label.SetText(text)
	size := l.bubble.MinSize()
	l.bubble.Resize(size)
	origin := driver.AbsolutePositionForObject(l.layer)
	pos := tooltipPlacement(
		driver.AbsolutePositionForObject(anchor).Subtract(origin), anchor.Size(), size, l.layer.Size())
	l.bubble.Move(pos)
	l.bubble.Show()
}

// tooltipPlacement positions a bubble below its anchor, left-aligned with
// it, flipping above when there is no room below and shifting left or
// right to stay inside bounds.
func tooltipPlacement(anchorPos fyne.Position, anchorSize, bubble, bounds fyne.Size) fyne.Position {
	pos := fyne.NewPos(anchorPos.X, anchorPos.Y+anchorSize.Height+tooltipGap)
	if pos.Y+bubble.Height > bounds.Height-tooltipMargin {
		pos.Y = anchorPos.Y - bubble.Height - tooltipGap
	}
	pos.X = min(pos.X, bounds.Width-bubble.Width-tooltipMargin)
	pos.X = max(pos.X, tooltipMargin)
	pos.Y = max(pos.Y, tooltipMargin)
	return pos
}

// tipButton is a button with a hover tooltip. The tip is a function so it
// can explain the current state (for example why the button is disabled);
// disabled buttons still show it, because the desktop driver delivers
// hover events regardless of the disabled state.
type tipButton struct {
	widget.Button
	tips *tooltipLayer
	tip  func() string
	// activity, when set, is called on every pointer movement over the
	// button; the full-screen overlay uses it to stay visible while the
	// pointer is on its controls.
	activity func()
}

// newTipButton builds an icon button with a fixed tooltip.
func newTipButton(tips *tooltipLayer, icon fyne.Resource, tip string, tapped func()) *tipButton {
	return newTipButtonFunc(tips, icon, func() string { return tip }, tapped)
}

// newTipButtonFunc builds an icon button whose tooltip is computed when
// it is about to show.
func newTipButtonFunc(tips *tooltipLayer, icon fyne.Resource, tip func() string, tapped func()) *tipButton {
	b := &tipButton{tips: tips, tip: tip}
	b.Icon = icon
	b.OnTapped = tapped
	b.Importance = widget.LowImportance
	b.ExtendBaseWidget(b)
	return b
}

// Compile-time checks: the embedded Button's hover and tap handlers are
// overridden below, and the controller disables buttons through
// fyne.Disableable.
var (
	_ desktop.Hoverable = (*tipButton)(nil)
	_ fyne.Tappable     = (*tipButton)(nil)
	_ fyne.Disableable  = (*tipButton)(nil)
)

// MouseMoved reports pointer activity.
func (b *tipButton) MouseMoved(e *desktop.MouseEvent) {
	b.Button.MouseMoved(e)
	if b.activity != nil {
		b.activity()
	}
}

// MouseIn starts the tooltip delay.
func (b *tipButton) MouseIn(e *desktop.MouseEvent) {
	b.Button.MouseIn(e)
	if b.activity != nil {
		b.activity()
	}
	if b.tips != nil && b.tip != nil {
		b.tips.schedule(b, b.tip, tooltipDelay)
	}
}

// MouseOut hides the tooltip.
func (b *tipButton) MouseOut() {
	b.Button.MouseOut()
	if b.tips != nil {
		b.tips.cancel()
	}
}

// Tapped hides the tooltip before running the action.
func (b *tipButton) Tapped(e *fyne.PointEvent) {
	if b.tips != nil {
		b.tips.cancel()
	}
	b.Button.Tapped(e)
}

// withTooltips stacks the controller's tooltip layer over window content.
func (c *Controller) withTooltips(content fyne.CanvasObject) fyne.CanvasObject {
	return c.tips.wrap(content)
}
