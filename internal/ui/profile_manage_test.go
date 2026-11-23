package ui

import (
	"strings"
	"testing"

	"fyne.io/fyne/v2/widget"

	"git.skobk.in/skobkin/yave/internal/model"
)

func TestSanitizeFileName(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{
		"HEVC VAAPI 720p": "HEVC VAAPI 720p",
		"a/b\\c:d":        "a_b_c_d",
		"   ":             "profile",
		"x*y?":            "x_y_",
	} {
		if got := sanitizeFileName(in); got != want {
			t.Errorf("sanitizeFileName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSaveAndSetDefaultGiveFeedbackAndMarker(t *testing.T) {
	c := dirtyTestController(t)
	c.defaultMarker = newHiddenLabel()
	c.config.DefaultProfile = "someone else"
	c.updateDefaultMarker()
	if c.defaultMarker.Visible() {
		t.Fatal("marker must be hidden when the selected profile is not the default")
	}
	c.setDefault()
	if !c.defaultMarker.Visible() || !strings.Contains(c.status.Text, "set as default") {
		t.Fatalf("after setDefault: marker=%v status=%q", c.defaultMarker.Visible(), c.status.Text)
	}
	c.saveCurrentProfile()
	if !strings.Contains(c.status.Text, "saved") {
		t.Fatalf("status = %q, want save feedback", c.status.Text)
	}
}

func TestRestoreBuiltinsAddsMissingProfiles(t *testing.T) {
	c := dirtyTestController(t)
	before := len(c.config.Profiles)
	c.restoreBuiltins()
	if got := len(c.config.Profiles); got != before+len(model.BuiltinProfiles()) {
		t.Fatalf("profiles = %d, want %d", got, before+len(model.BuiltinProfiles()))
	}
	if !strings.Contains(c.status.Text, "restored") {
		t.Fatalf("status = %q", c.status.Text)
	}
	if c.config.Profiles[0].Name != "test" {
		t.Fatal("the user's profile must keep its place")
	}
}

func TestImportedProfileIsAddedPersistedAndSelected(t *testing.T) {
	c := dirtyTestController(t)
	p := c.config.Profiles[0]
	p.Name = "imported"
	c.addImported(p)
	if c.profile.Selected != "imported" || !c.profileNameTaken("imported") {
		t.Fatalf("selected = %q", c.profile.Selected)
	}
	if !strings.Contains(c.status.Text, "imported") {
		t.Fatalf("status = %q", c.status.Text)
	}
}

func newHiddenLabel() *widget.Label {
	l := widget.NewLabel("★ default")
	l.Hide()
	return l
}
