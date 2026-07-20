package ui

import (
	"fyne.io/fyne/v2"

	"git.skobk.in/skobkin/yave/internal/resources"
)

type trayProgressIndicator struct {
	setIcon     func(fyne.Resource)
	currentName string
}

func newTrayProgressIndicator(setIcon func(fyne.Resource)) *trayProgressIndicator {
	return &trayProgressIndicator{
		setIcon:     setIcon,
		currentName: resources.TrayIcon().Name(),
	}
}

func (i *trayProgressIndicator) SetProgress(progress float64) {
	icon := resources.TrayProgressIcon(progress)
	if icon.Name() == i.currentName {
		return
	}
	i.setIcon(icon)
	i.currentName = icon.Name()
}

func (i *trayProgressIndicator) Reset() {
	icon := resources.TrayIcon()
	if icon.Name() == i.currentName {
		return
	}
	i.setIcon(icon)
	i.currentName = icon.Name()
}

func (c *Controller) setTrayProgress(progress float64) {
	if c.trayProgress != nil {
		c.trayProgress.SetProgress(progress)
	}
}

func (c *Controller) resetTrayProgress() {
	if c.trayProgress != nil {
		c.trayProgress.Reset()
	}
}
