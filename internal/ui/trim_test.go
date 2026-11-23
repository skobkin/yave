package ui

import (
	"context"
	"errors"
	"image"
	"image/color"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/layout"
	fynetest "fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"

	"git.skobk.in/skobkin/yave/internal/frames"
	"git.skobk.in/skobkin/yave/internal/model"
	"git.skobk.in/skobkin/yave/internal/validation"
)

// testTrimController builds the production widget wiring on a bare
// controller: createWidgets installs every trim callback exactly like
// the app does, so lifecycle tests exercise the real refresh/message
// paths. UI dispatch is queued into a buffered channel so tests assert
// synchronous state without racing background goroutines.
func testTrimController(t *testing.T) *Controller {
	t.Helper()
	app := fynetest.NewApp()
	t.Cleanup(app.Quit)
	updates := make(chan func(), 64)
	c := &Controller{
		media:     model.MediaInfo{Duration: 10 * time.Minute},
		inputPath: "/tmp/input.mkv",
	}
	c.previews = newTrimPreviews()
	c.createWidgets()
	c.uiDispatch = func(f func()) { updates <- f }
	return c
}

func TestFormatTimestamp(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in   time.Duration
		want string
	}{
		{0, "00:00:00.000"},
		{1500 * time.Millisecond, "00:00:01.500"},
		{59 * time.Second, "00:00:59.000"},
		{time.Minute + time.Second, "00:01:01.000"},
		{time.Hour + 2*time.Minute + 3*time.Second + 400*time.Millisecond, "01:02:03.400"},
		{599 * time.Hour, "599:00:00.000"},
		{-time.Second, "00:00:00.000"},
	}
	for _, tt := range tests {
		if got := formatTimestamp(tt.in); got != tt.want {
			t.Errorf("formatTimestamp(%s) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestParseTimestamp(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in      string
		want    time.Duration
		wantErr bool
	}{
		{"00:00:00.000", 0, false},
		{"0:01.500", 1500 * time.Millisecond, false},
		{"01:00:00", time.Hour, false},
		{"1:02:03.400", time.Hour + 2*time.Minute + 3*time.Second + 400*time.Millisecond, false},
		{"59:59.999", 59*time.Minute + 59*time.Second + 999*time.Millisecond, false},
		{"  0:05  ", 5 * time.Second, false},
		{"0:00.005", 5 * time.Millisecond, false},
		{"", 0, true},
		{"12", 12 * time.Second, false},
		{"90", 90 * time.Second, false},
		{"90.5", 90*time.Second + 500*time.Millisecond, false},
		{"0.005", 5 * time.Millisecond, false},
		{"-5", 0, true},
		{"abc", 0, true},
		{"5.", 0, true},
		{"1.2345", 0, true},
		{"999999999999", 0, true},
		{"1:2:3:4", 0, true},
		{"01:60.000", 0, true},
		{"60:00", 0, true},
		{"-1:00.000", 0, true},
		{"00:00.1000", 0, true},
		{"00:0a.000", 0, true},
		{"00:00.5x", 0, true},
	}
	for _, tt := range tests {
		got, err := parseTimestamp(tt.in)
		if tt.wantErr {
			if err == nil {
				t.Errorf("parseTimestamp(%q) expected an error, got %s", tt.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseTimestamp(%q) unexpected error: %v", tt.in, err)
			continue
		}
		if got != tt.want {
			t.Errorf("parseTimestamp(%q) = %s, want %s", tt.in, got, tt.want)
		}
	}
}

func TestTimestampRoundTrip(t *testing.T) {
	t.Parallel()
	for _, at := range []time.Duration{
		0, time.Millisecond, 999 * time.Millisecond, time.Minute, 100 * time.Hour,
	} {
		back, err := parseTimestamp(formatTimestamp(at))
		if err != nil {
			t.Fatalf("round trip %s: %v", at, err)
		}
		if back != at {
			t.Errorf("round trip %s became %s", at, back)
		}
	}
}

func TestTrimRequestGating(t *testing.T) {
	c := testTrimController(t)
	c.trimInValid, c.trimOutValid = true, true
	if c.trimRequest().Active() {
		t.Fatal("disabled trim must produce an empty selection")
	}
	c.trimEnabled = true
	if c.trimRequest().Active() {
		t.Fatal("enabled trim without a selection must produce an empty selection")
	}
	c.trimSelection = model.TrimSelection{Segments: []model.TimeRange{{Start: time.Second, End: 5 * time.Second}}}
	if !c.trimRequest().Active() {
		t.Fatal("enabled trim with a selection must produce it")
	}
	c.trimInValid = false
	if c.trimRequest().Active() {
		t.Fatal("invalid entry must produce an empty selection")
	}
	c.trimInValid = true
	c.trimOutValid = false
	if c.trimRequest().Active() {
		t.Fatal("invalid out entry must produce an empty selection")
	}
}

func TestTrimEntryIssuesBlockConversion(t *testing.T) {
	c := testTrimController(t)
	c.trimEnabled = true
	c.trimInValid, c.trimOutValid = true, true
	c.trimSelection = model.TrimSelection{Segments: []model.TimeRange{{Start: time.Second, End: 5 * time.Second}}}
	if issues := c.trimEntryIssues(); len(issues) != 0 {
		t.Fatalf("valid entries must produce no issues: %v", issues)
	}
	c.trimIn.SetText("garbage")
	c.setTrimBoundary("in", c.trimIn.Text)
	issues := c.trimEntryIssues()
	if len(issues) != 1 || !strings.Contains(issues[0].Message, "In timecode is invalid") {
		t.Fatalf("expected a blocking invalid-entry issue, got %v", issues)
	}
	if issues[0].Severity != validation.SeverityError {
		t.Fatalf("entry issue must be an error, got %v", issues[0].Severity)
	}
	// The last valid selection survives for display, but the request is
	// empty so nothing stale is previewed or converted.
	if !c.trimSelection.Active() {
		t.Fatal("invalid entry must keep the last valid selection")
	}
	if c.trimRequest().Active() {
		t.Fatal("invalid entry must empty the conversion request")
	}
	result := validation.Result{}
	result.Errors = append(result.Errors, issues...)
	if result.Valid() {
		t.Fatal("merged result with entry issues must not be valid")
	}
}

// Regression: an EMPTIED boundary produced no blocking issue because the
// checks required non-empty text — but the stored selection still
// validates and trimRequest() returns an empty selection, so Start
// silently converted the whole input without -ss/-t while showing no
// error anywhere.
func TestTrimEmptyBoundaryBlocksConversion(t *testing.T) {
	c := testTrimController(t)
	c.trimEnabled = true
	c.trimInValid, c.trimOutValid = true, true
	c.trimSelection = model.TrimSelection{Segments: []model.TimeRange{{Start: time.Second, End: 5 * time.Second}}}

	c.trimIn.SetText("")
	c.setTrimBoundary("in", c.trimIn.Text)

	issues := c.trimEntryIssues()
	if len(issues) != 1 || !strings.Contains(issues[0].Message, "In timecode is required") {
		t.Fatalf("expected a blocking required-entry issue, got %v", issues)
	}
	if issues[0].Severity != validation.SeverityError {
		t.Fatalf("entry issue must be an error, got %v", issues[0].Severity)
	}
	if c.trimRequest().Active() {
		t.Fatal("empty boundary must empty the conversion request")
	}
	if !c.trimSelection.Active() {
		t.Fatal("the last valid selection must survive for display")
	}
	c.updateTrimMessages("libx265", model.AudioSettings{})
	if !strings.Contains(c.trimMessage.Text, "In timecode is required") {
		t.Fatalf("validation message must surface the empty boundary: %q", c.trimMessage.Text)
	}
}

func TestUpdateTrimMessagesTracksEncoder(t *testing.T) {
	c := testTrimController(t)
	c.trimEnabled = true
	c.trimSelection = model.TrimSelection{Segments: []model.TimeRange{{Start: time.Second, End: 5 * time.Second}}}

	c.updateTrimMessages("libx265", model.AudioSettings{})
	if strings.Contains(c.trimMessage.Text, "keyframes") {
		t.Fatalf("real encoder must not show the copy warning: %q", c.trimMessage.Text)
	}
	c.updateTrimMessages("copy", model.AudioSettings{})
	if !strings.Contains(c.trimMessage.Text, "keyframes") {
		t.Fatalf("copy encoder must show the keyframe warning: %q", c.trimMessage.Text)
	}
	c.updateTrimMessages("hevc_vaapi", model.AudioSettings{})
	if strings.Contains(c.trimMessage.Text, "keyframes") {
		t.Fatalf("warning must disappear after switching away from copy: %q", c.trimMessage.Text)
	}
	c.trimEnabled = false
	c.updateTrimMessages("copy", model.AudioSettings{})
	if strings.Contains(c.trimMessage.Text, "keyframes") {
		t.Fatalf("disabled trim must hide the warning: %q", c.trimMessage.Text)
	}
}

// --- segment management ---

func TestAddTrimSegmentClampsIntoGap(t *testing.T) {
	c := testTrimController(t)
	c.trimEnabled = true
	c.media = model.MediaInfo{Duration: 30 * time.Second}
	c.trimTimeline.SetDuration(30 * time.Second)
	c.trimSelection = model.TrimSelection{Segments: []model.TimeRange{
		{Start: time.Second, End: 5 * time.Second},
		{Start: 8 * time.Second, End: 12 * time.Second},
	}}
	c.trimTimeline.SetSegmentsSilent(c.trimSelection.Segments)

	// A gap with ≥5s of room caps the new segment at 5 seconds.
	c.trimTimeline.SetPlayhead(20 * time.Second)
	c.addTrimSegment()
	if len(c.trimSelection.Segments) != 3 {
		t.Fatalf("segments after add = %v", c.trimSelection.Segments)
	}
	if seg := c.trimSelection.Segments[2]; seg.Start != 20*time.Second || seg.End != 25*time.Second {
		t.Fatalf("new segment = %v, want [20s, 25s] (5s cap)", seg)
	}
	if c.trimSelected != 2 || c.trimTimeline.Selected() != 2 {
		t.Fatalf("selection after add = %d (widget %d), want 2", c.trimSelected, c.trimTimeline.Selected())
	}
	if c.trimIn.Text != formatTimestamp(20*time.Second) || c.trimOut.Text != formatTimestamp(25*time.Second) {
		t.Fatalf("entries after add = %q / %q, want the new segment", c.trimIn.Text, c.trimOut.Text)
	}

	// A narrower gap clamps to adjacency instead of overlapping.
	c.trimTimeline.SetPlayhead(6 * time.Second)
	c.addTrimSegment()
	if seg := c.trimSelection.Segments[1]; seg.Start != 6*time.Second || seg.End != 8*time.Second {
		t.Fatalf("gap-limited segment = %v, want [6s, 8s] (adjacent to the next segment)", seg)
	}

	// Segment edges count: a playhead exactly at a segment's End starts
	// an adjacent segment (half-open).
	c.trimTimeline.SetPlayhead(12 * time.Second)
	c.addTrimSegment()
	if seg := c.trimSelection.Segments[3]; seg.Start != 12*time.Second || seg.End != 17*time.Second {
		t.Fatalf("edge segment = %v, want [12s, 17s]", seg)
	}

	// No overlaps anywhere.
	for i := 1; i < len(c.trimSelection.Segments); i++ {
		if c.trimSelection.Segments[i].Start < c.trimSelection.Segments[i-1].End {
			t.Fatalf("add created an overlap: %v", c.trimSelection.Segments)
		}
	}
}

func TestAddTrimSegmentRejectsImpossibleSpots(t *testing.T) {
	c := testTrimController(t)
	c.trimEnabled = true
	c.media = model.MediaInfo{Duration: 30 * time.Second}
	c.trimTimeline.SetDuration(30 * time.Second)
	c.trimSelection = model.TrimSelection{Segments: []model.TimeRange{
		{Start: time.Second, End: 5 * time.Second},
		{Start: 8 * time.Second, End: 12 * time.Second},
	}}

	// At a segment Start (half-open) any positive length would overlap.
	c.trimTimeline.SetPlayhead(8 * time.Second)
	c.addTrimSegment()
	if len(c.trimSelection.Segments) != 2 {
		t.Fatalf("add at a segment start created %v", c.trimSelection.Segments)
	}
	if c.trimMessage.Text == "" {
		t.Fatal("an impossible add must explain itself, not silently no-op")
	}

	// Strictly inside a segment: same.
	c.trimTimeline.SetPlayhead(10 * time.Second)
	c.addTrimSegment()
	if len(c.trimSelection.Segments) != 2 || c.trimMessage.Text == "" {
		t.Fatalf("add inside a segment: %v / %q", c.trimSelection.Segments, c.trimMessage.Text)
	}

	// No room at the very end.
	c.trimTimeline.SetPlayhead(30 * time.Second)
	c.addTrimSegment()
	if len(c.trimSelection.Segments) != 2 || c.trimMessage.Text == "" {
		t.Fatalf("add past the end: %v / %q", c.trimSelection.Segments, c.trimMessage.Text)
	}

	// A disordered (typed) list refuses instead of inserting ambiguously.
	c.trimSelection = model.TrimSelection{Segments: []model.TimeRange{
		{Start: 8 * time.Second, End: 12 * time.Second},
		{Start: time.Second, End: 5 * time.Second},
	}}
	c.trimTimeline.SetPlayhead(20 * time.Second)
	c.addTrimSegment()
	if len(c.trimSelection.Segments) != 2 || c.trimMessage.Text == "" {
		t.Fatalf("add into a disordered list: %v / %q", c.trimSelection.Segments, c.trimMessage.Text)
	}
}

func TestDeleteTrimSegment(t *testing.T) {
	c := testTrimController(t)
	c.trimEnabled = true
	c.media = model.MediaInfo{Duration: 30 * time.Second}
	c.trimTimeline.SetDuration(30 * time.Second)
	segments := []model.TimeRange{
		{Start: time.Second, End: 5 * time.Second},
		{Start: 8 * time.Second, End: 12 * time.Second},
	}
	c.trimSelection = model.TrimSelection{Segments: append([]model.TimeRange(nil), segments...)}
	c.trimTimeline.SetSegmentsSilent(c.trimSelection.Segments)

	// Deleting the first leaves the second; the selection lands on the
	// segment that took the deleted slot and the entries follow it.
	c.trimSelected = 0
	c.trimTimeline.SetSelected(0)
	c.deleteTrimSegment()
	if len(c.trimSelection.Segments) != 1 || c.trimSelection.Segments[0] != segments[1] {
		t.Fatalf("segments after delete = %v, want [%v]", c.trimSelection.Segments, segments[1])
	}
	if c.trimSelected != 0 || c.trimTimeline.Selected() != 0 {
		t.Fatalf("selection after delete = %d (widget %d), want 0", c.trimSelected, c.trimTimeline.Selected())
	}
	if c.trimIn.Text != formatTimestamp(8*time.Second) || c.trimOut.Text != formatTimestamp(12*time.Second) {
		t.Fatalf("entries after delete = %q / %q, want the surviving segment", c.trimIn.Text, c.trimOut.Text)
	}

	// Down to one segment Delete is disabled and a programmatic call
	// keeps the last segment.
	if !c.trimDelete.Disabled() {
		t.Fatal("Delete must be disabled with a single segment")
	}
	c.deleteTrimSegment()
	if len(c.trimSelection.Segments) != 1 {
		t.Fatalf("delete kept %v, want the single segment", c.trimSelection.Segments)
	}

	// Deleting the last segment folds the selection to the new last.
	c.trimSelection = model.TrimSelection{Segments: append([]model.TimeRange(nil), segments...)}
	c.trimSelected = 1
	c.trimTimeline.SetSelected(1)
	c.deleteTrimSegment()
	if len(c.trimSelection.Segments) != 1 || c.trimSelected != 0 ||
		c.trimIn.Text != formatTimestamp(time.Second) {
		t.Fatalf("tail delete: %v / sel %d / in %q", c.trimSelection.Segments, c.trimSelected, c.trimIn.Text)
	}
}

func TestResetTrimForNewInputResetsSegmentState(t *testing.T) {
	c := testTrimController(t)
	c.trimEnabled = true
	c.media = model.MediaInfo{Duration: 30 * time.Second}
	c.trimTimeline.SetDuration(30 * time.Second)
	c.trimSelection = model.TrimSelection{Segments: []model.TimeRange{
		{Start: time.Second, End: 5 * time.Second},
		{Start: 8 * time.Second, End: 12 * time.Second},
	}}
	c.trimTimeline.SetSegmentsSilent(c.trimSelection.Segments)
	c.trimSelected = 1
	// Switch to join through the real dropdown callback.
	c.trimExport.SetSelected(model.TrimExportJoin.String())
	if c.trimExportMode != model.TrimExportJoin {
		t.Fatalf("dropdown callback did not update the mode: %v", c.trimExportMode)
	}

	c.resetTrimForNewInput()

	if c.trimExportMode != model.TrimExportSeparate {
		t.Fatalf("export mode after reset = %v, want separate", c.trimExportMode)
	}
	if got := c.trimExport.Selected; got != model.TrimExportSeparate.String() {
		t.Fatalf("export dropdown after reset = %q, want %q", got, model.TrimExportSeparate.String())
	}
	if c.trimSelected != 0 {
		t.Fatalf("selected index after reset = %d, want 0", c.trimSelected)
	}
	if c.trimEnabled || c.trimSelection.Active() {
		t.Fatal("trim must be fully disabled after an input change")
	}
}

// Join-mode export checks surface inline: video stream copy is a blocking
// error, copied audio a warning, and the separate-files mode adds nothing.
func TestJoinExportInlineMessages(t *testing.T) {
	c := testTrimController(t)
	c.trimEnabled = true
	c.media = model.MediaInfo{Duration: 30 * time.Second}
	c.trimSelection = model.TrimSelection{Segments: []model.TimeRange{
		{Start: time.Second, End: 5 * time.Second},
		{Start: 8 * time.Second, End: 12 * time.Second},
	}}
	c.trimExportMode = model.TrimExportJoin

	// Video copy + join: error.
	c.updateTrimMessages("copy", model.AudioSettings{})
	if !strings.Contains(c.trimMessage.Text, "video stream copy cannot be combined with joining") {
		t.Fatalf("video copy + join must error inline: %q", c.trimMessage.Text)
	}
	// A real encoder + copied audio: warning only (via the Mode
	// representation).
	c.updateTrimMessages("libx265", model.AudioSettings{Mode: model.AudioCopy})
	if strings.Contains(c.trimMessage.Text, "cannot be combined") {
		t.Fatalf("encoded video + join must not error: %q", c.trimMessage.Text)
	}
	if !strings.Contains(c.trimMessage.Text, "joining with copied audio") {
		t.Fatalf("copied audio + join must warn inline: %q", c.trimMessage.Text)
	}
	// The Encoder="copy" representation warns identically.
	c.updateTrimMessages("libx265", model.AudioSettings{Mode: model.AudioEncode, Encoder: " copy "})
	if !strings.Contains(c.trimMessage.Text, "joining with copied audio") {
		t.Fatalf("encoder=\"copy\" + join must warn inline: %q", c.trimMessage.Text)
	}
	// Encoded audio: no warning.
	c.updateTrimMessages("libx265", model.AudioSettings{Mode: model.AudioEncode, Encoder: "libopus"})
	if strings.Contains(c.trimMessage.Text, "joining with copied audio") {
		t.Fatalf("encoded audio + join must not warn: %q", c.trimMessage.Text)
	}
	// Switching back to separate files drops every join-specific message.
	c.trimExportMode = model.TrimExportSeparate
	c.updateTrimMessages("copy", model.AudioSettings{})
	if strings.Contains(c.trimMessage.Text, "joining") {
		t.Fatalf("separate mode must not show join messages: %q", c.trimMessage.Text)
	}
	if !strings.Contains(c.trimMessage.Text, "keyframes") {
		t.Fatalf("the plain copy warning must survive in separate mode: %q", c.trimMessage.Text)
	}
}

// --- frame index ---

func TestLookupStep(t *testing.T) {
	t.Parallel()
	times := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second}
	if next, ok := lookupStep(times, time.Second, 1); !ok || next != 2*time.Second {
		t.Errorf("forward neighbor = %s (%v), want 2s", next, ok)
	}
	if prev, ok := lookupStep(times, 4*time.Second, -1); !ok || prev != 2*time.Second {
		t.Errorf("backward neighbor = %s (%v), want 2s", prev, ok)
	}
	// VFR gaps are handled by the same lookup.
	if next, ok := lookupStep(times, 2*time.Second, 1); !ok || next != 4*time.Second {
		t.Errorf("VFR forward neighbor = %s (%v), want 4s", next, ok)
	}
	if _, ok := lookupStep(times, 4*time.Second, 1); ok {
		t.Error("no forward neighbor past the last frame")
	}
	if _, ok := lookupStep(times, time.Second, -1); ok {
		t.Error("no backward neighbor before the first frame")
	}
	if next, ok := lookupStep(times, 1500*time.Millisecond, 1); !ok || next != 2*time.Second {
		t.Errorf("from mid-gap forward = %s (%v), want 2s", next, ok)
	}
	if prev, ok := lookupStep(times, 1500*time.Millisecond, -1); !ok || prev != time.Second {
		t.Errorf("from mid-gap backward = %s (%v), want 1s", prev, ok)
	}
}

func TestFrameIndexStepAndPending(t *testing.T) {
	t.Parallel()
	index := newFrameIndex()
	duration := 10 * time.Minute

	// Stepping backward from the video's first frame is the start edge:
	// it stops immediately instead of queueing a fetch.
	_, state := index.step(0, -1, duration)
	if state != frameStepEdge || index.pending != 0 {
		t.Fatalf("backward from first frame = %v pending %d, want edge/0", state, index.pending)
	}

	// Nothing loaded: a forward step queues and reports loading. The
	// empty index covers only 0, so the playhead stays put.
	at, state := index.step(0, 1, duration)
	if state != frameStepLoading || index.pending != 1 {
		t.Fatalf("first step state = %v pending = %d, want loading/1", state, index.pending)
	}
	if at != 0 {
		t.Fatalf("queued step must not move the playhead, got %s", at)
	}

	// Rapid repeated presses accumulate into a single net offset.
	index.step(at, 1, duration)
	index.step(at, 1, duration)
	if index.pending != 3 {
		t.Fatalf("pending = %d, want 3", index.pending)
	}

	// The window lands: queued steps apply as one atomic walk.
	times := []time.Duration{0, 100 * time.Millisecond, 200 * time.Millisecond, 300 * time.Millisecond}
	at, state = index.applyWindow(0, 2*time.Second, times, duration, duration)
	if state != frameStepMoved || at != 300*time.Millisecond {
		t.Fatalf("after window: at = %s state = %v, want 300ms/moved", at, state)
	}
	if index.pending != 0 {
		t.Fatalf("queue must drain, pending = %d", index.pending)
	}

	// Loaded index without a forward neighbor and with room beyond the
	// window: fetch the adjacent window instead of approximating.
	at, state = index.step(300*time.Millisecond, 1, duration)
	if state != frameStepLoading || at != 300*time.Millisecond {
		t.Fatalf("past the last cached frame = %s/%v, want 300ms/loading", at, state)
	}
}

func TestFrameIndexWindowCrossing(t *testing.T) {
	t.Parallel()
	index := newFrameIndex()
	duration := 10 * time.Minute
	index.applyWindow(0, 2*time.Second, []time.Duration{0, time.Second, 2 * time.Second}, duration, duration)

	// A five-frame jump queued from the window's last frame.
	for range 5 {
		if _, state := index.step(2*time.Second, 1, duration); state != frameStepLoading {
			t.Fatalf("beyond-window step must load, got %v", state)
		}
	}
	if index.pending != 5 {
		t.Fatalf("pending = %d, want 5", index.pending)
	}

	// The adjacent window lands with only two more frames: the walk
	// consumes them and keeps the rest queued for the next window —
	// never clamped to the cache boundary.
	at, state := index.applyWindow(2*time.Second, 4*time.Second, []time.Duration{3 * time.Second, 4 * time.Second}, duration, duration)
	if state != frameStepLoading || at != 4*time.Second || index.pending != 3 {
		t.Fatalf("after adjacent window: at = %s state = %v pending = %d, want 4s/loading/3", at, state, index.pending)
	}

	// The next window completes the walk as one atomic move.
	at, state = index.applyWindow(4*time.Second, 8*time.Second,
		[]time.Duration{5 * time.Second, 6 * time.Second, 7 * time.Second, 8 * time.Second}, duration, duration)
	if state != frameStepMoved || at != 7*time.Second || index.pending != 0 {
		t.Fatalf("after final window: at = %s state = %v pending = %d, want 7s/moved/0", at, state, index.pending)
	}

	// Stepping to the actual last frame works, then past it stops at
	// the edge once the final window confirms there is nothing beyond.
	if at, state = index.step(7*time.Second, 1, duration); state != frameStepMoved || at != 8*time.Second {
		t.Fatalf("step to last frame = %s/%v, want 8s/moved", at, state)
	}
	if _, state = index.step(8*time.Second, 1, duration); state != frameStepLoading {
		t.Fatalf("past last cached frame = %v, want loading", state)
	}
	at, state = index.applyWindow(8*time.Second, duration, []time.Duration{9 * time.Second}, duration, duration)
	if state != frameStepMoved || at != 9*time.Second {
		t.Fatalf("after final fetch = %s/%v, want 9s/moved", at, state)
	}
	at, state = index.step(9*time.Second, 1, duration)
	if state != frameStepEdge || at != 9*time.Second || index.pending != 0 {
		t.Fatalf("past the video's last frame = %s/%v pending %d, want 9s/edge/0", at, state, index.pending)
	}
}

func TestFrameIndexFailureDisablesSteppingOnly(t *testing.T) {
	t.Parallel()
	index := newFrameIndex()
	duration := 10 * time.Minute
	if _, state := index.step(0, 1, duration); state != frameStepLoading {
		t.Fatalf("want loading, got %v", state)
	}
	index.applyError(errors.New("probe failed"))
	at, state := index.step(0, 1, duration)
	if state != frameStepUnavailable || at != 0 {
		t.Fatalf("after failure: at = %s state = %v, want 0/unavailable", at, state)
	}
	if index.pending != 0 {
		t.Fatalf("failure must drain the queue, pending = %d", index.pending)
	}
}

func TestFrameIndexEmptyWindowAtMediaEnd(t *testing.T) {
	t.Parallel()
	index := newFrameIndex()
	duration := 30 * time.Second
	// The window reaches the media end and contains no frame at the
	// end: stepping forward stops at the edge instead of looping.
	index.applyWindow(20*time.Second, duration, nil, duration, duration)
	_, state := index.step(25*time.Second, 1, duration)
	if state != frameStepEdge {
		t.Fatalf("state = %v, want edge (no frames after 25s)", state)
	}
}

// Regression: the walk's edge check used the container duration, so on
// an input whose audio outlives its video (3s video, 1h audio) a Right
// press at the last video frame reported "loading" and chained window
// fetches through the entire audio tail (~1800 fetches) before ever
// reporting the edge. The video stream end FrameTimes returns now gates
// the walk: the edge lands in one step, on the direct path and in a
// queued walk alike.
func TestFrameIndexWalkStopsAtVideoStreamEnd(t *testing.T) {
	t.Parallel()
	index := newFrameIndex()
	duration := time.Hour
	videoEnd := 3 * time.Second
	index.applyWindow(0, videoEnd,
		[]time.Duration{0, time.Second, 2 * time.Second, videoEnd}, duration, videoEnd)

	at, state := index.step(videoEnd, 1, duration)
	if state != frameStepEdge || at != videoEnd || index.pending != 0 {
		t.Fatalf("step past the last video frame = %s/%v pending %d, want %s/edge/0",
			at, state, index.pending, videoEnd)
	}

	// A queued walk that runs into the video end commits the partial
	// movement and reports the edge — same gate, pending path.
	index.queue(1, videoEnd)
	at, state = index.applyPending(duration)
	if state != frameStepEdge || at != videoEnd || index.pending != 0 {
		t.Fatalf("walk into the video end = %s/%v pending %d, want %s/edge/0",
			at, state, index.pending, videoEnd)
	}
}

// Regression: queued steps that run past the video's actual edge used to
// be discarded wholesale — the walk's partial movement (1s -> 2s in this
// fixture) was reported as an edge and dropped, leaving the playhead
// where it started.
func TestFrameIndexPartialWalkCommitsAtEdge(t *testing.T) {
	index := newFrameIndex()
	duration := 2 * time.Second
	index.applyWindow(0, duration, []time.Duration{0, time.Second, 2 * time.Second}, duration, duration)

	// Four forward steps queued from 1s: only one is possible (2s is the
	// last frame); the rest run into the media edge.
	for range 4 {
		index.queue(1, time.Second)
	}
	at, state := index.applyPending(duration)
	if state != frameStepEdge || at != 2*time.Second {
		t.Fatalf("partial walk = %s/%v, want 2s/edge", at, state)
	}

	// The controller commits the partial movement to the playhead.
	c := testTrimController(t)
	c.media = model.MediaInfo{Duration: duration}
	c.trimEnabled = true
	c.trimTimeline.SetDuration(duration)
	c.trimTimeline.SetPlayhead(time.Second)
	c.applyFrameStep(at, state)
	if c.trimTimeline.Playhead() != 2*time.Second {
		t.Fatalf("playhead = %s, want the partial walk position 2s", c.trimTimeline.Playhead())
	}
}

// Regression: an opposite press that cancelled the last queued step
// reported loading instead of committing the walk. Three Rights from
// 2s advance the walk to 4s with one step remaining; Left zeroes the
// queue, the landing window applied nothing (idle), so the displayed
// playhead stayed at 2s and the loading status never cleared. A newly
// zeroed queue is a completed walk at pendingAt — commit it. This is
// distinct from retirement by a manual seek, which points pendingAt at
// the seek target before the fetch lands.
func TestFrameIndexCancelledWalkCommitsWalkPosition(t *testing.T) {
	index := newFrameIndex()
	duration := 10 * time.Second
	index.applyWindow(0, 2*time.Second, []time.Duration{0, time.Second, 2 * time.Second}, duration, duration)

	// Three Rights from the stale playhead 2s queue a walk of three.
	for range 3 {
		if _, state := index.step(2*time.Second, 1, duration); state != frameStepLoading {
			t.Fatalf("queued step state = %v, want loading", state)
		}
	}
	// The fetch lands short: the walk consumes two steps and pauses at
	// 4s with one step still queued.
	at, state := index.applyWindow(2*time.Second, 5*time.Second,
		[]time.Duration{2 * time.Second, 3 * time.Second, 4 * time.Second}, duration, duration)
	if state != frameStepLoading || at != 5*time.Second {
		t.Fatalf("walk pause = %s/%v, want 5s/loading", at, state)
	}
	if index.pendingAt != 4*time.Second || index.pending != 1 {
		t.Fatalf("walk state = %s/%d, want 4s/1", index.pendingAt, index.pending)
	}

	// Left cancels the remainder: the walk position commits as movement.
	at, state = index.step(2*time.Second, -1, duration)
	if state != frameStepMoved || at != 4*time.Second {
		t.Fatalf("cancelled walk = %s/%v, want 4s/moved", at, state)
	}

	c := testTrimController(t)
	c.media = model.MediaInfo{Duration: duration}
	c.trimEnabled = true
	c.trimTimeline.SetDuration(duration)
	c.setPreviewStatus("Loading frame index…")
	c.applyFrameStep(at, state)
	if c.trimTimeline.Playhead() != 4*time.Second {
		t.Fatalf("playhead = %s, want the walk position 4s", c.trimTimeline.Playhead())
	}
	if !c.previewStatus.Hidden {
		t.Fatalf("loading status still shown after the cancelled walk committed")
	}

	// The in-flight window lands with nothing queued: idle, and the
	// committed playhead stays at the walk position.
	if _, state := index.applyWindow(3*time.Second, 7*time.Second,
		[]time.Duration{4 * time.Second, 5 * time.Second}, duration, duration); state != frameStepIdle {
		t.Fatalf("landing state = %v, want idle", state)
	}
	if c.trimTimeline.Playhead() != 4*time.Second {
		t.Fatalf("playhead = %s, want it to stay at 4s after the landing", c.trimTimeline.Playhead())
	}
}

// Regression: when the walk position has no neighbor and the video
// continues past the cached window (a frame gap longer than the fetch
// window), the controller re-fetched the identical range around the
// unchanged walk position forever. The stuck branch must anchor the
// next fetch at the window edge so the window extends outward, while
// the walk itself stays on the real position.
func TestFrameIndexAdvancesAnchorAcrossFrameGaps(t *testing.T) {
	t.Parallel()
	index := newFrameIndex()
	duration := 10 * time.Second
	// Initial window holds only a frame at 0; the next frame is at 5s.
	index.applyWindow(0, 2*time.Second, []time.Duration{0}, duration, duration)
	if _, state := index.step(0, 1, duration); state != frameStepLoading {
		t.Fatalf("state = %v, want loading", state)
	}
	// A re-probe of the same range finds nothing: the anchor must move
	// to the window edge instead of staying at the walk position.
	at, state := index.applyWindow(0, 2*time.Second, []time.Duration{0}, duration, duration)
	if state != frameStepLoading || at != 2*time.Second {
		t.Fatalf("anchor = %s/%v, want 2s/loading (the window edge)", at, state)
	}
	if index.pendingAt != 0 || index.pending != 1 {
		t.Fatalf("walk drifted: pendingAt = %s pending = %d, want 0s/1", index.pendingAt, index.pending)
	}
	// A fetch anchored at that edge extends the window; still nothing.
	if at, state = index.applyWindow(0, 4*time.Second, []time.Duration{0}, duration, duration); state != frameStepLoading || at != 4*time.Second {
		t.Fatalf("anchor = %s/%v, want 4s/loading", at, state)
	}
	// The window reaches the gap's far side and the walk completes.
	at, state = index.applyWindow(2*time.Second, 6*time.Second, []time.Duration{5 * time.Second}, duration, duration)
	if state != frameStepMoved || at != 5*time.Second || index.pending != 0 {
		t.Fatalf("gap walk = %s/%v pending %d, want 5s/moved/0", at, state, index.pending)
	}
}

// Regression: an audio tail past the last video frame walks the anchor
// to the media end; the edge commit must land on the real walk position
// (the last frame), not on the drifted fetch anchor.
func TestFrameIndexEdgeCommitStaysAtWalkPosition(t *testing.T) {
	t.Parallel()
	index := newFrameIndex()
	duration := 10 * time.Second
	index.applyWindow(0, 4*time.Second, []time.Duration{0, 3 * time.Second}, duration, duration)
	if _, state := index.step(3*time.Second, 1, duration); state != frameStepLoading {
		t.Fatalf("state = %v, want loading", state)
	}
	for _, window := range [][2]time.Duration{
		{2 * time.Second, 6 * time.Second},
		{4 * time.Second, 8 * time.Second},
	} {
		at, state := index.applyWindow(window[0], window[1], []time.Duration{3 * time.Second}, duration, duration)
		if state != frameStepLoading {
			t.Fatalf("window %v: state = %v, want loading (anchor advances)", window, state)
		}
		if at != window[1] {
			t.Fatalf("window %v: anchor = %s, want the window edge %s", window, at, window[1])
		}
	}
	at, state := index.applyWindow(6*time.Second, duration, []time.Duration{3 * time.Second}, duration, duration)
	if state != frameStepEdge || at != 3*time.Second || index.pending != 0 {
		t.Fatalf("edge commit = %s/%v pending %d, want 3s/edge/0", at, state, index.pending)
	}
}

// Regression: five Right presses from 2s queue a walk; while the next
// window fetch is in flight (the walk stuck at 6s with one step left),
// a sixth press used to find a cached neighbor from the stale displayed
// playhead (4s), commit 5s immediately, and the playhead callback
// cleared the queued remainder — landing at 5s instead of 8s. A press
// during a walk must merge into it; the production widget callback path
// must report no move, so onTimelinePlayhead never retires the queue.
func TestFrameIndexMergesPressesIntoPendingWalk(t *testing.T) {
	c := testTrimController(t)
	c.trimEnabled = true
	duration := 10 * time.Second
	c.media = model.MediaInfo{Duration: duration}
	c.trimTimeline.SetDuration(duration)

	index := c.previews.index
	index.applyWindow(0, 4*time.Second, []time.Duration{
		0, time.Second, 2 * time.Second, 3 * time.Second, 4 * time.Second,
	}, duration, duration)
	step := func(at time.Duration) (time.Duration, bool) {
		return c.trimTimeline.stepFrame(at, 1)
	}
	// Five presses from 2s: two immediate moves, three queued.
	if at, ok := step(2 * time.Second); !ok || at != 3*time.Second {
		t.Fatalf("first step = %s/%v, want 3s/moved", at, ok)
	}
	if at, ok := step(3 * time.Second); !ok || at != 4*time.Second {
		t.Fatalf("second step = %s/%v, want 4s/moved", at, ok)
	}
	for range 3 {
		if _, ok := step(4 * time.Second); ok {
			t.Fatal("steps past the cached edge must report loading")
		}
	}
	// The [4,6] window lands mid-walk: the walk advances 4->5->6 and
	// sticks (one step left), anchored at the edge. The displayed
	// playhead is still 4s — loading never commits.
	at, state := index.applyWindow(4*time.Second, 6*time.Second,
		[]time.Duration{5 * time.Second, 6 * time.Second}, duration, duration)
	if state != frameStepLoading || at != 6*time.Second || index.pending != 1 || index.pendingAt != 6*time.Second {
		t.Fatalf("partial walk = %s/%v pending %d at %s, want 6s/loading/1 at 6s", at, state, index.pending, index.pendingAt)
	}

	// The sixth press, with the fetch in flight: must join the walk (no
	// move reported, so the timeline never commits or retires).
	if at, ok := step(4 * time.Second); ok || at != 6*time.Second {
		t.Fatalf("press during walk = %s/%v, want 6s/loading (merged)", at, ok)
	}
	if index.pending != 2 || index.pendingAt != 6*time.Second {
		t.Fatalf("queue = %d at %s, want 2 at 6s", index.pending, index.pendingAt)
	}

	// The [4,8] fetch lands and the walk completes at 8s.
	at, state = index.applyWindow(4*time.Second, 8*time.Second,
		[]time.Duration{7 * time.Second, 8 * time.Second}, duration, duration)
	if state != frameStepMoved || at != 8*time.Second || index.pending != 0 {
		t.Fatalf("completed walk = %s/%v pending %d, want 8s/moved/0", at, state, index.pending)
	}
}

// A landing fetch with an empty queue applies nothing — in particular it
// must not move the playhead anywhere (a retired queue must not yank the
// playhead back to a stale walk position).
func TestFrameIndexIdleLandingDoesNotMovePlayhead(t *testing.T) {
	c := testTrimController(t)
	c.trimTimeline.SetPlayhead(7 * time.Second)

	at, state := c.previews.index.applyWindow(6*time.Second, 8*time.Second,
		[]time.Duration{6 * time.Second, 7 * time.Second}, c.media.Duration, c.media.Duration)
	if state != frameStepIdle || at != 0 {
		t.Fatalf("landing with no queue = %s/%v, want 0/idle", at, state)
	}
	c.applyFrameStep(at, state)
	if c.trimTimeline.Playhead() != 7*time.Second {
		t.Fatalf("idle landing moved the playhead to %s", c.trimTimeline.Playhead())
	}
}

// --- timeline widget ---

// newSizedTimeline builds a timeline sized to a 200px width so the
// coordinate-mapping math has fixed, human-checkable numbers.
func newSizedTimeline(t *testing.T) *timeline {
	t.Helper()
	app := fynetest.NewApp()
	t.Cleanup(app.Quit)
	tl := newTimeline()
	tl.Resize(fyne.NewSize(200, timelineMinHeight))
	return tl
}

func TestTimelineCoordinateMapping(t *testing.T) {
	tl := newSizedTimeline(t)
	tl.SetDuration(10 * time.Second)
	if got := tl.timeAt(0); got != 0 {
		t.Errorf("timeAt(0) = %s, want 0", got)
	}
	if got := tl.timeAt(100); got != 5*time.Second {
		t.Errorf("timeAt(100) = %s, want 5s", got)
	}
	if got := tl.timeAt(200); got != 10*time.Second {
		t.Errorf("timeAt(200) = %s, want 10s", got)
	}
	if got := tl.timeAt(-50); got != 0 {
		t.Errorf("timeAt(-50) = %s, want clamped 0", got)
	}
	if got := tl.timeAt(500); got != 10*time.Second {
		t.Errorf("timeAt(500) = %s, want clamped 10s", got)
	}
	if got := tl.xAt(5 * time.Second); got != 100 {
		t.Errorf("xAt(5s) = %f, want 100", got)
	}
	if got := tl.xAt(99 * time.Second); got != 200 {
		t.Errorf("xAt(99s) = %f, want clamped 200", got)
	}
}

func TestTimelinePickTarget(t *testing.T) {
	tl := newSizedTimeline(t)
	tl.duration = 10 * time.Second
	tl.SetSegmentsSilent([]model.TimeRange{{Start: time.Second, End: 9 * time.Second}})
	if got, seg := tl.pickTarget(tl.xAt(time.Second)); got != tlIn || seg != 0 {
		t.Errorf("on the in handle: %v seg %d, want tlIn/0", got, seg)
	}
	if got, seg := tl.pickTarget(tl.xAt(9 * time.Second)); got != tlOut || seg != 0 {
		t.Errorf("on the out handle: %v seg %d, want tlOut/0", got, seg)
	}
	if got, _ := tl.pickTarget(tl.xAt(5 * time.Second)); got != tlPlayhead {
		t.Errorf("middle: %v, want tlPlayhead", got)
	}
	// Overlapping handles (in ≈ out): nearest wins; in wins ties.
	tl.SetSegmentsSilent([]model.TimeRange{{Start: 5 * time.Second, End: 5 * time.Second}})
	if got, _ := tl.pickTarget(tl.xAt(5*time.Second) + 2); got != tlIn {
		t.Errorf("tie at overlapping handles: %v, want tlIn", got)
	}
	tl.SetSegmentsSilent([]model.TimeRange{{Start: 5 * time.Second, End: 5*time.Second + 5*time.Millisecond}})
	if got, _ := tl.pickTarget(tl.xAt(tl.Segments()[0].End)); got != tlOut {
		t.Errorf("closer to out: %v, want tlOut", got)
	}
	// Multi-segment: each segment contributes its own handle pair; the
	// nearest handle across all segments wins.
	tl.SetSegmentsSilent([]model.TimeRange{
		{Start: time.Second, End: 3 * time.Second},
		{Start: 5 * time.Second, End: 7 * time.Second},
	})
	if got, seg := tl.pickTarget(tl.xAt(5 * time.Second)); got != tlIn || seg != 1 {
		t.Errorf("segment 2 in handle: %v seg %d, want tlIn/1", got, seg)
	}
	if got, seg := tl.pickTarget(tl.xAt(3 * time.Second)); got != tlOut || seg != 0 {
		t.Errorf("segment 1 out handle: %v seg %d, want tlOut/0", got, seg)
	}
}

func TestTimelineKeyboard(t *testing.T) {
	tl := newSizedTimeline(t)
	tl.SetDuration(10 * time.Second)
	steps := 0
	tl.stepFrame = func(at time.Duration, dir int) (time.Duration, bool) {
		steps++
		return at + time.Duration(dir)*100*time.Millisecond, true
	}
	var committed []time.Duration
	tl.onPlayhead = func(at time.Duration) { committed = append(committed, at) }

	tl.TypedKey(&fyne.KeyEvent{Name: fyne.KeyRight})
	if tl.Playhead() != 100*time.Millisecond || steps != 1 {
		t.Fatalf("right step = %s (steps %d), want 100ms/1", tl.Playhead(), steps)
	}
	tl.TypedKey(&fyne.KeyEvent{Name: fyne.KeyLeft})
	if tl.Playhead() != 0 {
		t.Fatalf("left step = %s, want 0", tl.Playhead())
	}

	// Shift+Right moves one second via the tracked modifier state.
	tl.KeyDown(&fyne.KeyEvent{Name: desktop.KeyShiftLeft})
	tl.TypedKey(&fyne.KeyEvent{Name: fyne.KeyRight})
	if tl.Playhead() != time.Second {
		t.Fatalf("shift+right = %s, want 1s", tl.Playhead())
	}
	tl.KeyUp(&fyne.KeyEvent{Name: desktop.KeyShiftLeft})
	tl.TypedKey(&fyne.KeyEvent{Name: fyne.KeyRight})
	if tl.Playhead() != 1100*time.Millisecond {
		t.Fatalf("after shift release = %s, want frame step to 1.1s", tl.Playhead())
	}

	// Ctrl+[ / Ctrl+] set the boundaries at the playhead.
	tl.SetSegments([]model.TimeRange{{Start: 0, End: tl.Duration()}})
	tl.KeyDown(&fyne.KeyEvent{Name: desktop.KeyControlLeft})
	tl.TypedKey(&fyne.KeyEvent{Name: fyne.KeyLeftBracket})
	if tl.Segments()[0].Start != 1100*time.Millisecond {
		t.Fatalf("ctrl+[ set in = %s, want 1.1s", tl.Segments()[0].Start)
	}
	tl.TypedKey(&fyne.KeyEvent{Name: fyne.KeyRightBracket})
	if tl.Segments()[0].End != 1100*time.Millisecond {
		t.Fatalf("ctrl+] set out = %s, want 1.1s", tl.Segments()[0].End)
	}
	tl.KeyUp(&fyne.KeyEvent{Name: desktop.KeyControlLeft})
	// Without ctrl held, brackets do nothing.
	tl.SetPlayhead(2 * time.Second)
	tl.TypedKey(&fyne.KeyEvent{Name: fyne.KeyLeftBracket})
	if tl.Segments()[0].Start != 1100*time.Millisecond {
		t.Fatalf("bracket without ctrl moved in to %s", tl.Segments()[0].Start)
	}
	// Only the four committed steps fired onPlayhead; boundary and
	// programmatic moves did not.
	if len(committed) != 4 {
		t.Fatalf("committed playhead moves = %d, want 4", len(committed))
	}
}

func TestTimelineFocusLossClearsModifiersAndDrag(t *testing.T) {
	tl := newSizedTimeline(t)
	tl.SetDuration(10 * time.Second)
	tl.FocusGained()
	tl.KeyDown(&fyne.KeyEvent{Name: desktop.KeyShiftLeft})
	tl.KeyDown(&fyne.KeyEvent{Name: desktop.KeyControlLeft})
	tl.drag = tlIn
	tl.FocusLost()
	if tl.shiftHeld || tl.ctrlHeld || tl.drag != tlNone {
		t.Fatalf("focus loss must clear modifiers and drag: shift %v ctrl %v drag %v", tl.shiftHeld, tl.ctrlHeld, tl.drag)
	}
	// A stale modifier must not affect a later key press.
	tl.FocusGained()
	tl.stepFrame = func(at time.Duration, dir int) (time.Duration, bool) {
		return at + time.Duration(dir)*100*time.Millisecond, true
	}
	tl.TypedKey(&fyne.KeyEvent{Name: fyne.KeyRight})
	if tl.Playhead() != 100*time.Millisecond {
		t.Fatalf("post-refocus step = %s, want frame step (not 1s)", tl.Playhead())
	}
}

func TestTimelineHoverDoesNotMovePlayhead(t *testing.T) {
	tl := newSizedTimeline(t)
	tl.SetDuration(10 * time.Second)
	tl.SetPlayhead(0)
	var hovered *time.Duration
	tl.onHover = func(at *time.Duration) { hovered = at }
	tl.MouseIn(&desktop.MouseEvent{PointEvent: fyne.PointEvent{Position: fyne.NewPos(100, 0)}})
	if hovered == nil || *hovered != 5*time.Second {
		t.Fatalf("hover = %v, want 5s", hovered)
	}
	if tl.Playhead() != 0 {
		t.Fatalf("hover must not move the committed playhead, got %s", tl.Playhead())
	}
	tl.MouseOut()
	if hovered != nil {
		t.Fatal("MouseOut must report the hover ended")
	}
}

func TestTimelineDisabledIgnoresKeys(t *testing.T) {
	tl := newSizedTimeline(t)
	tl.SetDuration(10 * time.Second)
	tl.Disable()
	if !tl.Disabled() {
		t.Fatal("timeline must report disabled")
	}
	tl.stepFrame = func(at time.Duration, dir int) (time.Duration, bool) {
		return at + time.Second, true
	}
	tl.TypedKey(&fyne.KeyEvent{Name: fyne.KeyRight})
	if tl.Playhead() != 0 {
		t.Fatalf("disabled timeline moved to %s", tl.Playhead())
	}
	tl.Enable()
	tl.TypedKey(&fyne.KeyEvent{Name: fyne.KeyRight})
	if tl.Playhead() != time.Second {
		t.Fatalf("enabled timeline did not step: %s", tl.Playhead())
	}
}

// The desktop driver delivers ctrl chords as desktop.CustomShortcut via
// fyne.Shortcutable, returning before TypedKey ever runs, so the Ctrl+[ /
// Ctrl+] boundary shortcuts must work through this path. Regression: only
// the TypedKey branch existed and the shortcuts were dead on the desktop
// backend.
func TestTimelineCtrlBracketsViaTypedShortcut(t *testing.T) {
	tl := newSizedTimeline(t)
	tl.SetDuration(10 * time.Second)
	tl.SetPlayhead(2 * time.Second)
	tl.SetSegments([]model.TimeRange{{Start: 0, End: tl.Duration()}})
	var commits int
	tl.onSegments = func([]model.TimeRange, int) {
		commits++
	}

	tl.TypedShortcut(&desktop.CustomShortcut{KeyName: fyne.KeyLeftBracket, Modifier: fyne.KeyModifierControl})
	if tl.Segments()[0].Start != 2*time.Second {
		t.Fatalf("ctrl+[ via shortcut set in = %s, want 2s", tl.Segments()[0].Start)
	}
	tl.TypedShortcut(&desktop.CustomShortcut{KeyName: fyne.KeyRightBracket, Modifier: fyne.KeyModifierControl})
	if tl.Segments()[0].End != 2*time.Second {
		t.Fatalf("ctrl+] via shortcut set out = %s, want 2s", tl.Segments()[0].End)
	}
	if commits != 2 {
		t.Fatalf("onSegments fired %d times, want 2", commits)
	}

	// Unrelated shortcuts must not move the boundaries.
	tl.TypedShortcut(&desktop.CustomShortcut{KeyName: fyne.KeyC, Modifier: fyne.KeyModifierControl})
	if tl.Segments()[0].Start != 2*time.Second || tl.Segments()[0].End != 2*time.Second || commits != 2 {
		t.Fatalf("unrelated shortcut changed boundaries: in=%s out=%s commits=%d",
			tl.Segments()[0].Start, tl.Segments()[0].End, commits)
	}

	// Disabled timelines ignore shortcuts, like they ignore TypedKey.
	tl.Disable()
	tl.SetPlayhead(3 * time.Second)
	tl.TypedShortcut(&desktop.CustomShortcut{KeyName: fyne.KeyLeftBracket, Modifier: fyne.KeyModifierControl})
	if tl.Segments()[0].Start != 2*time.Second {
		t.Fatalf("disabled timeline accepted shortcut: in=%s", tl.Segments()[0].Start)
	}
}

// The controller's trimControls loops disable widgets through
// fyne.Disableable assertions. Regression: the timeline implemented
// Enabled() instead of the interface's Disabled(), so the assertions
// failed and the timeline stayed interactive while trimming was off.
func TestTimelineSatisfiesDisableable(t *testing.T) {
	tl := newSizedTimeline(t)
	var d fyne.Disableable = tl
	if d.Disabled() {
		t.Fatal("fresh timeline must report enabled")
	}
	d.Disable()
	if !d.Disabled() || !tl.Disabled() {
		t.Fatal("Disable must disable")
	}
	d.Enable()
	if d.Disabled() || tl.Disabled() {
		t.Fatal("Enable must enable")
	}
}

func TestTimelineSetThumbnailProgressive(t *testing.T) {
	tl := newSizedTimeline(t)
	tl.SetThumbnails(make([]image.Image, 3))
	tl.SetThumbnail(1, image.NewRGBA(image.Rect(0, 0, 2, 2)))
	if tl.thumbs[1] == nil {
		t.Fatal("thumbnail slot 1 must be set")
	}
	tl.SetThumbnail(5, image.NewRGBA(image.Rect(0, 0, 2, 2)))
	if len(tl.thumbs) != 6 {
		t.Fatalf("strip grew to %d slots, want 6", len(tl.thumbs))
	}
	if tl.thumbs[2] != nil || tl.thumbs[4] != nil {
		t.Fatal("unset slots must stay nil placeholders")
	}
}

// --- enable / reset lifecycle ---

func TestSetTrimEnabledSeedsFullRangeAndResetKeepsEnabled(t *testing.T) {
	c := testTrimController(t)
	c.trimEnable.SetChecked(true)
	if !c.trimEnabled {
		t.Fatal("trim must be enabled")
	}
	if !c.trimSelection.Active() || c.trimSelection.Segments[0].Start != 0 || c.trimSelection.Segments[0].End != 10*time.Minute {
		t.Fatalf("first enable must seed the full range, got %v", c.trimSelection)
	}
	if c.trimIn.Text != "00:00:00.000" || c.trimOut.Text != "00:10:00.000" {
		t.Fatalf("boundary entries not seeded: %q / %q", c.trimIn.Text, c.trimOut.Text)
	}

	// Reset restores the full range and keeps trimming enabled: the
	// command keeps -ss/-t until trimming is disabled.
	c.trimSelection = withSegmentBoundary(c.trimSelection, 0, 3*time.Second, 7*time.Second)
	c.resetTrimSelection()
	if !c.trimEnabled {
		t.Fatal("reset must keep trimming enabled")
	}
	if c.trimSelection.Segments[0].Start != 0 || c.trimSelection.Segments[0].End != 10*time.Minute {
		t.Fatalf("reset must restore the full range, got %v", c.trimSelection)
	}

	// Disabling empties the request: no -ss/-t can reach the command.
	c.trimEnable.SetChecked(false)
	if c.trimEnabled || c.trimRequest().Active() {
		t.Fatal("disabling trim must empty the request")
	}
}

func TestSetTrimEnabledGatesOnKnownDuration(t *testing.T) {
	c := testTrimController(t)
	c.media = model.MediaInfo{}
	c.trimEnable.SetChecked(true)
	if c.trimEnabled {
		t.Fatal("trim must not enable without a known duration")
	}
}

// Reset moves the playhead to 0, and SetPlayhead fires no callback — the
// Frame preview must still follow the reset instead of showing whatever
// was on screen before (cache-hit path here; a miss takes the usual
// debounced explicit request).
func TestResetRefreshesCommittedPreview(t *testing.T) {
	c := testTrimController(t)
	c.trimEnabled = true
	stale := image.NewRGBA(image.Rect(0, 0, 2, 2))
	c.previews.cache.Put(5*time.Minute, stale)
	c.displayPreview(stale, 5*time.Minute)
	c.previews.cache.Put(0, image.NewRGBA(image.Rect(0, 0, 3, 3)))

	c.resetTrimSelection()

	if c.previewTimeLabel.Text != formatTimestamp(0) {
		t.Fatalf("preview label = %q, want the reset position", c.previewTimeLabel.Text)
	}
}

// Regression: loading a new input while trimming was enabled set
// trimEnabled=false before SetChecked(false) ran, so the checkbox's
// OnChanged callback early-returned in setTrimEnabled and the boundary
// entries, buttons, and timeline stayed interactive with trimming off —
// typing into them mutated the selection.
func TestNewInputDisablesTrimControls(t *testing.T) {
	c := testTrimController(t)
	c.trimEnable.SetChecked(true)

	c.resetTrimForNewInput()

	for name, w := range map[string]fyne.Disableable{
		"timeline": c.trimTimeline,
		"in":       c.trimIn,
		"out":      c.trimOut,
		"set in":   c.trimSetIn,
		"set out":  c.trimSetOut,
		"reset":    c.trimReset,
	} {
		if !w.Disabled() {
			t.Fatalf("%s control must be disabled after an input change", name)
		}
	}
	if c.trimEnable.Disabled() {
		t.Fatal("the enable checkbox itself must stay usable")
	}
}

// Regression: typing into a boundary entry re-fires onSegments on every
// keystroke (valid prefixes parse and apply immediately), and
// onTimelineSegments rewrote the very entry being typed into — the
// user's next keystrokes landed inside the reformatted text and
// corrupted it. The focused entry keeps its in-flight text; the other
// entry mirrors the committed value; an unfocused commit rewrites both.
func TestOnTimelineSegmentsKeepFocusedEntryText(t *testing.T) {
	c := testTrimController(t)
	c.trimEnabled = true
	c.trimSelection = model.TrimSelection{Segments: []model.TimeRange{{Start: 0, End: 5 * time.Second}}}
	// Attach the entries to a canvas so focus queries resolve (the real
	// layout does this via the window content).
	content := container.NewWithoutLayout(c.trimIn, c.trimOut)
	win := fynetest.NewWindow(content)
	defer win.Close()
	canvas := fyne.CurrentApp().Driver().CanvasForObject(c.trimIn)
	if canvas == nil {
		t.Fatal("entry must resolve to a canvas once attached")
	}

	canvas.Focus(c.trimIn) // focus before editing: the edit itself echoes through SetSegments→onSegments
	c.trimIn.SetText("00:00")
	if c.trimIn.Text != "00:00" {
		t.Fatalf("typing echo rewrote the focused entry: %q", c.trimIn.Text)
	}
	c.onTimelineSegments([]model.TimeRange{{Start: 0, End: 10 * time.Second}}, -1)
	if c.trimIn.Text != "00:00" {
		t.Fatalf("focused entry text rewritten mid-edit: %q", c.trimIn.Text)
	}
	if c.trimOut.Text != formatTimestamp(10*time.Second) {
		t.Fatalf("unfocused out entry = %q, want the committed boundary", c.trimOut.Text)
	}

	canvas.Focus(c.trimOut)
	c.trimOut.SetText("")
	c.onTimelineSegments([]model.TimeRange{{Start: time.Second, End: 9 * time.Second}}, -1)
	if c.trimOut.Text != "" {
		t.Fatalf("focused out entry rewritten mid-edit: %q", c.trimOut.Text)
	}
	if c.trimIn.Text != formatTimestamp(time.Second) {
		t.Fatalf("unfocused in entry = %q, want the committed boundary", c.trimIn.Text)
	}
}

// Regression: boundary display writes went through Entry.SetText, which
// fires OnChanged whenever the text differs — the editing callback then
// re-parsed the millisecond-truncated display text over the exact
// selection. A Ctrl+[ boundary at 66.667ms (30fps frame) collapsed to
// 66ms, degrading the microsecond-precision seek times generated from
// it. Display writes must not re-enter the editing path.
func TestBoundaryDisplayWritesDoNotReenterEditing(t *testing.T) {
	c := testTrimController(t)
	c.trimEnabled = true
	duration := 10 * time.Second
	c.media = model.MediaInfo{Duration: duration}
	c.trimTimeline.SetDuration(duration)
	// Seed the selection directly; SetText on the entries is itself an
	// editing path, so it runs before the state is pinned.
	c.trimIn.SetText(formatTimestamp(33333 * time.Microsecond))
	c.trimSelection = model.TrimSelection{Segments: []model.TimeRange{
		{Start: 33333 * time.Microsecond, End: 100 * time.Millisecond},
	}}

	// Ctrl+[ at 66.667ms commits the exact frame position; the entry
	// displays its millisecond truncation without feeding it back.
	c.trimTimeline.SetSegments([]model.TimeRange{{Start: 66667 * time.Microsecond, End: 100 * time.Millisecond}})
	c.onTimelineSegments(c.trimTimeline.Segments(), -1)
	if got := segmentBoundary(c.trimSelection, 0, 0); got != 66667*time.Microsecond {
		t.Fatalf("exact in boundary collapsed to display text: %s, want 66.667ms", got)
	}
	if c.trimIn.Text != formatTimestamp(66667*time.Microsecond) {
		t.Fatalf("in entry = %q, want the truncated display text", c.trimIn.Text)
	}
	if got := segmentBoundary(c.trimSelection, 0, 1); got != 100*time.Millisecond {
		t.Fatalf("out boundary disturbed: %s, want 100ms", got)
	}

	// Set In from the playhead keeps the exact position too.
	c.trimTimeline.SetPlayhead(133333 * time.Microsecond)
	c.setInFromPlayhead()
	if got := segmentBoundary(c.trimSelection, 0, 0); got != 133333*time.Microsecond {
		t.Fatalf("Set In lost frame precision: %s, want 133.333ms", got)
	}

	// A new-input reset clears the entries with display writes: the
	// empty text must not fire the editing callback and mark the fields
	// invalid (it parsed "" as an error after the valid flags were set).
	c.resetTrimForNewInput()
	if !c.trimInValid || !c.trimOutValid {
		t.Fatalf("reset left entries invalid: in=%v out=%v", c.trimInValid, c.trimOutValid)
	}
	if c.trimIn.Text != "" || c.trimOut.Text != "" {
		t.Fatalf("reset left entry text: %q / %q", c.trimIn.Text, c.trimOut.Text)
	}
}

// Regression (Codex review round 1): committing a handle drag replaced
// the whole authoritative selection with the timeline's clamped display
// copy, so an out-of-range boundary typed into one segment was silently
// clamped — and its validation error cleared — when another segment's
// handle was dragged. A drag commits only the segment it edited; the
// programmatic echo (SetSegments) keeps the wholesale semantics.
func TestDragCommitKeepsTypedBoundsOfOtherSegments(t *testing.T) {
	c := testTrimController(t)
	c.trimEnabled = true
	duration := 30 * time.Second
	c.media = model.MediaInfo{Duration: duration}
	c.trimTimeline.SetDuration(duration)
	setTrimSegments(c,
		model.TimeRange{Start: 0, End: 5 * time.Second},
		model.TimeRange{Start: 8 * time.Second, End: 12 * time.Second},
		model.TimeRange{Start: 15 * time.Second, End: 20 * time.Second},
	)

	// Type an out-of-range Out into segment 3: the typed value stays
	// authoritative while the timeline displays the clamped view.
	c.onTimelineSelected(2)
	c.trimOut.SetText("00:00:40.000")
	if got := segmentBoundary(c.trimSelection, 2, 1); got != 40*time.Second {
		t.Fatalf("typed out boundary = %s, want 40s kept authoritative", got)
	}

	// Drag segment 1's Out handle to 3s and commit the gesture.
	c.trimTimeline.drag = tlOut
	c.trimTimeline.dragSeg = 0
	c.trimTimeline.segments[0].End = 3 * time.Second
	c.trimTimeline.release()

	if got := segmentBoundary(c.trimSelection, 0, 1); got != 3*time.Second {
		t.Fatalf("dragged boundary = %s, want 3s committed", got)
	}
	if got := segmentBoundary(c.trimSelection, 2, 1); got != 40*time.Second {
		t.Fatalf("typed boundary clamped by an unrelated drag: %s, want 40s", got)
	}
	if !strings.Contains(c.trimMessage.Text, "exceeds the input duration") {
		t.Fatalf("validation error lost after unrelated drag: %q", c.trimMessage.Text)
	}

	// The programmatic echo (SetSegments) still commits the whole
	// clamped copy: the controller sent it, so it is authoritative.
	c.onTimelineSegments(c.trimTimeline.Segments(), -1)
	if got := segmentBoundary(c.trimSelection, 2, 1); got != duration {
		t.Fatalf("echo boundary = %s, want clamped to %s", got, duration)
	}
}

// Regression: Set In with an empty or malformed Out entry fired the loud
// SetRange, whose onRange marks both entries valid and rewrites the
// opposite one from the last valid value — unblocking conversion while
// the user's Out text was still invalid, and discarding the in-progress
// edit. A single-boundary action owns only its own entry and validity
// flag; the Ctrl+[ / Ctrl+] shortcuts share the handlers.
func TestSetBoundaryFromPlayheadKeepsOppositeEntryAuthoritative(t *testing.T) {
	c := testTrimController(t)
	c.trimEnabled = true
	c.media = model.MediaInfo{Duration: 10 * time.Minute}
	c.resetTrimSelection()
	c.trimTimeline.SetDuration(10 * time.Minute)
	c.setTrimControlsEnabled(true)

	// The user is editing Out: empty text, flagged invalid.
	c.trimOut.SetText("")
	c.trimOutValid = false
	c.trimTimeline.SetPlayhead(30 * time.Second)

	c.setInFromPlayhead()
	if got := segmentBoundary(c.trimSelection, 0, 0); got != 30*time.Second {
		t.Fatalf("Set In boundary = %s, want 30s", got)
	}
	if !c.trimInValid {
		t.Fatal("Set In must validate the In entry")
	}
	if c.trimOutValid {
		t.Fatal("Set In must not validate the Out entry while its text is invalid")
	}
	if c.trimOut.Text != "" {
		t.Fatalf("Set In rewrote the Out entry: %q, want the user's text preserved", c.trimOut.Text)
	}

	// Symmetric for Set Out with an in-progress In edit.
	c.trimIn.SetText("")
	c.trimInValid = false
	c.trimTimeline.SetPlayhead(time.Minute)
	c.setOutFromPlayhead()
	if got := segmentBoundary(c.trimSelection, 0, 1); got != time.Minute {
		t.Fatalf("Set Out boundary = %s, want 1m", got)
	}
	if c.trimInValid {
		t.Fatal("Set Out must not validate the In entry while its text is invalid")
	}
	if c.trimIn.Text != "" {
		t.Fatalf("Set Out rewrote the In entry: %q, want the user's text preserved", c.trimIn.Text)
	}

	// Ctrl+[ shares the single-boundary handler (playhead 45s is within
	// the stored [30s, 1m] range, so the widget's guard passes).
	c.trimOut.SetText("")
	c.trimOutValid = false
	c.trimTimeline.SetPlayhead(45 * time.Second)
	c.trimTimeline.TypedShortcut(&desktop.CustomShortcut{
		KeyName:  fyne.KeyLeftBracket,
		Modifier: fyne.KeyModifierControl,
	})
	if got := segmentBoundary(c.trimSelection, 0, 0); got != 45*time.Second {
		t.Fatalf("Ctrl+[ boundary = %s, want 45s", got)
	}
	if c.trimOutValid || c.trimOut.Text != "" {
		t.Fatalf("Ctrl+[ disturbed the Out entry: valid=%v text=%q", c.trimOutValid, c.trimOut.Text)
	}
}

// Regression: a typed out-of-range boundary was clamped by SetRange and
// echoed back through onRange, overwriting the typed selection: the
// field kept showing 00:11:00.000 on a 10-minute input while the stored
// selection (and the generated command) silently used 10 minutes, and
// ValidateTrim — which rejects End > Duration — never saw the typed
// value. The typed value must stay authoritative; only the timeline's
// view clamps.
func TestTypedOutOfRangeBoundaryStaysAuthoritative(t *testing.T) {
	c := testTrimController(t)
	c.trimEnabled = true
	duration := 10 * time.Minute
	c.media = model.MediaInfo{Duration: duration}
	c.resetTrimSelection()

	c.setTrimBoundary("out", "00:11:00.000")
	if got := segmentBoundary(c.trimSelection, 0, 1); got != 11*time.Minute {
		t.Fatalf("typed out boundary clamped to %s, want the typed 11m", got)
	}
	if out := c.trimTimeline.Segments()[0].End; out != duration {
		t.Fatalf("timeline displays out %s, want the clamped view %s", out, duration)
	}
	if !strings.Contains(c.trimMessage.Text, "exceeds the input duration") {
		t.Fatalf("validation message = %q, want the exceeds-duration error", c.trimMessage.Text)
	}
	sel := c.trimRequest()
	if !sel.Active() || segmentBoundary(sel, 0, 1) != 11*time.Minute {
		t.Fatalf("trimRequest lost the typed boundary: %+v", sel)
	}
	if result := validation.ValidateTrim(sel, &c.media, "libx265"); result.Valid() {
		t.Fatal("Start must see the typed out-of-range boundary as invalid")
	}

	// A crossed typed pair stays authoritative the same way (the
	// timeline displays it as-is) and validation rejects it.
	c.setTrimBoundary("in", "00:12:00.000")
	if got := segmentBoundary(c.trimSelection, 0, 0); got != 12*time.Minute {
		t.Fatalf("typed in boundary = %s, want the typed 12m", got)
	}
	if result := validation.ValidateTrim(c.trimRequest(), &c.media, "libx265"); result.Valid() {
		t.Fatal("crossed boundaries must fail validation")
	}
}

// Regression: a queued frame walk survived a manual seek — when the
// window fetch landed, applyPending walked from the stale position and
// moved the playhead back, overriding the user's newer click.
func TestManualSeekRetiresQueuedFrameWalk(t *testing.T) {
	c := testTrimController(t)
	c.trimEnabled = true

	// A frame step at 5s queues work (with a fetch in flight in
	// production).
	c.previews.index.queue(1, 5*time.Second)

	// The user clicks 30s: movePlayhead commits, then the callback runs.
	c.trimTimeline.SetPlayhead(30 * time.Second)
	c.onTimelinePlayhead(30 * time.Second)
	if c.previews.index.pending != 0 {
		t.Fatalf("manual seek must retire the queued walk, pending = %d", c.previews.index.pending)
	}

	// The window lands: nothing is applied, the playhead stays.
	at, state := c.previews.index.applyWindow(4*time.Second, 6*time.Second,
		[]time.Duration{5 * time.Second, 5*time.Second + 33*time.Millisecond}, c.media.Duration, c.media.Duration)
	if state != frameStepIdle {
		t.Fatalf("landing state = %v, want idle", state)
	}
	c.applyFrameStep(at, state)
	if c.trimTimeline.Playhead() != 30*time.Second {
		t.Fatalf("playhead yanked to %s, want the manually chosen 30s", c.trimTimeline.Playhead())
	}
}

// Reset retires the queued walk the same way: it is a committed move to 0.
func TestResetRetiresQueuedFrameWalk(t *testing.T) {
	c := testTrimController(t)
	c.trimEnabled = true
	c.trimTimeline.SetPlayhead(3 * time.Minute)
	c.previews.index.queue(1, 2*time.Minute)

	c.resetTrimSelection()

	if c.previews.index.pending != 0 {
		t.Fatalf("reset must retire the queued walk, pending = %d", c.previews.index.pending)
	}
	if c.trimTimeline.Playhead() != 0 {
		t.Fatalf("playhead = %s, want 0 after reset", c.trimTimeline.Playhead())
	}
}

// Regression: at capacity, merging a backward window always dropped the
// oldest timestamps — exactly the low-end frames the pending backward
// walk had just fetched. The walk target then sat outside the window,
// re-queued, the same window was fetched and evicted again: an unbounded
// probe loop with no progress. Eviction now anchors the retained slice
// at the walk position and extends it in the walk direction.
func TestFrameIndexEvictsAwayFromBackwardWalk(t *testing.T) {
	index := newFrameIndex()
	index.capacity = 4
	duration := 10 * time.Minute

	// A full window near the middle.
	index.applyWindow(20*time.Second, 24*time.Second,
		[]time.Duration{20 * time.Second, 21 * time.Second, 22 * time.Second, 23 * time.Second}, duration, duration)

	// The user steps backward past the window edge; the adjacent (lower)
	// window lands and merges past capacity. The retained slice ends at
	// the walk position and reaches backward, so the fetched low frames
	// survive and the walk moves.
	index.queue(-1, 20*time.Second)
	at, state := index.applyWindow(18*time.Second, 20*time.Second,
		[]time.Duration{17 * time.Second, 18 * time.Second, 19 * time.Second}, duration, duration)
	if state != frameStepMoved || at != 19*time.Second {
		t.Fatalf("backward walk = %s/%v, want 19s/moved", at, state)
	}
	if index.windowStart != 18*time.Second {
		t.Fatalf("windowStart = %s, want the just-fetched 18s (low end kept)", index.windowStart)
	}

	// Forward walks anchor the same way: the slice starts at the walk
	// position and reaches forward, dropping the low end.
	forward := newFrameIndex()
	forward.capacity = 4
	forward.applyWindow(20*time.Second, 24*time.Second,
		[]time.Duration{20 * time.Second, 21 * time.Second, 22 * time.Second, 23 * time.Second}, duration, duration)
	forward.queue(1, 23*time.Second)
	forward.applyWindow(23*time.Second, 25*time.Second,
		[]time.Duration{24 * time.Second, 25 * time.Second}, duration, duration)
	if forward.windowStart != 23*time.Second {
		t.Fatalf("forward eviction windowStart = %s, want 23s (anchored at the walk position)", forward.windowStart)
	}
}

func TestResetTrimForNewInputClearsState(t *testing.T) {
	c := testTrimController(t)
	c.trimEnable.SetChecked(true)
	c.previews.index.applyWindow(0, time.Second, []time.Duration{0, time.Second}, c.media.Duration, c.media.Duration)
	c.previews.cache.Put(time.Second, image.NewRGBA(image.Rect(0, 0, 2, 2)))
	c.previews.stripRunning = true

	c.resetTrimForNewInput()
	if c.trimEnabled || c.trimSelection.Active() {
		t.Fatal("input reset must disable trimming and clear the selection")
	}
	if c.trimIn.Text != "" || c.trimOut.Text != "" {
		t.Fatalf("entries must be cleared: %q %q", c.trimIn.Text, c.trimOut.Text)
	}
	if c.trimTimeline.Duration() != 0 || len(c.trimTimeline.thumbs) != 0 {
		t.Fatal("timeline must be cleared")
	}
	if c.previews.index.covers(time.Second) {
		t.Fatal("frame index must be reset")
	}
	if _, ok := c.previews.cache.Get(time.Second); ok {
		t.Fatal("preview cache must be cleared")
	}
	if c.previews.stripRunning {
		t.Fatal("strip state must be reset")
	}
	if c.previewImage.Image != nil {
		t.Fatal("preview image must be cleared")
	}
}

func TestSetInputResetsStaleStateSynchronously(t *testing.T) {
	c := testTrimController(t)
	c.trimEnable.SetChecked(true)
	if !c.trimEnabled {
		t.Fatal("precondition: trimming enabled for the old input")
	}

	c.setInput("/new/input.mkv")
	if c.media.Duration != 0 {
		t.Fatalf("stale MediaInfo must be invalidated synchronously, duration = %s", c.media.Duration)
	}
	if c.inputPath != "/new/input.mkv" {
		t.Fatalf("input path = %q", c.inputPath)
	}
	if c.trimEnabled {
		t.Fatal("trimming must be disabled by an input change")
	}
	if c.inputGeneration == 0 {
		t.Fatal("input generation must advance")
	}
	if !strings.Contains(c.mediaInfo.Text, "Inspecting") {
		t.Fatalf("status = %q, want the inspecting message", c.mediaInfo.Text)
	}
}

func TestEnsureExtractorRebuildsOnPathChange(t *testing.T) {
	c := testTrimController(t)
	first := c.ensureExtractor()
	second := c.ensureExtractor()
	if first != second {
		t.Fatal("same path must reuse the extractor")
	}
	c.config.FFmpeg.FFmpegPath = "/usr/bin/ffmpeg"
	third := c.ensureExtractor()
	if third == first {
		t.Fatal("a changed path must rebuild the extractor")
	}
}

// Regression: a fresh index's empty window "covers" position 0, and the
// old ensureFrameIndex guard refused to fetch on that basis — enabling
// trimming and pressing Right stayed in "Loading frame index…" forever
// because no ffprobe ever started. Fetches must trigger for the initial
// window and for steps past either cached edge; single-flight, failure,
// and empty-input guards stay.
func TestEnsureFrameIndexFetchesAroundUncoveredEdges(t *testing.T) {
	c := testTrimController(t)
	fetches := make(chan [2]time.Duration, 8)
	c.previews.indexFetch = func(ctx context.Context, exe, path string, from, to time.Duration) ([]time.Duration, time.Duration, error) {
		fetches <- [2]time.Duration{from, to}
		return []time.Duration{0, 100 * time.Millisecond}, 0, nil
	}

	// First enable: the fresh (empty) index must fetch the window at 0.
	c.setTrimEnabled(true)
	select {
	case got := <-fetches:
		if got != [2]time.Duration{0, 2 * time.Second} {
			t.Fatalf("first window = %v, want [0s, 2s]", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no window fetch started for a fresh index at position 0")
	}

	// The in-flight fetch must be single-flight.
	c.ensureFrameIndex(0)
	select {
	case got := <-fetches:
		t.Fatalf("unexpected second fetch while in flight: %v", got)
	default:
	}

	// A failed index disables fetching entirely.
	c.previews.index.applyError(errors.New("probe failed"))
	c.ensureFrameIndex(time.Second)
	select {
	case got := <-fetches:
		t.Fatalf("fetch started despite a failed index: %v", got)
	default:
	}

	// No fetch without an input.
	c.previews.index = newFrameIndex()
	c.inputPath = ""
	c.ensureFrameIndex(0)
	select {
	case got := <-fetches:
		t.Fatalf("fetch started without an input path: %v", got)
	default:
	}
}

func TestLazyNoPreviewWorkWithoutMedia(t *testing.T) {
	c := testTrimController(t)
	c.media = model.MediaInfo{}
	// Enabling without an input/duration must not schedule any async
	// work, and direct preview requests must be no-ops.
	c.trimEnable.SetChecked(true)
	c.requestPreview(time.Second)
	c.requestHover(time.Second)
	if c.previews.previewTimer != nil || c.previews.hoverTimer != nil {
		t.Fatal("no preview timer may exist without a known duration")
	}
	if c.previews.stripRunning {
		t.Fatal("thumbnail strip must not run without a known duration")
	}
	if c.previews.pendingPreview || c.previewSpinner.Visible() || !c.previewVeil.Hidden {
		t.Fatal("no pending indication may arm without a known duration")
	}
}

// TestTrimTabLayout pins the space-saving tab composition: no heading
// cards, an expanding frame preview cell at the top with every control
// glued to the bottom edge of the tab by a Border layout, the
// timestamps overlaid on a translucent chip over the image, and
// In/Out/Duration/Reset sharing one row.
// TestTrimBoundaryEntriesUsable pins the boundary row's entry sizing:
// a plain HBox shrinks the In/Out entries to their natural
// couple-of-characters width, so they sit in fixed-width cells wide
// enough for a full HH:MM:SS.mmm timestamp, and the duration label
// shows the bare timestamp next to its "Duration" caption.
func TestTrimBoundaryEntriesUsable(t *testing.T) {
	app := fynetest.NewApp()
	defer app.Quit()

	c := testController()
	c.createWidgets()

	rowObj := trimBoundariesRow(c)
	row, ok := rowObj.(*fyne.Container)
	if !ok {
		t.Fatalf("boundary row is %T, want a container", rowObj)
	}
	// Drive the HBox layout: without the fixed-width cells the entries
	// would collapse to their natural width.
	row.Resize(row.MinSize())
	if w := c.trimIn.Size().Width; w < 100 {
		t.Errorf("In entry width = %.0f, want at least 100 to fit HH:MM:SS.mmm", w)
	}
	if w := c.trimOut.Size().Width; w < 100 {
		t.Errorf("Out entry width = %.0f, want at least 100 to fit HH:MM:SS.mmm", w)
	}
	if h := c.trimIn.Size().Height; h < c.trimSetIn.MinSize().Height {
		t.Errorf("In entry height = %.0f, want at least the button height %.0f",
			h, c.trimSetIn.MinSize().Height)
	}

	c.trimEnabled = true
	total := 2*time.Minute + 58*time.Second + 850*time.Millisecond
	c.trimSelection = model.TrimSelection{Segments: []model.TimeRange{{Start: 0, End: total}}}
	c.updateTrimDurationLabel()
	if got := c.trimDuration.Text; got != formatTimestamp(total) {
		t.Errorf("duration label = %q, want the bare timestamp %q", got, formatTimestamp(total))
	}
}

// TestTrimStatusLabelsCollapseWhenEmpty pins the dead-space fix: the
// validation message and preview status lines are hidden while empty,
// so they cost no vertical space between the timeline and the boundary
// row (Fyne box layouts skip hidden children entirely).
func TestTrimStatusLabelsCollapseWhenEmpty(t *testing.T) {
	c := testTrimController(t)

	if !c.trimMessage.Hidden || !c.previewStatus.Hidden {
		t.Fatal("empty trim message and preview status must start hidden")
	}

	c.setPreviewStatus("Loading frame index…")
	if c.previewStatus.Hidden {
		t.Error("a non-empty preview status must be visible")
	}
	c.setPreviewStatus("")
	if !c.previewStatus.Hidden {
		t.Error("a cleared preview status must hide again")
	}

	c.setTrimMessage("Error: In timecode is invalid")
	if c.trimMessage.Hidden {
		t.Error("a non-empty trim message must be visible")
	}

	// A clean validation run hides the message again; the stream-copy
	// warning shows it.
	c.trimEnabled = true
	c.trimSelection = model.TrimSelection{Segments: []model.TimeRange{{Start: time.Second, End: 5 * time.Second}}}
	c.trimIn.Text = formatTimestamp(time.Second)
	c.trimInValid = true
	c.trimOut.Text = formatTimestamp(5 * time.Second)
	c.trimOutValid = true
	c.updateTrimMessages("libx265", model.AudioSettings{})
	if !c.trimMessage.Hidden {
		t.Errorf("a clean validation run must hide the trim message, got %q", c.trimMessage.Text)
	}
	c.updateTrimMessages("copy", model.AudioSettings{})
	if c.trimMessage.Hidden {
		t.Errorf("the stream-copy warning must show the trim message, got %q", c.trimMessage.Text)
	}

	c.resetTrimSelection()
	if !c.trimMessage.Hidden {
		t.Error("reset must clear and hide the trim message")
	}
}

func TestTrimTabLayout(t *testing.T) {
	app := fynetest.NewApp()
	defer app.Quit()

	c := testController()
	c.createWidgets()
	c.buildLayout()

	trimTab := c.tabs.Items[1]
	if trimTab.Text != "Trim" {
		t.Fatalf("second tab = %q, want Trim", trimTab.Text)
	}
	content, ok := trimTab.Content.(*fyne.Container)
	if !ok {
		t.Fatalf("trim tab content is %T, want a container", trimTab.Content)
	}

	borderType := reflect.TypeOf(layout.NewBorderLayout(nil, nil, nil, nil))
	if reflect.TypeOf(content.Layout) != borderType {
		t.Fatalf("trim tab layout is %T, want a Border layout", content.Layout)
	}
	// container.NewBorder appends the non-nil slots after the center,
	// so the children are [preview cell, bottom controls].
	if len(content.Objects) != 2 {
		t.Fatalf("trim tab children = %d, want [preview, controls]", len(content.Objects))
	}
	previewCell, ok := content.Objects[0].(*fyne.Container)
	if !ok {
		t.Fatalf("border center is %T, want the preview cell container", content.Objects[0])
	}
	controls, ok := content.Objects[1].(*fyne.Container)
	if !ok {
		t.Fatalf("border bottom is %T, want the controls container", content.Objects[1])
	}

	var boundaries, segments, transport *fyne.Container
	containsAll := func(objs []fyne.CanvasObject, want ...fyne.CanvasObject) bool {
		for _, w := range want {
			found := false
			for _, o := range objs {
				if o == w {
					found = true
					break
				}
			}
			if !found {
				return false
			}
		}
		return true
	}
	var walk func(box *fyne.Container)
	walk = func(box *fyne.Container) {
		if containsAll(box.Objects, c.trimReset) {
			boundaries = box
		}
		if containsAll(box.Objects, c.trimExport, c.trimAdd, c.trimDelete) {
			segments = box
		}
		if containsAll(box.Objects, c.trimEnable, c.trimPlay, c.trimPlaySelection) {
			transport = box
		}
		for _, obj := range box.Objects {
			switch o := obj.(type) {
			case *fyne.Container:
				walk(o)
			case *widget.Card:
				t.Errorf("trim tab must not contain heading cards, found %q", o.Title)
			}
		}
	}
	walk(content)

	// The chip lives inside the preview cell, over the image's top edge.
	var chip *canvas.Rectangle
	var findChip func(box *fyne.Container)
	findChip = func(box *fyne.Container) {
		for _, obj := range box.Objects {
			switch o := obj.(type) {
			case *fyne.Container:
				findChip(o)
			case *canvas.Rectangle:
				if n := color.NRGBAModel.Convert(o.FillColor).(color.NRGBA); n.A < 0xff {
					chip = o
				}
			}
		}
	}
	findChip(previewCell)

	if boundaries == nil {
		t.Fatal("In, Out and Reset must share one row container")
	}
	if segments == nil {
		t.Fatal("export mode and segment buttons must share one row container")
	}
	if transport == nil {
		t.Fatal("the enable check and the Play buttons must share one row container")
	}
	// The controls VBox is glued to the bottom edge, in this order; the
	// transport buttons ride on the enable check's row.
	wantControls := []fyne.CanvasObject{
		c.previewStatus, transport, c.trimTimeline, c.trimMessage, segments, boundaries,
	}
	if len(controls.Objects) != len(wantControls) {
		t.Fatalf("bottom controls hold %d rows, want %d", len(controls.Objects), len(wantControls))
	}
	for i, w := range wantControls {
		if controls.Objects[i] != w {
			t.Errorf("bottom control row %d = %T, want %T", i, controls.Objects[i], w)
		}
	}

	// The preview cell fills the center slot and stretches the image to
	// the whole cell — resizing the tab resizes the preview image.
	previewCell.Resize(fyne.NewSize(800, 450))
	if got := c.previewImage.Size(); got != fyne.NewSize(800, 450) {
		t.Errorf("preview image size after resize = %v, want the whole 800x450 cell", got)
	}
	if c.previewImage.FillMode != canvas.ImageFillContain {
		t.Error("preview image must letterbox (ImageFillContain) inside the expanding cell")
	}
	// The chip lives inside the preview cell, over the image's top edge.
	if chip == nil {
		t.Fatal("preview timestamps must be drawn on a translucent (alpha < 255) chip over the preview image")
	}
}

// The extraction-in-flight spinner is an overlay floating over the
// preview image's center at its natural square size, and its rotation
// animation runs only while it is visible.
func TestPreviewSpinnerOverlay(t *testing.T) {
	app := fynetest.NewApp()
	defer app.Quit()

	c := testController()
	c.createWidgets()

	// The spinner must stay wired into the preview section's stack, or
	// it would never be laid out or shown.
	var walk func(box *fyne.Container)
	found := false
	walk = func(box *fyne.Container) {
		for _, obj := range box.Objects {
			if obj == c.previewSpinner {
				found = true
			}
			if inner, ok := obj.(*fyne.Container); ok {
				walk(inner)
			}
		}
	}
	if section, ok := trimPreviewSection(c).(*fyne.Container); ok {
		walk(section)
	}
	if !found {
		t.Fatal("the spinner must live inside the preview section's stack")
	}

	// Hidden from birth: no rotation before first Show.
	if c.previewSpinner.anim != nil {
		t.Fatal("a hidden-from-birth spinner must not animate")
	}
	c.previewSpinner.Show()
	if c.previewSpinner.anim == nil {
		t.Fatal("Show must start the rotation")
	}

	// Centered on both axes at its natural square size.
	cell, ok := trimPreviewSection(c).(*fyne.Container)
	if !ok {
		t.Fatalf("preview section is %T, want a container", trimPreviewSection(c))
	}
	cell.Resize(fyne.NewSize(800, 450))
	want := c.previewSpinner.MinSize()
	if want.Width <= 0 || want.Width != want.Height {
		t.Fatalf("spinner min size = %v, want a nonzero square", want)
	}
	if got := c.previewSpinner.Size(); got != want {
		t.Errorf("spinner size = %v, want its natural %v", got, want)
	}
	wantPos := fyne.NewPos((800-want.Width)/2, (450-want.Height)/2)
	if got := c.previewSpinner.Position(); got != wantPos {
		t.Errorf("spinner position = %v, want centered at %v", got, wantPos)
	}

	// Hiding stops the rotation.
	c.previewSpinner.Hide()
	if c.previewSpinner.anim != nil {
		t.Error("Hide must stop the rotation")
	}
}

// Regression: a successful preview cleared the indexing-failure status —
// after a Shift+arrow seek the "Frame-accurate stepping unavailable"
// diagnostic vanished while the failure state stayed set, so frame-step
// presses silently did nothing with no explanation anywhere. The
// failure persists for the input; its message must too. A new input
// replaces the index, and the message retires with it.
func TestPreviewStatusKeepsIndexingFailure(t *testing.T) {
	c := testTrimController(t)
	c.trimEnabled = true
	c.previews.index.applyError(errors.New("probe failed"))
	c.setPreviewStatus("Frame-accurate stepping unavailable: probe failed")
	if got := c.previewStatus.Text; !strings.Contains(got, "Frame-accurate stepping unavailable") {
		t.Fatalf("status = %q, want the failure message", got)
	}

	// A successful preview after a time-based seek clears the transient
	// status; the persistent failure must re-surface, not vanish.
	c.setPreviewStatus("")
	if got := c.previewStatus.Text; !strings.Contains(got, "probe failed") || c.previewStatus.Hidden {
		t.Fatalf("status = %q hidden=%v, want the preserved failure", c.previewStatus.Text, c.previewStatus.Hidden)
	}

	// A fresh input replaces the index: the failure is gone and the
	// status clears normally.
	c.previews.index = newFrameIndex()
	c.setPreviewStatus("")
	if !c.previewStatus.Hidden {
		t.Fatalf("status = %q, want hidden after the index reset", c.previewStatus.Text)
	}
}

// Regression: eviction used the walk direction alone. A Right walk
// parked just below the cached window's start fetched the low-end
// frames around pendingAt, and the front eviction threw exactly those
// away — applyPending found its position uncovered, re-fetched the
// identical range, and every landing evicted it again: stepping stuck
// while ffprobe was spawned forever. Eviction now anchors the retained
// slice at the walk position, extending in the walk direction.
func TestFrameIndexEvictsAroundPendingPosition(t *testing.T) {
	index := newFrameIndex()
	index.capacity = 5
	duration := 60 * time.Second
	index.applyWindow(10*time.Second, 20*time.Second,
		[]time.Duration{10 * time.Second, 12 * time.Second, 14 * time.Second, 16 * time.Second, 18 * time.Second, 20 * time.Second}, duration, duration)

	// Right from 9s: the walk queues at 9s, below the window start.
	if _, state := index.step(9*time.Second, 1, duration); state != frameStepLoading {
		t.Fatalf("state = %v, want loading", state)
	}
	// The fetch lands [7s, 11s]; merged over capacity, the retained
	// slice starts at the walk position 9s and reaches forward, keeping
	// the walk's neighbor and the frames the walk is heading into.
	at, state := index.applyWindow(7*time.Second, 11*time.Second,
		[]time.Duration{7 * time.Second, 8 * time.Second, 9 * time.Second, 10 * time.Second, 11 * time.Second}, duration, duration)
	if state != frameStepMoved || at != 10*time.Second {
		t.Fatalf("walk = %s/%v, want 10s/moved", at, state)
	}
	if index.windowStart != 9*time.Second || index.windowEnd != 14*time.Second {
		t.Fatalf("retained window = [%s, %s], want [9s, 14s]", index.windowStart, index.windowEnd)
	}
}

// Regression: when a single fetch window holds more frames than the
// cache capacity (a 2s window at 2500fps against 4096 entries), the
// midpoint rule kept the half ending exactly at the walk position —
// the walk's neighbor sat in the evicted half, the landing re-fetched
// the identical range and re-evicted the neighbor, and pressing Right
// looped ffprobe forever with no movement. The anchored slice keeps
// the neighbor and the walk moves in one landing.
func TestFrameIndexEvictionKeepsWalkNeighborOnDenseSources(t *testing.T) {
	t.Parallel()
	index := newFrameIndex()
	index.capacity = 4
	duration := time.Minute

	// A dense source: the initial window holds more frames than fit.
	index.applyWindow(0, 200*time.Millisecond,
		[]time.Duration{0, 50 * time.Millisecond, 100 * time.Millisecond, 150 * time.Millisecond, 200 * time.Millisecond}, duration, duration)

	// Right at the cached end 150ms: no forward neighbor, fetch queued.
	if _, state := index.step(150*time.Millisecond, 1, duration); state != frameStepLoading {
		t.Fatalf("state = %v, want loading", state)
	}
	// The adjacent window lands and merges over capacity: the retained
	// slice starts at the walk position 150ms and reaches forward.
	at, state := index.applyWindow(150*time.Millisecond, 350*time.Millisecond,
		[]time.Duration{
			150 * time.Millisecond, 200 * time.Millisecond, 250 * time.Millisecond,
			300 * time.Millisecond, 350 * time.Millisecond,
		}, duration, duration)
	if state != frameStepMoved || at != 200*time.Millisecond {
		t.Fatalf("dense walk = %s/%v, want 200ms/moved (neighbor retained)", at, state)
	}
}

func TestTimelineSetSegmentsClampsAndFires(t *testing.T) {
	tl := newSizedTimeline(t)
	tl.SetDuration(10 * time.Second)
	var fired [][]model.TimeRange
	tl.onSegments = func(segments []model.TimeRange, _ int) {
		fired = append(fired, segments)
	}
	// Out-of-range bounds clamp; the loud variant fires onSegments with
	// the clamped copy.
	tl.SetSegments([]model.TimeRange{{Start: -time.Second, End: 20 * time.Second}})
	if seg := tl.Segments()[0]; seg.Start != 0 || seg.End != 10*time.Second {
		t.Fatalf("clamped segment = %v, want [0, 10s]", seg)
	}
	if len(fired) != 1 || fired[0][0].Start != 0 || fired[0][0].End != 10*time.Second {
		t.Fatalf("onSegments fired with %v", fired)
	}
	// The silent variant updates the view without the callback.
	tl.SetSegmentsSilent([]model.TimeRange{{Start: time.Second, End: 2 * time.Second}})
	if len(fired) != 1 {
		t.Fatalf("SetSegmentsSilent fired onSegments %d times", len(fired)-1)
	}
	if tl.Segments()[0].Start != time.Second {
		t.Fatal("SetSegmentsSilent must still update the view")
	}
	// A selection past the end folds back into range.
	tl.SetSelected(5)
	if tl.Selected() != 0 {
		t.Fatalf("selection past the end = %d, want 0", tl.Selected())
	}
}

func TestTimelineDragClampsToNeighbors(t *testing.T) {
	tl := newSizedTimeline(t)
	tl.SetDuration(30 * time.Second)
	segments := []model.TimeRange{
		{Start: time.Second, End: 5 * time.Second},
		{Start: 8 * time.Second, End: 12 * time.Second},
		{Start: 15 * time.Second, End: 20 * time.Second},
	}
	tl.SetSegmentsSilent(segments)

	// Dragging segment 1's In below segment 0's End clamps to adjacency.
	tl.MouseDown(&desktop.MouseEvent{
		Button:     desktop.MouseButtonPrimary,
		PointEvent: fyne.PointEvent{Position: fyne.NewPos(tl.xAt(8*time.Second), 0)},
	})
	tl.Dragged(&fyne.DragEvent{PointEvent: fyne.PointEvent{Position: fyne.NewPos(tl.xAt(3*time.Second), 0)}})
	tl.DragEnd()
	if got := tl.Segments()[1].Start; got != 5*time.Second {
		t.Fatalf("segment 1 In dragged to %s, want clamped 5s (segment 0 End)", got)
	}

	// Dragging segment 1's Out past segment 2's Start clamps likewise.
	tl.MouseDown(&desktop.MouseEvent{
		Button:     desktop.MouseButtonPrimary,
		PointEvent: fyne.PointEvent{Position: fyne.NewPos(tl.xAt(12*time.Second), 0)},
	})
	tl.Dragged(&fyne.DragEvent{PointEvent: fyne.PointEvent{Position: fyne.NewPos(tl.xAt(18*time.Second), 0)}})
	tl.DragEnd()
	if got := tl.Segments()[1].End; got != 15*time.Second {
		t.Fatalf("segment 1 Out dragged to %s, want clamped 15s (segment 2 Start)", got)
	}

	// No overlaps were created anywhere.
	for i := 1; i < len(tl.Segments()); i++ {
		if tl.Segments()[i].Start < tl.Segments()[i-1].End {
			t.Fatalf("drag created an overlap: %v", tl.Segments())
		}
	}

	// A handle drag on segment 0 keeps segment 2 untouched. The click at
	// 5s ties seg0's Out with seg1's In (both moved to 5s by the earlier
	// drags); the earlier candidate wins — seg0's Out — and dragging it
	// right clamps to seg1's current Start (adjacency).
	tl.MouseDown(&desktop.MouseEvent{
		Button:     desktop.MouseButtonPrimary,
		PointEvent: fyne.PointEvent{Position: fyne.NewPos(tl.xAt(5*time.Second), 0)},
	})
	tl.Dragged(&fyne.DragEvent{PointEvent: fyne.PointEvent{Position: fyne.NewPos(tl.xAt(9*time.Second), 0)}})
	tl.DragEnd()
	if tl.Segments()[2] != segments[2] {
		t.Fatalf("segment 2 disturbed: %v", tl.Segments()[2])
	}
	if got := tl.Segments()[0].End; got != 5*time.Second {
		t.Fatalf("segment 0 End dragged to %s, want clamped 5s (segment 1 Start)", got)
	}
}

func TestTimelineSelectionViaRegionClickAndHandleGrab(t *testing.T) {
	tl := newSizedTimeline(t)
	tl.SetDuration(30 * time.Second)
	tl.SetSegmentsSilent([]model.TimeRange{
		{Start: time.Second, End: 5 * time.Second},
		{Start: 8 * time.Second, End: 12 * time.Second},
		{Start: 15 * time.Second, End: 20 * time.Second},
	})
	var selected []int
	tl.onSelected = func(index int) { selected = append(selected, index) }

	// A region click inside segment 2 selects it AND moves the playhead.
	tl.MouseDown(&desktop.MouseEvent{
		Button:     desktop.MouseButtonPrimary,
		PointEvent: fyne.PointEvent{Position: fyne.NewPos(tl.xAt(17*time.Second), 0)},
	})
	if tl.Selected() != 2 || len(selected) != 1 || selected[0] != 2 {
		t.Fatalf("region click selected %d (%v), want 2", tl.Selected(), selected)
	}
	// xAt/timeAt round-trips through 200px lose a fraction of a
	// microsecond at 17/30 of the span; compare with a tolerance.
	if diff := tl.Playhead() - 17*time.Second; diff > 10*time.Millisecond || diff < -10*time.Millisecond {
		t.Fatalf("region click moved the playhead to %s, want 17s", tl.Playhead())
	}
	// Releasing the playhead drag fires no further selection.
	tl.DragEnd()
	if len(selected) != 1 {
		t.Fatalf("release re-fired selection: %v", selected)
	}

	// Grabbing segment 0's out handle selects segment 0.
	tl.MouseDown(&desktop.MouseEvent{
		Button:     desktop.MouseButtonPrimary,
		PointEvent: fyne.PointEvent{Position: fyne.NewPos(tl.xAt(5*time.Second), 0)},
	})
	if tl.Selected() != 0 || len(selected) != 2 || selected[1] != 0 {
		t.Fatalf("handle grab selected %d (%v), want 0", tl.Selected(), selected)
	}
	tl.DragEnd()

	// A click in a gap (no segment) selects nothing new.
	tl.MouseDown(&desktop.MouseEvent{
		Button:     desktop.MouseButtonPrimary,
		PointEvent: fyne.PointEvent{Position: fyne.NewPos(tl.xAt(6*time.Second), 0)},
	})
	if tl.Selected() != 0 || len(selected) != 2 {
		t.Fatalf("gap click changed selection: %d (%v)", tl.Selected(), selected)
	}
	tl.DragEnd()

	// Programmatic SetSelected fires nothing.
	tl.SetSelected(1)
	if len(selected) != 2 {
		t.Fatalf("SetSelected fired onSelected: %v", selected)
	}
}

func TestTimelineKeyboardTargetsSelectedSegment(t *testing.T) {
	tl := newSizedTimeline(t)
	tl.SetDuration(30 * time.Second)
	tl.SetSegments([]model.TimeRange{
		{Start: time.Second, End: 5 * time.Second},
		{Start: 8 * time.Second, End: 12 * time.Second},
		{Start: 15 * time.Second, End: 20 * time.Second},
	})
	tl.SetSelected(1)

	// Ctrl+] may extend Out toward (not past) the next segment's Start:
	// at 13s — between segment 1's End and segment 2's Start — it
	// extends segment 1 to 13s; past segment 2's Start it is a no-op.
	tl.SetPlayhead(13 * time.Second)
	tl.TypedShortcut(&desktop.CustomShortcut{KeyName: fyne.KeyRightBracket, Modifier: fyne.KeyModifierControl})
	if tl.Segments()[1].End != 13*time.Second {
		t.Fatalf("ctrl+] inside the gap did not extend: %s", tl.Segments()[1].End)
	}
	tl.SetPlayhead(16 * time.Second)
	tl.TypedShortcut(&desktop.CustomShortcut{KeyName: fyne.KeyRightBracket, Modifier: fyne.KeyModifierControl})
	if tl.Segments()[1].End != 13*time.Second {
		t.Fatalf("ctrl+] past the next segment's start changed it to %s", tl.Segments()[1].End)
	}
	// Ctrl+] at 10s applies to segment 1 only.
	tl.SetPlayhead(10 * time.Second)
	tl.TypedShortcut(&desktop.CustomShortcut{KeyName: fyne.KeyRightBracket, Modifier: fyne.KeyModifierControl})
	if tl.Segments()[1].End != 10*time.Second || tl.Segments()[2].End != 20*time.Second {
		t.Fatalf("ctrl+] disturbed other segments: %v", tl.Segments())
	}
	// Ctrl+[ below the previous segment's End is a no-op.
	tl.SetPlayhead(4 * time.Second)
	tl.TypedShortcut(&desktop.CustomShortcut{KeyName: fyne.KeyLeftBracket, Modifier: fyne.KeyModifierControl})
	if tl.Segments()[1].Start != 8*time.Second {
		t.Fatalf("ctrl+[ before the previous segment's end changed it to %s", tl.Segments()[1].Start)
	}
	// Ctrl+[ at 8s (the segment's own Start) is fine.
	tl.SetPlayhead(8 * time.Second)
	tl.TypedShortcut(&desktop.CustomShortcut{KeyName: fyne.KeyLeftBracket, Modifier: fyne.KeyModifierControl})
	if tl.Segments()[1].Start != 8*time.Second || tl.Segments()[0].Start != time.Second {
		t.Fatalf("ctrl+[ disturbed other segments: %v", tl.Segments())
	}
}

func TestTimelineFocusLossClearsDragSeg(t *testing.T) {
	tl := newSizedTimeline(t)
	tl.SetDuration(10 * time.Second)
	tl.SetSegmentsSilent([]model.TimeRange{
		{Start: time.Second, End: 3 * time.Second},
		{Start: 5 * time.Second, End: 7 * time.Second},
	})
	tl.drag, tl.dragSeg = tlIn, 1
	tl.FocusLost()
	if tl.drag != tlNone || tl.dragSeg != -1 {
		t.Fatalf("focus loss must clear drag state: drag=%v dragSeg=%d", tl.drag, tl.dragSeg)
	}
	tl.drag, tl.dragSeg = tlOut, 1
	tl.Disable()
	if tl.drag != tlNone || tl.dragSeg != -1 {
		t.Fatalf("disable must clear drag state: drag=%v dragSeg=%d", tl.drag, tl.dragSeg)
	}
}

// --- pending frame-preview indication ---

// previewTestController returns a controller with trimming force-enabled
// (bypassing setTrimEnabled, which would launch the thumbnail strip and
// the frame index — neither is under test here) and a fresh dispatch
// channel so tests can pump preview callbacks deterministically.
func previewTestController(t *testing.T) (*Controller, chan func()) {
	t.Helper()
	c := testTrimController(t)
	updates := make(chan func(), 64)
	c.uiDispatch = func(f func()) { updates <- f }
	c.trimEnabled = true
	return c, updates
}

// runDispatch pumps one queued preview callback, failing when none
// arrives.
func runDispatch(t *testing.T, updates chan func()) {
	t.Helper()
	select {
	case f := <-updates:
		f()
	case <-time.After(2 * time.Second):
		t.Fatal("no preview dispatch arrived")
	}
}

// spyFetch parks every extraction on its release channel; the release
// value is the error to return (nil = a small opaque frame). entered
// synchronizes the fetch's arrival at the park point. The parked select
// also honors ctx cancellation, so a canceled extraction returns
// without a release.
type spyFetch struct {
	mu      sync.Mutex
	ats     []time.Duration
	exts    []*frames.Extractor
	entered chan struct{}
	release chan error
}

func (s *spyFetch) fetch(ctx context.Context, ext *frames.Extractor, _ string, at time.Duration, _ int) (frames.Frame, error) {
	s.mu.Lock()
	s.ats = append(s.ats, at)
	s.exts = append(s.exts, ext)
	s.mu.Unlock()
	if s.entered != nil {
		s.entered <- struct{}{}
	}
	var err error
	select {
	case err = <-s.release:
	case <-ctx.Done():
		return frames.Frame{}, ctx.Err()
	}
	if err != nil {
		return frames.Frame{}, err
	}
	return frames.Frame{Image: image.NewRGBA(image.Rect(0, 0, 4, 4))}, nil
}

func (s *spyFetch) snapshot() ([]time.Duration, []*frames.Extractor) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]time.Duration{}, s.ats...), append([]*frames.Extractor{}, s.exts...)
}

func (s *spyFetch) lastExt() *frames.Extractor {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.exts) == 0 {
		return nil
	}
	return s.exts[len(s.exts)-1]
}

