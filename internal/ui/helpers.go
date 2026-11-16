package ui

import (
	"strconv"
	"strings"

	"fyne.io/fyne/v2"

	"git.skobk.in/skobkin/yave/internal/model"
)

func (c *Controller) dispatchUI(fn func()) {
	if c.uiDispatch != nil {
		c.uiDispatch(fn)
		return
	}
	fyne.Do(fn)
}

// firstNonEmpty returns the first value with non-whitespace content,
// or the empty string when there is none.
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func atoi(value string) int {
	n, _ := strconv.Atoi(strings.TrimSpace(value))
	return n
}

func intText(value int) string {
	if value == 0 {
		return ""
	}
	return strconv.Itoa(value)
}

func dimensions(p model.Profile, media model.MediaInfo) (int, int) {
	width, height := p.Video.MaxWidth, p.Video.MaxHeight
	if source, ok := media.PrimaryVideo(); ok {
		if width <= 0 {
			width = source.Width
		}
		if height <= 0 {
			height = source.Height
		}
	}
	return width, height
}
