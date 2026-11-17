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

	"git.skobk.in/skobkin/yave/internal/model"
)

// tlDrag identifies which part of the timeline a pointer gesture is
// operating on. The target is acquired on MouseDown (desktop.Mouseable)
// so the whole gesture is unambiguous, including when handle hit zones
// overlap near the timeline edges. dragSeg (on the widget) carries the
// segment index the gesture grabbed.
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
	hoverMarkWidth     = 2 // ghost marker following the pointer while it previews
)

// timeline is the trim tab's scrubber: a thumbnail strip with a playhead
// and one pair of draggable In/Out boundary handles per segment, with
// the selected segment highlighted. It is the app's only custom-rendered
// widget and implements the desktop extensions (Keyable, Mouseable,
// Hoverable) directly:
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
//
// Segments are half-open intervals in caller-owned display order.
// Pointer drags clamp against neighboring bounds, so they can never
// create overlaps or crossings; typed edits bypass the widget and are
// validated by the controller instead.
type timeline struct {
	widget.BaseWidget

	duration time.Duration
	playhead time.Duration
	segments []model.TimeRange
	selected int
	thumbs   []image.Image
	dragSeg  int // segment index for tlIn/tlOut drags; -1 otherwise

	focused      bool
	enabled      bool
	shiftHeld    bool
	ctrlHeld     bool
	drag         tlDrag
	hoverX       float32 // -1 when the pointer is outside the widget
	hoverPending bool    // the hovered frame's preview is still being extracted

	// stepFrame resolves one frame-accurate step; it returns the new
	// position and false while the frame index is loading or stepping
	// is unavailable (the controller surfaces that in its status label).
	stepFrame func(at time.Duration, dir int) (time.Duration, bool)
	// onPlayhead fires when the committed playhead moves (click, drag,
	// keyboard). It does NOT fire for hover positions.
	onPlayhead func(time.Duration)
	// onSegments fires when a handle drag commits (MouseUp / DragEnd)
	// and when segments are set programmatically via SetSegments.
	// edited is the index of the segment whose handle the drag moved
	// (>= 0), or -1 for programmatic calls — the controller keeps the
	// authoritative boundaries of every segment the gesture did not
	// edit, so a typed out-of-range value elsewhere is not lost.
	onSegments func(segments []model.TimeRange, edited int)
	// onSelected fires when a pointer gesture changes the selected
	// segment (region click or handle grab). Programmatic SetSelected
	// is controller-driven and fires nothing.
	onSelected func(index int)
	// onHover fires with the hovered position, or nil when the pointer
	// leaves the widget.
	onHover func(*time.Duration)
	// onSetIn/onSetOut fire for the Ctrl+[ / Ctrl+] shortcuts so the
	// controller applies a single-boundary update (own entry and
	// validity flag only). Nil falls back to the widget's loud
	// SetSegments.
	onSetIn  func()
	onSetOut func()
}

