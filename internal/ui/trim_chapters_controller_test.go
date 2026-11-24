package ui

import (
	"strings"
	"testing"
	"time"

	"git.skobk.in/skobkin/yave/internal/model"
)

// chapterController is a trim controller (fake fetches, 1000px timeline)
// over a 100s input with three chapters, with trimming enabled and the
// default full-range selection.
func chapterController(t *testing.T) *Controller {
	t.Helper()
	c, _ := stripController(t, okThumb)
	s := time.Second
	c.media = model.MediaInfo{
		Duration: 100 * s,
		Chapters: []model.Chapter{
			{Title: "Intro", Start: 0, End: 20 * s},
			{Title: "Main", Start: 20 * s, End: 70 * s},
			{Start: 70 * s, End: 100 * s},
		},
	}
	c.trimTimeline.SetDuration(c.media.Duration)
	c.syncChapterMarkers()
	c.resetTrimSelection()
	c.trimEnabled = true
	return c
}

func TestChapterMarkersFollowTheInput(t *testing.T) {
	c := chapterController(t)
	got := c.trimTimeline.Markers(markerChapter)
	if len(got) != 3 || got[1].Label != "Main" || got[2].Label != "Chapter 3" {
		t.Fatalf("markers = %v", got)
	}
	c.resetTrimForNewInput()
	if got := c.trimTimeline.Markers(markerChapter); len(got) != 0 {
		t.Fatalf("a new input must clear the chapter markers, still have %v", got)
	}
}

func TestHoverShowsTheChapterTitle(t *testing.T) {
	c := chapterController(t)
	at := 30 * time.Second
	c.onTimelineHover(&at)
	if got := c.hoverTimeLabel.Text; !strings.Contains(got, "00:00:30.000") || !strings.Contains(got, "Main") {
		t.Errorf("hover label = %q, want the timestamp and the chapter title", got)
	}
	at = 85 * time.Second
	c.onTimelineHover(&at)
	if got := c.hoverTimeLabel.Text; !strings.Contains(got, "Chapter 3") {
		t.Errorf("untitled chapter hover = %q, want the fallback name", got)
	}
	c.media.Chapters = nil
	c.syncChapterMarkers()
	c.onTimelineHover(&at)
	if got := c.hoverTimeLabel.Text; got != "00:01:25.000" {
		t.Errorf("no chapters: hover label = %q, want the bare timestamp", got)
	}
}

func TestJumpToMarkerMovesThePlayhead(t *testing.T) {
	c := chapterController(t)
	c.trimTimeline.SetPlayhead(25 * time.Second)

	c.jumpToMarker(1)
	if got := c.trimTimeline.Playhead(); got != 70*time.Second {
		t.Fatalf("next marker from 25s = %s, want 70s", got)
	}
	c.jumpToMarker(-1)
	if got := c.trimTimeline.Playhead(); got != 20*time.Second {
		t.Fatalf("previous marker from 70s = %s, want 20s", got)
	}
	c.jumpToMarker(-1)
	c.jumpToMarker(-1) // already on the first marker (0s)
	if got := c.trimTimeline.Playhead(); got != 0 {
		t.Fatalf("playhead = %s, want 0", got)
	}
	if got := c.previewStatus.Text; got != "No earlier marker" {
		t.Errorf("status = %q, want an explanation instead of a silent no-op", got)
	}
	c.trimTimeline.SetPlayhead(70 * time.Second)
	c.jumpToMarker(1)
	if got := c.previewStatus.Text; got != "No later marker" {
		t.Errorf("status = %q", got)
	}
}

func TestJumpToMarkerNeedsTrimming(t *testing.T) {
	c := chapterController(t)
	c.trimEnabled = false
	c.jumpToMarker(1)
	if c.trimTimeline.Playhead() != 0 {
		t.Error("marker jumps are a trim action")
	}
}

func TestApplyChaptersReplacesTheDefaultSelectionWithoutAsking(t *testing.T) {
	c := chapterController(t)
	asked := false
	c.confirmHook = func(string, string, func()) { asked = true }

	c.applyChapterSelection([]int{0, 1, 2})
	if asked {
		t.Fatal("the untouched full-range selection holds no work: no confirmation")
	}
	want := []model.TimeRange{{Start: 0, End: 20 * time.Second}, {Start: 20 * time.Second, End: 70 * time.Second}, {Start: 70 * time.Second, End: 100 * time.Second}}
	if got := c.trimSelection.Segments; len(got) != 3 || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Fatalf("segments = %v, want %v", got, want)
	}
	if c.trimSelected != 0 || c.trimIn.Text != "00:00:00.000" || c.trimOut.Text != "00:00:20.000" {
		t.Errorf("entries/selection out of sync: selected %d, in %q, out %q", c.trimSelected, c.trimIn.Text, c.trimOut.Text)
	}
	if got := c.trimTimeline.Segments(); len(got) != 3 || got[1] != want[1] {
		t.Errorf("timeline segments = %v", got)
	}
	if !c.trimInValid || !c.trimOutValid {
		t.Error("replacing segments must leave both entries valid")
	}
}

