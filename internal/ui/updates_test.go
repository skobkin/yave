package ui

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	fynetest "fyne.io/fyne/v2/test"

	updates "github.com/skobkin/go4updates"

	"git.skobk.in/skobkin/yave/internal/app"
	"git.skobk.in/skobkin/yave/internal/model"
)

// stubUpdateChecker records lifecycle calls and serves a scripted CheckNow.
type stubUpdateChecker struct {
	mu       sync.Mutex
	running  bool
	started  int
	stopped  int
	checkNow func(context.Context) (updates.Result, error)
}

func (s *stubUpdateChecker) Running() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running
}

func (s *stubUpdateChecker) Start() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.started++
	s.running = true
}

func (s *stubUpdateChecker) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopped++
	s.running = false
}

func (s *stubUpdateChecker) CheckNow(ctx context.Context) (updates.Result, error) {
	s.mu.Lock()
	check := s.checkNow
	s.mu.Unlock()
	if check == nil {
		return updates.Result{}, nil
	}
	return check(ctx)
}

// swapUpdateLogger replaces the bridge logger and restores it on cleanup.
func swapUpdateLogger(t *testing.T, buf *bytes.Buffer) {
	t.Helper()
	original := updateLogger
	if buf != nil {
		updateLogger = slog.New(slog.NewTextHandler(buf, nil))
	} else {
		updateLogger = slog.New(slog.DiscardHandler)
	}
	t.Cleanup(func() { updateLogger = original })
}

// The tray menu must contain the update action before the quit entry.
// trayMenu is the single source of the composition, so setUpdateItemDisabled
// republishes the same items configureTray installed.
func TestTrayMenuIncludesUpdateItem(t *testing.T) {
	quit := fyne.NewMenuItem("Quit", nil)
	quit.IsQuit = true
	c := &Controller{
		trayCheckItem: fyne.NewMenuItem("Check for updates…", nil),
		trayQuitItem:  quit,
	}

	items := c.trayMenu().Items
	if len(items) != 2 {
		t.Fatalf("menu items = %d, want 2", len(items))
	}
	if items[0].Label != "Check for updates…" {
		t.Fatalf("item[0] = %q, want the update action", items[0].Label)
	}
	if items[1].Label != "Quit" || !items[1].IsQuit {
		t.Fatalf("item[1] = %q (quit %v), want the quit action", items[1].Label, items[1].IsQuit)
	}
}

func TestSetupUpdateCheckerInjectsStub(t *testing.T) {
	original := newUpdateChecker
	newUpdateChecker = func(func(updates.Result)) (updateChecker, error) {
		return &stubUpdateChecker{}, nil
	}
	t.Cleanup(func() { newUpdateChecker = original })

	c := &Controller{}
	c.setupUpdateChecker()
	if c.updateChecker == nil {
		t.Fatal("expected the stub checker to be installed")
	}
}

// A manual check while another is in flight must be a no-op: the busy guard
// protects against native tray backends that deliver duplicate activations.
func TestCheckForUpdatesBusyGuard(t *testing.T) {
	application := fynetest.NewApp()
	defer application.Quit()
	swapUpdateLogger(t, nil)

	release := make(chan struct{})
	var checks atomic.Int64
	stub := &stubUpdateChecker{
		checkNow: func(context.Context) (updates.Result, error) {
			checks.Add(1)
			<-release
			return updates.Result{}, nil
		},
	}
	dispatched := make(chan func(), 4)
	c := &Controller{
		window:        application.NewWindow("busy"),
		updateChecker: stub,
		windowVisible: true,
		uiDispatch:    func(update func()) { dispatched <- update },
	}

	c.checkForUpdates()

	// Wait until the in-flight check reached the stub.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && checks.Load() == 0 {
		time.Sleep(5 * time.Millisecond)
	}

	c.checkForUpdates()

	// The first check is still in flight; the second must not have called
	// CheckNow again.
	if got := checks.Load(); got != 1 {
		t.Fatalf("CheckNow calls = %d, want 1", got)
	}
	if !c.checkingUpdates.Load() {
		t.Fatal("expected the busy flag to be set while the check runs")
	}

	// Let the check finish and drain its presentation callback so the
	// goroutine does not leak into the following tests.
	close(release)
	waitForUIUpdate(t, dispatched)()
	if c.checkingUpdates.Load() {
		t.Fatal("expected the busy flag to clear after the check finished")
	}
}

// A manual check on a hidden (tray-minimized) window must reveal it before
// presenting the result.
func TestPresentManualCheckResultRevealsHiddenWindow(t *testing.T) {
	application := fynetest.NewApp()
	defer application.Quit()
	swapUpdateLogger(t, nil)

	c := &Controller{
		window:        application.NewWindow("reveal"),
		windowVisible: false,
	}
	c.presentManualCheckResult(updates.Result{Status: updates.StatusUpToDate}, nil)

	if !c.windowVisible {
		t.Fatal("expected the hidden window to be revealed")
	}
}

