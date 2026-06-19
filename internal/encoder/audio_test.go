package encoder

import (
	"slices"
	"testing"

	"git.skobk.in/skobkin/yave/internal/model"
)

// findAudio returns the AudioEncoder for name, failing the test if it
// is not registered. Using the registry keeps the tests honest: if a
// future refactor forgets to register a family member, the test fails.
func findAudio(t *testing.T, name string) AudioEncoder {
	t.Helper()
	e, ok := DefaultRegistry().Get(name)
	if !ok {
		t.Fatalf("audio encoder %q is not registered", name)
	}
	ae, ok := e.(AudioEncoder)
	if !ok {
		t.Fatalf("%q is registered but does not implement AudioEncoder", name)
	}
	return ae
}

func TestAudioEncodersAreRegistered(t *testing.T) {
	// Sorted by Priority desc, then Name asc:
	// aac(100) libopus(100) opus(90) flac(70) libmp3lame(70) mp3(60)
	want := []string{"aac", "libopus", "opus", "flac", "libmp3lame", "mp3"}
	got := DefaultRegistry().AudioNames()
	if !slices.Equal(got, want) {
		t.Errorf("AudioNames() = %v, want %v", got, want)
	}
}

func TestAudioEncoderMetadata(t *testing.T) {
	tests := []struct {
		name     string
		media    string
		kind     string
		family   string
		hw       HWBackend
		priority int
	}{
		{name: "libopus", media: MediaAudio, kind: KindEncode, family: "opus", hw: HWNone, priority: 100},
		{name: "opus", media: MediaAudio, kind: KindEncode, family: "opus", hw: HWNone, priority: 90},
		{name: "aac", media: MediaAudio, kind: KindEncode, family: "aac", hw: HWNone, priority: 100},
		{name: "libmp3lame", media: MediaAudio, kind: KindEncode, family: "mp3", hw: HWNone, priority: 70},
		{name: "mp3", media: MediaAudio, kind: KindEncode, family: "mp3", hw: HWNone, priority: 60},
		{name: "flac", media: MediaAudio, kind: KindEncode, family: "flac", hw: HWNone, priority: 70},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ae := findAudio(t, tt.name)
			if ae.Media() != tt.media {
				t.Errorf("Media() = %q, want %q", ae.Media(), tt.media)
			}
			if ae.Kind() != tt.kind {
				t.Errorf("Kind() = %q, want %q", ae.Kind(), tt.kind)
			}
			if ae.CodecFamily() != tt.family {
				t.Errorf("CodecFamily() = %q, want %q", ae.CodecFamily(), tt.family)
			}
			if ae.HWBackend() != tt.hw {
				t.Errorf("HWBackend() = %v, want %v", ae.HWBackend(), tt.hw)
			}
			if ae.Priority() != tt.priority {
				t.Errorf("Priority() = %d, want %d", ae.Priority(), tt.priority)
			}
		})
	}
}

func TestAudioEncoderBuildArgs(t *testing.T) {
	tests := []struct {
		name     string
		audio    model.AudioSettings
		wantArgs []string
	}{
		{
			name:     "libopus with bitrate",
			audio:    model.AudioSettings{Encoder: "libopus", Bitrate: "128k"},
			wantArgs: []string{"-c:a", "libopus", "-b:a", "128k"},
		},
		{
			name:     "libopus without bitrate",
			audio:    model.AudioSettings{Encoder: "libopus"},
			wantArgs: []string{"-c:a", "libopus"},
		},
		{
			name:     "opus with bitrate",
			audio:    model.AudioSettings{Encoder: "opus", Bitrate: "96k"},
			wantArgs: []string{"-c:a", "opus", "-b:a", "96k"},
		},
		{
			name:     "aac with bitrate",
			audio:    model.AudioSettings{Encoder: "aac", Bitrate: "192k"},
			wantArgs: []string{"-c:a", "aac", "-b:a", "192k"},
		},
		{
			name:     "libmp3lame with bitrate",
			audio:    model.AudioSettings{Encoder: "libmp3lame", Bitrate: "320k"},
			wantArgs: []string{"-c:a", "libmp3lame", "-b:a", "320k"},
		},
		{
			name:     "mp3 with bitrate",
			audio:    model.AudioSettings{Encoder: "mp3", Bitrate: "128k"},
			wantArgs: []string{"-c:a", "mp3", "-b:a", "128k"},
		},
		{
			name:     "flac with compression",
			audio:    model.AudioSettings{Encoder: "flac", Compression: 8},
			wantArgs: []string{"-c:a", "flac", "-compression_level", "8"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ae := findAudio(t, tt.audio.Encoder)
			got, err := ae.BuildAudioArgs(tt.audio)
			if err != nil {
				t.Fatalf("BuildAudioArgs returned error: %v", err)
			}
			if !slices.Equal(got, tt.wantArgs) {
				t.Errorf("BuildAudioArgs = %v, want %v", got, tt.wantArgs)
			}
		})
	}
}

