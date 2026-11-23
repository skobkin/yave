package ui

import (
	"path/filepath"
	"slices"
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/storage"

	"git.skobk.in/skobkin/yave/internal/profiles"
)

func TestAddRecent(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		list  []string
		add   string
		limit int
		want  []string
	}{
		{"empty", nil, "a", 3, []string{"a"}},
		{"newest first", []string{"a", "b"}, "c", 3, []string{"c", "a", "b"}},
		{"dedupe moves to front", []string{"a", "b", "c"}, "b", 3, []string{"b", "a", "c"}},
		{"limit drops the oldest", []string{"a", "b", "c"}, "d", 3, []string{"d", "a", "b"}},
	}
	for _, tt := range tests {
		if got := addRecent(tt.list, tt.add, tt.limit); !slices.Equal(got, tt.want) {
			t.Errorf("%s: %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestExistingFilesSkipsMissing(t *testing.T) {
	dir := t.TempDir()
	present := filepath.Join(dir, "a.mkv")
	writeFile(t, present, "x")
	got := existingFiles([]string{filepath.Join(dir, "gone.mkv"), present, dir})
	if !slices.Equal(got, []string{present}) {
		t.Fatalf("existing = %v", got)
	}
}

func TestRememberInputPersists(t *testing.T) {
	c := dirtyTestController(t)
	c.inputPath = "" // no current input: the last folder decides
	dir := t.TempDir()
	path := filepath.Join(dir, "movie.mkv")
	c.rememberInput(path)
	loaded, err := profiles.Load(c.configPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.UI.RecentFiles) != 1 || loaded.UI.RecentFiles[0] != path || loaded.UI.LastInputDir != dir {
		t.Fatalf("persisted ui = %#v", loaded.UI)
	}
	if c.dialogStartDir() != dir {
		t.Fatalf("start dir = %q", c.dialogStartDir())
	}
}

func TestHandleDropWithSeveralFilesLoadsFirst(t *testing.T) {
	c := dirtyTestController(t)
	uris := []fyne.URI{storage.NewFileURI("/tmp/first.mkv"), storage.NewFileURI("/tmp/second.mkv")}
	c.handleDrop(uris)
	if c.inputPath != "/tmp/first.mkv" {
		t.Fatalf("input = %q, want the first dropped file", c.inputPath)
	}
	c.handleDrop(nil) // must not panic or change anything
	if c.inputPath != "/tmp/first.mkv" {
		t.Fatal("an empty drop changed the input")
	}
}

func TestOpenFileDialogSize(t *testing.T) {
	t.Parallel()
	if got := openFileDialogSize(fyne.NewSize(1000, 800)); got != fyne.NewSize(800, 640) {
		t.Fatalf("size = %v", got)
	}
	if got := openFileDialogSize(fyne.NewSize(100, 100)); got != fyne.NewSize(600, 400) {
		t.Fatalf("small window size = %v", got)
	}
}
