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

type frameDocument struct {
	Frames []frameEntry `json:"frames"`
}

// Window reads extend past the requested end so decoder-buffered
// frames are flushed: -read_intervals stops reading at the end and
// frames held behind the B-frame reorder delay are then lost (a 1fps
// mpeg4 window through 2s reported only frame 0 — the 2-frame delay
// outlasted any fixed second-based margin). The read therefore
// extends until a frame lands beyond the window end, proving the
// buffer flushed; the extension doubles per attempt, bounded by the
// attempt cap. A read that reaches the selected video stream's end
// flushes its decoder completely (a read past the last video packet
// of a 3s-video/15s-audio fixture reported every frame, including the
// reorder-delay tail), so a capped result there is final — the
// container duration alone is the wrong reference when audio outlives
// the video. A cap reached short of the stream end means readahead
// remained that could still prove completeness — the probe fails
// rather than return a silently partial window, which the frame index
// would navigate by frames that do not exist. The [from, to] filter
// keeps every returned result exact.
const (
	frameProbeMargin   = time.Second // first extension past the window end
	frameProbeAttempts = 3           // read extensions 1s, 2s, then 4s
)

// FrameTimes returns decoded first-video-stream frame timestamps
// (presentation order, best_effort_timestamp_time) within [from, to],
// normalized against the container's format start_time — the same
// origin ffmpeg's input-side -ss seeks against — so the values sit on
// the timeline of ffmpeg's -ss seeks and the UI's [0, duration]
// playhead; the origin itself is returned as the second value. It reads
// only the requested window (-read_intervals seeks on the input side),
// so cost is independent of source length. Timestamps are sorted,
// "N/A"/missing values skipped, duplicates deduplicated.
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

	// -read_intervals expects absolute container timestamps, while the
	// requested window is input-relative (the timeline ffmpeg's
	// input-side -ss and the UI playhead use). Probe the container's
	// format start_time and shift the bounds into the absolute domain.
	origin, videoEnd, err := inputOrigin(ctx, resolved, inputPath)
	if err != nil {
		return nil, 0, err
	}

	for attempt := 0; ; attempt++ {
		margin := frameProbeMargin << attempt
		args := []string{
			"-v", "error",
			"-select_streams", "v:0",
			"-show_frames",
			// Normalization uses the origin from the separate format probe
			// above, not the video stream's own start_time: rebasing against
			// the stream would shift its first frame to zero even when the
			// container (and ffmpeg's -ss) starts earlier — audio at zero
			// with video delayed one second indexes its frames at one
			// second, not zero.
			"-show_entries", "frame=best_effort_timestamp_time",
			"-of", "json",
			// The absolute-end form A%B is required: ffprobe measures a
			// +duration interval from where the keyframe seek actually
			// landed, not from the requested start, so with a large GOP a
			// relative window stops decoding before reaching to.
			"-read_intervals", fmt.Sprintf("%s%%%s", formatProbeSeconds(from+origin), formatProbeSeconds(to+margin+origin)),
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
		times, err := parseFrameTimes(output, origin)
		if err != nil {
			return nil, 0, err
		}
		// -read_intervals reads from the keyframe at or before from, so
		// the raw document may span more than the requested window: keep
		// only normalized positions within [from, to].
		filtered := make([]time.Duration, 0, len(times))
		for _, t := range times {
			if t >= from && t <= to {
				filtered = append(filtered, t)
			}
		}
		// Complete once a frame landed beyond the window end — every
		// buffered frame at or before to has then been flushed.
		if len(times) > 0 && times[len(times)-1] > to {
			return filtered, origin, nil
		}
		// The cap is exhausted without proof. A final read that reached
		// the video stream's end exhausted its packets, which flushes
		// the decoder completely — the result is final and an empty
		// window genuinely holds no frames. Short of the stream end, an
		// extension could still prove completeness: fail rather than
		// return a silently partial window. An unknown stream end cannot
		// prove EOF, so it keeps the lenient final result.
		if attempt >= frameProbeAttempts-1 {
			if videoEnd > 0 && to+margin < videoEnd {
				return nil, 0, fmt.Errorf("probe frame times %q: window %s..%s incomplete: frames buffered behind the decoder reorder delay outlast the %s read margin, and the video continues past the read end", inputPath, from, to, margin)
			}
			return filtered, origin, nil
		}
	}
}

