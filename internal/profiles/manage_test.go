package profiles

import (
	"strings"
	"testing"

	"git.skobk.in/skobkin/yave/internal/model"
)

func TestExportImportRoundTrip(t *testing.T) {
	for _, builtin := range model.BuiltinProfiles() {
		data, err := ExportProfile(builtin)
		if err != nil {
			t.Fatal(err)
		}
		got, err := ImportProfile(strings.NewReader(string(data)))
		if err != nil {
			t.Fatalf("%s: %v", builtin.Name, err)
		}
		if got != builtin {
			t.Fatalf("%s: round trip changed the profile:\n got %#v\nwant %#v", builtin.Name, got, builtin)
		}
	}
}

func TestImportProfileRejectsBadInput(t *testing.T) {
	tests := []struct {
		name string
		data string
		want string
	}{
		{"not yaml", "name: [unclosed", "decode profile"},
		{"unknown key", "name: x\ncontainer: mkv\nvideo: {encoder: libx264}\nbogus: 1\n", "decode profile"},
		{"no name", "container: mkv\nvideo: {encoder: libx264}\n", "empty"},
		{"no container", "name: x\nvideo: {encoder: libx264}\n", "required"},
		{"no encoder", "name: x\ncontainer: mkv\n", "required"},
		{"control chars in name", "name: \"a\\tb\"\ncontainer: mkv\nvideo: {encoder: libx264}\n", "control"},
		{"empty file", "", "decode profile"},
	}
	for _, tt := range tests {
		_, err := ImportProfile(strings.NewReader(tt.data))
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s: err = %v, want containing %q", tt.name, err, tt.want)
		}
	}
	if _, err := ImportProfile(strings.NewReader(strings.Repeat("a", maxImportBytes+10))); err == nil {
		t.Error("oversized file must be rejected")
	}
}

func TestImportNormalizesEnums(t *testing.T) {
	p, err := ImportProfile(strings.NewReader("name: x\ncontainer: MKV\nvideo: {encoder: Libx264}\n"))
	if err != nil || p.Container != "mkv" || p.Video.Encoder != "libx264" {
		t.Fatalf("got %#v, %v", p, err)
	}
}

func TestValidateNameAndUniqueName(t *testing.T) {
	taken := func(s string) bool { return s == "a" || s == "a (2)" }
	for _, tt := range []struct {
		name    string
		wantErr bool
	}{{"b", false}, {" b ", false}, {"", true}, {"   ", true}, {"a", true}, {"x\ny", true}, {strings.Repeat("é", 101), true}} {
		if err := ValidateName(tt.name, taken); (err != nil) != tt.wantErr {
			t.Errorf("ValidateName(%q) err = %v, wantErr %v", tt.name, err, tt.wantErr)
		}
	}
	if got := UniqueName("a", taken); got != "a (3)" {
		t.Errorf("UniqueName = %q", got)
	}
	if got := UniqueName("fresh", taken); got != "fresh" {
		t.Errorf("UniqueName = %q", got)
	}
}

func TestRestoreBuiltins(t *testing.T) {
	builtins := model.BuiltinProfiles()
	user := model.Profile{Name: "mine", Container: "mkv", Video: model.VideoSettings{Encoder: "libx264"}}

	t.Run("adds missing without touching user or modified profiles", func(t *testing.T) {
		modified := builtins[0]
		modified.Video.Bitrate = "1M"
		cfg := model.Config{Profiles: []model.Profile{user, modified}}
		status := RestoreBuiltins(&cfg, false)
		if len(status.Missing) != len(builtins)-1 || status.Modified != nil {
			t.Fatalf("status = %#v", status)
		}
		if len(cfg.Profiles) != 1+len(builtins) {
			t.Fatalf("profiles = %d", len(cfg.Profiles))
		}
		if got, _ := Find(cfg.Profiles, modified.Name); got != modified {
			t.Fatal("the modified built-in must be kept when not resetting")
		}
		if got, _ := Find(cfg.Profiles, "mine"); got != user {
			t.Fatal("user profile changed")
		}
	})
	t.Run("reset restores modified built-ins", func(t *testing.T) {
		modified := builtins[0]
		modified.Video.Bitrate = "1M"
		cfg := model.Config{Profiles: []model.Profile{user, modified}}
		status := RestoreBuiltins(&cfg, true)
		if len(status.Modified) != 1 {
			t.Fatalf("status = %#v", status)
		}
		if got, _ := Find(cfg.Profiles, modified.Name); got != builtins[0] {
			t.Fatal("built-in not reset")
		}
	})
	t.Run("nothing to do", func(t *testing.T) {
		cfg := model.Config{Profiles: builtins}
		if status := CheckBuiltins(cfg); len(status.Missing)+len(status.Modified) != 0 {
			t.Fatalf("status = %#v", status)
		}
	})
}
