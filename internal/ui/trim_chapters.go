package ui

import (
	"fmt"
	"slices"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"

	"git.skobk.in/skobkin/yave/internal/model"
)

// chapterLabel names a chapter for lists and tooltips: its title, or
// "Chapter N" when the container carries none.
func chapterLabel(index int, chapter model.Chapter) string {
	if chapter.Title != "" {
		return chapter.Title
	}
	return fmt.Sprintf("Chapter %d", index+1)
}

// chapterMarkers converts chapters into timeline markers.
func chapterMarkers(chapters []model.Chapter) []timelineMarker {
	markers := make([]timelineMarker, len(chapters))
	for i, ch := range chapters {
		markers[i] = timelineMarker{At: ch.Start, End: ch.End, Label: chapterLabel(i, ch)}
	}
	return markers
}

// chaptersToSegments turns the picked chapters (indexes into chapters)
// into trim segments that satisfy the selection rules: inside
// [0, duration], chronological and non-overlapping. Chapters are taken in
// start order; a chapter that overlaps the previous segment starts where
// that one ends, and one that ends up empty (zero length, or swallowed by
// an overlap) is skipped. The returned notes name every skipped chapter
// and why, so the picker can show what will not be created.
func chaptersToSegments(chapters []model.Chapter, picked []int, duration time.Duration) ([]model.TimeRange, []string) {
	order := slices.Clone(picked)
	slices.Sort(order)
	order = slices.Compact(order)
	var segments []model.TimeRange
	var skipped []string
	var previousEnd time.Duration
	for _, index := range order {
		if index < 0 || index >= len(chapters) {
			continue
		}
		chapter := chapters[index]
		name := fmt.Sprintf("%d. %s", index+1, chapterLabel(index, chapter))
		start, end := max(chapter.Start, 0), chapter.End
		if duration > 0 {
			end = min(end, duration)
		}
		if end <= start {
			skipped = append(skipped, name+": zero length")
			continue
		}
		if start < previousEnd {
			start = previousEnd
			if end <= start {
				skipped = append(skipped, name+": overlaps the previous chapter")
				continue
			}
		}
		segments = append(segments, model.TimeRange{Start: start, End: end})
		previousEnd = end
	}
	return segments, skipped
}

// isFullRangeSelection reports whether the selection is the untouched
// default: one segment covering the whole media. Replacing it destroys no
// work, so it needs no confirmation.
func isFullRangeSelection(sel model.TrimSelection, duration time.Duration) bool {
	return len(sel.Segments) == 1 && sel.Segments[0].Start == 0 && sel.Segments[0].End == duration
}

// syncChapterMarkers mirrors the inspected input's chapters onto the
// timeline.
func (c *Controller) syncChapterMarkers() {
	c.trimTimeline.SetMarkers(markerChapter, chapterMarkers(c.media.Chapters))
}

// chapterTitleAt returns the label of the chapter under a position.
func (c *Controller) chapterTitleAt(at time.Duration) (string, bool) {
	return c.trimTimeline.markerLabelAt(markerChapter, at)
}

// jumpToMarker moves the playhead to the previous (-1) or next (+1)
// marker of any layer. The move is committed like a click, so playback
// seeks and a static preview follows.
func (c *Controller) jumpToMarker(dir int) {
	if !c.trimEnabled || c.media.Duration <= 0 {
		return
	}
	times := c.trimTimeline.MarkerTimes(markerChapter, markerScene, markerKeyframe)
	at, ok := adjacentMarker(times, c.trimTimeline.Playhead(), dir)
	if !ok {
		if dir > 0 {
			c.setPreviewStatus("No later marker")
		} else {
			c.setPreviewStatus("No earlier marker")
		}
		return
	}
	c.trimTimeline.movePlayhead(at)
}

