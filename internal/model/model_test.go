package model

import (
	"testing"
	"time"
)

func TestTimeRangeLength(t *testing.T) {
	tests := []struct {
		name string
		rng  TimeRange
		want time.Duration
	}{
		{name: "positive", rng: TimeRange{Start: 2 * time.Second, End: 5 * time.Second}, want: 3 * time.Second},
		{name: "zero length", rng: TimeRange{Start: time.Second, End: time.Second}},
		{name: "empty range", rng: TimeRange{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.rng.Length(); got != tt.want {
				t.Fatalf("Length() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestTrimSelectionTotal(t *testing.T) {
	sel := TrimSelection{Segments: []TimeRange{
		{Start: 0, End: 2 * time.Second},
		{Start: 4 * time.Second, End: 5 * time.Second},
	}}
	if got, want := sel.Total(), 3*time.Second; got != want {
		t.Fatalf("Total() = %v, want %v", got, want)
	}
	if (TrimSelection{}).Total() != 0 {
		t.Fatal("empty selection Total() must be 0")
	}
}

func TestTrimExportModeString(t *testing.T) {
	tests := []struct {
		mode TrimExportMode
		want string
	}{
		{TrimExportSeparate, "Separate files"},
		{TrimExportJoin, "Join into one file"},
	}
	for _, tt := range tests {
		if got := tt.mode.String(); got != tt.want {
			t.Fatalf("String() = %q, want %q", got, tt.want)
		}
	}
	// The zero value must be the default export mode.
	var zero TrimExportMode
	if zero != TrimExportSeparate {
		t.Fatal("TrimExportSeparate must be the zero value")
	}
}

func TestAudioSettingsIsStreamCopy(t *testing.T) {
	tests := []struct {
		name  string
		audio AudioSettings
		want  bool
	}{
		{name: "mode copy", audio: AudioSettings{Mode: AudioCopy}, want: true},
		{name: "encoder copy", audio: AudioSettings{Mode: AudioEncode, Encoder: "copy"}, want: true},
		{name: "encoder copy case insensitive", audio: AudioSettings{Mode: AudioEncode, Encoder: "Copy"}, want: true},
		{name: "encoder copy with spaces", audio: AudioSettings{Mode: AudioEncode, Encoder: "  copy  "}, want: true},
		{name: "configured encoder", audio: AudioSettings{Mode: AudioEncode, Encoder: "aac"}, want: false},
		{name: "empty", audio: AudioSettings{}, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.audio.IsStreamCopy(); got != tt.want {
				t.Fatalf("IsStreamCopy() = %v, want %v", got, tt.want)
			}
		})
	}
}
