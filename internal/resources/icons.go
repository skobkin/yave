package resources

import (
	"embed"
	"fmt"
	"sync"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/theme"
)

// Names of the embedded toolbar icons (file names without extension).
// Material Symbols, Apache-2.0; see icons/NOTICE.
const (
	IconZoomIn          = "zoom_in"
	IconZoomOut         = "zoom_out"
	IconFit             = "fit_screen"
	IconZoomToSelection = "zoom_in_map"
)

//go:embed icons/*.svg
var iconFS embed.FS

var (
	iconMu    sync.Mutex
	iconCache = map[string]fyne.Resource{}
)

// Icon returns the named embedded SVG as a theme-aware resource: Fyne
// recolors it with the foreground color of the active theme variant (and
// the disabled color on disabled buttons), so it follows light and dark
// themes. An unknown name panics — every name is a compile-time constant
// above and covered by TestIconsLoad, so a miss is a programming error.
func Icon(name string) fyne.Resource {
	iconMu.Lock()
	defer iconMu.Unlock()
	if res, ok := iconCache[name]; ok {
		return res
	}
	data, err := iconFS.ReadFile("icons/" + name + ".svg")
	if err != nil {
		panic(fmt.Sprintf("resources: embedded icon %q missing: %v", name, err))
	}
	res := theme.NewThemedResource(fyne.NewStaticResource(name+".svg", data))
	iconCache[name] = res
	return res
}
