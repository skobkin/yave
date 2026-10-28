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
	"fyne.io/fyne/v2/storage"
	fynetest "fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"

	updates "github.com/skobkin/go4updates"
	"github.com/skobkin/go4updates/ui/fyne/markdown"

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

// mustParseURI parses raw into a fyne.URI, failing the test on a bad value.
func mustParseURI(t *testing.T, raw string) fyne.URI {
	t.Helper()
	parsed, err := storage.ParseURI(raw)
	if err != nil {
		t.Fatalf("parsing URI %q: %v", raw, err)
	}

	return parsed
}

// findImageSegments collects every stock image segment in a segment tree,
// including the table cells go4updates' own rewriter skips.
func findImageSegments(segments []widget.RichTextSegment) []*widget.ImageSegment {
	var found []*widget.ImageSegment
	for _, segment := range segments {
		switch current := segment.(type) {
		case *widget.ImageSegment:
			found = append(found, current)
		case *widget.ListSegment:
			found = append(found, findImageSegments(current.Items)...)
		case *widget.ParagraphSegment:
			found = append(found, findImageSegments(current.Texts)...)
		case *widget.TableSegment:
			for _, row := range current.Headers {
				found = append(found, findImageSegments(row)...)
			}
			for _, cells := range current.Rows {
				for _, cell := range cells {
					found = append(found, findImageSegments(cell)...)
				}
			}
		}
	}

	return found
}

// A release note image inside a GFM table survives go4updates' rewriter on
// Fyne 2.8 and would fetch its URL synchronously on the UI thread during the
// dialog's first layout; withoutRemoteImages must catch exactly that case.
func TestWithoutRemoteImagesCoversTableCells(t *testing.T) {
	notes := markdown.NewRichText(
		"| before | after |\n| --- | --- |\n| ![old](https://example.com/old.png) | plain |\n",
		markdown.Options{},
	)

	if before := findImageSegments(notes.Segments); len(before) != 1 {
		t.Fatalf("stock image segments in the parsed table = %d, want 1 (the unrewritten cell)", len(before))
	}

	notes.Segments = withoutRemoteImages(notes.Segments)

	if remaining := findImageSegments(notes.Segments); len(remaining) != 0 {
		t.Fatalf("stock image segments after sanitizing = %d, want 0", len(remaining))
	}
}

func TestWithoutRemoteImages(t *testing.T) {
	remoteImage := &widget.ImageSegment{Source: mustParseURI(t, "https://example.com/badge.png")}
	localImage := &widget.ImageSegment{Source: mustParseURI(t, "file:///tmp/picture.png")}
	plain := &widget.TextSegment{Text: "plain"}

	t.Run("replaces remote images everywhere including table cells", func(t *testing.T) {
		table := &widget.TableSegment{
			Headers: [][]widget.RichTextSegment{{remoteImage, plain}},
			Rows:    [][][]widget.RichTextSegment{{{remoteImage}, {plain}}},
		}
		nested := &widget.ListSegment{Items: []widget.RichTextSegment{
			&widget.ParagraphSegment{Texts: []widget.RichTextSegment{remoteImage}},
		}}

		out := withoutRemoteImages([]widget.RichTextSegment{table, nested})

		if remaining := findImageSegments(out); len(remaining) != 0 {
			t.Fatalf("image segments after sanitizing = %d, want 0", len(remaining))
		}
		sanitized := out[0].(*widget.TableSegment)
		cell := sanitized.Rows[0][0]
		if len(cell) != 1 {
			t.Fatalf("sanitized cell segments = %d, want 1", len(cell))
		}
		placeholder, ok := cell[0].(*widget.TextSegment)
		if !ok || placeholder.Text != "[image]" {
			t.Fatalf("sanitized cell segment = %T, want the [image] placeholder", cell[0])
		}
	})

	t.Run("keeps local images and leaves the input untouched", func(t *testing.T) {
		paragraph := &widget.ParagraphSegment{Texts: []widget.RichTextSegment{localImage, plain}}
		original := []widget.RichTextSegment{paragraph, &widget.ImageSegment{}}

		out := withoutRemoteImages(original)

		if remaining := findImageSegments(out); len(remaining) != 2 {
			t.Fatalf("image segments after sanitizing = %d, want the local and schemeless ones", len(remaining))
		}
		if remaining := findImageSegments(original); len(remaining) != 2 {
			t.Fatalf("original image segments = %d, want 2 (the input must not be mutated)", len(remaining))
		}
	})
}
