// Package resources embeds the application icon assets and exposes them
// as Fyne resources so they can be assigned to the app, the main window,
// and the system tray without touching the filesystem at runtime.
package resources

import (
	_ "embed"

	"fyne.io/fyne/v2"
)

//go:embed assets/app_icon.svg
var appIconSVG []byte

// AppIconResource is the raw Fyne resource for the application icon.
// It is exported so that theme-aware code (a future light variant or a
// runtime theme switcher) can wrap it without going through the
// AppIcon()/TrayIcon() helpers.
var AppIconResource = fyne.NewStaticResource("app_icon.svg", appIconSVG)

// AppIcon returns the resource used for app-level and window-level icons
// (app switcher, taskbar, dock, and window title bar).
func AppIcon() fyne.Resource { return AppIconResource }

// TrayIcon returns the resource used for the system tray icon. It
// currently returns the same resource as AppIcon, but is kept as a
// separate accessor so the tray and window icons can diverge later
// without touching every call site.
func TrayIcon() fyne.Resource { return AppIconResource }
