package ui

import (
	"image/color"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	fynetest "fyne.io/fyne/v2/test"

	"git.skobk.in/skobkin/yave/internal/analysis"
)

func peak(min, max int16) analysis.Peak { return analysis.Peak{Min: min, Max: max} }

func TestColumnPeaks(t *testing.T) {
	// Ten 1s buckets whose Max is their index+1 and Min its negation.
	peaks := make([]analysis.Peak, 10)
	for i := range peaks {
		peaks[i] = peak(-int16(i+1), int16(i+1)) //nolint:gosec // G115: tiny test values
	}
	bucket := time.Second
	fit := newTimelineView(10 * time.Second)

	t.Run("zoomed out folds several buckets into a column", func(t *testing.T) {
		got := columnPeaks(peaks, bucket, fit, 5) // 2 buckets per column
		want := []analysis.Peak{peak(-2, 2), peak(-4, 4), peak(-6, 6), peak(-8, 8), peak(-10, 10)}
		for i, w := range want {
			if !got[i].ok || got[i].Peak != w {
				t.Errorf("column %d = %+v, want %+v", i, got[i], w)
			}
		}
	})
	t.Run("zoomed in repeats a bucket across columns", func(t *testing.T) {
		view := timelineView{duration: 10 * time.Second, start: 3 * time.Second, span: 2 * time.Second} // buckets 3 and 4
		got := columnPeaks(peaks, bucket, view, 8)
		for x := range 4 {
			if got[x].Peak != peak(-4, 4) {
				t.Errorf("column %d = %+v, want bucket 3", x, got[x])
			}
		}
		for x := 4; x < 8; x++ {
			if got[x].Peak != peak(-5, 5) {
				t.Errorf("column %d = %+v, want bucket 4", x, got[x])
			}
		}
	})
	t.Run("columns past the computed audio are not drawn", func(t *testing.T) {
		got := columnPeaks(peaks[:4], bucket, fit, 10) // only 4 of 10 seconds computed
		for x := range 4 {
			if !got[x].ok {
				t.Errorf("column %d should have data", x)
			}
		}
		for x := 4; x < 10; x++ {
			if got[x].ok {
				t.Errorf("column %d is beyond the audio and must be empty", x)
			}
		}
	})
	t.Run("degenerate inputs", func(t *testing.T) {
		if columnPeaks(peaks, bucket, fit, 0) != nil || columnPeaks(peaks, 0, fit, 5) != nil {
			t.Error("no columns or no bucket yields nothing")
		}
		if got := columnPeaks(nil, bucket, fit, 3); len(got) != 3 || got[0].ok {
			t.Errorf("no peaks: %+v", got)
		}
		// A bucket coarser than the whole view still fills every column.
		coarse := []analysis.Peak{peak(-7, 7)}
		got := columnPeaks(coarse, 10*time.Second, timelineView{duration: 10 * time.Second, start: 2 * time.Second, span: 3 * time.Second}, 4)
		for x, c := range got {
			if !c.ok || c.Peak != peak(-7, 7) {
				t.Errorf("column %d = %+v", x, c)
			}
		}
	})
	t.Run("a fractional bucket boundary includes both neighbors", func(t *testing.T) {
		// Column 0 covers [0.5s, 1.5s): buckets 0 and 1.
		view := timelineView{duration: 10 * time.Second, start: 500 * time.Millisecond, span: time.Second}
		got := columnPeaks(peaks, bucket, view, 1)
		if got[0].Peak != peak(-2, 2) {
			t.Errorf("column = %+v, want buckets 0 and 1 merged", got[0])
		}
	})
}

