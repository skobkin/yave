package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	fynetest "fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"

	"git.skobk.in/skobkin/yave/internal/model"
	"git.skobk.in/skobkin/yave/internal/profiles"
)

type fakeConversionControl struct {
	cancelRequests int
	terminations   int
}

func (c *fakeConversionControl) RequestCancel() {
	c.cancelRequests++
}

func (c *fakeConversionControl) Terminate() {
	c.terminations++
}

func TestCancelEscalatesToTerminateAfterGracePeriod(t *testing.T) {
	app := fynetest.NewApp()
	defer app.Quit()

	control := &fakeConversionControl{}
	updates := make(chan func(), 1)
	c := &Controller{
		runControl:  control,
		cancelDelay: 10 * time.Millisecond,
		status:      widget.NewLabel("Running"),
		cancel:      widget.NewButton("Cancel", nil),
		uiDispatch:  func(update func()) { updates <- update },
	}

	c.cancelConversion()
	if control.cancelRequests != 1 {
		t.Fatalf("cancel requests = %d, want 1", control.cancelRequests)
	}
	if c.status.Text != "Canceling…" || c.cancel.Disabled() {
		t.Fatalf("initial cancel state = status %q, disabled %v", c.status.Text, c.cancel.Disabled())
	}

	waitForUIUpdate(t, updates)()
	if c.cancel.Text != "Terminate" || c.cancel.Disabled() {
		t.Fatalf("escalated button = %q, disabled %v", c.cancel.Text, c.cancel.Disabled())
	}
	if c.status.Text != "FFmpeg is not responding" {
		t.Fatalf("escalated status = %q", c.status.Text)
	}

	c.cancelConversion()
	if control.terminations != 1 {
		t.Fatalf("terminations = %d, want 1", control.terminations)
	}
	if c.status.Text != "Terminating…" || !c.cancel.Disabled() {
		t.Fatalf("termination state = status %q, disabled %v", c.status.Text, c.cancel.Disabled())
	}
}

func TestSecondCancelTerminatesWhenTimerUIUpdateIsDelayed(t *testing.T) {
	app := fynetest.NewApp()
	defer app.Quit()

	control := &fakeConversionControl{}
	updates := make(chan func(), 1)
	c := &Controller{
		runControl:  control,
		cancelDelay: 10 * time.Millisecond,
		status:      widget.NewLabel("Running"),
		cancel:      widget.NewButton("Cancel", nil),
		uiDispatch:  func(update func()) { updates <- update },
	}

	c.cancelConversion()
	time.Sleep(20 * time.Millisecond)
	c.cancelConversion()

	if control.terminations != 1 {
		t.Fatalf("terminations = %d, want 1", control.terminations)
	}
	if c.cancelStage != cancellationTerminating || !c.cancel.Disabled() {
		t.Fatalf("termination state = stage %v, disabled %v", c.cancelStage, c.cancel.Disabled())
	}
}

func TestRunLogUpdatesDoNotQueueBehindSlowRender(t *testing.T) {
	app := fynetest.NewApp()
	defer app.Quit()

	control := &fakeConversionControl{}
	updates := make(chan func(), 4)
	log := newRunLog("", nil)
	c := &Controller{
		runControl: control,
		log:        widget.NewMultiLineEntry(),
		uiDispatch: func(update func()) { updates <- update },
	}
	stop := c.startRunLogUpdates(log, control)
	defer close(stop)

	log.AppendLine("first")
	firstUpdate := waitForUIUpdate(t, updates)
	log.AppendLine("second")
	time.Sleep(3 * runLogFlushInterval)
	if queued := len(updates); queued != 0 {
		t.Fatalf("log updates queued behind pending render = %d, want 0", queued)
	}

	firstUpdate()
	secondUpdate := waitForUIUpdate(t, updates)
	secondUpdate()
	if !strings.Contains(c.log.Text, "second") {
		t.Fatalf("rendered log = %q, want latest output", c.log.Text)
	}
}

