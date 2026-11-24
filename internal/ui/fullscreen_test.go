package ui

import (
	"context"
	"errors"
	"image"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/driver/desktop"
	fynetest "fyne.io/fyne/v2/test"

	"git.skobk.in/skobkin/yave/internal/frames"
	"git.skobk.in/skobkin/yave/internal/model"
	"git.skobk.in/skobkin/yave/internal/playback"
	"git.skobk.in/skobkin/yave/internal/resources"
)

func TestPreviewDecodeWidth(t *testing.T) {
	tests := []struct {
		name  string
		size  fyne.Size
		scale float32
		want  int
	}{
		{"HD screen", fyne.NewSize(1280, 720), 1, 1280},
		{"full HD", fyne.NewSize(1920, 1080), 1, 1920},
		{"4K at 1x is bounded", fyne.NewSize(3840, 2160), 1, 1920},
		{"hi-DPI counts physical pixels, bounded", fyne.NewSize(1050, 760), 2, 1920},
		{"hi-DPI inside the bound", fyne.NewSize(800, 600), 1.5, 1200},
		{"a small window keeps the normal preview width", fyne.NewSize(500, 300), 1, previewImageWidth},
		{"the normal width is the floor", fyne.NewSize(640, 480), 1, previewImageWidth},
		{"odd widths round down to even", fyne.NewSize(1281, 720), 1, 1280},
		{"fractional scale truncates then evens", fyne.NewSize(1000, 700), 1.2345, 1234},
	}
	for _, tt := range tests {
		got := previewDecodeWidth(tt.size, tt.scale)
		if got != tt.want {
			t.Errorf("%s: previewDecodeWidth = %d, want %d", tt.name, got, tt.want)
		}
		if got%2 != 0 || got < previewImageWidth || got > previewMaxDecodeWidth {
			t.Errorf("%s: %d is odd or outside [%d, %d]", tt.name, got, previewImageWidth, previewMaxDecodeWidth)
		}
	}
	if got := playbackDecodeWidth(fyne.NewSize(3840, 2160), 1); got != fullscreenPlaybackMaxWidth {
		t.Errorf("playback width at 4K = %d, want the %d cap", got, fullscreenPlaybackMaxWidth)
	}
	if got := playbackDecodeWidth(fyne.NewSize(1000, 700), 1); got != 1000 {
		t.Errorf("playback width at 1000px = %d, want 1000 (below the cap)", got)
	}
}

// fsHarness is a playback harness whose controller has a real (test)
// window showing the full layout, with fake preview extraction recording
// the widths it is asked for.
type fsHarness struct {
	*playbackHarness
	mu     sync.Mutex
	widths []int
}

func newFullscreenHarness(t *testing.T) *fsHarness {
	t.Helper()
	h := &fsHarness{playbackHarness: newPlaybackHarness(t)}
	c := h.c
	c.window = fynetest.NewWindow(c.withTooltips(c.buildLayout()))
	c.window.Resize(fyne.NewSize(1050, 760))
	// Like a user clicking the preview, start on the visible Trim tab with
	// the timeline focused.
	c.tabs.SelectIndex(1)
	c.window.Canvas().Focus(c.trimTimeline)
	if c.window.Canvas().Focused() != fyne.Focusable(c.trimTimeline) {
		t.Fatal("test setup: the timeline could not take focus")
	}
	c.previews.previewFetch = func(_ context.Context, _ *frames.Extractor, _ string, _ time.Duration, width int) (frames.Frame, error) {
		h.mu.Lock()
		h.widths = append(h.widths, width)
		h.mu.Unlock()
		return frames.Frame{Image: image.NewRGBA(image.Rect(0, 0, 4, 4))}, nil
	}
	c.previews.indexFetch = func(context.Context, string, string, time.Duration, time.Duration) ([]time.Duration, time.Duration, error) {
		return nil, 0, errors.New("no index in this test")
	}
	c.updatePlaybackControls()
	return h
}

func (h *fsHarness) fetchedWidths() []int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]int(nil), h.widths...)
}

