package playback

import (
	"slices"
	"testing"
	"time"
)

func TestVideoArgs(t *testing.T) {
	got := VideoArgs(Options{Input: "/in/a b.mkv", From: 90*time.Second + 500*time.Millisecond, Until: 100 * time.Second, FrameRate: "30000/1001"})
	want := []string{
		"-hide_banner", "-nostdin", "-loglevel", "error", "-ss", "90.500000", "-i", "/in/a b.mkv", "-t", "9.500000",
		"-map", "0:v:0", "-an", "-sn", "-dn",
		"-vf", "scale=w='trunc(iw*sar/2)*2':h=ih,setsar=1,scale='min(640,iw)':-2,fps=30000/1001,format=yuv420p",
		"-f", "yuv4mpegpipe", "pipe:1",
	}
	if !slices.Equal(got, want) {
		t.Errorf("VideoArgs() =\n%q\nwant\n%q", got, want)
	}
}

func TestVideoArgsOpenEndedAndWidth(t *testing.T) {
	got := VideoArgs(Options{Input: "in.mkv", MaxWidth: 320})
	if slices.Contains(got, "-t") {
		t.Errorf("open-ended playback must not pass -t: %q", got)
	}
	if !slices.Contains(got, "scale-placeholder") && !containsSubstr(got, "min(320,iw)") {
		t.Errorf("MaxWidth not applied: %q", got)
	}
	if !containsSubstr(got, "fps=30,") {
		t.Errorf("unknown source rate should fall back to MaxFPS: %q", got)
	}
}

func TestAudioArgs(t *testing.T) {
	got := AudioArgs(Options{Input: "in.mkv", From: 2 * time.Second, Until: 5 * time.Second})
	want := []string{
		"-hide_banner", "-nostdin", "-loglevel", "error", "-ss", "2.000000", "-i", "in.mkv", "-t", "3.000000",
		"-map", "0:a:0", "-vn", "-sn", "-dn", "-ac", "2", "-ar", "48000", "-f", "s16le", "pipe:1",
	}
	if !slices.Equal(got, want) {
		t.Errorf("AudioArgs() =\n%q\nwant\n%q", got, want)
	}
}

func TestChooseFPS(t *testing.T) {
	tests := map[string]string{
		"30000/1001":  "30000/1001",
		"25":          "25/1",
		"25/1":        "25/1",
		"30/1":        "30/1",
		"60/1":        "30",
		"120000/1001": "30",
		"":            "30",
		"0/0":         "30",
		"abc":         "30",
		"-24/1":       "30",
		"24/x":        "30",
	}
	for in, want := range tests {
		if got := ChooseFPS(in); got != want {
			t.Errorf("ChooseFPS(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFormatSeconds(t *testing.T) {
	tests := map[time.Duration]string{
		0:                            "0.000000",
		-time.Second:                 "0.000000",
		1500 * time.Millisecond:      "1.500000",
		time.Hour + time.Microsecond: "3600.000001",
	}
	for in, want := range tests {
		if got := formatSeconds(in); got != want {
			t.Errorf("formatSeconds(%v) = %q, want %q", in, got, want)
		}
	}
}

func containsSubstr(args []string, sub string) bool {
	for _, a := range args {
		for i := 0; i+len(sub) <= len(a); i++ {
			if a[i:i+len(sub)] == sub {
				return true
			}
		}
	}
	return false
}