func newTimeline() *timeline {
	t := &timeline{enabled: true, hoverX: -1, dragSeg: -1}
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
// positions into the new range. Unlike SetSegments it does not fire
// onSegments: a media change (new input, reset) is controller-driven,
// and re-entering the controller would resurrect state it just cleared.
func (t *timeline) SetDuration(d time.Duration) {
	if d < 0 {
		d = 0
	}
	t.duration = d
	t.SetPlayhead(t.playhead)
	for i := range t.segments {
		t.segments[i].Start = t.clamp(t.segments[i].Start)
		t.segments[i].End = t.clamp(t.segments[i].End)
	}
	t.Refresh()
}

// Duration returns the media duration the timeline shows.
func (t *timeline) Duration() time.Duration { return t.duration }

// SetSegments replaces the displayed segments, clamping every bound
// into [0, duration], and fires onSegments with the clamped copy so
// callers stay in sync. A crossed segment (Start > End) is displayed
// as-is so the bars show the honest positions — validation reports it;
// only pointer drags are prevented from crossing (they clamp against
// neighbors in MouseDown/Dragged).
func (t *timeline) SetSegments(segments []model.TimeRange) {
	t.SetSegmentsSilent(segments)
	if t.onSegments != nil {
		t.onSegments(t.Segments(), -1)
	}
}

// SetSegmentsSilent updates the displayed segments without firing
// onSegments. Boundary-entry edits use it: the typed value stays
// authoritative in the controller — validation must see an out-of-range
// entry as an error, not have it silently clamp to whatever the
// timeline displays — while the widget still shows the clamped view.
// Pointer drags and the boundary shortcuts keep using SetSegments,
// whose callback commits the clamped coordinates as the selection.
func (t *timeline) SetSegmentsSilent(segments []model.TimeRange) {
	t.segments = make([]model.TimeRange, len(segments))
	for i, seg := range segments {
		t.segments[i] = model.TimeRange{
			Start: t.clamp(seg.Start),
			End:   t.clamp(seg.End),
		}
	}
	if t.selected >= len(t.segments) {
		t.selected = max(0, len(t.segments)-1)
	}
	t.Refresh()
}

// Segments returns a copy of the displayed segments.
func (t *timeline) Segments() []model.TimeRange {
	out := make([]model.TimeRange, len(t.segments))
	copy(out, t.segments)
	return out
}

// SetSelected moves the selection highlight (clamped); fires nothing.
func (t *timeline) SetSelected(index int) {
	t.selected = t.clampIndex(index)
	t.Refresh()
}

// Selected returns the selected segment index.
func (t *timeline) Selected() int {
	if len(t.segments) == 0 {
		return 0
	}
	return t.selected
}

// clampIndex folds an index into [0, len(segments)-1]; an empty list
// keeps 0.
func (t *timeline) clampIndex(index int) int {
	if len(t.segments) == 0 {
		return 0
	}
	return min(max(0, index), len(t.segments)-1)
}

// SetPlayhead moves the committed playhead, clamped to [0, duration].
// onPlayhead is not fired for programmatic moves.
func (t *timeline) SetPlayhead(at time.Duration) {
	t.playhead = t.clamp(at)
	t.Refresh()
}

// Playhead returns the committed playhead position.
func (t *timeline) Playhead() time.Duration { return t.playhead }

// SetPreviewPending recolors the ghost hover marker: accent while the
// hovered frame's preview is still being extracted, neutral once it
// landed. Controller-driven; fires nothing.
func (t *timeline) SetPreviewPending(pending bool) {
	if t.hoverPending == pending {
		return
	}
	t.hoverPending = pending
	t.Refresh()
}

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
	t.dragSeg = -1
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
	t.dragSeg = -1
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

// selectedBounds returns the selected segment's index and its
// neighbors' clamping bounds: the previous segment's End (or 0) and the
// next segment's Start (or duration).
func (t *timeline) selectedBounds() (index int, prevEnd, nextStart time.Duration) {
	index = t.Selected()
	prevEnd, nextStart = 0, t.duration
	if index > 0 && index-1 < len(t.segments) {
		prevEnd = t.segments[index-1].End
	}
	if index+1 < len(t.segments) {
		nextStart = t.segments[index+1].Start
	}
	return index, prevEnd, nextStart
}

func (t *timeline) setInToPlayhead() {
	index, prevEnd, _ := t.selectedBounds()
	if index >= len(t.segments) {
		return
	}
	if t.playhead < prevEnd || t.playhead > t.segments[index].End {
		return
	}
	// The controller's single-boundary handler keeps the opposite entry
	// authoritative (its validity flag and in-progress text stay
	// untouched); a loud SetSegments would mark both entries valid and
	// rewrite the other one from the stale stored value.
	if t.onSetIn != nil {
		t.onSetIn()
		return
	}
	t.commitSelectedStart(t.playhead)
}

func (t *timeline) setOutToPlayhead() {
	index, _, nextStart := t.selectedBounds()
	if index >= len(t.segments) {
		return
	}
	if t.playhead < t.segments[index].Start || t.playhead > nextStart {
		return
	}
	if t.onSetOut != nil {
		t.onSetOut()
		return
	}
	t.commitSelectedEnd(t.playhead)
}

// commitSelectedStart/commitSelectedEnd apply a boundary to the
// selected segment and fire onSegments — the loud fallback for
// keyboard shortcuts when no single-boundary handler is installed.
func (t *timeline) commitSelectedStart(at time.Duration) {
	index, _, _ := t.selectedBounds()
	if index >= len(t.segments) {
		return
	}
	t.segments[index].Start = t.clamp(at)
	t.SetSegments(t.segments)
}

func (t *timeline) commitSelectedEnd(at time.Duration) {
	index, _, _ := t.selectedBounds()
	if index >= len(t.segments) {
		return
	}
	t.segments[index].End = t.clamp(at)
	t.SetSegments(t.segments)
}

// movePlayhead commits a keyboard-driven playhead move.
func (t *timeline) movePlayhead(at time.Duration) {
	t.SetPlayhead(at)
	if t.onPlayhead != nil {
		t.onPlayhead(t.playhead)
	}
}

// --- desktop.Mouseable + fyne.Draggable ---

// MouseDown acquires focus and the drag target for this gesture. A
// handle grab also selects its segment; a plain click inside a segment
// selects it while still moving the playhead (the click is a scrub).
func (t *timeline) MouseDown(ev *desktop.MouseEvent) {
	if !t.enabled || ev.Button != desktop.MouseButtonPrimary || t.duration <= 0 {
		return
	}
	if c := fyne.CurrentApp().Driver().CanvasForObject(t); c != nil {
		c.Focus(t)
	}
	kind, seg := t.pickTarget(ev.Position.X)
	switch kind {
	case tlIn:
		t.drag, t.dragSeg = tlIn, seg
		t.selectViaPointer(seg)
		t.segments[seg].Start = t.clampToStartLower(t.timeAt(ev.Position.X), seg)
	case tlOut:
		t.drag, t.dragSeg = tlOut, seg
		t.selectViaPointer(seg)
		t.segments[seg].End = t.clampToEndUpper(t.timeAt(ev.Position.X), seg)
	default:
		t.drag, t.dragSeg = tlPlayhead, -1
		t.SetPlayhead(t.timeAt(ev.Position.X))
		if t.onPlayhead != nil {
			t.onPlayhead(t.playhead)
		}
		if at := t.timeAt(ev.Position.X); at > 0 || len(t.segments) > 0 {
			t.selectSegmentContaining(at)
		}
	}
	t.Refresh()
}

// MouseUp commits handle drags.
func (t *timeline) MouseUp(ev *desktop.MouseEvent) {
	t.release()
}

// Dragged moves the target acquired in MouseDown, clamped against the
// neighboring segment bounds so a drag can never create overlaps or
// crossings; equality is allowed (adjacent segments).
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
		t.segments[t.dragSeg].Start = t.clampToStartLower(at, t.dragSeg)
	case tlOut:
		t.segments[t.dragSeg].End = t.clampToEndUpper(at, t.dragSeg)
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
		if t.onSegments != nil {
			t.onSegments(t.Segments(), t.dragSeg)
		}
	}
	t.drag = tlNone
	t.dragSeg = -1
}