// inputOrigin reports the input's format start_time — the reference
// ffmpeg's input-side -ss seeks against and the domain -read_intervals
// expects — as an absolute timestamp, together with the input-relative
// end of the selected video stream: the window loop needs that end to
// tell a final read (past the last video packet, whose decoder flush is
// complete) from one that stopped short with readahead remaining. The
// stream end is (stream start_time − origin) + stream duration; the
// input-relative container duration (normalizeDuration) stands in when
// the stream reports no usable timestamps — audio outliving the video
// would otherwise hold windows short of the video end incomplete
// forever. A plain zero-origin file (the common case) yields 0; a
// missing, "N/A" or unparseable origin degrades to 0 rather than
// failing the window fetch.
func inputOrigin(ctx context.Context, resolved, inputPath string) (time.Duration, time.Duration, error) {
	out, err := exec.CommandContext(ctx, resolved, //nolint:gosec // G204: resolved via exec.LookPath
		"-v", "error",
		"-select_streams", "v:0",
		"-show_entries", "stream=start_time,duration:format=start_time,duration,format_name",
		"-of", "json",
		inputPath,
	).CombinedOutput()
	if err != nil {
		if ctx.Err() != nil {
			return 0, 0, ctx.Err()
		}
		detail := strings.TrimSpace(string(out))
		if detail == "" {
			detail = err.Error()
		}
		return 0, 0, fmt.Errorf("probe input origin %q: %s", inputPath, detail)
	}
	// ffprobe emits a single format object ("format"), not an array —
	// decoding a "formats" array always produced an empty document and a
	// zero origin, probing input-relative windows as if they were
	// absolute and leaving frame times unnormalized on any
	// nonzero-origin input.
	var document struct {
		Streams []struct {
			StartTime string `json:"start_time"`
			Duration  string `json:"duration"`
		} `json:"streams"`
		Format struct {
			FormatName string `json:"format_name"`
			Duration   string `json:"duration"`
			StartTime  string `json:"start_time"`
		} `json:"format"`
	}
	if err := json.Unmarshal(out, &document); err != nil {
		return 0, 0, fmt.Errorf("decode ffprobe format JSON: %w", err)
	}
	duration := normalizeDuration(document.Format.Duration, document.Format.StartTime, document.Format.FormatName)
	// ParseFloat unlike parseSeconds so a negative container origin
	// still shifts the window correctly. A missing, "N/A" or unparseable
	// value errors out and degrades to zero by design.
	seconds, err := strconv.ParseFloat(strings.TrimSpace(document.Format.StartTime), 64)
	if err != nil {
		seconds = 0 // "N/A" or unparseable origin degrades to zero by design
	}
	origin := time.Duration(seconds * float64(time.Second))
	videoEnd := duration
	if len(document.Streams) > 0 {
		start, startOK := parseSeconds(document.Streams[0].StartTime)
		length, lengthOK := parseSeconds(document.Streams[0].Duration)
		if startOK && lengthOK && length >= 0 {
			videoEnd = start - origin + length
		}
	}
	return origin, videoEnd, nil
}

// parseFrameTimes converts raw absolute frame timestamps into
// input-relative positions by subtracting origin (the container's
// format start_time), sorted with duplicates removed.
func parseFrameTimes(data []byte, origin time.Duration) ([]time.Duration, error) {
	var document frameDocument
	if err := json.Unmarshal(data, &document); err != nil {
		return nil, fmt.Errorf("decode ffprobe frame JSON: %w", err)
	}
	times := make([]time.Duration, 0, len(document.Frames))
	seen := make(map[time.Duration]struct{}, len(document.Frames))
	for _, frame := range document.Frames {
		raw, ok := parseSeconds(frame.BestEffortTimestampTime)
		if !ok {
			continue
		}
		normalized := raw - origin
		if _, duplicate := seen[normalized]; duplicate {
			continue
		}
		seen[normalized] = struct{}{}
		times = append(times, normalized)
	}
	sort.Slice(times, func(i, j int) bool { return times[i] < times[j] })
	return times, nil
}

// parseSeconds parses an ffprobe seconds value ("1.043000", "N/A").
// Unlike parseDuration it distinguishes malformed input from a valid 0.
// Negative values are valid input: with a negative container origin,
// absolute frame timestamps before zero are real positions, and the
// [from, to] filter after normalization (raw minus origin) is what
// bounds them onto the UI timeline.
func parseSeconds(value string) (time.Duration, bool) {
	seconds, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	if err != nil {
		return 0, false
	}
	return time.Duration(seconds * float64(time.Second)), true
}

func formatProbeSeconds(d time.Duration) string {
	return strconv.FormatFloat(d.Seconds(), 'f', 3, 64)
}