func (h *fsHarness) waitForWidth(t *testing.T, want int) {
	t.Helper()
	h.pump(t, func() bool {
		for _, w := range h.fetchedWidths() {
			if w == want {
				return true
			}
		}
		return false
	})
}

func typeKey(c *Controller, name fyne.KeyName) {
	if handler := c.window.Canvas().OnTypedKey(); handler != nil {
		handler(&fyne.KeyEvent{Name: name})
	}
}

// fireShortcut runs the Ctrl+<key> action the full-screen view registered
// on the canvas; it reports whether one was registered (none outside
// full-screen).
func fireShortcut(c *Controller, key fyne.KeyName) bool {
	action, ok := c.fs.actions[key]
	if ok {
		action()
	}
	return ok
}

func TestEnterAndExitRestoreTheWindowExactly(t *testing.T) {
	h := newFullscreenHarness(t)
	c := h.c
	canvas := c.window.Canvas()
	content := c.window.Content()
	typed := canvas.OnTypedKey() // whatever handler was installed before

	c.enterFullscreen()
	if !c.fs.active || !c.window.FullScreen() {
		t.Fatalf("after enter: active=%v window full-screen=%v", c.fs.active, c.window.FullScreen())
	}
	if c.window.Content() == content {
		t.Error("the window content must be swapped for the full-screen view")
	}
	if canvas.Focused() != nil {
		t.Errorf("focus = %v, want none in full-screen (the timeline lost it)", canvas.Focused())
	}

	c.exitFullscreen()
	if c.fs.active || c.window.FullScreen() {
		t.Fatalf("after exit: active=%v window full-screen=%v", c.fs.active, c.window.FullScreen())
	}
	if c.window.Content() != content {
		t.Error("the original window content must be restored")
	}
	if canvas.Focused() != fyne.Focusable(c.trimTimeline) {
		t.Errorf("focus = %v, want the timeline that had it before", canvas.Focused())
	}
	if (canvas.OnTypedKey() == nil) != (typed == nil) {
		t.Error("the canvas key handler must be restored")
	}
	if len(c.fs.shortcuts) != 0 || c.fs.content != nil || c.fs.focus != nil {
		t.Error("exit must drop its saved state")
	}
	// Idempotent.
	c.exitFullscreen()
	c.enterFullscreen()
	c.enterFullscreen()
	if !c.fs.active {
		t.Error("entering twice must stay in full-screen")
	}
	c.exitFullscreen()
}

func TestFocusFallsBackToTheTimelineWhenNothingHadIt(t *testing.T) {
	h := newFullscreenHarness(t)
	c := h.c
	c.window.Canvas().Unfocus()
	c.enterFullscreen()
	c.exitFullscreen()
	if c.window.Canvas().Focused() != fyne.Focusable(c.trimTimeline) {
		t.Error("with no previous focus, the timeline gets it so Space and the arrows keep working")
	}
}

func TestFullscreenIsBlockedWithAReason(t *testing.T) {
	h := newFullscreenHarness(t)
	c := h.c
	cases := []struct {
		name   string
		mutate func()
		undo   func()
		want   string
	}{
		{"trimming off", func() { c.trimEnabled = false }, func() { c.trimEnabled = true }, "Enable trimming"},
		{"no video", func() { c.media.VideoStreams = nil }, func() { c.media.VideoStreams = []model.VideoStream{{Index: 0}} }, "no video stream"},
		{"no input", func() { c.inputPath = "" }, func() { c.inputPath = "/tmp/input.mkv" }, "Open a video"},
	}
	for _, tc := range cases {
		tc.mutate()
		c.updatePlaybackControls()
		c.enterFullscreen()
		if c.fs.active || c.window.FullScreen() {
			t.Errorf("%s: entered full-screen although blocked", tc.name)
		}
		if got := c.transport.fullscreen.tip(); got == "" || !strings.Contains(got, tc.want) {
			t.Errorf("%s: tooltip %q, want the reason %q", tc.name, got, tc.want)
		}
		if !c.transport.fullscreen.Disabled() {
			t.Errorf("%s: the button must be disabled", tc.name)
		}
		tc.undo()
	}
	c.updatePlaybackControls()
	if c.transport.fullscreen.Disabled() || c.transport.fullscreen.tip() != "Full-screen preview (or double-click the preview)" {
		t.Errorf("ready: disabled=%v tip=%q", c.transport.fullscreen.Disabled(), c.transport.fullscreen.tip())
	}

	noWindow := &Controller{}
	noWindow.enterFullscreen() // must not panic
	noWindow.exitFullscreen()
}

