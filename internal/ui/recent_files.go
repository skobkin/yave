package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/storage"

	"git.skobk.in/skobkin/yave/internal/profiles"
)

// maxRecentFiles bounds the recent-inputs list.
const maxRecentFiles = 10

// mediaExtensions is the filter of the open-file dialog; "Show all files"
// lifts it.
var mediaExtensions = []string{
	".mkv", ".mp4", ".m4v", ".webm", ".mov", ".avi", ".ts", ".m2ts", ".mts",
	".flv", ".wmv", ".mpg", ".mpeg", ".ogv", ".3gp", ".mxf", ".vob",
}

// addRecent puts path first, removing an earlier occurrence and keeping at
// most limit entries.
func addRecent(list []string, path string, limit int) []string {
	out := make([]string, 0, min(len(list)+1, limit))
	out = append(out, path)
	for _, p := range list {
		if p != path && len(out) < limit {
			out = append(out, p)
		}
	}
	return out
}

// existingFiles drops entries that no longer exist on disk.
func existingFiles(list []string) []string {
	out := make([]string, 0, len(list))
	for _, p := range list {
		if info, err := os.Stat(p); err == nil && !info.IsDir() {
			out = append(out, p)
		}
	}
	return out
}

// rememberInput records an opened input in the recent list and as the
// dialog's start folder, persisting quietly (a failed write must not
// interrupt opening a file).
func (c *Controller) rememberInput(path string) {
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	recent := addRecent(c.config.UI.RecentFiles, abs, maxRecentFiles)
	dir := filepath.Dir(abs)
	if slices.Equal(recent, c.config.UI.RecentFiles) && dir == c.config.UI.LastInputDir {
		return
	}
	c.config.UI.RecentFiles, c.config.UI.LastInputDir = recent, dir
	if c.configPath != "" {
		_ = profiles.Save(c.configPath, c.config)
	}
}

// recentMenu lists the recent inputs that still exist.
func (c *Controller) recentMenu() *fyne.Menu {
	files := existingFiles(c.config.UI.RecentFiles)
	if len(files) == 0 {
		item := fyne.NewMenuItem("No recent files", nil)
		item.Disabled = true
		return fyne.NewMenu("", item)
	}
	items := make([]*fyne.MenuItem, len(files))
	for i, path := range files {
		items[i] = fyne.NewMenuItem(filepath.Base(path)+"  —  "+filepath.Dir(path), func() { c.setInput(path) })
	}
	return fyne.NewMenu("", items...)
}

// openFileDialogSize is about 80% of the window, so the dialog is roomy
// without covering the window edge to edge.
func openFileDialogSize(window fyne.Size) fyne.Size {
	return fyne.NewSize(max(window.Width*0.8, 600), max(window.Height*0.8, 400))
}

// handleDrop loads the first dropped file and says so when more were
// dropped, since only one input is supported.
func (c *Controller) handleDrop(uris []fyne.URI) {
	if len(uris) == 0 {
		return
	}
	c.setInput(uris[0].Path())
	if len(uris) > 1 {
		dialog.ShowInformation("Only one file loaded",
			fmt.Sprintf("%d files were dropped. Only the first (%s) was loaded.", len(uris), filepath.Base(uris[0].Path())),
			c.window)
	}
}

// locationFor returns the dialog start folder when it still exists.
func locationFor(dir string) fyne.ListableURI {
	if dir == "" {
		return nil
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return nil
	}
	lister, err := storage.ListerForURI(storage.NewFileURI(dir))
	if err != nil {
		return nil
	}
	return lister
}
