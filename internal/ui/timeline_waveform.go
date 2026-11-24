package ui

import (
	"image"
	"image/color"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/theme"

	"git.skobk.in/skobkin/yave/internal/analysis"
)

const (
	// waveformLaneHeight is the strip the audio waveform occupies under
	// the thumbnails.
	waveformLaneHeight = 36
	// waveformMinScale is the amplitude (a tenth of full scale) the lane
	// never amplifies beyond, so near-silent recordings are not blown up
	// to full height.
	waveformMinScale = 3277
)

// columnPeak is the envelope of the media time one pixel column covers.
// ok is false for columns beyond the audio computed so far.
type columnPeak struct {
	analysis.Peak
	ok bool
}

// columnPeaks folds a waveform into one min/max pair per pixel column of
// the view: the buckets overlapping each column's time range are merged,
// so a zoomed-out view summarizes many buckets per column and a zoomed-in
// view repeats one bucket across several. Pure and table-tested.
func columnPeaks(peaks []analysis.Peak, bucket time.Duration, view timelineView, columns int) []columnPeak {
	if columns <= 0 || bucket <= 0 {
		return nil
	}
	out := make([]columnPeak, columns)
	for x := range columns {
		from := view.xToTime(float32(x), float32(columns))
		to := view.xToTime(float32(x+1), float32(columns))
		// The pixel→time mapping goes through float32, so a column edge that
		// lies exactly on a bucket boundary can land a hair to either side
		// of it and would pull in the neighboring bucket; buckets are at
		// least 10ms, so a microsecond of tolerance is invisible.
		const edge = time.Microsecond
		first := int((from + edge) / bucket)
		last := int((to - edge + bucket - 1) / bucket) // exclusive
		if last <= first {
			last = first + 1
		}
		if first >= len(peaks) {
			continue
		}
		last = min(last, len(peaks))
		agg := peaks[first]
		for _, p := range peaks[first+1 : last] {
			agg.Min = min(agg.Min, p.Min)
			agg.Max = max(agg.Max, p.Max)
		}
		out[x] = columnPeak{Peak: agg, ok: true}
	}
	return out
}

// waveformScale returns the amplitude drawn at full lane height: the
// loudest sample of the whole waveform (so zooming never rescales), but
// never less than waveformMinScale.
func waveformScale(peaks []analysis.Peak) float64 {
	loudest := 0
	for _, p := range peaks {
		loudest = max(loudest, int(p.Max), -int(p.Min))
	}
	return float64(max(loudest, waveformMinScale))
}

// SetWaveformLane reserves (or releases) the lane's space under the
// thumbnails; the controller shows it as soon as an input with audio is
// being analyzed, so the strip does not jump when the first peaks land.
func (t *timeline) SetWaveformLane(show bool) {
	if t.waveLane == show {
		return
	}
	t.waveLane = show
	t.Refresh()
}

// WaveformLane reports whether the lane is shown.
func (t *timeline) WaveformLane() bool { return t.waveLane }

// SetWaveform installs the envelope to draw (a zero waveform clears it).
// The peaks may be a snapshot of a still-growing computation.
func (t *timeline) SetWaveform(w analysis.Waveform) {
	t.wave = w
	t.waveScale = waveformScale(w.Peaks)
	t.waveVersion++
	t.Refresh()
}

// Waveform returns the installed envelope.
func (t *timeline) Waveform() analysis.Waveform { return t.wave }

// renderWaveform draws the lane for the current view: one vertical
// min-to-max line per pixel column, at least one pixel tall so silence
// still shows as a faint center line.
func (t *timeline) renderWaveform(w, h int) image.Image {
	img := image.NewNRGBA(image.Rect(0, 0, max(w, 1), max(h, 1)))
	if w <= 0 || h <= 0 || len(t.wave.Peaks) == 0 {
		return img
	}
	cols := columnPeaks(t.wave.Peaks, t.wave.Bucket, t.viewNow(), w)
	scale := t.waveScale
	if scale <= 0 {
		scale = waveformMinScale
	}
	ink := waveformInk()
	mid, reach := float64(h)/2, float64(h)/2-1
	for x, col := range cols {
		if !col.ok {
			continue
		}
		top := int(mid - float64(col.Max)/scale*reach)
		bottom := int(mid - float64(col.Min)/scale*reach)
		top, bottom = min(max(top, 0), h-1), min(max(bottom, 0), h-1)
		if bottom <= top {
			bottom = min(top+1, h-1)
		}
		for y := top; y <= bottom; y++ {
			img.SetNRGBA(x, y, ink)
		}
	}
	return img
}

// waveformInk is the lane's color: the theme foreground, slightly
// translucent so it reads on the dark strip background in both variants.
func waveformInk() color.NRGBA {
	return translucent(theme.Color(theme.ColorNameForeground), 0xC0)
}

// waveSignature identifies what the lane's raster currently depicts, so
// the (comparatively expensive) re-render happens only when the envelope,
// the view, the size or the theme changed — not on every playhead move.
type waveSignature struct {
	version     uint64
	start, span time.Duration
	width       float32
	height      float32
	ink         color.NRGBA
}

// layoutWaveform places the lane and refreshes its raster when stale.
func (r *timelineRenderer) layoutWaveform(size fyne.Size, stripHeight float32) {
	t := r.t
	if !t.waveLane {
		r.waveRaster.Hidden = true
		return
	}
	r.waveRaster.Hidden = false
	r.waveRaster.Move(fyne.NewPos(0, stripHeight))
	r.waveRaster.Resize(fyne.NewSize(size.Width, size.Height-stripHeight))
	start, end := t.viewNow().window()
	sig := waveSignature{
		version: t.waveVersion, start: start, span: end - start,
		width: size.Width, height: size.Height - stripHeight, ink: waveformInk(),
	}
	if sig != r.waveSig {
		r.waveSig = sig
		r.waveRefreshes++
		r.waveRaster.Refresh()
	}
}

func newWaveRaster(t *timeline) *canvas.Raster {
	raster := canvas.NewRaster(t.renderWaveform)
	raster.ScaleMode = canvas.ImageScalePixels
	raster.Hidden = true
	return raster
}
