package ui

import (
	"image/color"
	"strconv"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"git.skobk.in/skobkin/yave/internal/model"
)

// runFigures is what the status bar shows about a running export. Empty
// strings mean "not known (yet)".
type runFigures struct {
	Fraction float64 // 0..1 for the bar
	Percent  string  // "24%"; empty with an unknown duration
	Time     string  // "0:23 / ~1:33", or "0:23 · 1:20 encoded"
	Speed    string  // "1.9×"
	Size     string  // "2.5 MB"
}

// runFiguresFor derives the figures from an FFmpeg progress report.
// Elapsed time comes first; with a known duration and enough progress it
// is followed by the estimated total, with an unknown one by the encoded
// position. The bitrate is left to the log: early in a run it swings
// wildly and reads as noise.
func runFiguresFor(p model.Progress, elapsed time.Duration, durationKnown bool) runFigures {
	f := runFigures{Time: formatRunClock(elapsed), Speed: formatSpeed(p.Speed)}
	if durationKnown {
		f.Fraction = min(max(p.Percentage, 0), 1)
		if f.Fraction != f.Fraction { //nolint:gocritic // NaN check
			f.Fraction = 0
		}
		f.Percent = strconv.Itoa(percentValue(p.Percentage)) + "%"
		if eta, ok := estimateRemaining(p.Percentage, elapsed); ok {
			f.Time += " / ~" + formatRunClock(elapsed+eta)
		}
	} else {
		f.Time += " · " + formatRunClock(p.OutTime) + " encoded"
	}
	if p.TotalSize > 0 {
		f.Size = formatByteSize(p.TotalSize)
	}
	return f
}

