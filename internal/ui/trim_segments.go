package ui

import (
	"fmt"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"git.skobk.in/skobkin/yave/internal/model"
)

// segmentRowText renders one list row: index, boundaries and length.
func segmentRowText(index int, seg model.TimeRange) string {
	return fmt.Sprintf("#%d   %s → %s   (%s)", index+1, formatTimestamp(seg.Start), formatTimestamp(seg.End), formatTimestamp(seg.Length()))
}

// segmentListHeader summarizes the list: segment count and total length.
func segmentListHeader(segments []model.TimeRange) string {
	var total time.Duration
	for _, seg := range segments {
		total += seg.Length()
	}
	return fmt.Sprintf("Segments: %d — total %s", len(segments), formatTimestamp(total))
}

// segmentRow is one rebuilt-in-place row of the segment list.
type segmentRow struct {
	pick   *widget.Button
	remove *widget.Button
	view   fyne.CanvasObject
}

// buildSegmentPanel creates the list under the timeline: a header and a
// scrolling column of rows, each with a select-and-seek button and a
// delete button. Its height is capped so many segments scroll instead of
// growing the window.
func (c *Controller) buildSegmentPanel() fyne.CanvasObject {
	c.segmentHeader = widget.NewLabel("")
	c.segmentRows = container.NewVBox()
	scroll := container.NewVScroll(c.segmentRows)
	scroll.SetMinSize(fyne.NewSize(0, 96))
	c.segmentPanel = container.NewVBox(c.segmentHeader, scroll)
	c.segmentPanel.Hide()
	return c.segmentPanel
}

// updateSegmentList mirrors the selection into the list: it reuses the
// existing row widgets when the count is unchanged, so dragging a handle
// only rewrites text. Called from refresh().
func (c *Controller) updateSegmentList() {
	if c.segmentPanel == nil {
		return
	}
	segments := c.trimSelection.Segments
	if !c.trimEnabled || len(segments) == 0 {
		c.segmentPanel.Hide()
		return
	}
	c.segmentPanel.Show()
	c.segmentHeader.SetText(segmentListHeader(segments))
	if len(c.segmentRowViews) != len(segments) {
		c.segmentRows.RemoveAll()
		c.segmentRowViews = c.segmentRowViews[:0]
		for i := range segments {
			c.segmentRowViews = append(c.segmentRowViews, c.newSegmentRow(i))
		}
		for _, row := range c.segmentRowViews {
			c.segmentRows.Add(row.view)
		}
	}
	for i, seg := range segments {
		row := c.segmentRowViews[i]
		row.pick.SetText(segmentRowText(i, seg))
		if i == c.trimSelected {
			row.pick.Importance = widget.HighImportance
		} else {
			row.pick.Importance = widget.LowImportance
		}
		row.pick.Refresh()
		if len(segments) > 1 {
			row.remove.Enable()
		} else {
			row.remove.Disable()
		}
	}
}

func (c *Controller) newSegmentRow(index int) segmentRow {
	pick := widget.NewButton("", func() { c.selectSegment(index) })
	pick.Alignment = widget.ButtonAlignLeading
	remove := widget.NewButtonWithIcon("", theme.DeleteIcon(), func() {
		c.selectSegment(index)
		c.deleteTrimSegment()
	})
	remove.Importance = widget.LowImportance
	return segmentRow{pick: pick, remove: remove, view: container.NewBorder(nil, nil, nil, remove, pick)}
}

// selectSegment selects a segment from the list and moves the playhead
// to its In point, keeping the timeline highlight and the boundary
// entries in sync.
func (c *Controller) selectSegment(index int) {
	segments := c.trimSelection.Segments
	if !c.trimEnabled || index < 0 || index >= len(segments) {
		return
	}
	c.trimTimeline.SetSelected(index)
	c.onTimelineSelected(index)
	at := segments[index].Start
	c.trimTimeline.SetPlayhead(at)
	c.onTimelinePlayhead(at)
}
