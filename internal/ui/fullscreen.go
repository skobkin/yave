package ui

import (
	"image/color"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/widget"
)

// The full-screen preview puts the main window itself into full-screen
// and swaps its content for the preview with an auto-hiding control
// strip, leaving the window's layout, key handlers and focus exactly as
// they were to be restored on exit. It is entered with the strip's
// full-screen button or a double-click on the preview and left with Esc,
// a double-click or the strip's exit button. Space, ←/→ (Shift: one
// second) and Ctrl+[ / Ctrl+] keep working through the same actions the
// timeline's keys use. Nothing here touches playback: toggling full-screen
// never interrupts it.

const (
	// previewMaxDecodeWidth bounds the width static previews are decoded
	// at in full-screen (a full-HD frame costs about 8 MiB decoded).
	previewMaxDecodeWidth = 1920
	// fullscreenPlaybackMaxWidth bounds the playback decode width: every
	// frame crosses a pipe and is uploaded as a texture, which stays
	// smooth at 1280px on modest hardware where 1920px may not.
	fullscreenPlaybackMaxWidth = 1280
	// fullscreenOverlayIdle is how long the control strip stays after the
	// last pointer or key activity.
	fullscreenOverlayIdle = 2 * time.Second
)

// previewDecodeWidth picks the width a full-screen preview is decoded at:
// the canvas width in physical pixels (so a hi-DPI screen gets enough
// detail), rounded down to an even number as the scaler requires, kept
// within [previewImageWidth, previewMaxDecodeWidth]. Pure and
// table-tested.
func previewDecodeWidth(size fyne.Size, scale float32) int {
	pixels := int(size.Width * scale)
	width := min(max(pixels, previewImageWidth), previewMaxDecodeWidth)
	return width &^ 1
}

// playbackDecodeWidth is previewDecodeWidth capped for live playback.
func playbackDecodeWidth(size fyne.Size, scale float32) int {
	return min(previewDecodeWidth(size, scale), fullscreenPlaybackMaxWidth)
}

// fullscreenState is the full-screen bookkeeping. UI thread only.
type fullscreenState struct {
	active bool

	content   fyne.CanvasObject // the window content to restore
	focus     fyne.Focusable    // what had keyboard focus
	typedKey  func(*fyne.KeyEvent)
	shortcuts []*desktop.CustomShortcut
	// actions are the Ctrl+<key> handlers registered on the canvas while
	// full-screen, kept so tests can drive them (the test canvas cannot
	// deliver shortcuts).
	actions map[fyne.KeyName]func()

	bar     *transportBar   // the overlay's transport bar, built once
	overlay *fyne.Container // the strip, shown on activity
	idle    *time.Timer     // hides the strip
	delay   time.Duration   // zero means fullscreenOverlayIdle; tests shorten it
}

// previewWidth is the width previews are decoded at right now.
func (c *Controller) previewWidth() int {
	if c.fs.active && c.window != nil {
		canvas := c.window.Canvas()
		return previewDecodeWidth(canvas.Size(), canvas.Scale())
	}
	return previewImageWidth
}

// playbackWidth is the playback decode width: zero (the playback
// package's default) outside full-screen.
func (c *Controller) playbackWidth() int {
	if c.fs.active && c.window != nil {
		canvas := c.window.Canvas()
		return playbackDecodeWidth(canvas.Size(), canvas.Scale())
	}
	return 0
}

// fullscreenBlockReason explains why full-screen is unavailable, or "".
func (c *Controller) fullscreenBlockReason() string {
	_, hasVideo := c.media.PrimaryVideo()
	switch {
	case c.window == nil:
		return "No window"
	case c.inputPath == "" || c.media.Duration <= 0:
		return "Open a video on the Input tab first"
	case !c.trimEnabled:
		return "Enable trimming to use the full-screen preview"
	case !hasVideo:
		return "The input has no video stream"
	}
	return ""
}

func (c *Controller) fullscreenTip() string {
	if c.fs.active {
		return "Exit full-screen (Esc)"
	}
	if reason := c.fullscreenBlockReason(); reason != "" {
		return reason
	}
	return "Full-screen preview (or double-click the preview)"
}

// toggleFullscreen is the strip's button and the preview's double-click.
func (c *Controller) toggleFullscreen() {
	if c.fs.active {
		c.exitFullscreen()
		return
	}
	c.enterFullscreen()
}

// enterFullscreen shows the preview full-screen. It remembers the window
// content, the focus and the key handler so exitFullscreen restores them.
func (c *Controller) enterFullscreen() {
	if c.fs.active || c.fullscreenBlockReason() != "" {
		return
	}
	canvas := c.window.Canvas()
	c.fs.focus = canvas.Focused()
	c.fs.content = c.window.Content()
	c.fs.typedKey = canvas.OnTypedKey()
	canvas.Unfocus() // the timeline's FocusLost clears its tracked modifier state

	if c.fs.bar == nil {
		c.fs.bar = c.newTransportBar(true)
		for _, b := range []*tipButton{c.fs.bar.prev, c.fs.bar.play, c.fs.bar.next, c.fs.bar.playSel, c.fs.bar.mute, c.fs.bar.fullscreen} {
			b.activity = c.fullscreenActivity
		}
	}
	c.fs.active = true
	c.window.SetContent(c.withTooltips(c.buildFullscreenContent()))
	c.window.SetFullScreen(true)
	canvas.SetOnTypedKey(c.fullscreenKey)
	c.fs.shortcuts = c.fs.shortcuts[:0]
	c.fs.actions = map[fyne.KeyName]func(){}
	for _, key := range []struct {
		name   fyne.KeyName
		action func()
	}{
		{fyne.KeyLeftBracket, c.trimTimeline.SetInToPlayhead},
		{fyne.KeyRightBracket, c.trimTimeline.SetOutToPlayhead},
	} {
		shortcut := &desktop.CustomShortcut{KeyName: key.name, Modifier: fyne.KeyModifierControl}
		action := key.action
		canvas.AddShortcut(shortcut, func(fyne.Shortcut) { action() })
		c.fs.shortcuts = append(c.fs.shortcuts, shortcut)
		c.fs.actions[key.name] = action
	}
	c.fullscreenActivity()
	c.updatePlaybackControls() // the new bar learns the current state
	// The shown frame was decoded for the small preview; fetch a sharper
	// one unless playback is supplying frames.
	if !c.playbackActive() {
		c.requestPreview(c.trimTimeline.Playhead())
	}
}