func TestDoubleClickOnThePreviewTogglesFullscreen(t *testing.T) {
	h := newFullscreenHarness(t)
	c := h.c

	var findSurface func(o fyne.CanvasObject) *previewSurface
	findSurface = func(o fyne.CanvasObject) *previewSurface {
		switch v := o.(type) {
		case *previewSurface:
			return v
		case *fyne.Container:
			for _, child := range v.Objects {
				if s := findSurface(child); s != nil {
					return s
				}
			}
		}
		return nil
	}
	tab := findSurface(c.tabs.Items[1].Content)
	if tab == nil {
		t.Fatal("the Trim tab's preview has no double-tap layer")
	}
	tab.DoubleTapped(&fyne.PointEvent{})
	if !c.fs.active {
		t.Fatal("double-clicking the preview must enter full-screen")
	}
	fullscreen := findSurface(c.window.Content())
	if fullscreen == nil {
		t.Fatal("the full-screen view has no double-tap layer")
	}
	fullscreen.DoubleTapped(&fyne.PointEvent{})
	if c.fs.active {
		t.Fatal("double-clicking again must leave full-screen")
	}

	// A blocked state ignores the double-click.
	c.trimEnabled = false
	tab.DoubleTapped(&fyne.PointEvent{})
	if c.fs.active {
		t.Error("double-click must respect the same availability rules as the button")
	}
}

func TestFullscreenKeysUseTheTimelineActions(t *testing.T) {
	h := newFullscreenHarness(t)
	c := h.c
	var steps []int
	c.trimTimeline.stepFrame = func(at time.Duration, dir int) (time.Duration, bool) {
		steps = append(steps, dir)
		return at + time.Duration(dir)*40*time.Millisecond, true
	}
	c.enterFullscreen()
	start := c.trimTimeline.Playhead() // 30s

	typeKey(c, fyne.KeyRight)
	typeKey(c, fyne.KeyRight)
	typeKey(c, fyne.KeyLeft)
	if len(steps) != 3 || steps[0] != 1 || steps[2] != -1 {
		t.Fatalf("frame steps = %v", steps)
	}
	if got := c.trimTimeline.Playhead(); got != start+40*time.Millisecond {
		t.Errorf("playhead = %s, want one net frame step", got)
	}

	// Shift steps a second without asking the frame index.
	c.trimTimeline.modifiers = func() fyne.KeyModifier { return fyne.KeyModifierShift }
	before := c.trimTimeline.Playhead()
	typeKey(c, fyne.KeyRight)
	if got := c.trimTimeline.Playhead(); got != before+time.Second || len(steps) != 3 {
		t.Errorf("Shift+Right: playhead %s (steps %v), want +1s with no frame step", got, steps)
	}
	typeKey(c, fyne.KeyLeft)
	if got := c.trimTimeline.Playhead(); got != before {
		t.Errorf("Shift+Left: playhead %s, want back to %s", got, before)
	}
	c.trimTimeline.modifiers = nil

	// Space plays and pauses.
	typeKey(c, fyne.KeySpace)
	if !c.playbackActive() {
		t.Fatal("Space must start playback")
	}
	typeKey(c, fyne.KeySpace)
	if c.playbackActive() {
		t.Error("Space must pause playback")
	}
	typeKey(c, fyne.KeyF) // F is deliberately not a shortcut
	if !c.fs.active {
		t.Error("an unbound key must do nothing")
	}

	typeKey(c, fyne.KeyEscape)
	if c.fs.active {
		t.Error("Esc must leave full-screen")
	}
}