// selectViaPointer reports a pointer-driven selection change; the
// callback is deliberately not fired for programmatic SetSelected.
func (t *timeline) selectViaPointer(index int) {
	index = t.clampIndex(index)
	if t.selected == index {
		return
	}
	t.selected = index
	if t.onSelected != nil {
		t.onSelected(index)
	}
}

// selectSegmentContaining selects the segment holding at, if any. A
// segment is half-open: [Start, End); a click exactly on a following
// segment's Start belongs to that segment. at == 0 with a segment
// starting at 0 still selects segment 0 via the first branch.
func (t *timeline) selectSegmentContaining(at time.Duration) {
	for i, seg := range t.segments {
		if at >= seg.Start && at < seg.End {
			t.selectViaPointer(i)
			return
		}
	}
	// Degenerate (Start == End) segments contain no open interval; the
	// click lands in a gap and changes nothing.
}

// clampToStartLower clamps a dragged Start into [prevEnd, seg.End].
func (t *timeline) clampToStartLower(at time.Duration, seg int) time.Duration {
	if seg < 0 || seg >= len(t.segments) {
		return t.clamp(at)
	}
	lower := time.Duration(0)
	if seg > 0 {
		lower = t.segments[seg-1].End
	}
	return min(max(at, lower), t.segments[seg].End)
}

