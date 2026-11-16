package ffprobe

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"
)

// frameEntry is the subset of ffprobe's frame output the frame index
// needs. best_effort_timestamp_time is ffprobe's most reliable
// presentation-time estimate: unlike packet pts_time it reflects the
// decoded, presentation-ordered frame even across B-frame reordering,
// and unlike plain pts_time it survives containers where pts is stored
// but the muxer's offsets make raw values unusable.
type frameEntry struct {
	BestEffortTimestampTime string `json:"best_effort_timestamp_time"`
}

// frameStream is the subset of stream output needed to normalize frame
// timestamps onto the input timeline.
type frameStream struct {
	StartTime string `json:"start_time"`
}

type frameDocument struct {
	Frames  []frameEntry  `json:"frames"`
	Streams []frameStream `json:"streams"`
}

// FrameTimes returns decoded first-video-stream frame timestamps
// (presentation order, best_effort_timestamp_time) within [from, to],
// normalized against the stream's start_time so the values sit on the
// same timeline as ffmpeg's input-side -ss seeks and the UI's
// [0, duration] playhead. It reads only the requested window
// (-read_intervals seeks on the input side), so cost is independent of
// source length. Timestamps are sorted, "N/A"/missing values skipped,
// duplicates deduplicated.
//
// -read_intervals seeks to the nearest keyframe at or before from, so
// the raw window may include frames from before from — they are
// filtered out after normalization. An inaccurate seek (rare) may miss
// edge frames; callers detect the gap and fetch an adjacent window.
func FrameTimes(ctx context.Context, executable, inputPath string, from, to time.Duration) ([]time.Duration, time.Duration, error) {
	executable = strings.TrimSpace(executable)
	if executable == "" {
		executable = "ffprobe"
	}
	resolved, err := exec.LookPath(executable)
	if err != nil {
		return nil, 0, fmt.Errorf("ffprobe executable %q not found: %w", executable, err)
	}
	if strings.TrimSpace(inputPath) == "" {
		return nil, 0, fmt.Errorf("frame times: input path is empty")
	}
	if to <= from {
		return nil, 0, fmt.Errorf("frame times: window end (%s) must be after start (%s)", to, from)
	}

	args := []string{
		"-v", "error",
		"-select_streams", "v:0",
		"-show_frames",
		"-show_entries", "frame=best_effort_timestamp_time:stream=start_time",
		"-of", "json",
		"-read_intervals", fmt.Sprintf("%s%%+%s", formatProbeSeconds(from), formatProbeSeconds(to-from)),
		inputPath,
	}
	output, err := exec.CommandContext(ctx, resolved, args...).CombinedOutput() //nolint:gosec // G204: resolved via exec.LookPath
	if err != nil {
		if ctx.Err() != nil {
			return nil, 0, ctx.Err()
		}
		detail := strings.TrimSpace(string(output))
		if detail == "" {
			detail = err.Error()
		}
		return nil, 0, fmt.Errorf("probe frame times %q: %s", inputPath, detail)
	}
	times, start, err := parseFrameTimes(output)
	if err != nil {
		return nil, 0, err
	}
	// -read_intervals reads from the keyframe at or before from, so the
	// raw document may span more than the requested window: keep only
	// normalized positions within [from, to].
	filtered := times[:0]
	for _, t := range times {
		if t >= from && t <= to {
			filtered = append(filtered, t)
		}
	}
	return filtered, start, nil
}

func parseFrameTimes(data []byte) ([]time.Duration, time.Duration, error) {
	var document frameDocument
	if err := json.Unmarshal(data, &document); err != nil {
		return nil, 0, fmt.Errorf("decode ffprobe frame JSON: %w", err)
	}
	var start time.Duration
	if len(document.Streams) > 0 {
		if parsed, ok := parseSeconds(document.Streams[0].StartTime); ok {
			start = parsed
		}
	}
	times := make([]time.Duration, 0, len(document.Frames))
	seen := make(map[time.Duration]struct{}, len(document.Frames))
	for _, frame := range document.Frames {
		raw, ok := parseSeconds(frame.BestEffortTimestampTime)
		if !ok {
			continue
		}
		normalized := raw - start
		if _, duplicate := seen[normalized]; duplicate {
			continue
		}
		seen[normalized] = struct{}{}
		times = append(times, normalized)
	}
	sort.Slice(times, func(i, j int) bool { return times[i] < times[j] })
	return times, start, nil
}

// parseSeconds parses an ffprobe seconds value ("1.043000", "N/A").
// Unlike parseDuration it distinguishes malformed input from a valid 0.
func parseSeconds(value string) (time.Duration, bool) {
	seconds, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	if err != nil || seconds < 0 {
		return 0, false
	}
	return time.Duration(seconds * float64(time.Second)), true
}

func formatProbeSeconds(d time.Duration) string {
	return strconv.FormatFloat(d.Seconds(), 'f', 3, 64)
}