func TestFullscreenBracketShortcutsSetInAndOut(t *testing.T) {
	h := newFullscreenHarness(t)
	c := h.c
	c.enterFullscreen()

	c.trimTimeline.SetPlayhead(65 * time.Second) // inside segment 2 [60s, 90s)
	if !fireShortcut(c, fyne.KeyLeftBracket) || !fireShortcut(c, fyne.KeyRightBracket) {
		t.Fatal("full-screen must register Ctrl+[ and Ctrl+]")
	}
	c.trimTimeline.SetPlayhead(65 * time.Second)
	fireShortcut(c, fyne.KeyLeftBracket)
	if got := c.trimSelection.Segments[1].Start; got != 65*time.Second {
		t.Errorf("Ctrl+[ set In to %s, want the playhead's 65s", got)
	}
	c.trimTimeline.SetPlayhead(80 * time.Second)
	fireShortcut(c, fyne.KeyRightBracket)
	if got := c.trimSelection.Segments[1].End; got != 80*time.Second {
		t.Errorf("Ctrl+] set Out to %s, want 80s", got)
	}

	// Once full-screen is left, the shortcuts are gone again.
	c.exitFullscreen()
	if fireShortcut(c, fyne.KeyLeftBracket) || fireShortcut(c, fyne.KeyRightBracket) || len(c.fs.shortcuts) != 0 {
		t.Error("after exit the full-screen shortcuts must be unregistered")
	}
}

func TestTogglingFullscreenNeverInterruptsPlayback(t *testing.T) {
	h := newFullscreenHarness(t)
	c := h.c
	c.togglePlayback()
	session := c.play.session
	first := h.session(t, 0)
	gen := c.play.gen

	c.enterFullscreen()
	if c.play.session != session || c.play.gen != gen || first.wasCancelled() || !c.playbackActive() {
		t.Fatalf("entering full-screen interrupted playback (cancelled=%v active=%v)", first.wasCancelled(), c.playbackActive())
	}
	if h.sessionCount() != 1 {
		t.Errorf("entering full-screen restarted playback (%d sessions)", h.sessionCount())
	}
	if !showsPause(c.fs.bar.play) {
		t.Error("the overlay's play button must show the running state")
	}
	// Frames keep reaching the display.
	frame := image.NewYCbCr(image.Rect(0, 0, 2, 2), image.YCbCrSubsampleRatio420)
	first.frames <- playback.Frame{At: 32 * time.Second, Image: frame}
	h.pump(t, func() bool { return c.trimTimeline.Playhead() == 32*time.Second })

	c.exitFullscreen()
	if c.play.session != session || first.wasCancelled() || !c.playbackActive() || h.sessionCount() != 1 {
		t.Fatal("leaving full-screen interrupted playback")
	}
	if !showsPause(c.transport.play) {
		t.Error("the Trim tab's play button must show the running state")
	}
}

func TestStaticPreviewsAreDecodedWiderOnlyInFullscreen(t *testing.T) {
	h := newFullscreenHarness(t)
	c := h.c
	at := 30 * time.Second

	c.requestPreview(at)
	h.waitForWidth(t, previewImageWidth)

	// The small frame is cached under its width; entering full-screen must
	// not be satisfied by it — it asks for a sharper one.
	small := c.previews.displayedAt
	if small == nil || *small != at {
		t.Fatalf("the normal preview did not land at %s", at)
	}
	c.enterFullscreen()
	want := previewDecodeWidth(c.window.Canvas().Size(), c.window.Canvas().Scale())
	if want <= previewImageWidth {
		t.Fatalf("test window yields width %d; it must exceed the normal %d", want, previewImageWidth)
	}
	h.waitForWidth(t, want)

	// Back in the normal view the cached small frame serves the request
	// without another extraction.
	before := len(h.fetchedWidths())
	c.exitFullscreen()
	h.drain()
	if got := h.fetchedWidths(); len(got) != before {
		t.Errorf("leaving full-screen extracted again (%v); the %dpx frame is cached", got, previewImageWidth)
	}
	if c.previewWidth() != previewImageWidth {
		t.Errorf("previewWidth outside full-screen = %d", c.previewWidth())
	}
}