// clampToEndUpper clamps a dragged End into [seg.Start, nextStart].
func (t *timeline) clampToEndUpper(at time.Duration, seg int) time.Duration {
	if seg < 0 || seg >= len(t.segments) {
		return t.clamp(at)
	}
	upper := t.duration
	if seg+1 < len(t.segments) {
		upper = t.segments[seg+1].Start
	}
	return min(max(at, t.segments[seg].Start), upper)
}

// pickTarget resolves the gesture target: handles first (nearest wins
// when their hit zones overlap), then the playhead anywhere else,
// together with the segment index the gesture grabbed (-1 for the
// playhead). Full-height 12px zones keep the handles comfortably
// clickable despite their 4px visible bars. Ties resolve to the earlier
// candidate in (segment order, Start-before-End) order, preserving the
// previous single-segment behavior where In won ties.
func (t *timeline) pickTarget(x float32) (tlDrag, int) {
	best := tlPlayhead
	bestSeg := -1
	bestDist := float32(handleHitHalfWidth + 1)
	for i, seg := range t.segments {
		for _, candidate := range [2]struct {
			kind tlDrag
			at   time.Duration
		}{{tlIn, seg.Start}, {tlOut, seg.End}} {
			dist := absFloat32(x - t.xAt(candidate.at))
			if dist > handleHitHalfWidth || dist >= bestDist {
				continue
			}
			best, bestSeg, bestDist = candidate.kind, i, dist
		}
	}
	return best, bestSeg
}

