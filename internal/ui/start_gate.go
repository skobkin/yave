package ui

import (
	"strings"

	"fyne.io/fyne/v2/widget"

	"git.skobk.in/skobkin/yave/internal/validation"
)

// startBlockReason decides whether Start can be pressed and, when it
// cannot, returns a short reason for the hint beside the button. It is a
// pure function of the controller state it summarizes:
//
//   - inputPath is the applied input, entryText what the path entry shows;
//   - inspecting reports a running ffprobe;
//   - ffmpegDetected reports a successful FFmpeg detection;
//   - fieldProblems lists numeric entries that fail their format check.
//
// "" means Start is allowed (profile validation still runs on press).
func startBlockReason(inputPath, entryText string, inspecting, ffmpegDetected bool, fieldProblems []string) string {
	switch {
	case inputPath == "" && strings.TrimSpace(entryText) == "":
		return "Select an input"
	case strings.TrimSpace(entryText) != inputPath:
		return "Press Enter to apply the input path"
	case inspecting:
		return "Inspecting input…"
	case !ffmpegDetected:
		return "FFmpeg not detected"
	case len(fieldProblems) > 0:
		return fieldProblems[0]
	}
	return ""
}

// numericFieldProblems lists the numeric entries whose text is not a whole
// number. atoi would turn such text into 0, which means "no limit" or
// "default", so a typo like "720p" must block the run instead.
func (c *Controller) numericFieldProblems() []string {
	// Fields of an excluded video stream are neither validated nor used.
	if c.includeVideo != nil && !c.includeVideo.Checked {
		return nil
	}
	var problems []string
	for _, field := range []struct {
		label string
		entry *widget.Entry
	}{
		{"Maximum width", c.maxWidth},
		{"Maximum height", c.maxHeight},
		{"GPU index", c.gpuIndexEntry},
	} {
		if field.entry == nil {
			continue
		}
		if field.entry.Validate() != nil {
			problems = append(problems, field.label+" must be a whole number")
		}
	}
	return problems
}

// tabForField maps a validation field to the tab holding the control that
// fixes it, "" when unknown.
func tabForField(field string) string {
	switch {
	case strings.HasPrefix(field, "input"):
		return "Input"
	case strings.HasPrefix(field, "trim"):
		return "Trim"
	case field == "profile.container", field == "profile.output_template", field == "output_path":
		return "Output"
	case strings.HasPrefix(field, "profile.video"):
		return "Video"
	case strings.HasPrefix(field, "profile.audio"), strings.HasPrefix(field, "profile.subtitles"):
		return "Audio & Subtitles"
	}
	return ""
}

// firstErrorTab returns the tab of the first error that maps to one.
func firstErrorTab(issues []validation.Issue) string {
	for _, issue := range issues {
		if tab := tabForField(issue.Field); tab != "" {
			return tab
		}
	}
	return ""
}
