package profiles

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"go.yaml.in/yaml/v3"

	"git.skobk.in/skobkin/yave/internal/model"
)

const (
	configDirName  = "yave"
	configFileName = "config.yaml"
)

var (
	// ErrProfileNotFound is returned when a profile lookup or mutation targets a
	// name that does not exist in the configuration.
	ErrProfileNotFound = errors.New("profile not found")
	// ErrDuplicateProfile is returned when a profile name already exists in
	// the configuration.
	ErrDuplicateProfile = errors.New("profile name already exists")
)

// DefaultPath returns the platform-specific YAVE configuration path.
func DefaultPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("find user config directory: %w", err)
	}
	return filepath.Join(dir, configDirName, configFileName), nil
}

// ConfigPath is retained as a descriptive alias for DefaultPath.
func ConfigPath() (string, error) {
	return DefaultPath()
}

// LoadDefault reads the default YAVE configuration file.
func LoadDefault() (model.Config, error) {
	path, err := DefaultPath()
	if err != nil {
		return model.Config{}, err
	}
	return Load(path)
}

// Load reads a configuration file, applying defaults for omitted global
// values. A missing file returns the built-in configuration.
func Load(path string) (model.Config, error) {
	if strings.TrimSpace(path) == "" {
		return model.Config{}, errors.New("config path is empty")
	}
	cfg := model.DefaultConfig()

	data, err := os.ReadFile(path) //nolint:gosec // G304: path comes from user-controlled config, expected behaviour
	if errors.Is(err, os.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return model.Config{}, fmt.Errorf("read config %q: %w", path, err)
	}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return model.Config{}, fmt.Errorf("decode config %q: %w", path, err)
	}
	if err := Normalize(&cfg); err != nil {
		return model.Config{}, fmt.Errorf("normalize config %q: %w", path, err)
	}
	return cfg, nil
}

// LoadFile is retained as an alias for Load.
func LoadFile(path string) (model.Config, error) {
	return Load(path)
}

// SaveDefault validates and atomically writes the default configuration file.
func SaveDefault(cfg model.Config) error {
	path, err := DefaultPath()
	if err != nil {
		return err
	}
	return Save(path, cfg)
}

// Save validates and atomically writes a human-readable YAML file.
func Save(path string, cfg model.Config) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("config path is empty")
	}
	if err := Normalize(&cfg); err != nil {
		return err
	}

	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("encode config: %w", err)
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create config directory %q: %w", dir, err)
	}

	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temporary config: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()

	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("set temporary config permissions: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temporary config: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync temporary config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temporary config: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("replace config %q: %w", path, err)
	}

	// Best effort: the file is already safely replaced even on platforms that
	// do not support syncing directories.
	if d, err := os.Open(dir); err == nil { //nolint:gosec // G304: dir comes from filepath.Dir of config path
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

// SaveFile is retained as an alias for Save.
func SaveFile(path string, cfg model.Config) error {
	return Save(path, cfg)
}

// Normalize fills required global defaults and validates profile identity.
func Normalize(cfg *model.Config) error {
	if cfg == nil {
		return errors.New("nil config")
	}

	defaults := model.DefaultConfig()
	if cfg.Version == 0 {
		cfg.Version = model.ConfigVersion
	}
	if len(cfg.Profiles) == 0 {
		cfg.Profiles = defaults.Profiles
	}
	if strings.TrimSpace(cfg.FFmpeg.FFmpegPath) == "" {
		cfg.FFmpeg.FFmpegPath = defaults.FFmpeg.FFmpegPath
	}
	if strings.TrimSpace(cfg.FFmpeg.FFprobePath) == "" {
		cfg.FFmpeg.FFprobePath = defaults.FFmpeg.FFprobePath
	}

	if err := Validate(cfg); err != nil {
		return err
	}
	if _, ok := Find(cfg.Profiles, cfg.DefaultProfile); !ok {
		cfg.DefaultProfile = cfg.Profiles[0].Name
	}
	return nil
}

// Validate enforces non-empty, unique profile names.
func Validate(cfg *model.Config) error {
	if cfg == nil {
		return errors.New("nil config")
	}
	seen := make(map[string]struct{}, len(cfg.Profiles))
	for i := range cfg.Profiles {
		name := strings.TrimSpace(cfg.Profiles[i].Name)
		if name == "" {
			return fmt.Errorf("profile %d has an empty name", i+1)
		}
		if _, exists := seen[name]; exists {
			return fmt.Errorf("%w: %q", ErrDuplicateProfile, name)
		}
		cfg.Profiles[i].Name = name
		seen[name] = struct{}{}
	}
	return nil
}

// Find returns a profile by its exact display name.
func Find(items []model.Profile, name string) (model.Profile, bool) {
	for _, profile := range items {
		if profile.Name == name {
			return profile, true
		}
	}
	return model.Profile{}, false
}

// Add appends a profile after enforcing name uniqueness.
func Add(cfg *model.Config, profile model.Profile) error {
	if cfg == nil {
		return errors.New("nil config")
	}
	profile.Name = strings.TrimSpace(profile.Name)
	if profile.Name == "" {
		return errors.New("profile name is empty")
	}
	if _, exists := Find(cfg.Profiles, profile.Name); exists {
		return fmt.Errorf("%w: %q", ErrDuplicateProfile, profile.Name)
	}
	cfg.Profiles = append(cfg.Profiles, profile)
	if cfg.DefaultProfile == "" {
		cfg.DefaultProfile = profile.Name
	}
	return nil
}

// Rename changes a profile name and keeps the default-profile reference valid.
func Rename(cfg *model.Config, oldName, newName string) error {
	if cfg == nil {
		return errors.New("nil config")
	}
	newName = strings.TrimSpace(newName)
	if newName == "" {
		return errors.New("profile name is empty")
	}
	if oldName != newName {
		if _, exists := Find(cfg.Profiles, newName); exists {
			return fmt.Errorf("%w: %q", ErrDuplicateProfile, newName)
		}
	}
	for i := range cfg.Profiles {
		if cfg.Profiles[i].Name == oldName {
			cfg.Profiles[i].Name = newName
			if cfg.DefaultProfile == oldName {
				cfg.DefaultProfile = newName
			}
			return nil
		}
	}
	return fmt.Errorf("%w: %q", ErrProfileNotFound, oldName)
}

// Delete removes a profile. The last profile cannot be deleted.
func Delete(cfg *model.Config, name string) error {
	if cfg == nil {
		return errors.New("nil config")
	}
	if len(cfg.Profiles) <= 1 {
		return errors.New("cannot delete the last profile")
	}
	for i := range cfg.Profiles {
		if cfg.Profiles[i].Name != name {
			continue
		}
		cfg.Profiles = append(cfg.Profiles[:i], cfg.Profiles[i+1:]...)
		if cfg.DefaultProfile == name {
			cfg.DefaultProfile = cfg.Profiles[0].Name
		}
		return nil
	}
	return fmt.Errorf("%w: %q", ErrProfileNotFound, name)
}