func TestApplyChaptersConfirmsBeforeDiscardingEdits(t *testing.T) {
	c := chapterController(t)
	c.replaceSegments([]model.TimeRange{{Start: 5 * time.Second, End: 9 * time.Second}}, 0) // user work

	var message string
	var confirm func()
	c.confirmHook = func(_, msg string, onYes func()) { message, confirm = msg, onYes }

	c.applyChapterSelection([]int{1})
	if confirm == nil {
		t.Fatal("edited segments must be confirmed before they are replaced")
	}
	if !strings.Contains(message, "1 current") || !strings.Contains(message, "1 segment") {
		t.Errorf("confirmation text = %q", message)
	}
	if got := c.trimSelection.Segments; len(got) != 1 || got[0].Start != 5*time.Second {
		t.Fatalf("segments changed before the user confirmed: %v", got)
	}

	confirm()
	if got := c.trimSelection.Segments; len(got) != 1 || got[0].Start != 20*time.Second || got[0].End != 70*time.Second {
		t.Fatalf("after confirming: %v, want the Main chapter", got)
	}
}

func TestApplyChaptersDeclinedKeepsTheSegments(t *testing.T) {
	c := chapterController(t)
	c.replaceSegments([]model.TimeRange{{Start: 5 * time.Second, End: 9 * time.Second}}, 0)
	c.confirmHook = func(string, string, func()) {} // the user says no: onYes is never called
	c.applyChapterSelection([]int{0, 1})
	if got := c.trimSelection.Segments; len(got) != 1 || got[0].Start != 5*time.Second {
		t.Fatalf("declined replace changed the segments: %v", got)
	}
}

func TestApplyChaptersWithNothingUsableSaysSo(t *testing.T) {
	c := chapterController(t)
	c.media.Chapters = append(c.media.Chapters, model.Chapter{Title: "Marker", Start: 50 * time.Second, End: 50 * time.Second})
	before := c.trimSelection.Segments[0]
	c.applyChapterSelection([]int{3}) // only the zero-length chapter
	if c.trimSelection.Segments[0] != before || len(c.trimSelection.Segments) != 1 {
		t.Error("an empty result must leave the selection alone")
	}
	if c.previewStatus.Text == "" {
		t.Error("an empty result must be explained")
	}
}

func TestSegmentsFromChaptersUsesThePicker(t *testing.T) {
	c := chapterController(t)
	c.chapterPickerHook = func(onPicked func([]int)) { onPicked([]int{1}) }
	c.segmentsFromChapters()
	if got := c.trimSelection.Segments; len(got) != 1 || got[0].Start != 20*time.Second {
		t.Fatalf("segments = %v, want only the picked Main chapter", got)
	}

	c.trimEnabled = false
	c.chapterPickerHook = func(func([]int)) { t.Error("the picker must not open while trimming is off") }
	c.segmentsFromChapters()
}

func TestChaptersButtonAppearsOnlyForInputsWithChapters(t *testing.T) {
	c := chapterController(t)
	c.updateSegmentButtons()
	if c.trimChapters.Hidden || c.trimChapters.Disabled() {
		t.Errorf("chapters + trimming on: hidden=%v disabled=%v", c.trimChapters.Hidden, c.trimChapters.Disabled())
	}
	c.trimEnabled = false
	c.updateSegmentButtons()
	if c.trimChapters.Hidden || !c.trimChapters.Disabled() {
		t.Errorf("trimming off: hidden=%v disabled=%v, want visible but disabled", c.trimChapters.Hidden, c.trimChapters.Disabled())
	}
	c.trimEnabled = true
	c.media.Chapters = nil
	c.updateSegmentButtons()
	if !c.trimChapters.Hidden {
		t.Error("an input without chapters must not show the button")
	}
}

func TestSnapToggleDrivesTheTimeline(t *testing.T) {
	c := chapterController(t)
	if !c.trimSnap.Checked || !c.trimTimeline.snapEnabled {
		t.Fatal("snapping starts enabled")
	}
	c.trimSnap.SetChecked(false)
	if c.trimTimeline.snapEnabled {
		t.Error("unchecking Snap must turn snapping off")
	}
}
