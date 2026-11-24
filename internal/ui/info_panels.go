package ui

import (
	"context"
	"image"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"git.skobk.in/skobkin/yave/internal/frames"
	"git.skobk.in/skobkin/yave/internal/model"
)

// The Input tab's FFmpeg and Media panels. Each is a rounded surface whose
// body is swapped wholesale between states (status, error, result);
// action buttons live outside the body so they survive the swaps.

// infoPanel is a swappable panel body plus a plain-text mirror of what it
// shows (tests and diagnostics read it).
type infoPanel struct {
	body *fyne.Container
	text string
}

func newInfoPanel() *infoPanel { return &infoPanel{body: container.NewStack()} }

// String returns the plain text of what the panel currently shows.
func (p *infoPanel) String() string { return p.text }

func (p *infoPanel) show(text string, content fyne.CanvasObject) {
	p.text = text
	p.body.Objects = []fyne.CanvasObject{content}
	p.body.Refresh()
}

const (
	// The media panel's preview box is 16:9 and posterWidthShare of the
	// panel, between posterMinWidth and posterMaxWidth wide.
	// posterFetchHeight is the extracted frame height: about twice the
	// largest box, for hi-DPI screens.
	posterMinWidth    = 160
	posterMaxWidth    = 360
	posterWidthShare  = 0.3
	posterFetchHeight = 400
	// emptyStateHeight keeps the drop-zone panel from collapsing to a
	// single line while no input is selected.
	emptyStateHeight = 180
)