// replaceSegments installs a new segment list and selects one of its
// segments, keeping the timeline, the boundary entries, the validity
// flags and the command preview in step. It is the single funnel for
// wholesale list replacement (chapters today; undo/redo can wrap it).
func (c *Controller) replaceSegments(segments []model.TimeRange, selected int) {
	if len(segments) == 0 {
		return
	}
	c.cancelPlayback()
	segments = slices.Clone(segments)
	selected = min(max(selected, 0), len(segments)-1)
	c.trimSelection = model.TrimSelection{Segments: segments}
	c.trimSelected = selected
	c.trimInValid, c.trimOutValid = true, true
	c.trimTimeline.SetSegmentsSilent(segments)
	c.trimTimeline.SetSelected(selected)
	c.setBoundaryDisplay(c.trimIn, formatTimestamp(segments[selected].Start))
	c.setBoundaryDisplay(c.trimOut, formatTimestamp(segments[selected].End))
	c.updateTrimDurationLabel()
	c.refresh()
}

// askConfirm asks a yes/no question; tests replace confirmHook.
func (c *Controller) askConfirm(title, message string, onYes func()) {
	if c.confirmHook != nil {
		c.confirmHook(title, message, onYes)
		return
	}
	dialog.NewConfirm(title, message, func(ok bool) {
		if ok {
			onYes()
		}
	}, c.window).Show()
}

// applyChapterSelection replaces the segment list with the picked
// chapters, asking first when that would discard edited segments.
func (c *Controller) applyChapterSelection(picked []int) {
	segments, _ := chaptersToSegments(c.media.Chapters, picked, c.media.Duration)
	if len(segments) == 0 {
		c.setPreviewStatus("The selected chapters produce no segments")
		return
	}
	apply := func() { c.replaceSegments(segments, 0) }
	if !c.trimSelection.Active() || isFullRangeSelection(c.trimSelection, c.media.Duration) {
		apply()
		return
	}
	c.askConfirm("Replace segments?",
		fmt.Sprintf("Replace the %d current segment(s) with %d segment(s) from chapters?\nThe current segments will be lost.",
			len(c.trimSelection.Segments), len(segments)),
		apply)
}

// segmentsFromChapters is the "Segments from chapters…" button: it lets
// the user pick chapters, then applies them.
func (c *Controller) segmentsFromChapters() {
	if !c.trimEnabled || len(c.media.Chapters) == 0 {
		return
	}
	if c.chapterPickerHook != nil {
		c.chapterPickerHook(c.applyChapterSelection)
		return
	}
	c.showChapterPicker(c.applyChapterSelection)
}

// showChapterPicker lists the chapters with check boxes (all checked) and
// a live summary of the segments the choice would create.
func (c *Controller) showChapterPicker(onPicked func(picked []int)) {
	chapters := c.media.Chapters
	options := make([]string, len(chapters))
	for i, ch := range chapters {
		options[i] = fmt.Sprintf("%d. %s  (%s – %s)", i+1, chapterLabel(i, ch), formatTimestamp(ch.Start), formatTimestamp(ch.End))
	}
	group := widget.NewCheckGroup(options, nil)
	group.SetSelected(options)
	summary := widget.NewLabel("")
	summary.Wrapping = fyne.TextWrapWord
	pickedIndexes := func() []int {
		var picked []int
		for i, option := range options {
			if slices.Contains(group.Selected, option) {
				picked = append(picked, i)
			}
		}
		return picked
	}
	update := func() {
		segments, skipped := chaptersToSegments(chapters, pickedIndexes(), c.media.Duration)
		text := fmt.Sprintf("%d segment(s) will be created.", len(segments))
		for _, note := range skipped {
			text += "\nSkipped " + note
		}
		summary.SetText(text)
	}
	group.OnChanged = func([]string) { update() }
	update()

	selectAll := widget.NewButton("Select all", func() { group.SetSelected(options) })
	selectNone := widget.NewButton("Select none", func() { group.SetSelected(nil) })
	list := container.NewVScroll(group)
	list.SetMinSize(fyne.NewSize(460, 260))
	content := container.NewBorder(container.NewHBox(selectAll, selectNone), summary, nil, nil, list)
	dialog.NewCustomConfirm("Segments from chapters", "Create segments", "Cancel", content, func(ok bool) {
		if ok {
			onPicked(pickedIndexes())
		}
	}, c.window).Show()
}