// Cursor shows a resize cursor over the handles.
func (t *timeline) Cursor() desktop.Cursor {
	if !t.enabled || t.duration <= 0 {
		return desktop.DefaultCursor
	}
	if kind, _ := t.pickTarget(t.hoverX); kind == tlIn || kind == tlOut {
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

// translucent re-colors c with the given alpha, keeping its RGB. The
// conversion goes through color.NRGBAModel (like overlayChipRectangle)
// rather than hand-shifting the 16-bit RGBA() components, which gosec
// cannot prove fit into 8 bits.
func translucent(c color.Color, alpha uint8) color.NRGBA {
	nrgba := color.NRGBAModel.Convert(c).(color.NRGBA)
	nrgba.A = alpha
	return nrgba
}

// --- rendering ---

type timelineRenderer struct {
	t          *timeline
	background *canvas.Rectangle
	thumbs     []*canvas.Image
	dim        *canvas.Rectangle
	fills      []*canvas.Rectangle
	bars       []*canvas.Rectangle
	hoverMark  *canvas.Rectangle
	playhead   *canvas.Rectangle
	focusRing  *canvas.Rectangle
}

func (t *timeline) CreateRenderer() fyne.WidgetRenderer {
	r := &timelineRenderer{
		t:          t,
		background: canvas.NewRectangle(color.NRGBA{R: 32, G: 32, B: 32, A: 255}),
		dim:        canvas.NewRectangle(color.NRGBA{R: 0, G: 0, B: 0, A: 110}),
		hoverMark:  canvas.NewRectangle(color.Transparent),
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
		if r.thumbs[i].Image != img {
			r.thumbs[i].Image = img
			// A parent refresh re-lays-out children but does not
			// invalidate a child canvas.Image's cached texture when the
			// cell size is unchanged — thumbnails arriving after the
			// empty slots were painted would stay blank until a resize.
			// Refresh the image itself when its content changes.
			r.thumbs[i].Refresh()
		}
	}
}

// syncSegments matches the renderer's fill and bar rectangles to the
// widget's segment count: one translucent fill per segment and two
// handle bars per segment.
func (r *timelineRenderer) syncSegments() {
	for len(r.fills) < len(r.t.segments) {
		r.fills = append(r.fills, canvas.NewRectangle(color.Transparent))
	}
	r.fills = r.fills[:len(r.t.segments)]
	for len(r.bars) < 2*len(r.t.segments) {
		r.bars = append(r.bars, canvas.NewRectangle(color.Transparent))
	}
	r.bars = r.bars[:2*len(r.t.segments)]
}

func (r *timelineRenderer) Destroy() {}

func (r *timelineRenderer) Objects() []fyne.CanvasObject {
	r.syncThumbs()
	r.syncSegments()
	objects := make([]fyne.CanvasObject, 0, len(r.thumbs)+len(r.fills)+len(r.bars)+4)
	objects = append(objects, r.background)
	for _, img := range r.thumbs {
		objects = append(objects, img)
	}
	objects = append(objects, r.dim)
	for _, fill := range r.fills {
		objects = append(objects, fill)
	}
	for _, bar := range r.bars {
		objects = append(objects, bar)
	}
	return append(objects, r.hoverMark, r.playhead, r.focusRing)
}

func (r *timelineRenderer) MinSize() fyne.Size {
	return fyne.NewSize(timelineMinWidth, timelineMinHeight)
}

func (r *timelineRenderer) Layout(size fyne.Size) {
	r.syncThumbs()
	r.syncSegments()
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
	r.dim.Move(fyne.NewPos(0, 0))
	r.dim.Resize(size)
	// Everything outside the segments reads dimmed: the single full-size
	// overlay sits under the per-segment translucent fills.
	primary := theme.Color(theme.ColorNamePrimary)
	foreground := theme.Color(theme.ColorNameForeground)
	selectedFill, unselectedFill := translucent(primary, 0x30), translucent(primary, 0x18)
	for i, seg := range t.segments {
		startX, endX := t.xAt(seg.Start), t.xAt(seg.End)
		fill := r.fills[i]
		fill.Move(fyne.NewPos(startX, 0))
		fill.Resize(fyne.NewSize(max(0, endX-startX), size.Height))
		if i == t.selected {
			fill.FillColor = selectedFill
		} else {
			fill.FillColor = unselectedFill
		}
		barColor := foreground
		if i == t.selected {
			barColor = primary
		}
		for j, at := range []time.Duration{seg.Start, seg.End} {
			bar := r.bars[2*i+j]
			barX := t.xAt(at)
			bar.Move(fyne.NewPos(barX-handleBarWidth/2, 0))
			bar.Resize(fyne.NewSize(handleBarWidth, size.Height))
			bar.FillColor = barColor
		}
	}
	playX := t.xAt(t.playhead)
	r.playhead.Move(fyne.NewPos(playX-playheadBarWidth/2, 0))
	r.playhead.Resize(fyne.NewSize(playheadBarWidth, size.Height))
	// Ghost hover marker: follows the pointer while it is over the
	// widget, accent-colored while the hovered frame's preview is still
	// being extracted and neutral once it landed. It teaches that the
	// preview tracks the pointer and marks the displayed frame as stale
	// until the marker's color settles.
	if t.hoverX >= 0 {
		x := min(max(t.hoverX, 0), size.Width-hoverMarkWidth)
		r.hoverMark.Move(fyne.NewPos(x, 0))
		r.hoverMark.Resize(fyne.NewSize(hoverMarkWidth, size.Height))
		if t.hoverPending {
			r.hoverMark.FillColor = translucent(primary, 0x60)
		} else {
			r.hoverMark.FillColor = translucent(foreground, 0x60)
		}
		r.hoverMark.Hidden = false
	} else {
		r.hoverMark.Hidden = true
	}
	r.focusRing.Move(fyne.NewPos(0, 0))
	r.focusRing.Resize(size)
}

func (r *timelineRenderer) Refresh() {
	r.Layout(r.t.Size())
	r.focusRing.Hidden = !r.t.focused
	canvas.Refresh(r.t)
}