func TestCancelTimerDoesNotAffectNewRun(t *testing.T) {
	app := fynetest.NewApp()
	defer app.Quit()

	oldControl := &fakeConversionControl{}
	updates := make(chan func(), 1)
	c := &Controller{
		runControl:  oldControl,
		cancelDelay: 10 * time.Millisecond,
		status:      widget.NewLabel("Running"),
		cancel:      widget.NewButton("Cancel", nil),
		uiDispatch:  func(update func()) { updates <- update },
	}
	c.cancelConversion()

	update := waitForUIUpdate(t, updates)
	c.runControl = &fakeConversionControl{}
	c.cancelStage = cancellationRunning
	c.status.SetText("Running")
	c.cancel.SetText("Cancel")
	c.cancel.Enable()
	update()

	if c.cancelStage != cancellationRunning || c.cancel.Text != "Cancel" || c.cancel.Disabled() {
		t.Fatalf("new run mutated by stale timer: stage %v, text %q, disabled %v", c.cancelStage, c.cancel.Text, c.cancel.Disabled())
	}
}

func TestWindowCloseHandlerHidesMainWindow(t *testing.T) {
	app := fynetest.NewApp()
	defer app.Quit()

	c := &Controller{window: app.NewWindow("YAVE"), windowVisible: true}
	c.window.Show()
	c.hideWindow()
	if c.windowVisible {
		t.Fatal("windowVisible = true after hide")
	}
}

func TestActiveQuitPromptLeavesMainWindowStateUnchangedWhenClosed(t *testing.T) {
	app := fynetest.NewApp()
	defer app.Quit()

	control := &fakeConversionControl{}
	c := &Controller{
		app:           app,
		window:        app.NewWindow("YAVE"),
		windowVisible: false,
		runControl:    control,
	}
	c.requestQuit()
	if c.quitPrompt == nil {
		t.Fatal("quit prompt was not created")
	}
	firstPrompt := c.quitPrompt
	c.requestQuit()
	if c.quitPrompt != firstPrompt {
		t.Fatal("a duplicate quit prompt was created")
	}
	button := findButton(c.quitPrompt.Content(), "Keep Running")
	if button == nil {
		t.Fatal("Keep Running button not found")
	}
	fynetest.Tap(button)

	if c.quitPrompt != nil {
		t.Fatal("quit prompt was not cleared after close")
	}
	if c.windowVisible {
		t.Fatal("main window visibility changed")
	}
	if control.terminations != 0 {
		t.Fatalf("terminations = %d, want 0", control.terminations)
	}
}

func TestConfirmQuitTerminatesActiveConversion(t *testing.T) {
	app := fynetest.NewApp()

	control := &fakeConversionControl{}
	c := &Controller{
		app:        app,
		window:     app.NewWindow("YAVE"),
		runControl: control,
	}
	c.requestQuit()
	button := findButton(c.quitPrompt.Content(), "Terminate and Quit")
	if button == nil {
		t.Fatal("Terminate and Quit button not found")
	}
	fynetest.Tap(button)

	if control.terminations != 1 {
		t.Fatalf("terminations = %d, want 1", control.terminations)
	}
	if c.quitPrompt != nil {
		t.Fatal("quit prompt was not cleared")
	}
}

func findButton(object fyne.CanvasObject, label string) *widget.Button {
	switch object := object.(type) {
	case *widget.Button:
		if object.Text == label {
			return object
		}
	case *fyne.Container:
		for _, child := range object.Objects {
			if button := findButton(child, label); button != nil {
				return button
			}
		}
	}
	return nil
}

func waitForUIUpdate(t *testing.T, updates <-chan func()) func() {
	t.Helper()
	select {
	case update := <-updates:
		return update
	case <-time.After(2 * time.Second):
		t.Fatal("UI update was not dispatched")
		return nil
	}
}

