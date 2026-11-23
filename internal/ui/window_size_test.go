package ui

import (
	"path/filepath"
	"testing"

	"fyne.io/fyne/v2"

	"git.skobk.in/skobkin/yave/internal/model"
	"git.skobk.in/skobkin/yave/internal/profiles"
)

func TestInitialWindowSizeIgnoresInvalidValues(t *testing.T) {
	t.Parallel()
	def := fyne.NewSize(defaultWindowWidth, defaultWindowHeight)
	tests := []struct {
		name string
		w, h int
		want fyne.Size
	}{
		{"unset", 0, 0, def},
		{"too small", 100, 100, def},
		{"negative", -5, 800, def},
		{"absurd", 100000, 800, def},
		{"valid", 1280, 900, fyne.NewSize(1280, 900)},
		{"minimum", minWindowWidth, minWindowHeight, fyne.NewSize(minWindowWidth, minWindowHeight)},
	}
	for _, tt := range tests {
		if got := initialWindowSize(tt.w, tt.h); got != tt.want {
			t.Errorf("%s: %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestWindowSizeConfigRoundTrip(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	path := filepath.Join(t.TempDir(), "config.yaml")
	cfg := model.DefaultConfig()
	cfg.UI.WindowWidth, cfg.UI.WindowHeight = 1280, 900
	if err := profiles.Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	loaded, err := profiles.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.UI.WindowWidth != 1280 || loaded.UI.WindowHeight != 900 {
		t.Fatalf("window size = %dx%d", loaded.UI.WindowWidth, loaded.UI.WindowHeight)
	}
}

func TestMoreMenuHoldsRareActionsAndUpdateCheck(t *testing.T) {
	c := dirtyTestController(t)
	var labels []string
	for _, item := range c.moreMenu().Items {
		labels = append(labels, item.Label)
	}
	want := []string{"Duplicate profile", "Rename profile", "Delete profile", "Set as default", "", "Settings…", "Check for updates…", "About"}
	if len(labels) != len(want) {
		t.Fatalf("menu = %v", labels)
	}
	for i := range want {
		if labels[i] != want[i] {
			t.Fatalf("menu[%d] = %q, want %q", i, labels[i], want[i])
		}
	}
}
