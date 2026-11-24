package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFailedSaveKeepsProfileDirty(t *testing.T) {
	c := dirtyTestController(t)
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	c.configPath = filepath.Join(blocker, "config.yaml") // parent is a file
	c.bitrate.SetText("5M")
	if c.persistCurrentProfile() {
		t.Fatal("persist must report failure")
	}
	if !c.profileDirty() {
		t.Fatal("a failed write must keep the unsaved state")
	}
	if strings.Contains(c.status.Text, "saved") {
		t.Fatalf("status %q claims success", c.status.Text)
	}
}