func TestPlaybackDecodeWidthFollowsFullscreenOnlyForNewSessions(t *testing.T) {
	h := newFullscreenHarness(t)
	c := h.c

	c.togglePlayback() // a normal session
	normal := h.session(t, 0)
	if normal.opts.MaxWidth != 0 {
		t.Errorf("a normal session has MaxWidth %d, want the default (0)", normal.opts.MaxWidth)
	}
	c.enterFullscreen()
	if normal.wasCancelled() {
		t.Fatal("entering full-screen must not restart the running session")
	}
	c.togglePlayback() // pause
	c.togglePlayback() // play: a new session, now in full-screen
	wide := h.session(t, 1)
	want := playbackDecodeWidth(c.window.Canvas().Size(), c.window.Canvas().Scale())
	if wide.opts.MaxWidth != want || want <= previewImageWidth {
		t.Errorf("a full-screen session has MaxWidth %d, want %d (above the normal %d)", wide.opts.MaxWidth, want, previewImageWidth)
	}
	c.togglePlayback()
	c.exitFullscreen()
	c.togglePlayback()
	if got := h.session(t, 2).opts.MaxWidth; got != 0 {
		t.Errorf("after leaving full-screen new sessions have MaxWidth %d, want the default (0)", got)
	}
}

func TestOverlayHidesWhenIdleAndReturnsOnActivity(t *testing.T) {
	h := newFullscreenHarness(t)
	c := h.c
	c.fs.delay = 20 * time.Millisecond
	c.enterFullscreen()
	if c.fs.overlay.Hidden {
		t.Fatal("the control strip starts visible")
	}
	h.pump(t, func() bool { return c.fs.overlay.Hidden })

	typeKey(c, fyne.KeyLeft) // a key press brings it back
	if c.fs.overlay.Hidden {
		t.Error("a key press must show the strip")
	}
	h.pump(t, func() bool { return c.fs.overlay.Hidden })

	c.fs.bar.mute.MouseMoved(&desktop.MouseEvent{}) // the pointer over the strip itself
	if c.fs.overlay.Hidden {
		t.Error("pointer movement over a strip button must show (and keep) the strip")
	}
	h.pump(t, func() bool { return c.fs.overlay.Hidden })

	surface := newPreviewSurface(nil, c.fullscreenActivity)
	surface.MouseMoved(&desktop.MouseEvent{})
	if c.fs.overlay.Hidden {
		t.Error("pointer movement over the preview must show the strip")
	}
	c.fs.bar.volume.OnChanged(60) // adjusting the volume counts too
	if c.fs.overlay.Hidden {
		t.Error("changing the volume must keep the strip visible")
	}

	// Leaving cancels the timer: nothing may touch a stale overlay later.
	c.exitFullscreen()
	time.Sleep(60 * time.Millisecond)
	h.drain()
}

func TestOverlayBarShowsTheExitIconAndStaysUsable(t *testing.T) {
	h := newFullscreenHarness(t)
	c := h.c
	if c.transport.fullscreen.Icon != resources.Icon(resources.IconFullscreen) {
		t.Error("the Trim tab's button must show the enter icon")
	}
	c.enterFullscreen()
	if c.fs.bar.fullscreen.Icon != resources.Icon(resources.IconFullscreenExit) || c.fs.bar.fullscreen.tip() != "Exit full-screen (Esc)" {
		t.Errorf("overlay button: icon exit=%v tip=%q", c.fs.bar.fullscreen.Icon == resources.Icon(resources.IconFullscreenExit), c.fs.bar.fullscreen.tip())
	}
	// Even with playback unavailable the way out must stay enabled.
	c.runControl = nil
	c.media.VideoStreams = nil
	c.updatePlaybackControls()
	if c.fs.bar.fullscreen.Disabled() {
		t.Error("the exit button must never be disabled")
	}
	c.fs.bar.fullscreen.OnTapped()
	if c.fs.active {
		t.Error("the overlay's button must leave full-screen")
	}
	// Volume stays in sync with the Trim tab's bar while full-screen.
	c.media.VideoStreams = []model.VideoStream{{Index: 0}}
	c.enterFullscreen()
	c.fs.bar.volume.SetValue(25)
	if c.transport.volume.Value != 25 {
		t.Errorf("Trim tab volume = %v, want it synced to 25", c.transport.volume.Value)
	}
}