// exitFullscreen restores the window exactly as it was.
func (c *Controller) exitFullscreen() {
	if !c.fs.active {
		return
	}
	c.fs.active = false
	if c.fs.idle != nil {
		c.fs.idle.Stop()
		c.fs.idle = nil
	}
	canvas := c.window.Canvas()
	canvas.SetOnTypedKey(c.fs.typedKey)
	for _, shortcut := range c.fs.shortcuts {
		canvas.RemoveShortcut(shortcut)
	}
	c.fs.shortcuts = c.fs.shortcuts[:0]
	c.fs.actions = nil
	c.window.SetFullScreen(false)
	c.window.SetContent(c.fs.content)
	focus := c.fs.focus
	if focus == nil {
		focus = c.trimTimeline // keys keep working where the user left off
	}
	canvas.Focus(focus)
	c.fs.content, c.fs.focus, c.fs.typedKey = nil, nil, nil
	c.updatePlaybackControls()
	if !c.playbackActive() {
		c.requestPreview(c.trimTimeline.Playhead())
	}
}

// buildFullscreenContent assembles the full-screen view: the preview
// layers on black, a double-tap layer, and the control strip pinned to the
// bottom edge. The preview widgets are shared with the Trim tab, whose
// content is not displayed meanwhile.
func (c *Controller) buildFullscreenContent() fyne.CanvasObject {
	surface := newPreviewSurface(c.exitFullscreen, c.fullscreenActivity)
	strip := container.NewStack(
		overlayChipRectangle(),
		container.NewPadded(container.NewCenter(c.fs.bar.box)),
	)
	c.fs.overlay = container.NewVBox(layout.NewSpacer(), strip)
	return container.NewStack(
		canvas.NewRectangle(color.Black),
		c.previewLayers(surface, c.fs.overlay),
	)
}

// fullscreenKey routes the keys the Trim tab's timeline handles, through
// the same actions, plus Esc to leave.
func (c *Controller) fullscreenKey(ev *fyne.KeyEvent) {
	c.fullscreenActivity()
	switch ev.Name {
	case fyne.KeyEscape:
		c.exitFullscreen()
	case fyne.KeySpace:
		c.togglePlayback()
	case fyne.KeyLeft, fyne.KeyRight:
		dir := 1
		if ev.Name == fyne.KeyLeft {
			dir = -1
		}
		c.trimTimeline.StepBy(dir, c.trimTimeline.keyModifiers()&fyne.KeyModifierShift != 0)
	}
}

// fullscreenActivity shows the control strip and restarts its idle timer.
func (c *Controller) fullscreenActivity() {
	if !c.fs.active || c.fs.overlay == nil {
		return
	}
	c.fs.overlay.Show()
	if c.fs.idle != nil {
		c.fs.idle.Stop()
	}
	delay := c.fs.delay
	if delay <= 0 {
		delay = fullscreenOverlayIdle
	}
	gen := c.inputGeneration
	c.fs.idle = time.AfterFunc(delay, func() {
		c.dispatchUI(func() {
			if c.fs.active && c.inputGeneration == gen && c.fs.overlay != nil {
				c.fs.overlay.Hide()
			}
		})
	})
}

// previewSurface is a transparent layer over the preview that turns a
// double-click into the full-screen toggle and reports pointer movement
// (for the overlay's idle timer). It draws nothing and has no minimum
// size, so it can sit on top of the preview's other layers.
type previewSurface struct {
	widget.BaseWidget
	onDoubleTap func()
	onActivity  func()
}

func newPreviewSurface(onDoubleTap, onActivity func()) *previewSurface {
	s := &previewSurface{onDoubleTap: onDoubleTap, onActivity: onActivity}
	s.ExtendBaseWidget(s)
	return s
}

var (
	_ fyne.DoubleTappable = (*previewSurface)(nil)
	_ fyne.Tappable       = (*previewSurface)(nil)
	_ desktop.Hoverable   = (*previewSurface)(nil)
)

func (s *previewSurface) CreateRenderer() fyne.WidgetRenderer {
	return widget.NewSimpleRenderer(canvas.NewRectangle(color.Transparent))
}

// Tapped is a no-op: the layer exists for the double-click, and some
// drivers only route double-taps to objects that are also tappable.
func (s *previewSurface) Tapped(*fyne.PointEvent) {}

func (s *previewSurface) DoubleTapped(*fyne.PointEvent) {
	if s.onDoubleTap != nil {
		s.onDoubleTap()
	}
}

func (s *previewSurface) MouseIn(*desktop.MouseEvent) {
	if s.onActivity != nil {
		s.onActivity()
	}
}

func (s *previewSurface) MouseMoved(*desktop.MouseEvent) {
	if s.onActivity != nil {
		s.onActivity()
	}
}

func (s *previewSurface) MouseOut() {}