func TestPreviewPendingIndication(t *testing.T) {
	c, _ := previewTestController(t)

	// Cache miss: a hover request arms the cue on every surface.
	c.requestHover(3*time.Second + 120*time.Millisecond)
	if !c.previews.pendingPreview || c.previews.pendingAt == nil || *c.previews.pendingAt != 3*time.Second {
		t.Fatalf("hover miss must arm pending: pending=%v at=%v", c.previews.pendingPreview, c.previews.pendingAt)
	}
	if c.previewVeil.Hidden {
		t.Fatal("the stale frame must be tinted while pending")
	}
	if veil, ok := color.NRGBAModel.Convert(c.previewVeil.FillColor).(color.NRGBA); !ok || veil.A != previewVeilAlpha {
		t.Fatalf("veil alpha = %v, want %d", c.previewVeil.FillColor, previewVeilAlpha)
	}
	if !c.previewSpinner.Visible() {
		t.Fatal("busy bar must be visible while pending")
	}
	if !c.trimTimeline.hoverPending {
		t.Fatal("timeline marker must report pending")
	}

	// A committed request resolving from cache clears the cue and the
	// target instantly, and records the displayed position.
	c.previews.cache.Put(time.Second, image.NewRGBA(image.Rect(0, 0, 4, 4)))
	c.requestPreview(time.Second)
	if c.previews.pendingPreview || c.previews.pendingAt != nil {
		t.Fatal("a cache hit must resolve the pending state")
	}
	if !c.previewVeil.Hidden || c.previewSpinner.Visible() || c.trimTimeline.hoverPending {
		t.Fatal("a cache hit must clear the visual cue")
	}
	if c.previews.displayedAt == nil || *c.previews.displayedAt != time.Second {
		t.Fatalf("displayedAt = %v, want 1s", c.previews.displayedAt)
	}

	// Arming again after a hit: the visual toggle is not one-way.
	c.requestHover(5 * time.Second)
	if !c.previews.pendingPreview || !c.previewSpinner.Visible() {
		t.Fatal("a new miss must re-arm the cue")
	}
	c.previews.hoverTimer.Stop()
}

