package ui

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"fyne.io/fyne/v2/dialog"

	updates "github.com/skobkin/go4updates"
	"github.com/skobkin/go4updates/releasefmt"
	fyneupdate "github.com/skobkin/go4updates/ui/fyne/update"

	"git.skobk.in/skobkin/yave/internal/app"
	"git.skobk.in/skobkin/yave/internal/model"
	appupdates "git.skobk.in/skobkin/yave/internal/updates"
)

// updateLogger receives the bridge's diagnostics; a variable so tests can
// capture output. The manager logs its own check lifecycle.
var updateLogger = slog.Default()

// updateChecker is the seam between the UI and the update manager; the
// production implementation is *appupdates.Manager and tests substitute a
// stub.
type updateChecker interface {
	Running() bool
	Start()
	Stop()
	CheckNow(ctx context.Context) (updates.Result, error)
}

// newUpdateChecker builds the production update manager; kept as a variable
// so tests can stub construction without a Forgejo server.
var newUpdateChecker = func(onAvailable func(updates.Result)) (updateChecker, error) {
	return appupdates.New(appupdates.Options{
		CurrentVersion:    app.BuildVersion(),
		OnUpdateAvailable: onAvailable,
	})
}

// setupUpdateChecker creates the update manager for the application. A nil
// checker is valid: every entry point degrades to a diagnostic dialog.
// Construction only configures the release source and performs no network
// I/O, so failure is limited to a malformed repository constant.
func (c *Controller) setupUpdateChecker() {
	checker, err := newUpdateChecker(c.onAutoUpdateAvailable)
	if err != nil {
		updateLogger.Warn("initialize update checker", "error", err)
		return
	}
	c.updateChecker = checker
}

// startUpdateChecks enables periodic checks per config; development builds
// and disabled configs skip them. Manual checks remain available either way.
func (c *Controller) startUpdateChecks() {
	if c.updateChecker == nil {
		return
	}
	if !updateChecksEnabled(c.config, app.BuildVersion()) {
		// Info, not debug: at the default level this is the only evidence
		// that update checking exists, and it explains the silence on dev
		// builds and user-disabled configs alike.
		updateLogger.Info("automatic update checks disabled",
			"version", app.BuildVersion(),
			"configured", c.config.Updates.CheckAutomatically)
		return
	}
	c.updateChecker.Start()
}

// updateChecksEnabled reports whether automatic checks should run for this
// config and build: never on development builds, otherwise per config.
func updateChecksEnabled(cfg model.Config, version string) bool {
	if version == app.DevBuildVersion {
		return false
	}
	return cfg.Updates.CheckAutomatically
}

// applyUpdateCheckSetting stops or (re)starts periodic checks after a
// settings save. Called on the UI thread from showSettings.
func (c *Controller) applyUpdateCheckSetting(enabled bool) {
	if c.updateChecker == nil {
		return
	}
	if app.BuildVersion() == app.DevBuildVersion {
		return
	}
	if enabled == c.updateChecker.Running() {
		return
	}
	if enabled {
		// Start performs an immediate first check; the manager suppresses a
		// repeated prompt for an already-shown version.
		c.updateChecker.Start()
		return
	}
	c.updateChecker.Stop()
}

// onAutoUpdateAvailable bridges from a Manager goroutine to the UI thread.
// It only enqueues and never blocks the event drain.
func (c *Controller) onAutoUpdateAvailable(result updates.Result) {
	c.dispatchUI(func() {
		c.presentAutoUpdate(result)
	})
}

// presentAutoUpdate shows the new-release dialog; the manager already
// deduplicated, so this runs at most once per version.
func (c *Controller) presentAutoUpdate(result updates.Result) {
	c.revealMainWindowIfHidden()
	c.showUpdateDialog(result)
}

// checkForUpdates runs one manual update check and presents the outcome.
// Runs on the UI thread; the network round-trip happens in a goroutine.
func (c *Controller) checkForUpdates() {
	if c.updateChecker == nil {
		dialog.ShowError(errors.New("update checking is unavailable"), c.window)
		return
	}
	if !c.checkingUpdates.CompareAndSwap(false, true) {
		return
	}
	c.setUpdateItemDisabled(true)
	go func() {
		result, err := c.updateChecker.CheckNow(context.Background())
		c.dispatchUI(func() {
			c.setUpdateItemDisabled(false)
			c.presentManualCheckResult(result, err)
			// Cleared last: a busy-flag observation implies the previous
			// check, including its presentation, has fully completed.
			c.checkingUpdates.Store(false)
		})
	}()
}

// presentManualCheckResult shows the manual-check outcome: unlike the
// automatic path, every result is surfaced, including failures, because the
// user explicitly asked.
func (c *Controller) presentManualCheckResult(result updates.Result, err error) {
	c.revealMainWindowIfHidden()
	if err != nil {
		updateLogger.Warn("manual update check failed", "error", err)
		dialog.ShowError(fmt.Errorf("check for updates failed:\n%w", err), c.window)
		return
	}
	c.showUpdateDialog(result)
}

// revealMainWindowIfHidden makes the main window visible before a dialog is
// attached to it: a dialog on a hidden (tray-minimized) window is invisible
// on most platforms. Must run on the UI thread.
func (c *Controller) revealMainWindowIfHidden() {
	if c.windowVisible {
		return
	}
	c.windowVisible = true
	c.window.Show()
	c.window.RequestFocus()
}

// showUpdateDialog renders the go4updates release dialog for the fetched
// result. Must run on the UI thread.
func (c *Controller) showUpdateDialog(result updates.Result) {
	fyneupdate.ShowDialog(c.window, result, fyneupdate.Options{
		Format: releasefmt.Options{
			// The linker requires an absolute repository URL; the feed's
			// value is derived from the configured server.
			Linker:         releasefmt.ForgejoLinker(result.Feed.RepositoryURL),
			ShortCommitSHA: true,
		},
		// Markdown options stay zero: remote release-note images are not
		// fetched.
	})
}

// setUpdateItemDisabled toggles the tray item while a manual check is in
// flight. fyne.MenuItem is a plain data struct without Refresh, so the menu
// is republished — the same idiom trayMenu uses. Native tray backends may
// not repaint immediately; the busy guard in checkForUpdates is the actual
// protection.
func (c *Controller) setUpdateItemDisabled(disabled bool) {
	if c.trayApp == nil || c.trayCheckItem == nil {
		return
	}
	c.trayCheckItem.Disabled = disabled
	c.trayApp.SetSystemTrayMenu(c.trayMenu())
}
