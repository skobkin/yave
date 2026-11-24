package ui

import (
	"context"
	"errors"
	"image"
	"strings"
	"sync"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/widget"

	"git.skobk.in/skobkin/yave/internal/frames"
	"git.skobk.in/skobkin/yave/internal/model"
)

func TestTableLayoutAlignsColumns(t *testing.T) {
	_, _, _ = exportTestController(t) // a running test app for widget sizing
	cells := []fyne.CanvasObject{
		widget.NewLabel("Video"), widget.NewLabel("HEVC"), widget.NewLabel("1920×1080"),
		widget.NewLabel("Audio 12"), widget.NewLabel("E-AC-3"), widget.NewLabel("48 kHz"),
		widget.NewLabel("Chapters"), hiddenLabel(), widget.NewLabel("None"),
	}
	l := tableLayout{cols: 3}
	l.Layout(cells, fyne.NewSize(600, 300))
	for col := range 3 {
		x := cells[col].Position().X
		for row := 1; row < 3; row++ {
			cell := cells[row*3+col]
			if cell.Visible() && cell.Position().X != x {
				t.Errorf("row %d column %d starts at %v, want %v (aligned with row 0)", row, col, cell.Position().X, x)
			}
		}
	}
	last := cells[2]
	if got := last.Position().X + last.Size().Width; got != 600 {
		t.Errorf("last column ends at %v, want the full width 600", got)
	}
	if cells[3].Position().Y <= cells[0].Position().Y {
		t.Error("rows must stack downwards")
	}
	widest := max(cells[0].MinSize().Width, cells[3].MinSize().Width, cells[6].MinSize().Width)
	if cells[1].Position().X != widest+tableColumnGap {
		t.Errorf("second column at %v, want the widest label %v plus the gap", cells[1].Position().X, widest)
	}
	if minSize := l.MinSize(cells); minSize.Height <= 0 || minSize.Width < widest {
		t.Errorf("MinSize = %v, want room for the label column", minSize)
	}
}

func hiddenLabel() fyne.CanvasObject {
	l := widget.NewLabel("")
	l.Hide()
	return l
}

type posterSpy struct {
	mu      sync.Mutex
	calls   int
	at      time.Duration
	height  int
	path    string
	img     image.Image
	failure error
}

func (s *posterSpy) fetch(_ context.Context, _ *frames.Extractor, path string, at time.Duration, height int) (frames.Frame, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	s.at, s.height, s.path = at, height, path
	return frames.Frame{Image: s.img}, s.failure
}

func TestPosterShowsAFrameATenthIntoTheVideo(t *testing.T) {
	c, _, updates := exportTestController(t)
	spy := &posterSpy{img: image.NewRGBA(image.Rect(0, 0, 32, 18))}
	c.posterFetch = spy.fetch
	info := model.MediaInfo{Path: c.inputPath, Duration: 100 * time.Second, VideoStreams: []model.VideoStream{{Codec: "h264"}}}
	c.setMediaResult(info)
	c.startPoster(info)
	(<-updates)()
	if spy.calls != 1 || spy.at != 10*time.Second || spy.height != posterFetchHeight || spy.path != c.inputPath {
		t.Fatalf("fetch = %d calls at %v height %d path %q", spy.calls, spy.at, spy.height, spy.path)
	}
	if c.posterImage.Image != spy.img || c.posterIcon.Visible() {
		t.Fatal("the extracted frame must replace the placeholder icon")
	}
}

func TestPosterResultForAnOlderInputIsDropped(t *testing.T) {
	c, _, updates := exportTestController(t)
	spy := &posterSpy{img: image.NewRGBA(image.Rect(0, 0, 4, 4))}
	c.posterFetch = spy.fetch
	info := model.MediaInfo{Duration: time.Minute, VideoStreams: []model.VideoStream{{}}}
	c.setMediaResult(info)
	c.startPoster(info)
	c.inputGeneration++ // the user picked another file meanwhile
	(<-updates)()
	if c.posterImage.Image != nil || !c.posterIcon.Visible() {
		t.Fatal("a stale poster must not be shown")
	}
}