// Regression guard for the launch/dispatch path: pending survives a
// parked extraction, resolves on success and on error, and a retired
// result never disturbs a newer pending state.
func TestPreviewPendingAsyncLifecycle(t *testing.T) {
	c, updates := previewTestController(t)
	spy := &spyFetch{entered: make(chan struct{}), release: make(chan error)}
	c.previews.previewFetch = spy.fetch

	// Park an extraction.
	c.requestHover(time.Second)
	c.previews.hoverTimer.Stop()
	c.launchPreview(time.Second, c.previews.seq)
	<-spy.entered
	if !c.previews.pendingPreview {
		t.Fatal("pending must stay armed while the extraction runs")
	}

	// Error: the cue clears, the status explains, and the same bucket
	// may re-request instead of coalescing the failure.
	spy.release <- errors.New("boom")
	runDispatch(t, updates)
	if c.previews.pendingPreview || c.previews.pendingAt != nil {
		t.Fatal("an error must clear the pending state")
	}
	if !strings.Contains(c.previewStatus.Text, "boom") {
		t.Fatalf("status = %q, want the extraction error", c.previewStatus.Text)
	}
	c.requestHover(time.Second + 100*time.Millisecond)
	if !c.previews.pendingPreview || c.previews.hoverTimer == nil {
		t.Fatal("a same-bucket request after an error must re-request")
	}

	// Success: the frame lands, is cached, and resolves the cue.
	c.previews.hoverTimer.Stop()
	c.launchPreview(time.Second, c.previews.seq)
	<-spy.entered
	spy.release <- nil
	runDispatch(t, updates)
	if c.previews.pendingPreview || c.previews.pendingAt != nil {
		t.Fatal("success must clear the pending state")
	}
	if c.previewImage.Image == nil {
		t.Fatal("the extracted frame must be displayed")
	}
	if _, ok := c.previews.cache.Get(time.Second); !ok {
		t.Fatal("the landed frame must be cached")
	}

	// launchPreview's entry cache-hit branch resolves through the same
	// central path.
	c.requestHover(7 * time.Second)
	c.previews.hoverTimer.Stop()
	c.previews.cache.Put(7*time.Second, image.NewRGBA(image.Rect(0, 0, 4, 4)))
	c.launchPreview(7*time.Second, c.previews.seq)
	if c.previews.pendingPreview || c.previews.pendingAt != nil {
		t.Fatal("a launch-time cache hit must resolve the pending state")
	}

	// A late result for a retired request leaves a newer pending state
	// (and its display) alone.
	c.requestHover(5 * time.Second)
	c.previews.hoverTimer.Stop()
	c.launchPreview(5*time.Second, c.previews.seq)
	<-spy.entered
	c.previews.cache.Put(2*time.Second, image.NewRGBA(image.Rect(0, 0, 4, 4)))
	c.requestPreview(2 * time.Second) // committed hit resolves pending
	if c.previews.pendingPreview {
		t.Fatal("the committed hit must resolve pending")
	}
	spy.release <- nil
	runDispatch(t, updates) // the stale 5s result arrives
	if c.previews.pendingPreview || c.previews.pendingAt != nil {
		t.Fatal("a retired result must not re-arm pending")
	}
	if c.previews.displayedAt == nil || *c.previews.displayedAt != 2*time.Second {
		t.Fatalf("displayedAt = %v, want the newer 2s frame", c.previews.displayedAt)
	}
}