// Regression (Codex review round 2): quitting mid-export let the app
// process die while the run goroutine was still unwinding, so the
// staging directory never got removed — a Go process exits when main
// returns and goroutines cannot outlive it. quitApplication must await
// the run goroutine (after Terminate) before quitting.
func TestQuitAwaitsRunGoroutineCleanup(t *testing.T) {
	app := fynetest.NewApp()
	c := &Controller{
		app:    app,
		window: app.NewWindow("YAVE"),
	}

	staging := filepath.Join(t.TempDir(), "yave-export-X")
	if err := os.Mkdir(staging, 0o750); err != nil {
		t.Fatal(err)
	}
	runDone := make(chan struct{})
	c.runDone = runDone
	go func() {
		defer close(runDone)
		// Stand-in for the run goroutine's tail: cleanup happens before
		// the channel closes, exactly as in runJob.
		time.Sleep(20 * time.Millisecond)
		if err := os.Remove(staging); err != nil {
			t.Errorf("cleanup failed: %v", err)
		}
	}()

	c.quitApplication()

	if _, err := os.Stat(staging); !os.IsNotExist(err) {
		t.Fatal("quitApplication returned before the run goroutine removed the staging directory")
	}
}

// The cleanup wait is bounded: a wedged run goroutine must delay exit,
// never hang it.
func TestQuitProceedsAfterCleanupWaitTimeout(t *testing.T) {
	old := quitCleanupTimeout
	quitCleanupTimeout = 20 * time.Millisecond
	t.Cleanup(func() { quitCleanupTimeout = old })

	app := fynetest.NewApp()
	c := &Controller{
		app:    app,
		window: app.NewWindow("YAVE"),
	}
	c.runDone = make(chan struct{}) // never closes

	done := make(chan struct{})
	go func() {
		c.quitApplication()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("quitApplication hung past the cleanup wait timeout")
	}
}

func TestTrayMenuListsToggleFirstAndLabelFollowsVisibility(t *testing.T) {
	app := fynetest.NewApp()
	defer app.Quit()

	quit := fyne.NewMenuItem("Quit", nil)
	quit.IsQuit = true
	c := &Controller{
		window:         app.NewWindow("YAVE"),
		windowVisible:  true,
		trayToggleItem: fyne.NewMenuItem(trayToggleLabel(true), nil),
		trayCheckItem:  fyne.NewMenuItem("Check for updates…", nil),
		trayQuitItem:   quit,
	}
	items := c.trayMenu().Items
	if len(items) != 3 || items[0] != c.trayToggleItem {
		t.Fatalf("tray menu = %v, want the show/hide item first", items)
	}

	c.toggleWindow() // visible -> hidden
	if c.windowVisible || c.trayToggleItem.Label != "Show YAVE" {
		t.Fatalf("after hide: visible=%v label=%q", c.windowVisible, c.trayToggleItem.Label)
	}
	c.toggleWindow() // hidden -> visible
	if !c.windowVisible || c.trayToggleItem.Label != "Hide YAVE" {
		t.Fatalf("after show: visible=%v label=%q", c.windowVisible, c.trayToggleItem.Label)
	}
}

func TestCloseRequestedFollowsQuitOnCloseSetting(t *testing.T) {
	t.Run("quit on close goes through the quit flow", func(t *testing.T) {
		app := fynetest.NewApp()
		defer app.Quit()
		c := &Controller{
			app:           app,
			window:        app.NewWindow("YAVE"),
			windowVisible: true,
			runControl:    &fakeConversionControl{},
		}
		c.config.UI.QuitOnClose = true
		c.onCloseRequested()
		if c.quitPrompt == nil {
			t.Fatal("closing during a run with quit-on-close must show the quit confirmation")
		}
		if !c.windowVisible {
			t.Fatal("the window must stay visible while the prompt is open")
		}
	})

	t.Run("first hide shows the notice once and persists it", func(t *testing.T) {
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())
		app := fynetest.NewApp()
		defer app.Quit()
		c := &Controller{
			app:        app,
			window:     app.NewWindow("YAVE"),
			configPath: filepath.Join(t.TempDir(), "config.yaml"),
			config:     model.DefaultConfig(),
		}
		c.windowVisible = true
		c.window.Show()
		c.onCloseRequested()
		if !c.config.UI.TrayNoticeShown {
			t.Fatal("notice flag not recorded")
		}
		saved, err := profiles.Load(c.configPath)
		if err != nil || !saved.UI.TrayNoticeShown {
			t.Fatalf("notice flag not persisted: %v %#v", err, saved.UI)
		}
		// Second close hides immediately, without another notice.
		c.onCloseRequested()
		if c.windowVisible {
			t.Fatal("window must hide immediately once the notice was shown")
		}
	})
}
