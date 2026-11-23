package ui

import (
	"path/filepath"
	"testing"

	fynetest "fyne.io/fyne/v2/test"
)

func dirtyTestController(t *testing.T) *Controller {
	t.Helper()
	c, _, _ := exportTestController(t)
	c.window = fynetest.NewWindow(nil)
	c.configPath = filepath.Join(t.TempDir(), "config.yaml")
	c.loadProfile(c.config.Profiles[0])
	return c
}

func TestProfileDirtyTracking(t *testing.T) {
	c := dirtyTestController(t)
	if c.profileDirty() {
		t.Fatal("a freshly loaded profile must not look edited")
	}
	c.bitrate.SetText("5M")
	if !c.profileDirty() {
		t.Fatal("editing a control must mark the profile dirty")
	}
	if c.profileLabel != nil && c.profileLabel.Text != "Profile *:" {
		t.Fatalf("label = %q, want the unsaved marker", c.profileLabel.Text)
	}
	c.saveCurrentProfile()
	if c.profileDirty() {
		t.Fatal("saving must clear the dirty mark")
	}
	if got := c.config.Profiles[0].Video.Bitrate; got != "5M" {
		t.Fatalf("saved bitrate = %q", got)
	}
}

// Rename used to reload the saved copy through the dropdown callback and
// drop unsaved edits.
func TestRenameKeepsUnsavedEdits(t *testing.T) {
	c := dirtyTestController(t)
	c.bitrate.SetText("7M")
	c.renameProfile("renamed")
	if c.profile.Selected != "renamed" {
		t.Fatalf("selected = %q", c.profile.Selected)
	}
	if c.bitrate.Text != "7M" {
		t.Fatalf("bitrate = %q: rename discarded the edit", c.bitrate.Text)
	}
	if !c.profileDirty() {
		t.Fatal("the edit is still unsaved after rename")
	}
}

func TestSwitchProfileWithEditsKeepsSelectionUntilConfirmed(t *testing.T) {
	c := dirtyTestController(t)
	other := c.config.Profiles[0]
	other.Name = "other"
	c.config.Profiles = append(c.config.Profiles, other)
	c.profile.Options = c.profileNames()
	c.bitrate.SetText("9M")

	c.switchProfile("other")
	if c.profile.Selected != "test" {
		t.Fatalf("selected = %q: a dirty switch must snap back and prompt", c.profile.Selected)
	}
	if c.bitrate.Text != "9M" {
		t.Fatal("edits were discarded without confirmation")
	}
}