func TestWindowSizeIsNotRememberedInFullscreen(t *testing.T) {
	h := newFullscreenHarness(t)
	c := h.c
	c.configPath = filepath.Join(t.TempDir(), "config.yaml")
	c.window.Resize(fyne.NewSize(900, 700))
	c.rememberWindowSize()
	if c.config.UI.WindowWidth != 900 || c.config.UI.WindowHeight != 700 {
		t.Fatalf("normal size not remembered: %dx%d", c.config.UI.WindowWidth, c.config.UI.WindowHeight)
	}

	c.enterFullscreen()
	c.window.Resize(fyne.NewSize(3840, 2160)) // the screen
	c.rememberWindowSize()
	if c.config.UI.WindowWidth != 900 || c.config.UI.WindowHeight != 700 {
		t.Errorf("the full-screen size was remembered: %dx%d, want the window's 900x700",
			c.config.UI.WindowWidth, c.config.UI.WindowHeight)
	}
}

func TestFullscreenEndsWhenThePreviewGoesAway(t *testing.T) {
	t.Run("trimming disabled", func(t *testing.T) {
		h := newFullscreenHarness(t)
		c := h.c
		c.enterFullscreen()
		c.setTrimEnabled(false)
		if c.fs.active || c.window.FullScreen() {
			t.Error("disabling trimming must leave full-screen")
		}
	})
	t.Run("a new input", func(t *testing.T) {
		h := newFullscreenHarness(t)
		c := h.c
		content := c.window.Content()
		c.enterFullscreen()
		c.resetTrimForNewInput()
		if c.fs.active || c.window.FullScreen() || c.window.Content() != content {
			t.Error("loading another input must leave full-screen and restore the window")
		}
	})
}

func TestFullscreenSharesThePreviewWidgetsWithTheTrimTab(t *testing.T) {
	h := newFullscreenHarness(t)
	c := h.c
	c.enterFullscreen()
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	c.displayPreview(img, 12*time.Second)
	if c.previewImage.Image != image.Image(img) || c.previewTimeLabel.Text != "00:00:12.000" {
		t.Error("frames shown while full-screen must land on the shared preview widgets")
	}
	c.exitFullscreen()
	if c.previewImage.Image != image.Image(img) {
		t.Error("the Trim tab must show the frame that was displayed in full-screen")
	}
}

// TestExitRestoresTheWindowedSize pins the fix for a canvas stuck at the
// screen size after leaving full-screen: some window managers (KWin over
// XWayland) restore the window without the size callback Fyne relies
// on, so the layout kept the full-screen size — controls fell outside
// the window and the screen size was remembered as the window size.
func TestExitRestoresTheWindowedSize(t *testing.T) {
	h := newFullscreenHarness(t)
	c := h.c
	c.configPath = filepath.Join(t.TempDir(), "config.yaml")
	c.window.Resize(fyne.NewSize(900, 700))

	c.enterFullscreen()
	c.window.Resize(fyne.NewSize(1920, 1080)) // the screen, as the driver reports it
	c.exitFullscreen()                        // no resize callback follows here

	if got := c.window.Canvas().Size(); got != fyne.NewSize(900, 700) {
		t.Fatalf("canvas after exit = %v, want the windowed 900x700", got)
	}
	c.rememberWindowSize()
	if c.config.UI.WindowWidth != 900 || c.config.UI.WindowHeight != 700 {
		t.Errorf("remembered %dx%d, want 900x700", c.config.UI.WindowWidth, c.config.UI.WindowHeight)
	}
}
