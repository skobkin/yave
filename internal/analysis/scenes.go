package analysis

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// DefaultSceneFloor is the lowest scene score a detection run records.
// The run keeps every cut at or above it, so the UI can raise the
// threshold and filter the result without running FFmpeg again.
const DefaultSceneFloor = 0.1

// sceneWidth is the width the video is scaled to before scoring: scene
// scores need no detail, and a thumbnail-sized frame decodes and scores
// many times faster than the source resolution.
const sceneWidth = 160

// RequiredSceneFilters are the filters scene detection needs; the feature
// is offered only when the FFmpeg build lists all of them.
var RequiredSceneFilters = []string{"scale", "select", "metadata"}

// Scene is one detected cut: the position of the first frame of the new
// scene (on the input-relative timeline of ffmpeg's -ss) and its score,
// between the floor and 1.
type Scene struct {
	At    time.Duration
	Score float64
}

// SceneOptions describe one detection run.
type SceneOptions struct {
	FFmpegPath string
	Input      string
	// Floor is the lowest score recorded; zero or out of (0, 1) means
	// DefaultSceneFloor.
	Floor float64
	// Duration is the input's length, used only to report progress.
	Duration time.Duration
}

func (o SceneOptions) floor() float64 {
	if o.Floor <= 0 || o.Floor >= 1 {
		return DefaultSceneFloor
	}
	return o.Floor
}

// SceneArgs builds the detection command line: the first video stream
// scaled down, FFmpeg's scene score computed for every frame, and each
// frame's timestamp and score printed as metadata on stdout. Progress
// goes to stderr (-progress pipe:2) because stdout carries the metadata.
//
// Every frame is passed through (select='gte(scene,0)' is always true, but
// referencing "scene" makes FFmpeg compute and attach the score) and the
// floor is applied while parsing. Selecting only the cuts in the filter
// would be cheaper to parse but drops all other frames, so the output
// clock — which is what -progress reports — would only advance at cuts and
// the progress would stall between them. The expression is quoted at
// filtergraph level, so it survives its commas; no shell is involved.
func SceneArgs(o SceneOptions) []string {
	filter := fmt.Sprintf("scale=%d:-2,select='gte(scene,0)',metadata=mode=print:key=lavfi.scene_score:file=-", sceneWidth)
	return []string{
		"-hide_banner", "-nostdin", "-loglevel", "error", "-nostats",
		"-progress", "pipe:2",
		"-i", o.Input,
		"-map", "0:v:0", "-an", "-sn", "-dn",
		"-vf", filter,
		"-f", "null", "-",
	}
}

// DetectScenes scans the input for scene cuts, calling progress (when
// non-nil) with how much of the input has been scored. It returns the
// cuts in time order, or ctx's error when cancelled; FFmpeg's diagnostics
// are part of any other error. Timestamps are input-relative: FFmpeg
// rebases the container's start_time away, the same origin -ss uses.
func DetectScenes(ctx context.Context, o SceneOptions, progress func(done time.Duration)) ([]Scene, error) {
	if strings.TrimSpace(o.Input) == "" {
		return nil, fmt.Errorf("detect scenes: input path is empty")
	}
	parser := sceneParser{floor: o.floor()}
	err := run(ctx, o.FFmpegPath, SceneArgs(o),
		parser.line,
		func(line string) bool {
			done, ok := parseProgressLine(line)
			if ok && progress != nil {
				progress(done)
			}
			return ok || isProgressNoise(line)
		})
	if err != nil {
		return nil, fmt.Errorf("detect scenes in %q: %w", o.Input, err)
	}
	return parser.scenes, nil
}

// sceneParser pairs the two metadata lines FFmpeg prints per frame and
// keeps the frames scoring at or above floor:
//
//	frame:12   pts:2048     pts_time:2.048
//	lavfi.scene_score=0.512345
type sceneParser struct {
	floor  float64
	at     time.Duration
	have   bool
	scenes []Scene
}

func (p *sceneParser) line(line string) {
	if i := strings.Index(line, "pts_time:"); i >= 0 {
		rest := line[i+len("pts_time:"):]
		if end := strings.IndexAny(rest, " \t"); end >= 0 {
			rest = rest[:end]
		}
		seconds, err := strconv.ParseFloat(strings.TrimSpace(rest), 64)
		if err != nil || seconds < 0 {
			p.have = false
			return
		}
		p.at, p.have = time.Duration(seconds*float64(time.Second)), true
		return
	}
	if score, ok := strings.CutPrefix(line, "lavfi.scene_score="); ok {
		value, err := strconv.ParseFloat(strings.TrimSpace(score), 64)
		if err == nil && p.have && value >= p.floor {
			p.scenes = append(p.scenes, Scene{At: p.at, Score: value})
		}
		p.have = false
	}
}

// parseProgressLine extracts the processed media time from a -progress
// line. out_time_us carries it in microseconds; "N/A" (before the first
// frame) is not progress.
func parseProgressLine(line string) (time.Duration, bool) {
	value, ok := strings.CutPrefix(line, "out_time_us=")
	if !ok {
		return 0, false
	}
	micros, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil || micros < 0 {
		return 0, false
	}
	return time.Duration(micros) * time.Microsecond, true
}

// isProgressNoise reports whether a stderr line is one of the -progress
// key=value records (frame=, fps=, bitrate=, speed=, progress=…) that
// must not end up in an error message.
func isProgressNoise(line string) bool {
	key, _, ok := strings.Cut(line, "=")
	if !ok {
		return false
	}
	switch key {
	case "frame", "fps", "bitrate", "total_size", "out_time", "out_time_ms", "out_time_us",
		"dup_frames", "drop_frames", "speed", "progress":
		return true
	}
	return strings.HasPrefix(key, "stream_")
}

// FilterScenes returns the scenes scoring at or above threshold.
func FilterScenes(scenes []Scene, threshold float64) []Scene {
	out := make([]Scene, 0, len(scenes))
	for _, s := range scenes {
		if s.Score >= threshold {
			out = append(out, s)
		}
	}
	return out
}