func TestWaveformScale(t *testing.T) {
	if got := waveformScale(nil); got != waveformMinScale {
		t.Errorf("no peaks scale = %v, want the floor", got)
	}
	if got := waveformScale([]analysis.Peak{peak(-100, 200)}); got != waveformMinScale {
		t.Errorf("quiet audio scale = %v, want the floor (never amplified to full height)", got)
	}
	if got := waveformScale([]analysis.Peak{peak(-20000, 5000), peak(-1, 12000)}); got != 20000 {
		t.Errorf("scale = %v, want the loudest sample 20000", got)
	}
	if got := waveformScale([]analysis.Peak{peak(-32768, 0)}); got != 32768 {
		t.Errorf("scale = %v, want 32768", got)
	}
}

// alphaAt reports whether the rendered lane has ink at a pixel.
func inked(img interface {
	At(x, y int) color.Color
}, x, y int) bool {
	_, _, _, a := img.At(x, y).RGBA()
	return a != 0
}

func TestRenderWaveform(t *testing.T) {
	tl := newSizedTimeline(t)
	tl.SetDuration(10 * time.Second)
	peaks := make([]analysis.Peak, 10)
	for i := range peaks {
		peaks[i] = peak(0, 0) // silence...
	}
	peaks[5] = peak(-16000, 16000) // ...with a loud second at 5s
	tl.SetWaveform(analysis.Waveform{Bucket: time.Second, Peaks: peaks})

	const w, h = 100, 40 // 10px per second
	img := tl.renderWaveform(w, h)
	if img.Bounds().Dx() != w || img.Bounds().Dy() != h {
		t.Fatalf("image size = %v", img.Bounds())
	}
	mid := h / 2
	// Silence: a faint center line, nothing near the top.
	if !inked(img, 15, mid) || !inked(img, 15, mid-1) && !inked(img, 15, mid+1) {
		t.Error("silence must draw a one-pixel center line")
	}
	if inked(img, 15, 2) || inked(img, 15, h-3) {
		t.Error("silence must not reach the lane edges")
	}
	// The loud second (columns 50-59): tall line, centered, symmetric.
	for _, y := range []int{4, 10, mid, h - 11, h - 5} {
		if !inked(img, 55, y) {
			t.Errorf("loud column has no ink at y=%d", y)
		}
	}
	// Loudest = full height: the loud peak defines the scale, so it fills
	// the lane (within the 1px margin).
	if inked(img, 55, 0) && inked(img, 55, h-1) {
		t.Error("the envelope must keep a margin inside the lane")
	}
	if !inked(img, 55, 1) || !inked(img, 55, h-2) {
		t.Error("the loudest column must reach the lane margin")
	}

	// Zoomed onto [5s, 6s) the loud bucket spans the whole width.
	tl.view = timelineView{duration: 10 * time.Second, start: 5 * time.Second, span: time.Second}
	zoomed := tl.renderWaveform(w, h)
	for _, x := range []int{0, 50, 99} {
		if !inked(zoomed, x, 4) {
			t.Errorf("zoomed render has no loud ink at column %d", x)
		}
	}
}

func TestRenderWaveformEmptyAndPartial(t *testing.T) {
	tl := newSizedTimeline(t)
	tl.SetDuration(10 * time.Second)
	empty := tl.renderWaveform(50, 20)
	for x := range 50 {
		for y := range 20 {
			if inked(empty, x, y) {
				t.Fatalf("no waveform must render nothing, found ink at (%d,%d)", x, y)
			}
		}
	}
	if img := tl.renderWaveform(0, 0); img == nil {
		t.Error("a degenerate size must still return an image")
	}

	// Only the first 3 of 10 seconds are computed: columns after that stay empty.
	tl.SetWaveform(analysis.Waveform{Bucket: time.Second, Peaks: []analysis.Peak{peak(-9000, 9000), peak(-9000, 9000), peak(-9000, 9000)}})
	partial := tl.renderWaveform(100, 40)
	if !inked(partial, 20, 20) {
		t.Error("computed audio must be drawn")
	}
	for _, x := range []int{40, 70, 99} {
		if inked(partial, x, 20) {
			t.Errorf("column %d lies beyond the computed audio", x)
		}
	}
}

