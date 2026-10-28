package ui

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"

	updates "github.com/skobkin/go4updates"
	"github.com/skobkin/go4updates/releasefmt"
	"github.com/skobkin/go4updates/ui/fyne/markdown"

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

// Release dialog labels, mirrored from go4updates' fyne dialog so the UX
// stays identical while YAVE owns the changelog rendering.
const (
	updateDialogTitle         = "Software update"
	updateDialogCloseLabel    = "Close"
	updateDialogOpenLabel     = "Open release"
	updateDialogNoNotesText   = "No release notes are available."
	updateDialogUnknownVer    = "unknown"
	updateDialogInstalledText = "Installed version: "
	updateDialogLatestText    = "Latest release: "
)

// Sizes of the scrollable release notes area, matching the library dialog.
const (
	updateChangelogWidth  = float32(480)
	updateChangelogHeight = float32(280)
)

// showUpdateDialog renders the release dialog for a fetched result. Must run
// on the UI thread.
//
// The changelog is rendered here instead of through fyneupdate.ShowDialog:
// Fyne 2.8 enables GFM tables, and go4updates' remote-image rewriter does
// not descend into widget.TableSegment cells, so an image inside a release
// note table would load synchronously on the UI thread during the dialog's
// first layout. withoutRemoteImages replaces those segments with inert
// placeholders, keeping remote release-note images unfetched; once
// go4updates covers table cells this can go back to fyneupdate.ShowDialog.
func (c *Controller) showUpdateDialog(result updates.Result) {
	releaseDialog := dialog.NewCustomWithoutButtons(updateDialogTitle, container.NewVBox(
		updateVersionHeader(result),
		updateStatusLabel(result),
		newUpdateChangelog(result),
	), c.window)

	openURL := updateReleaseURL(result)
	open := widget.NewButton(updateDialogOpenLabel, func() {
		openUpdateReleasePage(openURL)
	})
	open.Importance = widget.HighImportance
	if openURL == "" {
		open.Disable()
	}
	releaseDialog.SetButtons([]fyne.CanvasObject{
		widget.NewButton(updateDialogCloseLabel, releaseDialog.Hide),
		open,
	})

	releaseDialog.Show()
}

// updateVersionHeader names the installed and latest versions; the latest
// line is omitted when the feed has no release.
func updateVersionHeader(result updates.Result) fyne.CanvasObject {
	installed := widget.NewLabel(updateDialogInstalledText + updateDisplayVersion(result.CurrentVersion))
	installed.TextStyle = fyne.TextStyle{Bold: true}

	if result.Latest == nil {
		return installed
	}

	latest := widget.NewLabel(updateDialogLatestText + updateDisplayVersion(result.Latest.Version))

	return container.NewVBox(installed, latest)
}

// updateDisplayVersion returns version for display, naming the unknown case
// when it is empty.
func updateDisplayVersion(version string) string {
	version = strings.TrimSpace(version)
	if version == "" {
		return updateDialogUnknownVer
	}

	return version
}

// updateStatusLabel describes the comparison outcome.
func updateStatusLabel(result updates.Result) fyne.CanvasObject {
	status := widget.NewLabel(updateStatusText(result))
	status.Importance = widget.MediumImportance
	if result.Status == updates.StatusUpdateAvailable {
		status.Importance = widget.HighImportance
	}
	status.Wrapping = fyne.TextWrapWord

	return status
}

// updateStatusText describes result in one line.
func updateStatusText(result updates.Result) string {
	switch result.Status {
	case updates.StatusUpdateAvailable:
		return "A newer release is available."

	case updates.StatusUpToDate:
		return "This installation is up to date."

	default:
		return "No release information is available."
	}
}

// newUpdateChangelog builds the scrollable release notes from the raw
// release bodies. A feed without release bodies shows a short note instead
// of an empty scroll area. Markdown options stay zero: remote release-note
// images are not fetched, and withoutRemoteImages enforces that inside GFM
// tables too.
func newUpdateChangelog(result updates.Result) fyne.CanvasObject {
	changelog := releasefmt.Changelog(result.Feed.Releases, releasefmt.Options{
		// The linker requires an absolute repository URL; the feed's value
		// is derived from the configured server.
		Linker:         releasefmt.ForgejoLinker(result.Feed.RepositoryURL),
		ShortCommitSHA: true,
	})
	if changelog == "" {
		notes := widget.NewLabel(updateDialogNoNotesText)
		notes.Wrapping = fyne.TextWrapWord

		return notes
	}

	notes := markdown.NewRichText(changelog, markdown.Options{})
	notes.Segments = withoutRemoteImages(notes.Segments)
	notes.Wrapping = fyne.TextWrapWord

	body := container.NewVScroll(notes)
	body.SetMinSize(fyne.NewSize(updateChangelogWidth, updateChangelogHeight))

	return body
}