func TestPresentManualCheckResultShowsError(t *testing.T) {
	application := fynetest.NewApp()
	defer application.Quit()
	swapUpdateLogger(t, nil)

	c := &Controller{
		window:        application.NewWindow("err"),
		windowVisible: true,
	}

	// Must not panic under the test driver; the error dialog is attached to
	// the window and dismissed with it.
	c.presentManualCheckResult(updates.Result{}, context.Canceled)
}

// onAutoUpdateAvailable must reveal a hidden window and stay non-blocking;
// the presentation path is shared with the manual check.
func TestOnAutoUpdateAvailableRevealsHiddenWindow(t *testing.T) {
	application := fynetest.NewApp()
	defer application.Quit()
	swapUpdateLogger(t, nil)

	c := &Controller{
		window:        application.NewWindow("auto"),
		windowVisible: false,
		uiDispatch:    func(update func()) { update() },
	}

	c.onAutoUpdateAvailable(updates.Result{Status: updates.StatusUpdateAvailable})

	if !c.windowVisible {
		t.Fatal("expected the hidden window to be revealed")
	}
}

// startUpdateChecks starts periodic checks only when the build is a release
// and the config enables them.
func TestStartUpdateChecksGuards(t *testing.T) {
	tests := []struct {
		name       string
		version    string
		configured bool
		wantStarts int
	}{
		{name: "release build and enabled", version: "0.4.0", configured: true, wantStarts: 1},
		{name: "disabled by config", version: "0.4.0", configured: false, wantStarts: 0},
		{name: "development build never checks", version: "dev", configured: true, wantStarts: 0},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			original := app.Version
			app.Version = tc.version
			t.Cleanup(func() { app.Version = original })

			var buf bytes.Buffer
			swapUpdateLogger(t, &buf)
			stub := &stubUpdateChecker{}
			c := &Controller{
				config:        model.Config{Updates: model.UpdatesConfig{CheckAutomatically: tc.configured}},
				updateChecker: stub,
			}

			c.startUpdateChecks()

			stub.mu.Lock()
			started := stub.started
			stub.mu.Unlock()
			if started != tc.wantStarts {
				t.Fatalf("starts = %d, want %d", started, tc.wantStarts)
			}
			// The skipped paths must say so at info level; this fails if the
			// notice regresses to debug.
			if tc.wantStarts == 0 && !strings.Contains(buf.String(), "automatic update checks disabled") {
				t.Fatalf("log output = %q, want the disabled notice", buf.String())
			}
		})
	}
}

// applyUpdateCheckSetting starts and stops periodic checks only on an actual
// change and never on development builds.
func TestApplyUpdateCheckSetting(t *testing.T) {
	tests := []struct {
		name       string
		version    string
		enabled    bool
		running    bool
		wantStarts int
		wantStops  int
	}{
		{name: "enable from stopped", enabled: true, running: false, wantStarts: 1},
		{name: "disable from running", enabled: false, running: true, wantStops: 1},
		{name: "enable while running is a no-op", enabled: true, running: true},
		{name: "disable while stopped is a no-op", enabled: false, running: false},
		{name: "development build never toggles", version: "dev", enabled: true, running: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.version == "" {
				tc.version = "0.4.0"
			}
			original := app.Version
			app.Version = tc.version
			t.Cleanup(func() { app.Version = original })

			swapUpdateLogger(t, nil)
			stub := &stubUpdateChecker{running: tc.running}
			c := &Controller{updateChecker: stub}

			c.applyUpdateCheckSetting(tc.enabled)

			stub.mu.Lock()
			started, stopped := stub.started, stub.stopped
			stub.mu.Unlock()
			if started != tc.wantStarts || stopped != tc.wantStops {
				t.Fatalf("starts = %d (want %d), stops = %d (want %d)",
					started, tc.wantStarts, stopped, tc.wantStops)
			}
		})
	}
}

func TestUpdateChecksEnabled(t *testing.T) {
	cfg := model.Config{}
	cfg.Updates.CheckAutomatically = true
	if !updateChecksEnabled(cfg, "0.4.0") {
		t.Fatal("expected enabled checks for a release build")
	}
	if updateChecksEnabled(cfg, app.DevBuildVersion) {
		t.Fatal("expected dev builds to disable automatic checks")
	}
	cfg.Updates.CheckAutomatically = false
	if updateChecksEnabled(cfg, "0.4.0") {
		t.Fatal("expected the config toggle to disable automatic checks")
	}
}
