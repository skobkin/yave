package profiles

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git.skobk.in/skobkin/yave/internal/model"
)

func TestConfigPath(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	got, err := ConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "yave", "config.yaml"); got != want {
		t.Fatalf("ConfigPath() = %q, want %q", got, want)
	}
}

func TestLoadFileMissingReturnsDefaults(t *testing.T) {
	got, err := LoadFile(filepath.Join(t.TempDir(), "missing.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	want := model.DefaultConfig()
	if got.DefaultProfile != want.DefaultProfile || len(got.Profiles) != len(want.Profiles) {
		t.Fatalf("LoadFile() did not return defaults: %#v", got)
	}
}

func TestLoadFileNormalizesGlobalDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	data := []byte("profiles:\n  - name: custom\n    container: mkv\n")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Version != model.ConfigVersion {
		t.Errorf("Version = %d", got.Version)
	}
	if got.DefaultProfile != "custom" {
		t.Errorf("DefaultProfile = %q", got.DefaultProfile)
	}
	if got.FFmpeg.FFmpegPath != "ffmpeg" || got.FFmpeg.FFprobePath != "ffprobe" {
		t.Errorf("FFmpeg defaults = %#v", got.FFmpeg)
	}
}

// Existing configurations predate the updates section and must keep getting
// the enabled default, not the zero value.
func TestLoadFileUpdatesDefaultEnabled(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	data := []byte("profiles:\n  - name: custom\n    container: mkv\n")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Updates.CheckAutomatically {
		t.Errorf("Updates.CheckAutomatically = false, want the enabled default")
	}
}

func TestSaveFileUpdatesRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	cfg := model.DefaultConfig()
	cfg.Updates.CheckAutomatically = false

	if err := SaveFile(path, cfg); err != nil {
		t.Fatal(err)
	}
	got, err := LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Updates.CheckAutomatically {
		t.Errorf("Updates.CheckAutomatically = true, want false after round trip")
	}
}

func TestSaveFileRoundTripAndPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "config.yaml")
	cfg := model.DefaultConfig()
	cfg.FFmpeg.FFmpegPath = "/opt/ffmpeg"

	if err := SaveFile(path, cfg); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path) //nolint:gosec // G304: path comes from t.TempDir, safe by construction
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "profiles:") || !strings.Contains(string(data), "ffmpeg_path: /opt/ffmpeg") {
		t.Fatalf("saved YAML is not readable:\n%s", data)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("permissions = %o, want 600", info.Mode().Perm())
	}

	got, err := LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.FFmpeg.FFmpegPath != cfg.FFmpeg.FFmpegPath || len(got.Profiles) != len(cfg.Profiles) {
		t.Fatalf("round trip mismatch: %#v", got)
	}
}

func TestDuplicateProfileNamesRejected(t *testing.T) {
	cfg := model.DefaultConfig()
	cfg.Profiles = append(cfg.Profiles, cfg.Profiles[0])

	err := SaveFile(filepath.Join(t.TempDir(), "config.yaml"), cfg)
	if !errors.Is(err, ErrDuplicateProfile) {
		t.Fatalf("SaveFile() error = %v, want ErrDuplicateProfile", err)
	}
}

func TestProfileMutationsKeepDefaultValid(t *testing.T) {
	cfg := model.DefaultConfig()
	oldDefault := cfg.DefaultProfile

	if err := Rename(&cfg, oldDefault, "renamed"); err != nil {
		t.Fatal(err)
	}
	if cfg.DefaultProfile != "renamed" {
		t.Fatalf("default after rename = %q", cfg.DefaultProfile)
	}
	if err := Delete(&cfg, "renamed"); err != nil {
		t.Fatal(err)
	}
	if cfg.DefaultProfile == "renamed" {
		t.Fatal("default still references deleted profile")
	}
}