func TestPosterFailureKeepsThePlaceholder(t *testing.T) {
	c, _, updates := exportTestController(t)
	spy := &posterSpy{failure: errors.New("no frame")}
	c.posterFetch = spy.fetch
	info := model.MediaInfo{VideoStreams: []model.VideoStream{{}}}
	c.setMediaResult(info)
	c.startPoster(info)
	(<-updates)()
	if spy.at != 0 {
		t.Errorf("unknown duration must take the first frame, got %v", spy.at)
	}
	if c.posterImage.Image != nil || !c.posterIcon.Visible() {
		t.Fatal("a failed extraction must keep the placeholder")
	}
}

func TestPosterSourceSelection(t *testing.T) {
	c, _, updates := exportTestController(t)
	spy := &posterSpy{}
	c.posterFetch = spy.fetch

	// Audio only: nothing to show, no ffmpeg run.
	c.startPoster(model.MediaInfo{Duration: time.Minute, AudioStreams: []model.AudioStream{{}}})
	if spy.calls != 0 || c.posterCancel != nil {
		t.Fatal("audio without cover art must not extract a poster")
	}
	// Cover art: its single frame, at the start.
	cover := model.MediaInfo{Duration: time.Minute, CoverArtStreams: []model.VideoStream{{}}}
	c.setMediaResult(cover)
	c.startPoster(cover)
	(<-updates)()
	if spy.calls != 1 || spy.at != 0 {
		t.Fatalf("cover art fetch = %d calls at %v, want one at 0", spy.calls, spy.at)
	}
	// Disabled extraction (nil seam) is a no-op.
	c.posterFetch = nil
	c.startPoster(model.MediaInfo{VideoStreams: []model.VideoStream{{}}})
	if spy.calls != 1 {
		t.Fatal("a nil posterFetch must not extract")
	}
}

func TestMediaPanelStates(t *testing.T) {
	c, _, _ := exportTestController(t)
	if !strings.Contains(c.mediaInfo.String(), "Drop a video") || c.copyMediaInfo.Visible() {
		t.Fatalf("initial state = %q (copy visible %v), want the drop zone without Copy", c.mediaInfo.String(), c.copyMediaInfo.Visible())
	}
	c.setMediaInspecting()
	if !strings.Contains(c.mediaInfo.String(), "Inspecting") || c.copyMediaInfo.Visible() {
		t.Fatal("inspecting must say so and hide Copy")
	}
	c.setMediaError("Inspection failed: boom")
	if c.mediaInfo.String() != "Inspection failed: boom" || !c.copyMediaInfo.Visible() || c.mediaInfoCopy != "Inspection failed: boom" {
		t.Fatal("an inspection error must be shown and copyable")
	}
	info := model.MediaInfo{Path: "/v/clip.mkv", Format: "matroska,webm", AudioStreams: []model.AudioStream{{Codec: "opus"}}}
	c.setMediaResult(info)
	if !strings.Contains(c.mediaInfo.String(), "clip.mkv") || c.mediaInfoCopy != formatMediaInfo(info) {
		t.Fatalf("result = %q / copy %q", c.mediaInfo.String(), c.mediaInfoCopy)
	}
}

func TestFFmpegPanelStates(t *testing.T) {
	c, _, _ := exportTestController(t)
	if c.detection.String() != "Detecting FFmpeg..." {
		t.Fatalf("initial = %q", c.detection.String())
	}
	c.setDetectionError("Detection failed: missing")
	if c.detectionCopy != "Detection failed: missing" || c.detection.String() != c.detectionCopy {
		t.Fatal("a detection error must be shown and copyable")
	}
	caps := model.Capabilities{FFmpegVersion: "ffmpeg version 9.1 x", FFprobeVersion: "ffprobe version 9.1 x", FFmpegPath: "/x/ffmpeg"}
	c.setDetectionResult(caps)
	if !strings.Contains(c.detection.String(), "FFmpeg 9.1") || !strings.Contains(c.detectionCopy, "/x/ffmpeg") {
		t.Fatalf("result = %q / copy %q", c.detection.String(), c.detectionCopy)
	}
}
