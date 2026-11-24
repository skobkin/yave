package ffprobe

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"time"
)

// packetEntry is the subset of ffprobe's packet output keyframe scanning
// needs. flags carries "K" for a keyframe packet ("K__", "KD_").
type packetEntry struct {
	PTSTime string `json:"pts_time"`
	DTSTime string `json:"dts_time"`
	Flags   string `json:"flags"`
}

type packetDocument struct {
	Packets []packetEntry `json:"packets"`
}

// KeyframeTimes returns the first video stream's keyframe positions
// (presentation time of keyframe packets) for a window of the input,
// normalized onto the input-relative timeline like FrameTimes: the
// container's start_time is subtracted, so the values are the positions
// ffmpeg's input-side -ss seeks (and stream copy snaps) to.
//
// It scans packets, not frames: nothing is decoded, so the cost is the
// demuxing of the window. -read_intervals seeks to the keyframe at or
// before from, and that keyframe is part of the result — the list starts
// with the keyframe stream copy would begin a cut at from on, which is
// exactly what the cut-time preview needs. Callers must therefore treat
// the answer as complete for [first returned keyframe, to], not [from, to].
// A window at the input's start (from <= 0) never seeks and reads from
// the first packet. Results are sorted and deduplicated; an input without
// video, or a window without keyframes, yields an empty list.
func KeyframeTimes(ctx context.Context, executable, inputPath string, from, to time.Duration) ([]time.Duration, error) {
	executable = strings.TrimSpace(executable)
	if executable == "" {
		executable = "ffprobe"
	}
	resolved, err := exec.LookPath(executable)
	if err != nil {
		return nil, fmt.Errorf("ffprobe executable %q not found: %w", executable, err)
	}
	if strings.TrimSpace(inputPath) == "" {
		return nil, fmt.Errorf("keyframe times: input path is empty")
	}
	if to <= from {
		return nil, fmt.Errorf("keyframe times: window end (%s) must be after start (%s)", to, from)
	}
	origin, _, err := inputOrigin(ctx, resolved, inputPath)
	if err != nil {
		return nil, err
	}
	// -read_intervals takes absolute container timestamps; the A%B form
	// bounds the read by an absolute end (see FrameTimes).
	interval := fmt.Sprintf("%s%%%s", formatProbeSeconds(from+origin), formatProbeSeconds(to+origin))
	if from <= 0 {
		interval = "%" + formatProbeSeconds(to+origin)
	}
	args := []string{
		"-v", "error",
		"-select_streams", "v:0",
		"-show_entries", "packet=pts_time,dts_time,flags",
		"-of", "json",
		"-read_intervals", interval,
		inputPath,
	}
	output, stderr, err := runProbe(exec.CommandContext(ctx, resolved, args...)) //nolint:gosec // G204: resolved via exec.LookPath
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		detail := stderr
		if detail == "" {
			detail = err.Error()
		}
		return nil, fmt.Errorf("probe keyframe times %q: %s", inputPath, detail)
	}
	return parseKeyframeTimes(output, origin, to)
}

// parseKeyframeTimes extracts keyframe packet positions from ffprobe's
// packet JSON, normalized by origin and bounded above by to (the read
// may run slightly past the requested end). Positions before the origin
// are dropped, as are packets without a usable timestamp; the decode
// timestamp stands in for a missing presentation timestamp.
func parseKeyframeTimes(data []byte, origin, to time.Duration) ([]time.Duration, error) {
	var document packetDocument
	if err := json.Unmarshal(data, &document); err != nil {
		return nil, fmt.Errorf("decode ffprobe packet JSON: %w", err)
	}
	times := make([]time.Duration, 0, len(document.Packets)/8)
	for _, packet := range document.Packets {
		if !strings.Contains(packet.Flags, "K") {
			continue
		}
		raw, ok := parseSeconds(packet.PTSTime)
		if !ok {
			raw, ok = parseSeconds(packet.DTSTime)
		}
		if !ok {
			continue
		}
		at := raw - origin
		if at < 0 || at > to {
			continue
		}
		times = append(times, at)
	}
	sort.Slice(times, func(i, j int) bool { return times[i] < times[j] })
	out := times[:0]
	for i, at := range times {
		if i == 0 || at != times[i-1] {
			out = append(out, at)
		}
	}
	return out, nil
}