func TestWaveformLaneLayout(t *testing.T) {
	tl := newSizedTimeline(t) // 200px wide, timelineMinHeight tall
	tl.SetDuration(10 * time.Second)
	tl.SetThumbnailGrid(0, 5*time.Second, 2)
	tl.Resize(fyne.NewSize(200, timelineMinHeight+waveformLaneHeight))
	r := fynetest.TempWidgetRenderer(t, tl).(*timelineRenderer)

	if got := r.MinSize().Height; got != timelineMinHeight {
		t.Errorf("min height without the lane = %v", got)
	}
	if r.Layout(tl.Size()); !r.waveRaster.Hidden {
		t.Error("the lane starts hidden")
	}

	tl.SetWaveformLane(true)
	if got := r.MinSize().Height; got != timelineMinHeight+waveformLaneHeight {
		t.Errorf("min height with the lane = %v, want %v", got, timelineMinHeight+waveformLaneHeight)
	}
	r.Layout(tl.Size())
	if r.waveRaster.Hidden {
		t.Fatal("the lane must show")
	}
	if pos, size := r.waveRaster.Position(), r.waveRaster.Size(); pos.Y != timelineMinHeight || size.Height != waveformLaneHeight || size.Width != 200 {
		t.Errorf("lane at %v size %v, want y=%d h=%d w=200", pos, size, timelineMinHeight, waveformLaneHeight)
	}
	// The thumbnails keep their height; they do not run under the lane.
	tl.SetThumbnail(0, nil)
	for i, obj := range r.thumbs {
		if got := obj.Size().Height; got != timelineMinHeight {
			t.Errorf("thumbnail %d height = %v, want %d (the lane takes the rest)", i, got, timelineMinHeight)
		}
	}
	tl.SetWaveformLane(false)
	r.Layout(tl.Size())
	if !r.waveRaster.Hidden {
		t.Error("the lane must hide again")
	}
}

func TestWaveformRasterRefreshesOnlyWhenStale(t *testing.T) {
	tl := newSizedTimeline(t)
	tl.SetDuration(100 * time.Second)
	tl.Resize(fyne.NewSize(200, timelineMinHeight+waveformLaneHeight))
	tl.SetWaveformLane(true)
	tl.SetWaveform(analysis.Waveform{Bucket: time.Second, Peaks: make([]analysis.Peak, 100)})
	r := fynetest.TempWidgetRenderer(t, tl).(*timelineRenderer)
	r.Layout(tl.Size())
	base := r.waveRefreshes
	if base == 0 {
		t.Fatal("the first layout must render the lane")
	}

	// Playback moves the playhead every frame: that must not re-render.
	for i := range 50 {
		tl.SetPlayhead(time.Duration(i) * 100 * time.Millisecond)
		r.Layout(tl.Size())
	}
	if r.waveRefreshes != base {
		t.Errorf("the playhead caused %d re-renders", r.waveRefreshes-base)
	}

	// New peaks, a zoom and a resize each do.
	tl.SetWaveform(analysis.Waveform{Bucket: time.Second, Peaks: make([]analysis.Peak, 100)})
	r.Layout(tl.Size())
	if r.waveRefreshes != base+1 {
		t.Errorf("new peaks: %d refreshes, want %d", r.waveRefreshes, base+1)
	}
	tl.ZoomStep(2)
	r.Layout(tl.Size())
	if r.waveRefreshes != base+2 {
		t.Errorf("zoom: %d refreshes, want %d", r.waveRefreshes, base+2)
	}
	tl.Resize(fyne.NewSize(300, timelineMinHeight+waveformLaneHeight))
	r.Layout(tl.Size())
	if r.waveRefreshes != base+3 {
		t.Errorf("resize: %d refreshes, want %d", r.waveRefreshes, base+3)
	}
}