// String is the figures as one line (tests, logs).
func (f runFigures) String() string {
	parts := []string{}
	for _, s := range []string{f.Percent, f.Time, f.Speed, f.Size} {
		if s != "" {
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, " · ")
}

// Cell widths start at these samples and only ever grow (growCell), so
// the bar does not jitter as the figures change. The time sample covers
// runs under an hour; a longer one widens its cell once.
const (
	runPercentSample = "100%"
	runTimeSample    = "59:59 / ~59:59"
	runSpeedSample   = "999.9×"
	runSizeSample    = "1023.9 MB"
)

// runProgressView draws a running export in the status bar the way the
// Input tab draws facts: a bold figure for what matters most (the
// percentage), a slim bar, and icon-tagged values instead of a "|"-joined
// text line:
//
//	24%  ━━━━━━━━━━━━━━━━━━──────────────────────   ⏱ 0:23 / ~1:33   ⏩ 1.9×   🖴 2.5 MB
type runProgressView struct {
	percent *widget.Label
	bar     *slimBar
	time    *widget.Label
	speed   *widget.Label
	size    *widget.Label
	box     *fyne.Container
	figures runFigures
}

func newRunProgressView() *runProgressView {
	v := &runProgressView{bar: newSlimBar()}
	v.percent = widget.NewLabelWithStyle("", fyne.TextAlignTrailing, fyne.TextStyle{Bold: true})
	v.percent.Importance = widget.HighImportance
	value := func() *widget.Label {
		return widget.NewLabelWithStyle("", fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	}
	v.time, v.speed, v.size = value(), value(), value()
	cell := func(sample string, label *widget.Label, style fyne.TextStyle) fyne.CanvasObject {
		size := widget.NewLabelWithStyle(sample, fyne.TextAlignLeading, style).MinSize()
		return container.New(&growCell{widest: size.Width}, label)
	}
	bold := fyne.TextStyle{Bold: true}
	stat := func(icon fyne.Resource, sample string, label *widget.Label) fyne.CanvasObject {
		return container.NewHBox(container.NewCenter(mutedIcon(icon)), cell(sample, label, bold))
	}
	stats := container.NewHBox(
		stat(theme.HistoryIcon(), runTimeSample, v.time),
		stat(theme.MediaFastForwardIcon(), runSpeedSample, v.speed),
		stat(theme.FileVideoIcon(), runSizeSample, v.size),
	)
	v.box = container.NewBorder(nil, nil, cell(runPercentSample, v.percent, bold), stats, v.bar)
	return v
}

// growCell gives its content at least the width it has ever needed: a
// cell sized to a sample text that widens when a figure outgrows it and
// never shrinks back, so neighbours move at most once.
type growCell struct{ widest float32 }

func (g *growCell) Layout(objects []fyne.CanvasObject, size fyne.Size) {
	for _, o := range objects {
		o.Move(fyne.NewPos(0, 0))
		o.Resize(size)
	}
}

func (g *growCell) MinSize(objects []fyne.CanvasObject) fyne.Size {
	var height float32
	for _, o := range objects {
		m := o.MinSize()
		g.widest = max(g.widest, m.Width)
		height = max(height, m.Height)
	}
	return fyne.NewSize(g.widest, height)
}

// mutedIcon draws icon in the placeholder colour, like the Input tab's
// captions: present, but quieter than the values beside it.
func mutedIcon(icon fyne.Resource) *widget.Icon {
	return widget.NewIcon(theme.NewColoredResource(icon, theme.ColorNamePlaceHolder))
}

// set shows new figures.
func (v *runProgressView) set(f runFigures) {
	v.figures = f
	v.percent.SetText(f.Percent)
	v.bar.SetValue(f.Fraction)
	for _, cell := range []struct {
		label *widget.Label
		text  string
	}{{v.time, f.Time}, {v.speed, f.Speed}, {v.size, f.Size}} {
		if cell.text == "" {
			cell.text = "–"
		}
		cell.label.SetText(cell.text)
	}
}

func (v *runProgressView) reset()             { v.set(runFigures{}) }
func (v *runProgressView) visible() bool      { return v.box.Visible() }
func (v *runProgressView) setVisible(on bool) { setShown(v.box, on) }

// slimBar is a thin rounded progress bar: a track and a primary-coloured
// fill, vertically centred in whatever height the row gives it. Fyne's
// ProgressBar is a full-height block with the percentage printed inside,
// which dominated the status bar.
type slimBar struct {
	widget.BaseWidget
	Value float64
}

const slimBarHeight = 6

func newSlimBar() *slimBar {
	b := &slimBar{}
	b.ExtendBaseWidget(b)
	return b
}

// SetValue sets the filled fraction, clamped to 0..1.
func (b *slimBar) SetValue(v float64) {
	if v != v { //nolint:gocritic // NaN check
		v = 0
	}
	v = min(max(v, 0), 1)
	if v == b.Value {
		return
	}
	b.Value = v
	b.Refresh()
}

func (b *slimBar) CreateRenderer() fyne.WidgetRenderer {
	r := &slimBarRenderer{bar: b, track: canvas.NewRectangle(color.Transparent), fill: canvas.NewRectangle(color.Transparent)}
	r.Refresh()
	return r
}

type slimBarRenderer struct {
	bar         *slimBar
	track, fill *canvas.Rectangle
}

func (r *slimBarRenderer) Layout(size fyne.Size) {
	y := (size.Height - slimBarHeight) / 2
	r.track.Move(fyne.NewPos(0, y))
	r.track.Resize(fyne.NewSize(size.Width, slimBarHeight))
	r.fill.Move(fyne.NewPos(0, y))
	r.fill.Resize(fyne.NewSize(size.Width*float32(r.bar.Value), slimBarHeight))
}

func (r *slimBarRenderer) MinSize() fyne.Size { return fyne.NewSize(40, slimBarHeight) }

func (r *slimBarRenderer) Objects() []fyne.CanvasObject {
	return []fyne.CanvasObject{r.track, r.fill}
}

func (r *slimBarRenderer) Destroy() {}

func (r *slimBarRenderer) Refresh() {
	track, _ := color.NRGBAModel.Convert(theme.ColorForWidget(theme.ColorNameForeground, r.bar)).(color.NRGBA)
	track.A = 0x24 // the badge tint: a quiet groove
	r.track.FillColor = track
	r.fill.FillColor = theme.ColorForWidget(theme.ColorNamePrimary, r.bar)
	r.track.CornerRadius, r.fill.CornerRadius = slimBarHeight/2, slimBarHeight/2
	r.Layout(r.bar.Size())
	r.track.Refresh()
	r.fill.Refresh()
}