func TestTrimDisableDuringDebounce(t *testing.T) {
	c, updates := previewTestController(t)
	c.previews.cache.Put(time.Second, image.NewRGBA(image.Rect(0, 0, 4, 4)))

	c.requestHover(3 * time.Second)
	if !c.previews.pendingPreview || c.previews.hoverTimer == nil {
		t.Fatal("the hover miss must arm the pending state")
	}

	c.setTrimEnabled(false)
	if c.previews.pendingPreview || c.previews.pendingAt != nil {
		t.Fatal("disable must clear the pending state")
	}
	if c.previews.previewTimer != nil || c.previews.hoverTimer != nil {
		t.Fatal("disable must stop the preview timers")
	}
	if !c.previewVeil.Hidden || c.previewSpinner.Visible() || c.trimTimeline.hoverPending {
		t.Fatal("disable must clear the visual cue")
	}
	if _, ok := c.previews.cache.Get(time.Second); !ok {
		t.Fatal("disable must keep the frame cache")
	}
	select {
	case f := <-updates:
		f()
		t.Fatal("disable must not leave deferred preview work")
	case <-time.After(250 * time.Millisecond):
		// The stopped hover debounce (200ms) would have fired by now.
	}
}

func TestTrimDisableDuringExtraction(t *testing.T) {
	c, updates := previewTestController(t)
	spy := &spyFetch{entered: make(chan struct{}), release: make(chan error)}
	c.previews.previewFetch = spy.fetch

	c.requestHover(time.Second)
	c.previews.hoverTimer.Stop()
	c.launchPreview(time.Second, c.previews.seq)
	<-spy.entered

	c.setTrimEnabled(false)
	if c.previews.pendingPreview || c.previews.pendingAt != nil {
		t.Fatal("disable must clear the pending state")
	}
	if c.previews.previewCancel != nil {
		t.Fatal("disable must drop the extraction cancel func")
	}
	// The parked fetch returns through cancellation; its dispatch is
	// retired by the trimEnabled guard.
	runDispatch(t, updates)
	if c.previews.pendingPreview || c.previewImage.Image != nil {
		t.Fatal("a canceled extraction must not display anything")
	}
}