func titleLabel(text string) *widget.Label {
	return widget.NewLabelWithStyle(text, fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
}

// infoTable renders rows as aligned columns: [icon,] muted label, bold
// primary value (codec), details. Details wrap in the last column.
func infoTable(rows []infoRow, withIcons bool, extra ...[]fyne.CanvasObject) fyne.CanvasObject {
	cols := 3
	if withIcons {
		cols = 4
	}
	cells := make([]fyne.CanvasObject, 0, (len(rows)+len(extra))*cols)
	for _, r := range rows {
		if withIcons {
			cells = append(cells, widget.NewIcon(rowIcon(r.Kind)))
		}
		var details fyne.CanvasObject = plainText(r.Details, true)
		if r.Muted {
			details = mutedText(r.Details, true)
		}
		cells = append(cells, mutedText(r.Label, false), primaryText(r.Primary), details)
	}
	// extra rows are caller-built cells, cols per row.
	for _, row := range extra {
		cells = append(cells, row...)
	}
	return compact(container.New(tableLayout{cols: cols}, cells...))
}

// primaryText is the bold primary-value cell; hidden when empty so the
// column takes no width for rows without one.
func primaryText(text string) fyne.CanvasObject {
	l := widget.NewLabelWithStyle(text, fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	if text == "" {
		l.Hide()
	}
	return l
}

func rowIcon(kind rowKind) fyne.Resource {
	switch kind {
	case rowVideo:
		return theme.MediaVideoIcon()
	case rowCoverArt:
		return theme.MediaPhotoIcon()
	case rowAudio:
		return theme.MediaMusicIcon()
	case rowSubtitles:
		return theme.FileTextIcon()
	case rowChapters:
		return theme.ListIcon()
	default:
		return nil
	}
}

func badges(texts []string, importance func(string) widget.Importance) fyne.CanvasObject {
	items := make([]*badge, len(texts))
	for i, t := range texts {
		items[i] = newBadge(t, importance(t))
	}
	return badgeRow(items...)
}

// --- FFmpeg panel ---

func (c *Controller) setDetectionStatus(text string) {
	c.detectionCopy = text
	c.detection.show(text, container.NewVBox(titleLabel("FFmpeg"), compact(mutedText(text, true))))
}

func (c *Controller) setDetectionError(text string) {
	c.detectionCopy = text
	msg := plainText(text, true)
	msg.Importance = widget.DangerImportance
	c.detection.show(text, container.NewVBox(titleLabel("FFmpeg"), compact(msg)))
}

func (c *Controller) setDetectionResult(caps model.Capabilities) {
	view := detectionView(caps)
	c.detectionCopy = detectionCopyText(caps)
	header := container.NewHBox(titleLabel(view.Title), badges(view.Badges, func(b string) widget.Importance {
		if b == softwareOnlyBadge {
			return widget.MediumImportance
		}
		return widget.SuccessImportance
	}))
	var extra [][]fyne.CanvasObject
	if len(view.GPUs) > 0 {
		pills := make([]fyne.CanvasObject, len(view.GPUs))
		for i, g := range view.GPUs {
			importance := widget.MediumImportance
			if g.Warning {
				importance = widget.WarningImportance
			}
			pills[i] = newTipBadge(c.tips, g.Text, g.Tip, importance)
		}
		extra = append(extra, []fyne.CanvasObject{
			mutedText("GPUs", false), primaryText(""),
			container.New(layout.NewRowWrapLayoutWithCustomPadding(6, 4), pills...),
		})
	}
	content := container.NewVBox(header)
	if len(view.Rows) > 0 || len(extra) > 0 {
		content.Add(infoTable(view.Rows, false, extra...))
	}
	c.detection.show(view.String(), content)
}

// --- Media panel ---

// setMediaEmpty shows the drop-zone empty state.
func (c *Controller) setMediaEmpty() {
	const text = "Drop a video file here"
	c.mediaInfoCopy = ""
	c.copyMediaInfo.Hide()
	icon := container.NewGridWrap(fyne.NewSize(48, 48), widget.NewIcon(theme.FileVideoIcon()))
	hint := mutedText("or choose one with Browse… above", false)
	zone := container.NewVBox(
		container.NewCenter(icon),
		widget.NewLabelWithStyle(text, fyne.TextAlignCenter, fyne.TextStyle{Bold: true}),
		container.NewCenter(hint),
	)
	c.mediaInfo.show(text, container.NewStack(
		container.NewGridWrap(fyne.NewSize(1, emptyStateHeight)),
		container.NewCenter(zone),
	))
}

func (c *Controller) setMediaInspecting() {
	const text = "Inspecting input..."
	c.mediaInfoCopy = text
	c.copyMediaInfo.Hide()
	activity := widget.NewActivity()
	activity.Start()
	c.mediaInfo.show(text, container.NewStack(
		container.NewGridWrap(fyne.NewSize(1, emptyStateHeight)),
		container.NewCenter(container.NewHBox(activity, widget.NewLabel(text))),
	))
}

func (c *Controller) setMediaError(text string) {
	c.mediaInfoCopy = text
	c.copyMediaInfo.Show()
	msg := plainText(text, true)
	msg.Importance = widget.DangerImportance
	c.mediaInfo.show(text, container.NewBorder(nil, nil,
		container.NewVBox(widget.NewIcon(theme.ErrorIcon())), nil, msg))
}

func (c *Controller) setMediaResult(info model.MediaInfo) {
	view := mediaInfoView(info)
	c.mediaInfoCopy = formatMediaInfo(info)
	c.copyMediaInfo.Show()

	c.posterImage = canvas.NewImageFromImage(nil)
	c.posterImage.FillMode = canvas.ImageFillContain
	c.posterImage.ScaleMode = canvas.ImageScaleSmooth
	placeholder := theme.MediaVideoIcon()
	if len(info.VideoStreams) == 0 {
		placeholder = theme.MediaMusicIcon()
	}
	c.posterIcon = widget.NewIcon(placeholder)
	poster := container.NewStack(
		newThemedRect(theme.ColorNameBackground),
		container.NewCenter(container.NewGridWrap(fyne.NewSize(32, 32), c.posterIcon)),
		c.posterImage,
	)

	title := titleLabel(view.Title)
	title.SizeName = theme.SizeNameSubHeadingText
	// A long file name must not widen the window.
	title.Truncation = fyne.TextTruncateEllipsis
	facts := container.NewVBox(title, mutedText(view.Summary, true))
	if len(view.Badges) > 0 {
		facts.Add(badges(view.Badges, func(b string) widget.Importance {
			if strings.HasPrefix(b, "HDR") {
				return widget.HighImportance
			}
			return widget.MediumImportance
		}))
	}
	details := container.NewVBox(compact(facts), infoTable(view.Rows, true))
	c.mediaInfo.show(view.String(), container.New(&posterSideLayout{}, details, poster))
}

// posterSideLayout puts objects[0] (the media facts) on the left and
// objects[1] (the preview) on the right, the preview a 16:9 box sized
// by the panel width. MinSize reports the height of the box at the last
// laid-out width, so a tall preview beside few facts is not clipped.
type posterSideLayout struct{ width float32 }

const posterGap = 12

func posterBoxSize(width float32) fyne.Size {
	w := min(max(width*posterWidthShare, posterMinWidth), posterMaxWidth)
	return fyne.NewSize(w, w*9/16)
}

func (l *posterSideLayout) Layout(objects []fyne.CanvasObject, size fyne.Size) {
	l.width = size.Width
	box := posterBoxSize(size.Width)
	left := max(size.Width-box.Width-posterGap, 0)
	objects[0].Move(fyne.NewPos(0, 0))
	objects[0].Resize(fyne.NewSize(left, size.Height))
	objects[1].Move(fyne.NewPos(left+posterGap, 0))
	objects[1].Resize(box)
}

func (l *posterSideLayout) MinSize(objects []fyne.CanvasObject) fyne.Size {
	facts := objects[0].MinSize()
	return fyne.NewSize(facts.Width+posterGap+posterMinWidth, max(facts.Height, posterBoxSize(l.width).Height))
}

// --- poster frame ---

// posterPosition picks the poster frame: a tenth into the media, which
// skips the black or title frames videos commonly open with; the start
// when the duration is unknown.
func posterPosition(duration time.Duration) time.Duration {
	if duration <= 0 {
		return 0
	}
	return duration / 10
}

func (c *Controller) cancelPoster() {
	if c.posterCancel != nil {
		c.posterCancel()
		c.posterCancel = nil
	}
}

// startPoster extracts the media panel's thumbnail: one cancellable
// ffmpeg run per inspected input (cover art for audio files). A failure
// leaves the placeholder icon; a result for an older input is dropped.
func (c *Controller) startPoster(info model.MediaInfo) {
	c.cancelPoster()
	if c.posterFetch == nil || (len(info.VideoStreams) == 0 && len(info.CoverArtStreams) == 0) {
		return
	}
	at := time.Duration(0)
	if len(info.VideoStreams) > 0 {
		at = posterPosition(info.Duration)
	}
	ctx, cancel := context.WithCancel(context.Background())
	c.posterCancel = cancel
	gen := c.inputGeneration
	ext := c.ensureExtractor()
	path := c.inputPath
	fetch := c.posterFetch
	go func() {
		frame, err := fetch(ctx, ext, path, at, posterFetchHeight)
		c.dispatchUI(func() {
			if c.inputGeneration != gen || err != nil || ctx.Err() != nil {
				return
			}
			c.showPoster(frame.Image)
		})
	}()
}

func (c *Controller) showPoster(img image.Image) {
	if c.posterImage == nil || img == nil {
		return
	}
	c.posterIcon.Hide()
	c.posterImage.Image = img
	c.posterImage.Refresh()
}

// defaultPosterFetch extracts the poster through the shared extractor.
func defaultPosterFetch(ctx context.Context, ext *frames.Extractor, path string, at time.Duration, height int) (frames.Frame, error) {
	return ext.Thumbnail(ctx, path, at, height)
}

// inputSection lays out the Input tab: the file row first, the media
// panel filling (and scrolling) the middle, and the FFmpeg panel pinned
// to the bottom.
func (c *Controller) inputSection(inputRow fyne.CanvasObject) fyne.CanvasObject {
	actions := func(objects ...fyne.CanvasObject) fyne.CanvasObject {
		return container.NewVBox(container.NewHBox(objects...))
	}
	media := newSurface(container.NewBorder(nil, nil, nil, actions(c.copyMediaInfo), c.mediaInfo.body))
	ffmpeg := newSurface(container.NewBorder(nil, nil, nil,
		actions(c.detectActivity, c.retryDetect, c.copyDetection), c.detection.body))
	return container.NewBorder(inputRow, ffmpeg, nil, nil,
		container.NewVScroll(container.NewVBox(media, layout.NewSpacer())))
}
