package ui

import (
	"git.skobk.in/skobkin/yave/internal/app"
)

// baseTitle is the human-readable application title. It is kept as a
// package-level constant so the test mirrors the implementation and
// stays honest when the wording changes.
const baseTitle = "YAVE - Yet Another Video Encoder"

// windowTitle returns the human-readable title for the main window,
// including the build version so users can confirm the running release
// at a glance.
func windowTitle() string {
	version := app.BuildVersion()
	if version == app.DevBuildVersion {
		return baseTitle
	}

	return baseTitle + " " + version
}