func TestFFmpegChangeDuringDebounce(t *testing.T) {
	c, updates := previewTestController(t)
	spy := &spyFetch{entered: make(chan struct{}), release: make(chan error)}
	c.previews.previewFetch = spy.fetch

	c.requestHover(time.Second)
	if c.previews.hoverTimer == nil {
		t.Fatal("the hover miss must arm the debounce")
	}

	c.config.FFmpeg.FFmpegPath = "/opt/new/ffmpeg"
	c.onFFmpegExecutableChanged()

	if c.previews.hoverTimer != nil {
		t.Fatal("the old hover debounce must be dropped")
	}
	if c.previews.previewTimer == nil {
		t.Fatal("the invalidation must schedule a fresh preview request")
	}
	if !c.previews.pendingPreview || c.previews.pendingAt == nil || *c.previews.pendingAt != time.Second {
		t.Fatal("pending must carry over at the requested position")
	}
	if _, ok := c.previews.cache.Get(time.Second); ok {
		t.Fatal("the old extractor's cache must be cleared")
	}
	if c.previews.extractorPath != "/opt/new/ffmpeg" {
		t.Fatalf("extractor path = %q, want the new executable", c.previews.extractorPath)
	}

	// The re-request extracts under the rebuilt extractor.
	c.previews.previewTimer.Stop()
	c.launchPreview(time.Second, c.previews.seq)
	<-spy.entered
	if ext := spy.lastExt(); ext != c.ensureExtractor() {
		t.Fatal("the extraction must run under the rebuilt extractor")
	}
	spy.release <- nil
	runDispatch(t, updates)
	if c.previews.pendingPreview || c.previews.pendingAt != nil {
		t.Fatal("the frame must land under the new extractor")
	}
}

