package ui

import "testing"

func TestStartBlockReason(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		input      string
		entry      string
		inspecting bool
		detected   bool
		problems   []string
		want       string
	}{
		{"no input", "", "", false, true, nil, "Select an input"},
		{"edited path not applied", "/a.mkv", "/b.mkv", false, true, nil, "Press Enter to apply the input path"},
		{"typed path never applied", "", "/b.mkv", false, true, nil, "Press Enter to apply the input path"},
		{"inspecting", "/a.mkv", "/a.mkv", true, true, nil, "Inspecting input…"},
		{"ffmpeg missing", "/a.mkv", "/a.mkv", false, false, nil, "FFmpeg not detected"},
		{"bad number", "/a.mkv", "/a.mkv", false, true, []string{"Maximum height must be a whole number"}, "Maximum height must be a whole number"},
		{"ready", "/a.mkv", " /a.mkv ", false, true, nil, ""},
	}
	for _, tt := range tests {
		if got := startBlockReason(tt.input, tt.entry, tt.inspecting, tt.detected, tt.problems); got != tt.want {
			t.Errorf("%s: reason = %q, want %q", tt.name, got, tt.want)
		}
	}
}

func TestTabForField(t *testing.T) {
	t.Parallel()
	tests := map[string]string{
		"input_path":              "Input",
		"trim.export":             "Trim",
		"trim in":                 "Trim",
		"output_path":             "Output",
		"profile.container":       "Output",
		"profile.video.max_width": "Video",
		"profile.audio.encoder":   "Audio & Subtitles",
		"profile.subtitles.mode":  "Audio & Subtitles",
		"unknown":                 "",
	}
	for field, want := range tests {
		if got := tabForField(field); got != want {
			t.Errorf("tabForField(%q) = %q, want %q", field, got, want)
		}
	}
}

func TestNumericFieldProblemsFlagsTypos(t *testing.T) {
	c, _, _ := exportTestController(t)
	if got := c.numericFieldProblems(); len(got) != 0 {
		t.Fatalf("empty fields reported problems: %v", got)
	}
	c.maxHeight.SetText("720p")
	got := c.numericFieldProblems()
	if len(got) != 1 || got[0] != "Maximum height must be a whole number" {
		t.Fatalf("problems = %v", got)
	}
}
