package ui

import (
	"fyne.io/fyne/v2"

	"git.skobk.in/skobkin/yave/internal/profiles"
)

const (
	defaultWindowWidth  = 1050
	defaultWindowHeight = 760
	minWindowWidth      = 640
	minWindowHeight     = 480
	maxWindowDimension  = 16384
)

// initialWindowSize returns the remembered size when it is sane, else the
// default. A hand-edited or stale config (zero, tiny, absurd values) must
// never produce an unusable window.
func initialWindowSize(width, height int) fyne.Size {
	if width < minWindowWidth || height < minWindowHeight ||
		width > maxWindowDimension || height > maxWindowDimension {
		return fyne.NewSize(defaultWindowWidth, defaultWindowHeight)
	}
	return fyne.NewSize(float32(width), float32(height))
}

// rememberWindowSize stores the current window size in the config. It
// runs when the window is hidden or the application quits; a failed write
// is ignored because there is nothing useful to tell the user at that
// point.
func (c *Controller) rememberWindowSize() {
	if c.window == nil {
		return
	}
	size := c.window.Canvas().Size()
	width, height := int(size.Width), int(size.Height)
	if width < minWindowWidth || height < minWindowHeight ||
		(width == c.config.UI.WindowWidth && height == c.config.UI.WindowHeight) {
		return
	}
	c.config.UI.WindowWidth, c.config.UI.WindowHeight = width, height
	if c.configPath != "" {
		_ = profiles.Save(c.configPath, c.config)
	}
}
