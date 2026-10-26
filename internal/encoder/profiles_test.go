package encoder

import (
	"slices"
	"testing"

	"git.skobk.in/skobkin/yave/internal/model"
)

// TestVideoEncoderProfiles pins the curated -profile:v list per video
// encoder. The lists are verified against reference ffmpeg builds and
// restricted to values that can encode through each encoder's fixed
// pixel-format chain (see the encoder constructors: hardware chains
// upload nv12, so 10-bit / non-4:2:0 profiles are excluded even when
// the build enumerates them); an accidental change here changes what
// the UI offers and what validation accepts, so it must be deliberate.
func TestVideoEncoderProfiles(t *testing.T) {
	tests := []struct {
		name     string
		profiles []string
	}{
		{"h264_vaapi", []string{"constrained_baseline", "main", "high"}},
		{"hevc_vaapi", []string{"main", "rext"}},
		{"av1_vaapi", []string{"main"}},
		{"vp9_vaapi", []string{"0"}},
		{"h264_qsv", []string{"baseline", "main", "high"}},
		{"hevc_qsv", []string{"main"}},
		{"av1_qsv", []string{"main"}},
		{"vp9_qsv", []string{"profile0"}},
		{"h264_nvenc", []string{"baseline", "main", "high"}},
		{"hevc_nvenc", []string{"main"}},
		{"av1_nvenc", nil},
		{"libx264", []string{"baseline", "main", "high"}},
		{"libx265", []string{"main", "main10", "main12"}},
		{"libsvtav1", []string{"main"}},
		{"libaom-av1", []string{"main"}},
		{"libvpx-vp9", nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e, ok := DefaultRegistry().Get(tt.name)
			if !ok {
				t.Fatalf("%q is not registered", tt.name)
			}
			ve, ok := e.(VideoEncoder)
			if !ok {
				t.Fatalf("%q does not implement VideoEncoder", tt.name)
			}
			if got := ve.Profiles(); !slices.Equal(got, tt.profiles) {
				t.Errorf("Profiles() = %v, want %v", got, tt.profiles)
			}
		})
	}
}

// TestValidateRejectsUnknownProfile covers the shared profileIssues
// behaviour through each family: out-of-list values are rejected,
// curated values pass (case-insensitively), and encoders that do not
// support -profile:v at all reject any non-empty value.
func TestValidateRejectsUnknownProfile(t *testing.T) {
	tests := []struct {
		name    string
		encoder string
		profile string
		wantErr bool
	}{
		{"hevc_vaapi main", "hevc_vaapi", "main", false},
		{"hevc_vaapi MAIN is case-insensitive", "hevc_vaapi", "MAIN", false},
		{"hevc_vaapi bogus", "hevc_vaapi", "main444", true},
		{"h264_vaapi constrained_baseline", "h264_vaapi", "constrained_baseline", false},
		{"vp9_vaapi 0", "vp9_vaapi", "0", false},
		{"vp9_vaapi main is not an int", "vp9_vaapi", "main", true},
		{"h264_nvenc high", "h264_nvenc", "high", false},
		{"hevc_nvenc mv is deliberately uncurated", "hevc_nvenc", "mv", true},
		{"av1_nvenc rejects any profile", "av1_nvenc", "main", true},
		{"libx264 High case-insensitive", "libx264", "High", false},
		{"libx265 main10", "libx265", "main10", false},
		{"libsvtav1 high rejected", "libsvtav1", "high", true},
		{"libaom-av1 main", "libaom-av1", "main", false},
		{"libvpx-vp9 rejects any profile", "libvpx-vp9", "main", true},
		{"empty profile is always fine", "hevc_vaapi", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e, ok := DefaultRegistry().Get(tt.encoder)
			if !ok {
				t.Fatalf("%q is not registered", tt.encoder)
			}
			ve, ok := e.(VideoEncoder)
			if !ok {
				t.Fatalf("%q does not implement VideoEncoder", tt.encoder)
			}
			issues, err := ve.Validate(model.VideoSettings{Encoder: tt.encoder, Profile: tt.profile})
			if err != nil {
				t.Fatalf("Validate returned error: %v", err)
			}
			gotErr := len(issues) > 0
			if gotErr != tt.wantErr {
				t.Errorf("Validate(profile=%q) issues=%v, wantErr=%v", tt.profile, issues, tt.wantErr)
			}
		})
	}
}

// TestAudioBitrateChoices pins the bitrate choice list per audio
// encoder; the validation refactor and the UI dropdown both read them.
func TestAudioBitrateChoices(t *testing.T) {
	tests := []struct {
		name    string
		choices []string
	}{
		{"libopus", []string{"96k", "128k", "160k", "192k", "256k"}},
		{"opus", []string{"96k", "128k", "160k", "192k", "256k"}},
		{"aac", []string{"128k", "160k", "192k", "256k"}},
		{"libmp3lame", []string{"128k", "192k", "256k", "320k"}},
		{"mp3", []string{"128k", "192k", "256k", "320k"}},
		{"flac", nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e, ok := DefaultRegistry().Get(tt.name)
			if !ok {
				t.Fatalf("%q is not registered", tt.name)
			}
			ae, ok := e.(AudioEncoder)
			if !ok {
				t.Fatalf("%q does not implement AudioEncoder", tt.name)
			}
			if got := ae.BitrateChoices(); !slices.Equal(got, tt.choices) {
				t.Errorf("BitrateChoices() = %v, want %v", got, tt.choices)
			}
		})
	}
}
