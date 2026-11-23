package profiles

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode"

	"go.yaml.in/yaml/v3"

	"git.skobk.in/skobkin/yave/internal/model"
)

// maxImportBytes bounds an imported profile file; a profile is a few
// hundred bytes, so anything bigger is not one.
const maxImportBytes = 1 << 20

// maxNameRunes bounds a profile name.
const maxNameRunes = 100

// ExportProfile renders one profile as standalone YAML.
func ExportProfile(p model.Profile) ([]byte, error) {
	data, err := yaml.Marshal(p)
	if err != nil {
		return nil, fmt.Errorf("encode profile %q: %w", p.Name, err)
	}
	return data, nil
}

// ImportProfile parses a standalone profile exported by ExportProfile.
// Unknown keys are rejected so a file that is not a YAVE profile (or comes
// from a newer schema) fails loudly instead of importing as defaults.
func ImportProfile(r io.Reader) (model.Profile, error) {
	data, err := io.ReadAll(io.LimitReader(r, maxImportBytes+1))
	if err != nil {
		return model.Profile{}, fmt.Errorf("read profile: %w", err)
	}
	if len(data) > maxImportBytes {
		return model.Profile{}, errors.New("profile file is too large")
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	var p model.Profile
	if err := decoder.Decode(&p); err != nil {
		return model.Profile{}, fmt.Errorf("decode profile: %w", err)
	}
	p.Name = strings.TrimSpace(p.Name)
	if err := ValidateName(p.Name, nil); err != nil {
		return model.Profile{}, fmt.Errorf("invalid profile: %w", err)
	}
	if strings.TrimSpace(p.Container) == "" || strings.TrimSpace(p.Video.Encoder) == "" {
		return model.Profile{}, errors.New("invalid profile: container and video encoder are required")
	}
	normalizeEnums(&p)
	return p, nil
}

// ValidateName checks a profile name: non-empty, bounded, free of control
// characters, and not taken (taken may be nil).
func ValidateName(name string, taken func(string) bool) error {
	name = strings.TrimSpace(name)
	switch {
	case name == "":
		return errors.New("profile name is empty")
	case len([]rune(name)) > maxNameRunes:
		return fmt.Errorf("profile name is longer than %d characters", maxNameRunes)
	case strings.IndexFunc(name, unicode.IsControl) >= 0:
		return errors.New("profile name contains control characters")
	case taken != nil && taken(name):
		return fmt.Errorf("a profile named %q already exists", name)
	}
	return nil
}

// UniqueName returns base, or "base (2)", "base (3)"… — the first name
// not taken.
func UniqueName(base string, taken func(string) bool) string {
	if !taken(base) {
		return base
	}
	for n := 2; ; n++ {
		if candidate := fmt.Sprintf("%s (%d)", base, n); !taken(candidate) {
			return candidate
		}
	}
}

// BuiltinStatus compares the configuration with model.BuiltinProfiles.
type BuiltinStatus struct {
	Missing  []string // built-ins absent from the configuration
	Modified []string // built-ins present but different from the default
}

// CheckBuiltins reports which built-in profiles are missing or modified.
func CheckBuiltins(cfg model.Config) BuiltinStatus {
	var status BuiltinStatus
	for _, builtin := range model.BuiltinProfiles() {
		current, ok := Find(cfg.Profiles, builtin.Name)
		switch {
		case !ok:
			status.Missing = append(status.Missing, builtin.Name)
		case current != builtin:
			status.Modified = append(status.Modified, builtin.Name)
		}
	}
	return status
}

// RestoreBuiltins adds the missing built-in profiles and, when
// resetModified is set, restores modified ones to their defaults. User
// profiles are never touched.
func RestoreBuiltins(cfg *model.Config, resetModified bool) BuiltinStatus {
	status := CheckBuiltins(*cfg)
	for _, builtin := range model.BuiltinProfiles() {
		switch {
		case contains(status.Missing, builtin.Name):
			cfg.Profiles = append(cfg.Profiles, builtin)
		case resetModified && contains(status.Modified, builtin.Name):
			for i := range cfg.Profiles {
				if cfg.Profiles[i].Name == builtin.Name {
					cfg.Profiles[i] = builtin
				}
			}
		}
	}
	if !resetModified {
		status.Modified = nil
	}
	return status
}

func contains(list []string, s string) bool {
	for _, item := range list {
		if item == s {
			return true
		}
	}
	return false
}
