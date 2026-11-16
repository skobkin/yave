package ui

import (
	"errors"
	"image"
	"strings"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/driver/desktop"
	fynetest "fyne.io/fyne/v2/test"

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
		{"12", 0, true},
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

func TestUpdateTrimMessagesTracksEncoder(t *testing.T) {
	c := testTrimController(t)
	c.trimEnabled = true
	c.trimSelection = model.TrimSelection{Segments: []model.TimeRange{{Start: time.Second, End: 5 * time.Second}}}

	c.updateTrimMessages("libx265")
	if strings.Contains(c.trimMessage.Text, "keyframes") {
		t.Fatalf("real encoder must not show the copy warning: %q", c.trimMessage.Text)
	}
	c.updateTrimMessages("copy")
	if !strings.Contains(c.trimMessage.Text, "keyframes") {
		t.Fatalf("copy encoder must show the keyframe warning: %q", c.trimMessage.Text)
	}
	c.updateTrimMessages("hevc_vaapi")
	if strings.Contains(c.trimMessage.Text, "keyframes") {
		t.Fatalf("warning must disappear after switching away from copy: %q", c.trimMessage.Text)
	}
	c.trimEnabled = false
	c.updateTrimMessages("copy")
	if strings.Contains(c.trimMessage.Text, "keyframes") {
		t.Fatalf("disabled trim must hide the warning: %q", c.trimMessage.Text)
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
	at, state = index.applyWindow(0, 2*time.Second, times, duration)
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
	index.applyWindow(0, 2*time.Second, []time.Duration{0, time.Second, 2 * time.Second}, duration)

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
	at, state := index.applyWindow(2*time.Second, 4*time.Second, []time.Duration{3 * time.Second, 4 * time.Second}, duration)
	if state != frameStepLoading || at != 4*time.Second || index.pending != 3 {
		t.Fatalf("after adjacent window: at = %s state = %v pending = %d, want 4s/loading/3", at, state, index.pending)
	}

	// The next window completes the walk as one atomic move.
	at, state = index.applyWindow(4*time.Second, 8*time.Second,
		[]time.Duration{5 * time.Second, 6 * time.Second, 7 * time.Second, 8 * time.Second}, duration)
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
	at, state = index.applyWindow(8*time.Second, duration, []time.Duration{9 * time.Second}, duration)
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
	index.applyWindow(20*time.Second, duration, nil, duration)
	_, state := index.step(25*time.Second, 1, duration)
	if state != frameStepEdge {
		t.Fatalf("state = %v, want edge (no frames after 25s)", state)
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
	tl.in, tl.out = time.Second, 9*time.Second
	if got := tl.pickTarget(tl.xAt(time.Second)); got != tlIn {
		t.Errorf("on the in handle: %v, want tlIn", got)
	}
	if got := tl.pickTarget(tl.xAt(9 * time.Second)); got != tlOut {
		t.Errorf("on the out handle: %v, want tlOut", got)
	}
	if got := tl.pickTarget(tl.xAt(5 * time.Second)); got != tlPlayhead {
		t.Errorf("middle: %v, want tlPlayhead", got)
	}
	// Overlapping handles (in ≈ out): nearest wins; in wins ties.
	tl.in, tl.out = 5*time.Second, 5*time.Second
	if got := tl.pickTarget(tl.xAt(5*time.Second) + 2); got != tlIn {
		t.Errorf("tie at overlapping handles: %v, want tlIn", got)
	}
	tl.out = 5*time.Second + 5*time.Millisecond
	if got := tl.pickTarget(tl.xAt(tl.out)); got != tlOut {
		t.Errorf("closer to out: %v, want tlOut", got)
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
	tl.SetRange(0, tl.Duration())
	tl.KeyDown(&fyne.KeyEvent{Name: desktop.KeyControlLeft})
	tl.TypedKey(&fyne.KeyEvent{Name: fyne.KeyLeftBracket})
	if tl.in != 1100*time.Millisecond {
		t.Fatalf("ctrl+[ set in = %s, want 1.1s", tl.in)
	}
	tl.TypedKey(&fyne.KeyEvent{Name: fyne.KeyRightBracket})
	if tl.out != 1100*time.Millisecond {
		t.Fatalf("ctrl+] set out = %s, want 1.1s", tl.out)
	}
	tl.KeyUp(&fyne.KeyEvent{Name: desktop.KeyControlLeft})
	// Without ctrl held, brackets do nothing.
	tl.SetPlayhead(2 * time.Second)
	tl.TypedKey(&fyne.KeyEvent{Name: fyne.KeyLeftBracket})
	if tl.in != 1100*time.Millisecond {
		t.Fatalf("bracket without ctrl moved in to %s", tl.in)
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
	tl.SetRange(0, tl.Duration())
	var ranges []struct{ in, out time.Duration }
	tl.onRange = func(in, out time.Duration) {
		ranges = append(ranges, struct{ in, out time.Duration }{in, out})
	}

	tl.TypedShortcut(&desktop.CustomShortcut{KeyName: fyne.KeyLeftBracket, Modifier: fyne.KeyModifierControl})
	if tl.in != 2*time.Second {
		t.Fatalf("ctrl+[ via shortcut set in = %s, want 2s", tl.in)
	}
	tl.TypedShortcut(&desktop.CustomShortcut{KeyName: fyne.KeyRightBracket, Modifier: fyne.KeyModifierControl})
	if tl.out != 2*time.Second {
		t.Fatalf("ctrl+] via shortcut set out = %s, want 2s", tl.out)
	}
	if len(ranges) != 2 {
		t.Fatalf("onRange fired %d times, want 2", len(ranges))
	}

	// Unrelated shortcuts must not move the boundaries.
	tl.TypedShortcut(&desktop.CustomShortcut{KeyName: fyne.KeyC, Modifier: fyne.KeyModifierControl})
	if tl.in != 2*time.Second || tl.out != 2*time.Second || len(ranges) != 2 {
		t.Fatalf("unrelated shortcut changed boundaries: in=%s out=%s ranges=%d", tl.in, tl.out, len(ranges))
	}

	// Disabled timelines ignore shortcuts, like they ignore TypedKey.
	tl.Disable()
	tl.SetPlayhead(3 * time.Second)
	tl.TypedShortcut(&desktop.CustomShortcut{KeyName: fyne.KeyLeftBracket, Modifier: fyne.KeyModifierControl})
	if tl.in != 2*time.Second {
		t.Fatalf("disabled timeline accepted shortcut: in=%s", tl.in)
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
	c.trimSelection = withBoundary(c.trimSelection, 3*time.Second, 7*time.Second)
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

func TestResetTrimForNewInputClearsState(t *testing.T) {
	c := testTrimController(t)
	c.trimEnable.SetChecked(true)
	c.previews.index.applyWindow(0, time.Second, []time.Duration{0, time.Second}, c.media.Duration)
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
}