func TestFFmpegChangeDuringBlockedExtraction(t *testing.T) {
	c, updates := previewTestController(t)
	spy := &spyFetch{entered: make(chan struct{}), release: make(chan error)}
	c.previews.previewFetch = spy.fetch

	c.requestHover(time.Second)
	c.previews.hoverTimer.Stop()
	c.launchPreview(time.Second, c.previews.seq)
	<-spy.entered
	oldExt := c.previews.extractor

	c.config.FFmpeg.FFmpegPath = "/opt/new/ffmpeg"
	c.onFFmpegExecutableChanged()
	if c.previews.extractor == oldExt {
		t.Fatal("the extractor must be rebuilt")
	}
	if !c.previews.pendingPreview || c.previews.pendingAt == nil || *c.previews.pendingAt != time.Second {
		t.Fatal("the fresh request must keep pending armed at the position")
	}

	// The old extraction's result arrives after the change: retired
	// without touching the fresh pending state.
	runDispatch(t, updates)
	if !c.previews.pendingPreview || c.previews.pendingAt == nil {
		t.Fatal("a retired old-extractor result must not disturb pending")
	}

	// The fresh request extracts under the new extractor and lands.
	c.previews.previewTimer.Stop()
	c.launchPreview(time.Second, c.previews.seq)
	<-spy.entered
	if ext := spy.lastExt(); ext != c.ensureExtractor() {
		t.Fatal("the second extraction must use the rebuilt extractor")
	}
	spy.release <- nil
	runDispatch(t, updates)
	if c.previews.pendingPreview || c.previews.pendingAt != nil {
		t.Fatal("pending must resolve under the new extractor")
	}
}

