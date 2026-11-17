package ui

import (
	"image"
	"image/color"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

// tlDrag identifies which part of the timeline a pointer gesture is
// operating on. The target is acquired on MouseDown (desktop.Mouseable)
// so the whole gesture is unambiguous, including when handle hit zones
// overlap near the timeline edges.
type tlDrag uint8

const (
	tlNone tlDrag = iota
	tlPlayhead
	tlIn
	tlOut
)

const (
	timelineMinWidth   = 240
	timelineMinHeight  = 72 // 1.5x the original 48: the strip is the tab's main working surface
	handleHitHalfWidth = 6  // handle hit zones are full-height and 12px wide
	handleBarWidth     = 4  // visible bars are narrower than their hit zones
	playheadBarWidth   = 2
)

// timeline is the trim tab's scrubber: a thumbnail strip with a
// playhead and draggable In/Out boundary handles. It is the app's only
// custom-rendered widget and implements the desktop extensions
// (Keyable, Mouseable, Hoverable) directly:
//
//   - Keyboard handling is strictly local. Keys reach only the focused
//     widget, so arrows keep working in Entry widgets and the
//     Ctrl+[ / Ctrl+] boundary shortcuts cannot modify trim state while
//     the user edits text or works in another tab. fyne.KeyEvent does
//     not carry modifiers, so the widget tracks Shift/Ctrl itself via
//     the modifier keys' own KeyDown/KeyUp events (desktop.Keyable) and
//     clears the tracked state on FocusLost so it can never go stale
//     across tab switches or application focus changes.
//   - Hit testing happens on MouseDown, not on the first Dragged event,
//     giving reliable drag-target acquisition and focus handling.
//   - Hover reports the hovered timestamp immediately without ever
//     moving the committed playhead; the controller turns it into a
//     debounced hover preview.
type timeline struct {
	widget.BaseWidget

	duration time.Duration
	playhead time.Duration
	in, out  time.Duration
	thumbs   []image.Image

	focused   bool
	enabled   bool
	shiftHeld bool
	ctrlHeld  bool
	drag      tlDrag
	hoverX    float32 // -1 when the pointer is outside the widget

	// stepFrame resolves one frame-accurate step; it returns the new
	// position and false while the frame index is loading or stepping
	// is unavailable (the controller surfaces that in its status label).
	stepFrame func(at time.Duration, dir int) (time.Duration, bool)
	// onPlayhead fires when the committed playhead moves (click, drag,
	// keyboard). It does NOT fire for hover positions.
	onPlayhead func(time.Duration)
	// onRange fires when an In/Out handle drag commits (MouseUp /
	// DragEnd) and when the boundaries are set programmatically via
	// SetRange.
	onRange func(in, out time.Duration)
	// onHover fires with the hovered position, or nil when the pointer
	// leaves the widget.
	onHover func(*time.Duration)
	// onSetIn/onSetOut fire for the Ctrl+[ / Ctrl+] shortcuts so the
	// controller applies a single-boundary update (own entry and
	// validity flag only). Nil falls back to the widget's loud SetRange.
	onSetIn  func()
	onSetOut func()
}

func newTimeline() *timeline {
	t := &timeline{enabled: true, hoverX: -1}
	t.ExtendBaseWidget(t)
	return t
}

// Compile-time interface checks: the controller's trimControls loops
// disable widgets through fyne.Disableable assertions, and the desktop
// driver routes ctrl chords through fyne.Shortcutable — the timeline
// used to miss both (Enabled() instead of Disabled(), no TypedShortcut)
// and stayed interactive with dead Ctrl+[ / Ctrl+] keys.
var (
	_ fyne.Disableable  = (*timeline)(nil)
	_ fyne.Shortcutable = (*timeline)(nil)
)

// SetDuration replaces the media duration and clamps the existing
// positions into the new range. Unlike SetRange it does not fire
// onRange: a media change (new input, reset) is controller-driven, and
// re-entering the controller would resurrect state it just cleared.
func (t *timeline) SetDuration(d time.Duration) {
	if d < 0 {
		d = 0
	}
	t.duration = d
	t.SetPlayhead(t.playhead)
	t.in = t.clamp(t.in)
	t.out = t.clamp(t.out)
	t.Refresh()
}

// Duration returns the media duration the timeline shows.
func (t *timeline) Duration() time.Duration { return t.duration }

// SetRange replaces the selection boundaries, clamped into
// [0, duration]. A crossed range (in > out) is displayed as-is so the
// bars show the honest positions — validation reports it; only pointer
// drags are prevented from crossing (they clamp in MouseDown/Dragged).
// onRange fires so callers stay in sync with the clamped values.
func (t *timeline) SetRange(in, out time.Duration) {
	t.in = t.clamp(in)
	t.out = t.clamp(out)
	t.Refresh()
	if t.onRange != nil {
		t.onRange(t.in, t.out)
	}
}

// Range returns the current selection boundaries.
func (t *timeline) Range() (time.Duration, time.Duration) { return t.in, t.out }

// SetRangeSilent updates the displayed boundaries without firing
// onRange. Boundary-entry edits use it: the typed value stays
// authoritative in the controller — validation must see an out-of-range
// entry as an error, not have it silently clamp to whatever the
// timeline displays — while the widget still shows the clamped view.
// Pointer drags and the boundary shortcuts keep using SetRange, whose
// callback commits the clamped coordinates as the selection.
func (t *timeline) SetRangeSilent(in, out time.Duration) {
	t.in = t.clamp(in)
	t.out = t.clamp(out)
	t.Refresh()
}

// SetPlayhead moves the committed playhead, clamped to [0, duration].
// onPlayhead is not fired for programmatic moves.
func (t *timeline) SetPlayhead(at time.Duration) {
	t.playhead = t.clamp(at)
	t.Refresh()
}

// Playhead returns the committed playhead position.
func (t *timeline) Playhead() time.Duration { return t.playhead }

// SetThumbnails installs the strip's placeholder slots.
func (t *timeline) SetThumbnails(thumbs []image.Image) {
	t.thumbs = thumbs
	t.Refresh()
}

// SetThumbnail fills one strip slot as its thumbnail arrives; the
// strip renders progressively.
func (t *timeline) SetThumbnail(index int, img image.Image) {
	if index < 0 {
		return
	}
	for len(t.thumbs) <= index {
		t.thumbs = append(t.thumbs, nil)
	}
	t.thumbs[index] = img
	t.Refresh()
}

// --- fyne.Disableable ---

// Enable turns keyboard and pointer handling back on.
func (t *timeline) Enable() {
	t.enabled = true
	t.Refresh()
}

// Disable ignores all pointer and keyboard input while enabled is
// false.
func (t *timeline) Disable() {
	t.enabled = false
	t.drag = tlNone
	t.Refresh()
}

// Disabled reports whether the timeline ignores input. The method name
// is dictated by fyne.Disableable: with Enabled() here instead, the
// controller's type assertions failed and the timeline stayed
// interactive while trimming was unchecked.
func (t *timeline) Disabled() bool { return !t.enabled }

// --- fyne.Focusable / desktop.Keyable ---

// FocusGained shows the focus ring; no canvas state changes happen
// here (Fyne forbids that inside focus hooks).
func (t *timeline) FocusGained() {
	t.focused = true
	t.Refresh()
}

// FocusLost hides the focus ring and clears modifier and drag state so
// it cannot go stale when focus moves to an Entry, another tab, or
// another application.
func (t *timeline) FocusLost() {
	t.focused = false
	t.shiftHeld = false
	t.ctrlHeld = false
	t.drag = tlNone
	t.Refresh()
}

// KeyDown tracks modifier state; fyne.KeyEvent carries no modifiers,
// so the widget derives Shift/Ctrl from the modifier keys' own events.
func (t *timeline) KeyDown(ev *fyne.KeyEvent) {
	switch ev.Name {
	case desktop.KeyShiftLeft, desktop.KeyShiftRight:
		t.shiftHeld = true
	case desktop.KeyControlLeft, desktop.KeyControlRight:
		t.ctrlHeld = true
	}
}

// KeyUp clears modifier state as the keys are released.
func (t *timeline) KeyUp(ev *fyne.KeyEvent) {
	switch ev.Name {
	case desktop.KeyShiftLeft, desktop.KeyShiftRight:
		t.shiftHeld = false
	case desktop.KeyControlLeft, desktop.KeyControlRight:
		t.ctrlHeld = false
	}
}

// TypedRune is required by fyne.Focusable; the timeline consumes no
// text input.
func (t *timeline) TypedRune(rune) {}

// TypedShortcut receives the desktop driver's modifier chords. The glfw
// driver turns any ctrl+key press into a desktop.CustomShortcut and
// delivers it to a Shortcutable focused widget — returning before
// TypedKey is ever called — so the Ctrl+[ / Ctrl+] boundary shortcuts
// must be handled here; the TypedKey branches below only serve direct
// callers (tests, other backends).
func (t *timeline) TypedShortcut(s fyne.Shortcut) {
	if !t.enabled || t.duration <= 0 {
		return
	}
	custom, ok := s.(*desktop.CustomShortcut)
	if !ok || custom.Modifier&fyne.KeyModifierControl == 0 {
		return
	}
	switch custom.KeyName {
	case fyne.KeyLeftBracket:
		t.setInToPlayhead()
	case fyne.KeyRightBracket:
		t.setOutToPlayhead()
	}
}

// TypedKey handles the navigation keys, including auto-repeat.
func (t *timeline) TypedKey(ev *fyne.KeyEvent) {
	if !t.enabled || t.duration <= 0 {
		return
	}
	switch ev.Name {
	case fyne.KeyLeft:
		t.step(-1)
	case fyne.KeyRight:
		t.step(1)
	case fyne.KeyLeftBracket:
		if t.ctrlHeld {
			t.setInToPlayhead()
		}
	case fyne.KeyRightBracket:
		if t.ctrlHeld {
			t.setOutToPlayhead()
		}
	}
}

func (t *timeline) step(dir int) {
	if t.shiftHeld {
		t.movePlayhead(t.playhead + time.Duration(dir)*time.Second)
		return
	}
	if t.stepFrame == nil {
		return
	}
	if next, ok := t.stepFrame(t.playhead, dir); ok {
		t.movePlayhead(next)
	}
}

func (t *timeline) setInToPlayhead() {
	if t.playhead > t.out {
		return
	}
	// The controller's single-boundary handler keeps the opposite entry
	// authoritative (its validity flag and in-progress text stay
	// untouched); a loud SetRange would mark both entries valid and
	// rewrite the other one from the stale stored value.
	if t.onSetIn != nil {
		t.onSetIn()
		return
	}
	t.SetRange(t.playhead, t.out)
}

func (t *timeline) setOutToPlayhead() {
	if t.playhead < t.in {
		return
	}
	if t.onSetOut != nil {
		t.onSetOut()
		return
	}
	t.SetRange(t.in, t.playhead)
}

// movePlayhead commits a keyboard-driven playhead move.
func (t *timeline) movePlayhead(at time.Duration) {
	t.SetPlayhead(at)
	if t.onPlayhead != nil {
		t.onPlayhead(t.playhead)
	}
}

// --- desktop.Mouseable + fyne.Draggable ---

// MouseDown acquires focus and the drag target for this gesture.
func (t *timeline) MouseDown(ev *desktop.MouseEvent) {
	if !t.enabled || ev.Button != desktop.MouseButtonPrimary || t.duration <= 0 {
		return
	}
	if c := fyne.CurrentApp().Driver().CanvasForObject(t); c != nil {
		c.Focus(t)
	}
	switch t.pickTarget(ev.Position.X) {
	case tlIn:
		t.drag = tlIn
		t.in = t.clamp(t.timeAt(ev.Position.X))
		if t.in > t.out {
			t.out = t.in
		}
	case tlOut:
		t.drag = tlOut
		t.out = t.clamp(t.timeAt(ev.Position.X))
		if t.out < t.in {
			t.in = t.out
		}
	default:
		t.drag = tlPlayhead
		t.SetPlayhead(t.timeAt(ev.Position.X))
		if t.onPlayhead != nil {
			t.onPlayhead(t.playhead)
		}
	}
	t.Refresh()
}

// MouseUp commits handle drags.
func (t *timeline) MouseUp(ev *desktop.MouseEvent) {
	t.release()
}

// Dragged moves the target acquired in MouseDown.
func (t *timeline) Dragged(ev *fyne.DragEvent) {
	if !t.enabled || t.drag == tlNone || t.duration <= 0 {
		return
	}
	at := t.clamp(t.timeAt(ev.Position.X))
	switch t.drag {
	case tlPlayhead:
		t.SetPlayhead(at)
		if t.onPlayhead != nil {
			t.onPlayhead(t.playhead)
		}
	case tlIn:
		t.in = at
		if t.in > t.out {
			t.out = t.in
		}
	case tlOut:
		t.out = at
		if t.out < t.in {
			t.in = t.out
		}
	}
	t.Refresh()
}

// DragEnd commits handle drags when the gesture ends as a drag (MouseUp
// handles the plain click case; both are idempotent).
func (t *timeline) DragEnd() {
	t.release()
}

func (t *timeline) release() {
	if t.drag == tlIn || t.drag == tlOut {
		if t.onRange != nil {
			t.onRange(t.in, t.out)
		}
	}
	t.drag = tlNone
}

// pickTarget resolves the gesture target: handles first (nearest wins
// when their hit zones overlap, e.g. in≈out), then the playhead
// anywhere else. Full-height 12px zones keep the handles comfortably
// clickable despite their 4px visible bars.
func (t *timeline) pickTarget(x float32) tlDrag {
	inX, outX := t.xAt(t.in), t.xAt(t.out)
	dxIn, dxOut := absFloat32(x-inX), absFloat32(x-outX)
	switch {
	case dxIn <= handleHitHalfWidth && dxOut <= handleHitHalfWidth:
		if dxIn <= dxOut {
			return tlIn
		}
		return tlOut
	case dxIn <= handleHitHalfWidth:
		return tlIn
	case dxOut <= handleHitHalfWidth:
		return tlOut
	default:
		return tlPlayhead
	}
}

// Cursor shows a resize cursor over the handles.
func (t *timeline) Cursor() desktop.Cursor {
	if !t.enabled || t.duration <= 0 {
		return desktop.DefaultCursor
	}
	if d := t.pickTarget(t.hoverX); d == tlIn || d == tlOut {
		return desktop.HResizeCursor
	}
	return desktop.DefaultCursor
}

// --- desktop.Hoverable ---

// MouseIn reports the hovered timestamp immediately.
func (t *timeline) MouseIn(ev *desktop.MouseEvent) {
	t.hover(ev.Position.X)
}

// MouseMoved keeps the hover report following the pointer.
func (t *timeline) MouseMoved(ev *desktop.MouseEvent) {
	t.hover(ev.Position.X)
}

// MouseOut ends the hover: the controller cancels the pending hover
// preview and restores the committed playhead's preview. The committed
// playhead itself never moved.
func (t *timeline) MouseOut() {
	t.hoverX = -1
	if t.onHover != nil {
		t.onHover(nil)
	}
	t.Refresh()
}

func (t *timeline) hover(x float32) {
	t.hoverX = x
	if t.onHover != nil && t.duration > 0 {
		at := t.timeAt(x)
		t.onHover(&at)
	}
	t.Refresh()
}

// --- coordinate mapping (pure, unit-tested) ---

// timeAt maps an x position onto [0, duration], clamped.
func (t *timeline) timeAt(x float32) time.Duration {
	if t.duration <= 0 {
		return 0
	}
	width := t.Size().Width
	if width <= 0 {
		return 0
	}
	ratio := x / width
	if ratio < 0 {
		ratio = 0
	}
	if ratio > 1 {
		ratio = 1
	}
	return time.Duration(float64(ratio) * float64(t.duration))
}

// xAt maps a timestamp onto the widget width, clamped.
func (t *timeline) xAt(d time.Duration) float32 {
	if t.duration <= 0 {
		return 0
	}
	width := t.Size().Width
	if width <= 0 {
		return 0
	}
	x := float32(float64(d) / float64(t.duration) * float64(width))
	if x < 0 {
		x = 0
	}
	if x > width {
		x = width
	}
	return x
}

func (t *timeline) clamp(d time.Duration) time.Duration {
	if d < 0 {
		return 0
	}
	if t.duration > 0 && d > t.duration {
		return t.duration
	}
	return d
}

func absFloat32(v float32) float32 {
	if v < 0 {
		return -v
	}
	return v
}

// --- rendering ---

type timelineRenderer struct {
	t          *timeline
	background *canvas.Rectangle
	thumbs     []*canvas.Image
	dimBefore  *canvas.Rectangle
	dimAfter   *canvas.Rectangle
	inBar      *canvas.Rectangle
	outBar     *canvas.Rectangle
	playhead   *canvas.Rectangle
	focusRing  *canvas.Rectangle
}

func (t *timeline) CreateRenderer() fyne.WidgetRenderer {
	r := &timelineRenderer{
		t:          t,
		background: canvas.NewRectangle(color.NRGBA{R: 32, G: 32, B: 32, A: 255}),
		dimBefore:  canvas.NewRectangle(color.NRGBA{R: 0, G: 0, B: 0, A: 110}),
		dimAfter:   canvas.NewRectangle(color.NRGBA{R: 0, G: 0, B: 0, A: 110}),
		inBar:      canvas.NewRectangle(theme.Color(theme.ColorNamePrimary)),
		outBar:     canvas.NewRectangle(theme.Color(theme.ColorNamePrimary)),
		playhead:   canvas.NewRectangle(color.NRGBA{R: 255, G: 255, B: 255, A: 255}),
		focusRing: &canvas.Rectangle{
			StrokeColor: theme.Color(theme.ColorNamePrimary),
			StrokeWidth: 2,
			FillColor:   color.Transparent,
		},
	}
	return r
}

// syncThumbs matches the renderer's image objects to the widget's
// thumbnail slots.
func (r *timelineRenderer) syncThumbs() {
	for len(r.thumbs) < len(r.t.thumbs) {
		img := canvas.NewImageFromImage(nil)
		img.FillMode = canvas.ImageFillContain
		r.thumbs = append(r.thumbs, img)
	}
	for len(r.thumbs) > len(r.t.thumbs) {
		r.thumbs = r.thumbs[:len(r.t.thumbs)]
	}
	for i, img := range r.t.thumbs {
		r.thumbs[i].Image = img
	}
}

func (r *timelineRenderer) Destroy() {}

func (r *timelineRenderer) Objects() []fyne.CanvasObject {
	r.syncThumbs()
	objects := make([]fyne.CanvasObject, 0, len(r.thumbs)+6)
	objects = append(objects, r.background)
	for _, img := range r.thumbs {
		objects = append(objects, img)
	}
	return append(objects, r.dimBefore, r.dimAfter, r.inBar, r.outBar, r.playhead, r.focusRing)
}

func (r *timelineRenderer) MinSize() fyne.Size {
	return fyne.NewSize(timelineMinWidth, timelineMinHeight)
}

func (r *timelineRenderer) Layout(size fyne.Size) {
	r.syncThumbs()
	t := r.t
	// Thumbnail cells stretch evenly across the width; ImageFillContain
	// letterboxes each cell so the source aspect ratio is preserved.
	cells := len(r.thumbs)
	cellWidth := float32(0)
	if cells > 0 {
		cellWidth = size.Width / float32(cells)
	}
	for i, img := range r.thumbs {
		img.Move(fyne.NewPos(float32(i)*cellWidth, 0))
		img.Resize(fyne.NewSize(cellWidth, size.Height))
	}
	inX, outX, playX := t.xAt(t.in), t.xAt(t.out), t.xAt(t.playhead)
	r.dimBefore.Move(fyne.NewPos(0, 0))
	r.dimBefore.Resize(fyne.NewSize(inX, size.Height))
	r.dimAfter.Move(fyne.NewPos(outX, 0))
	r.dimAfter.Resize(fyne.NewSize(size.Width-outX, size.Height))
	r.inBar.Move(fyne.NewPos(inX-handleBarWidth/2, 0))
	r.inBar.Resize(fyne.NewSize(handleBarWidth, size.Height))
	r.outBar.Move(fyne.NewPos(outX-handleBarWidth/2, 0))
	r.outBar.Resize(fyne.NewSize(handleBarWidth, size.Height))
	r.playhead.Move(fyne.NewPos(playX-playheadBarWidth/2, 0))
	r.playhead.Resize(fyne.NewSize(playheadBarWidth, size.Height))
	r.focusRing.Move(fyne.NewPos(0, 0))
	r.focusRing.Resize(size)
}

func (r *timelineRenderer) Refresh() {
	r.Layout(r.t.Size())
	r.focusRing.Hidden = !r.t.focused
	canvas.Refresh(r.t)
}
