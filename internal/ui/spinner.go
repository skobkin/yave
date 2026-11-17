package ui

import (
	"image/color"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

// spinnerArmSpan is the angular length of the rotating arc, in degrees.
const spinnerArmSpan = float32(100)

// spinnerPeriod is the duration of one full revolution.
const spinnerPeriod = time.Second

// spinnerSizeExtra grows the widget beyond the theme's inline-icon
// scale so the cue reads from across the preview cell.
const spinnerSizeExtra = 50

// spinnerRingCutout is the inner-radius ratio of the ring. The ring is
// a filled annulus (CutoutRatio < 1), not a stroked arc: a full cutout
// of 1.0 degenerates the band to zero thickness and the StrokeWidth
// hairlines it. Ring thickness is (1 - cutout) * outer radius, so at
// 0.75 a 98px widget draws a ~12px band — three times the old 4px.
const spinnerRingCutout = 0.75

// spinner is the frame-extraction loading indicator: a faint ring with a
// shorter accent arc rotating around the center. Fyne ships no spinner
// widget, so this is hand-rolled from two canvas.Arc rings (stroke-only
// via CutoutRatio 1.0) plus a fyne.Animation that runs only while the
// widget is visible: Show starts the rotation, Hide stops it.
type spinner struct {
	widget.BaseWidget

	track *canvas.Arc // the faint full ring
	arm   *canvas.Arc // the accent arc that rotates

	anim *fyne.Animation
}

func newSpinner() *spinner {
	s := &spinner{
		track: canvas.NewArc(0, 360, spinnerRingCutout, color.Transparent),
		arm:   canvas.NewArc(0, spinnerArmSpan, spinnerRingCutout, color.Transparent),
	}
	s.ExtendBaseWidget(s)
	return s
}

func (s *spinner) start() {
	if s.anim != nil {
		return
	}
	s.anim = &fyne.Animation{
		Duration:    spinnerPeriod,
		RepeatCount: fyne.AnimationRepeatForever,
		Tick: func(progress float32) {
			angle := progress * 360
			s.arm.StartAngle = angle
			s.arm.EndAngle = angle + spinnerArmSpan
			canvas.Refresh(s.arm)
		},
	}
	s.anim.Start()
}

func (s *spinner) stop() {
	if s.anim == nil {
		return
	}
	s.anim.Stop()
	s.anim = nil
}

// Show starts the rotation along with the usual visibility flip.
func (s *spinner) Show() {
	s.BaseWidget.Show()
	s.start()
}

// Hide stops the rotation along with the usual visibility flip, so a
// hidden spinner never animates in the background.
func (s *spinner) Hide() {
	s.stop()
	s.BaseWidget.Hide()
}

func (s *spinner) CreateRenderer() fyne.WidgetRenderer {
	return &spinnerRenderer{s: s}
}

type spinnerRenderer struct {
	s *spinner
}

func (r *spinnerRenderer) Objects() []fyne.CanvasObject {
	return []fyne.CanvasObject{r.s.track, r.s.arm}
}

func (r *spinnerRenderer) Destroy() {}

func (r *spinnerRenderer) MinSize() fyne.Size {
	side := theme.Size(theme.SizeNameInlineIcon)*2 + spinnerSizeExtra
	return fyne.NewSize(side, side)
}

func (r *spinnerRenderer) Layout(size fyne.Size) {
	// Both rings take the largest centered square that fits. Note that
	// the renderers treat canvas.Arc's position as the top-left of its
	// bounding square (like any canvas object) — the doc comment on
	// Arc.Move claiming "center" does not match the render path.
	side := min(size.Width, size.Height)
	topLeft := fyne.NewPos((size.Width-side)/2, (size.Height-side)/2)
	for _, arc := range []*canvas.Arc{r.s.track, r.s.arm} {
		arc.Resize(fyne.NewSize(side, side))
		arc.Move(topLeft)
	}
}

func (r *spinnerRenderer) Refresh() {
	// The arm's angles belong to the animation tick; a refresh (theme
	// switch, resize) only recolors the rings. Filled annuli need no
	// stroke: the band thickness comes from spinnerRingCutout.
	r.s.track.FillColor = translucent(theme.Color(theme.ColorNameForeground), 0x30)
	r.s.arm.FillColor = theme.Color(theme.ColorNamePrimary)
	// Soften the arm's two ends: corner radius = half the band width.
	band := (1 - spinnerRingCutout) * r.s.arm.Size().Width / 2
	r.s.arm.CornerRadius = band / 2
	r.s.track.CornerRadius = 0
	canvas.Refresh(r.s.track)
	canvas.Refresh(r.s.arm)
}
