package ui

import (
	"testing"
	"time"

	"fyne.io/fyne/v2/widget"

	"git.skobk.in/skobkin/yave/internal/model"
)

func TestSegmentListTexts(t *testing.T) {
	t.Parallel()
	segs := []model.TimeRange{{Start: time.Second, End: 3 * time.Second}, {Start: 10 * time.Second, End: 12500 * time.Millisecond}}
	if got := segmentRowText(1, segs[1]); got != "#2   00:00:10.000 → 00:00:12.500   (00:00:02.500)" {
		t.Fatalf("row = %q", got)
	}
	if got := segmentListHeader(segs); got != "Segments: 2 — total 00:00:04.500" {
		t.Fatalf("header = %q", got)
	}
	if got := segmentListHeader(nil); got != "Segments: 0 — total 00:00:00.000" {
		t.Fatalf("empty header = %q", got)
	}
}

func segmentListController(t *testing.T) *Controller {
	t.Helper()
	c, _, _ := exportTestController(t)
	c.buildSegmentPanel()
	c.trimTimeline.SetDuration(30 * time.Second)
	segs := []model.TimeRange{{Start: time.Second, End: 3 * time.Second}, {Start: 10 * time.Second, End: 12 * time.Second}}
	setTrimSegments(c, segs...)
	c.trimTimeline.SetSegmentsSilent(segs)
	c.updateSegmentList()
	return c
}

func TestSegmentListFollowsSelectionState(t *testing.T) {
	c := segmentListController(t)
	if !c.segmentPanel.Visible() || len(c.segmentRowViews) != 2 {
		t.Fatalf("panel visible=%v rows=%d", c.segmentPanel.Visible(), len(c.segmentRowViews))
	}
	if c.segmentRowViews[0].pick.Importance != widget.HighImportance || c.segmentRowViews[1].pick.Importance != widget.LowImportance {
		t.Fatal("the selected row must be highlighted")
	}

	c.trimSelection.Segments[1].End = 15 * time.Second // as after a handle drag
	c.updateSegmentList()
	if got := c.segmentRowViews[1].pick.Text; got != segmentRowText(1, c.trimSelection.Segments[1]) {
		t.Fatalf("row not updated live: %q", got)
	}

	c.trimEnabled = false
	c.updateSegmentList()
	if c.segmentPanel.Visible() {
		t.Fatal("the list is hidden while trimming is off")
	}
}

func TestSegmentRowClickSelectsAndSeeks(t *testing.T) {
	c := segmentListController(t)
	c.segmentRowViews[1].pick.OnTapped()
	if c.trimSelected != 1 || c.trimTimeline.Selected() != 1 {
		t.Fatalf("selected = %d / timeline %d, want 1", c.trimSelected, c.trimTimeline.Selected())
	}
	if got := c.trimTimeline.Playhead(); got != 10*time.Second {
		t.Fatalf("playhead = %v, want the segment's In point", got)
	}
}

func TestSegmentRowDeleteRemovesThatSegment(t *testing.T) {
	c := segmentListController(t)
	c.segmentRowViews[0].remove.OnTapped()
	if len(c.trimSelection.Segments) != 1 || c.trimSelection.Segments[0].Start != 10*time.Second {
		t.Fatalf("segments = %v", c.trimSelection.Segments)
	}
	c.updateSegmentList()
	if len(c.segmentRowViews) != 1 || c.segmentRowViews[0].remove.Disabled() == false {
		t.Fatal("the last remaining segment cannot be deleted")
	}
}
