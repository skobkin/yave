package resources

import (
	"bytes"
	"io/fs"
	"strings"
	"testing"
)

// allIcons lists every exported icon name; keep it in sync with the
// constants in icons.go.
var allIcons = []string{
	IconZoomIn, IconZoomOut, IconFit, IconZoomToSelection, IconPrevMarker, IconNextMarker,
	IconPlay, IconPause, IconPrevSegment, IconNextSegment, IconPlaySelection,
	IconVolumeHigh, IconVolumeLow, IconVolumeOff,
}

func TestIconsLoad(t *testing.T) {
	for _, name := range allIcons {
		res := Icon(name)
		if res == nil || res.Name() == "" {
			t.Errorf("icon %q did not load", name)
			continue
		}
		if !bytes.Contains(res.Content(), []byte("<svg")) {
			t.Errorf("icon %q is not an SVG", name)
		}
		if Icon(name) != res {
			t.Errorf("icon %q is not cached", name)
		}
	}
}

// TestEveryEmbeddedIconHasAConstant keeps the file set and the exported
// names from drifting apart.
func TestEveryEmbeddedIconHasAConstant(t *testing.T) {
	known := map[string]bool{}
	for _, name := range allIcons {
		known[name] = true
	}
	entries, err := fs.ReadDir(iconFS, "icons")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		name := strings.TrimSuffix(e.Name(), ".svg")
		if !known[name] {
			t.Errorf("embedded icon %q has no exported constant / test entry", e.Name())
		}
	}
	if len(entries) != len(allIcons) {
		t.Errorf("embedded %d icons, listed %d", len(entries), len(allIcons))
	}
}

func TestUnknownIconPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("unknown icon must panic")
		}
	}()
	Icon("does_not_exist")
}
