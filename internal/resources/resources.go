// Package resources embeds the application icon assets and exposes them
// as Fyne resources so they can be assigned to the app, the main window,
// and the system tray without touching the filesystem at runtime.
package resources

import (
	"bytes"
	_ "embed"
	"fmt"
	"math"

	"fyne.io/fyne/v2"
)

const (
	trayProgressSteps  = 50
	trayProgressTop    = 40.0
	trayProgressBottom = 360.0
	trayProgressMarker = "<!-- yave-tray-progress -->"
)

type trayProgressRect struct {
	x, y, width, height int
}

// trayProgressRects describes the visible white and gray Y in app_icon.svg,
// after applying the source SVG group's translate(-48, -208) transform.
var trayProgressRects = []trayProgressRect{
	{x: 48, y: 40, width: 32, height: 32},
	{x: 96, y: 40, width: 32, height: 32},
	{x: 288, y: 40, width: 32, height: 32},
	{x: 336, y: 40, width: 32, height: 32},
	{x: 48, y: 88, width: 32, height: 32},
	{x: 96, y: 88, width: 32, height: 32},
	{x: 144, y: 88, width: 32, height: 32},
	{x: 240, y: 88, width: 32, height: 32},
	{x: 288, y: 88, width: 32, height: 32},
	{x: 336, y: 88, width: 32, height: 32},
	{x: 96, y: 136, width: 32, height: 32},
	{x: 144, y: 136, width: 32, height: 32},
	{x: 192, y: 136, width: 32, height: 32},
	{x: 240, y: 136, width: 32, height: 32},
	{x: 288, y: 136, width: 32, height: 32},
	{x: 144, y: 184, width: 32, height: 32},
	{x: 192, y: 184, width: 32, height: 32},
	{x: 240, y: 184, width: 32, height: 32},
	{x: 176, y: 232, width: 64, height: 32},
	{x: 176, y: 280, width: 64, height: 32},
	{x: 176, y: 328, width: 64, height: 32},
}

//go:embed assets/app_icon.svg
var appIconSVG []byte

// AppIconResource is the raw Fyne resource for the application icon.
// It is exported so that theme-aware code (a future light variant or a
// runtime theme switcher) can wrap it without going through the
// AppIcon()/TrayIcon() helpers.
var AppIconResource = fyne.NewStaticResource("app_icon.svg", appIconSVG)

var trayProgressIcons = buildTrayProgressIcons()

// AppIcon returns the resource used for app-level and window-level icons
// (app switcher, taskbar, dock, and window title bar).
func AppIcon() fyne.Resource { return AppIconResource }

// TrayIcon returns the resource used for the system tray icon. It
// currently returns the same resource as AppIcon, but is kept as a
// separate accessor so the tray and window icons can diverge later
// without touching every call site.
func TrayIcon() fyne.Resource { return AppIconResource }

// TrayProgressIcon returns a tray icon whose Y is filled from bottom to top
// according to progress. Progress is clamped to [0, 1] and rounded down to a
// two-percent step so the tray never displays more work than has completed.
func TrayProgressIcon(progress float64) fyne.Resource {
	return trayProgressIcons[trayProgressBucket(progress)]
}

func buildTrayProgressIcons() [trayProgressSteps + 1]fyne.Resource {
	if !bytes.Contains(appIconSVG, []byte(trayProgressMarker)) {
		panic("resources: tray progress marker missing from app icon")
	}

	var icons [trayProgressSteps + 1]fyne.Resource
	for bucket := range icons {
		fraction := float64(bucket) / trayProgressSteps
		overlay := trayProgressOverlay(fraction)
		content := bytes.Replace(appIconSVG, []byte(trayProgressMarker), overlay, 1)
		icons[bucket] = fyne.NewStaticResource(
			fmt.Sprintf("app_icon_tray_progress_%03d.svg", bucket*2),
			content,
		)
	}
	return icons
}

func trayProgressOverlay(fraction float64) []byte {
	boundary := trayProgressBottom - fraction*(trayProgressBottom-trayProgressTop)
	var overlay []byte
	for _, rect := range trayProgressRects {
		bottom := float64(rect.y + rect.height)
		fillTop := max(float64(rect.y), boundary)
		if fillTop >= bottom {
			continue
		}
		overlay = fmt.Appendf(overlay,
			`<rect x="%d" y="%.1f" width="%d" height="%.1f" fill="#22C55E" />`,
			rect.x, fillTop, rect.width, bottom-fillTop,
		)
	}
	return overlay
}

func trayProgressBucket(progress float64) int {
	if math.IsNaN(progress) || progress <= 0 {
		return 0
	}
	if progress >= 1 {
		return trayProgressSteps
	}
	return int(math.Floor(progress * trayProgressSteps))
}