func TestAudioEncoderValidate(t *testing.T) {
	tests := []struct {
		name      string
		audio     model.AudioSettings
		wantIssue string // empty string -> expect no issues; otherwise expect an issue with this field
	}{
		{name: "libopus 128k accepted", audio: model.AudioSettings{Encoder: "libopus", Bitrate: "128k"}},
		{name: "libopus 96k accepted", audio: model.AudioSettings{Encoder: "libopus", Bitrate: "96k"}},
		{name: "libopus invalid bitrate", audio: model.AudioSettings{Encoder: "libopus", Bitrate: "64k"}, wantIssue: "profile.audio.bitrate"},
		{name: "aac invalid bitrate", audio: model.AudioSettings{Encoder: "aac", Bitrate: "64k"}, wantIssue: "profile.audio.bitrate"},
		{name: "aac 128k accepted", audio: model.AudioSettings{Encoder: "aac", Bitrate: "128k"}},
		{name: "libmp3lame invalid bitrate", audio: model.AudioSettings{Encoder: "libmp3lame", Bitrate: "192k"}}, // 192k IS valid for mp3
		{name: "libmp3lame 128k accepted", audio: model.AudioSettings{Encoder: "libmp3lame", Bitrate: "128k"}},
		{name: "libmp3lame 256k accepted", audio: model.AudioSettings{Encoder: "libmp3lame", Bitrate: "256k"}},
		{name: "mp3 320k accepted", audio: model.AudioSettings{Encoder: "mp3", Bitrate: "320k"}},
		{name: "mp3 invalid bitrate", audio: model.AudioSettings{Encoder: "mp3", Bitrate: "128k"}}, // 128k IS valid for mp3
		{name: "flac compression 8 accepted", audio: model.AudioSettings{Encoder: "flac", Compression: 8}},
		{name: "flac compression 0 accepted", audio: model.AudioSettings{Encoder: "flac", Compression: 0}},
		{name: "flac compression 12 accepted", audio: model.AudioSettings{Encoder: "flac", Compression: 12}},
		{name: "flac compression 13 rejected", audio: model.AudioSettings{Encoder: "flac", Compression: 13}, wantIssue: "profile.audio.compression"},
		{name: "flac compression -1 rejected", audio: model.AudioSettings{Encoder: "flac", Compression: -1}, wantIssue: "profile.audio.compression"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ae := findAudio(t, tt.audio.Encoder)
			issues, err := ae.Validate(tt.audio)
			if err != nil {
				t.Fatalf("Validate returned error: %v", err)
			}
			if tt.wantIssue == "" {
				if len(issues) != 0 {
					t.Errorf("Validate produced %d issues, want 0: %+v", len(issues), issues)
				}
				return
			}
			if len(issues) == 0 {
				t.Fatalf("Validate produced no issues, want one with field %q", tt.wantIssue)
			}
			if issues[0].Field != tt.wantIssue {
				t.Errorf("Validate issue field = %q, want %q", issues[0].Field, tt.wantIssue)
			}
		})
	}
}

func TestAudioEncoderCompressionRange(t *testing.T) {
	if r, ok := findAudio(t, "flac").AudioCompressionRange(); !ok || r.Min != 0 || r.Max != 12 {
		t.Errorf("flac AudioCompressionRange() = %+v, ok=%v, want {0,12} ok", r, ok)
	}
	if _, ok := findAudio(t, "libopus").AudioCompressionRange(); ok {
		t.Errorf("libopus AudioCompressionRange() reported ok=true, want ok=false")
	}
}

func TestAudioEncoderBuildArgsEmptyEncoder(t *testing.T) {
	ae := findAudio(t, "libopus")
	_, err := ae.BuildAudioArgs(model.AudioSettings{Encoder: ""})
	if err == nil {
		t.Errorf("BuildAudioArgs with empty encoder returned no error")
	}
}