func TestFFmpegChangeWhileHoveringOtherFrame(t *testing.T) {
	c, updates := previewTestController(t)
	spy := &spyFetch{entered: make(chan struct{}), release: make(chan error)}
	c.previews.previewFetch = spy.fetch

	// A committed frame at 1s is displayed.
	c.previews.cache.Put(time.Second, image.NewRGBA(image.Rect(0, 0, 4, 4)))
	c.requestPreview(time.Second)
	if c.previews.displayedAt == nil || *c.previews.displayedAt != time.Second {
		t.Fatalf("displayedAt = %v, want 1s", c.previews.displayedAt)
	}

	// A hover at 3.4s parks an extraction for its bucket (3.25s).
	c.requestHover(3400 * time.Millisecond)
	c.previews.hoverTimer.Stop()
	c.launchPreview(3250*time.Millisecond, c.previews.seq)
	<-spy.entered

	// The executable changes mid-hover: the refresh must target the
	// hovered frame, not the displayed one.
	c.config.FFmpeg.FFmpegPath = "/opt/new/ffmpeg"
	c.onFFmpegExecutableChanged()
	if c.previews.pendingAt == nil || *c.previews.pendingAt != 3250*time.Millisecond {
		t.Fatalf("pendingAt = %v, want the hovered 3.25s", c.previews.pendingAt)
	}
	// The parked old extraction unblocks through cancellation; consume
	// its retired dispatch before launching the fresh request, so the
	// queue order stays deterministic.
	runDispatch(t, updates)
	if !c.previews.pendingPreview || c.previews.pendingAt == nil {
		t.Fatal("a retired old-extractor result must not disturb pending")
	}
	c.previews.previewTimer.Stop()
	c.launchPreview(3250*time.Millisecond, c.previews.seq)
	<-spy.entered
	ats, _ := spy.snapshot()
	if ats[len(ats)-1] != 3250*time.Millisecond {
		t.Fatalf("re-request target = %s, want the hovered 3.25s", ats[len(ats)-1])
	}
	spy.release <- nil
	runDispatch(t, updates)
	if c.previews.displayedAt == nil || *c.previews.displayedAt != 3250*time.Millisecond {
		t.Fatal("the hovered frame must land under the new extractor")
	}

	// A pending target beyond the media duration folds to the end.
	huge := c.media.Duration + time.Hour
	c.previews.pendingAt = &huge
	c.config.FFmpeg.FFmpegPath = "/opt/third/ffmpeg"
	c.onFFmpegExecutableChanged()
	if c.previews.pendingAt == nil || *c.previews.pendingAt != c.media.Duration {
		t.Fatalf("pendingAt = %v, want clamped to the duration", c.previews.pendingAt)
	}
	c.previews.previewTimer.Stop()
}