// updateReleaseURL returns the page to open for result: the latest release
// page when the release has one, otherwise the release listing of the feed.
func updateReleaseURL(result updates.Result) string {
	if result.Latest != nil {
		if release := strings.TrimSpace(result.Latest.URL); release != "" {
			return release
		}
	}

	return strings.TrimSpace(result.Feed.ReleasesURL)
}

// openUpdateReleasePage opens rawURL through the running Fyne application,
// logging failures instead of showing a dialog: the release page stays
// reachable from the project repository either way.
func openUpdateReleasePage(rawURL string) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		updateLogger.Warn("open release page", "url", rawURL, "error", err)
		return
	}
	application := fyne.CurrentApp()
	if application == nil {
		updateLogger.Warn("open release page", "url", rawURL, "error", "no running Fyne application")
		return
	}
	if err := application.OpenURL(parsed); err != nil {
		updateLogger.Warn("open release page", "url", rawURL, "error", err)
	}
}

// withoutRemoteImages returns segments with every remote image segment
// replaced by an inert text placeholder, recursing into the container
// segments a Markdown document is built from.
//
// go4updates rewrites top-level paragraph and list images into lazily loaded
// segments that honor the no-fetch policy, but Fyne 2.8 renders GFM tables
// as widget.TableSegment, whose cells its rewriter never visits. A stock
// ImageSegment left in a table cell fetches its URL synchronously on the UI
// thread during the dialog's first layout. Local and data images stay:
// release notes on a forge effectively only reference remote ones, and the
// placeholder marks what would otherwise render there.
func withoutRemoteImages(segments []widget.RichTextSegment) []widget.RichTextSegment {
	rewritten := make([]widget.RichTextSegment, 0, len(segments))

	for _, segment := range segments {
		switch current := segment.(type) {
		case *widget.ImageSegment:
			if isRemoteImageSegment(current) {
				rewritten = append(rewritten, &widget.TextSegment{
					Style: widget.RichTextStyleInline,
					Text:  "[image]",
				})
				continue
			}
			rewritten = append(rewritten, segment)

		case *widget.ListSegment:
			clone := *current
			clone.Items = withoutRemoteImages(current.Items)
			rewritten = append(rewritten, &clone)

		case *widget.ParagraphSegment:
			clone := *current
			clone.Texts = withoutRemoteImages(current.Texts)
			rewritten = append(rewritten, &clone)

		case *widget.TableSegment:
			clone := *current
			clone.Headers = withoutRemoteImagesInCells(current.Headers)
			clone.Rows = withoutRemoteImagesInRows(current.Rows)
			rewritten = append(rewritten, &clone)

		default:
			rewritten = append(rewritten, segment)
		}
	}

	return rewritten
}

// withoutRemoteImagesInCells sanitizes the cells of one table row.
func withoutRemoteImagesInCells(cells [][]widget.RichTextSegment) [][]widget.RichTextSegment {
	sanitized := make([][]widget.RichTextSegment, len(cells))
	for i, cell := range cells {
		sanitized[i] = withoutRemoteImages(cell)
	}

	return sanitized
}

// withoutRemoteImagesInRows sanitizes every row of a table.
func withoutRemoteImagesInRows(rows [][][]widget.RichTextSegment) [][][]widget.RichTextSegment {
	sanitized := make([][][]widget.RichTextSegment, len(rows))
	for i, row := range rows {
		sanitized[i] = withoutRemoteImagesInCells(row)
	}

	return sanitized
}

// isRemoteImageSegment reports whether the image points at an http or https
// location, the only schemes a forge release page can reference.
func isRemoteImageSegment(segment *widget.ImageSegment) bool {
	if segment.Source == nil {
		return false
	}

	switch segment.Source.Scheme() {
	case "http", "https":
		return true
	default:
		return false
	}
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