func TestHoverCoalescingWithinBucket(t *testing.T) {
	c, updates := previewTestController(t)
	spy := &spyFetch{entered: make(chan struct{}), release: make(chan error)}
	c.previews.previewFetch = spy.fetch

	c.requestHover(3*time.Second + 10*time.Millisecond)
	firstSeq := c.previews.seq
	timer := c.previews.hoverTimer

	// Repeated hover events inside one bucket keep the same request.
	for _, off := range []time.Duration{50, 120, 200, 240} {
		c.requestHover(3*time.Second + off*time.Millisecond)
	}
	if c.previews.seq != firstSeq {
		t.Fatalf("seq = %d after same-bucket hovers, want %d", c.previews.seq, firstSeq)
	}
	if c.previews.hoverTimer != timer {
		t.Fatal("same-bucket hovers must not restart the debounce timer")
	}

	// While the bucket's extraction is parked, same-bucket hovers still
	// coalesce onto it.
	c.previews.hoverTimer.Stop()
	c.launchPreview(3*time.Second, firstSeq)
	<-spy.entered
	before := c.previews.seq
	c.requestHover(3*time.Second + 200*time.Millisecond)
	if c.previews.seq != before {
		t.Fatal("a same-bucket hover during extraction must coalesce")
	}

	// Release: exactly one extraction serves the whole bucket.
	spy.release <- nil
	runDispatch(t, updates)
	if c.previews.pendingPreview || c.previews.pendingAt != nil {
		t.Fatal("landing must resolve pending")
	}

	// A different bucket restarts on newest-request-wins.
	c.requestHover(4 * time.Second)
	if c.previews.seq != before+1 {
		t.Fatal("a different bucket must bump the request sequence")
	}
	if c.previews.hoverTimer == nil {
		t.Fatal("a different bucket must re-arm the debounce")
	}
	c.previews.hoverTimer.Stop()
}

func TestHoverAfterCacheInvalidation(t *testing.T) {
	c, updates := previewTestController(t)
	spy := &spyFetch{entered: make(chan struct{}), release: make(chan error)}
	c.previews.previewFetch = spy.fetch

	// Extract bucket 2s once.
	c.requestHover(2 * time.Second)
	c.previews.hoverTimer.Stop()
	c.launchPreview(2*time.Second, c.previews.seq)
	<-spy.entered
	spy.release <- nil
	runDispatch(t, updates)
	if c.previews.pendingAt != nil {
		t.Fatal("landing must clear the pending target")
	}

	// The same bucket now hits the cache instantly.
	c.requestHover(2*time.Second + 100*time.Millisecond)
	if c.previews.pendingPreview || c.previews.pendingAt != nil {
		t.Fatal("a cache hit must not arm pending")
	}

	// Invalidating the extractor clears the cache and re-requests the
	// shown position; the hover coalesces onto that fresh request.
	c.config.FFmpeg.FFmpegPath = "/opt/other/ffmpeg"
	c.onFFmpegExecutableChanged()
	if !c.previews.pendingPreview || c.previews.pendingAt == nil || *c.previews.pendingAt != 2*time.Second {
		t.Fatal("invalidation must re-request the shown position")
	}
	seqAfter := c.previews.seq
	c.requestHover(2*time.Second + 100*time.Millisecond)
	if c.previews.seq != seqAfter {
		t.Fatal("a same-bucket hover must coalesce with the pending re-request")
	}

	// The extraction runs under the rebuilt extractor — the cache was
	// cleared, so the entry cannot serve the frame from the old build.
	c.previews.previewTimer.Stop()
	c.launchPreview(2*time.Second, c.previews.seq)
	<-spy.entered
	if ext := spy.lastExt(); ext != c.ensureExtractor() {
		t.Fatal("the frame must be extracted under the rebuilt extractor")
	}
	spy.release <- nil
	runDispatch(t, updates)
	if c.previews.pendingPreview || c.previews.pendingAt != nil {
		t.Fatal("landing must resolve pending")
	}
}

func TestTimelinePreviewPendingMarker(t *testing.T) {
	tl := newSizedTimeline(t)
	tl.SetDuration(10 * time.Second)

	tl.SetPreviewPending(true)
	if !tl.hoverPending {
		t.Fatal("SetPreviewPending(true) must arm the marker state")
	}
	tl.SetPreviewPending(false)
	if tl.hoverPending {
		t.Fatal("SetPreviewPending(false) must disarm the marker state")
	}

	// The marker only exists while the pointer hovers: MouseOut hides
	// it regardless of pending state.
	tl.SetPreviewPending(true)
	tl.MouseIn(&desktop.MouseEvent{PointEvent: fyne.PointEvent{Position: fyne.NewPos(100, 0)}})
	tl.MouseOut()
	if tl.hoverX >= 0 {
		t.Fatal("MouseOut must clear the hover position")
	}
}

func TestTrimUnavailableReason(t *testing.T) {
	t.Parallel()
	video := model.MediaInfo{Duration: time.Minute, VideoStreams: []model.VideoStream{{}}}
	tests := []struct {
		name       string
		path       string
		inspecting bool
		media      model.MediaInfo
		want       string
	}{
		{"no input", "", false, model.MediaInfo{}, "Open a video on the Input tab to cut it"},
		{"inspecting", "/a.mkv", true, model.MediaInfo{}, "Inspecting input…"},
		{"available", "/a.mkv", false, video, ""},
		{"audio only", "/a.mp3", false, model.MediaInfo{AudioStreams: []model.AudioStream{{}}}, "Input has no video stream"},
		{"failed or unknown", "/a.mkv", false, model.MediaInfo{}, "Input duration is unknown"},
	}
	for _, tt := range tests {
		if got := trimUnavailableReason(tt.path, tt.inspecting, tt.media); got != tt.want {
			t.Errorf("%s: reason = %q, want %q", tt.name, got, tt.want)
		}
	}
}
