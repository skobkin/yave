package ffmpeg

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"git.skobk.in/skobkin/yave/internal/encoder"
	"git.skobk.in/skobkin/yave/internal/model"
)

type BuildRequest struct {
	FFmpegPath string
	InputPath  string
	OutputPath string
	Profile    model.Profile
	Media      model.MediaInfo
	Overwrite  bool
	// Trim, when active, restricts the output to the selected range via
	// an input-side -ss seek plus an output-side -t duration. The zero
	// value means no trimming. It must carry at most ONE segment:
	// multi-segment exports build one plan per segment (or use
	// BuildConcatCommand for the join pass); a longer selection is a
	// caller bug and is rejected here so it can never silently become a
	// single-segment job on Segments[0].
	Trim model.TrimSelection
}

// formatSeconds renders a duration as plain fractional seconds
// ("83.456789") — the timestamp form ffmpeg's -ss/-t accept on every
// build. Microsecond precision, truncating below the microsecond:
// ffmpeg drops frames whose timestamps are strictly before an input
// -ss target, so millisecond rounding (66.667 ms → 0.067) could skip
// a 29.97 fps frame; truncation lands at or before its timestamp and
// selects it exactly. The UI's HH:MM:SS.mmm fields only display three
// decimals; the generated command keeps the full position.
func formatSeconds(d time.Duration) string {
	return fmt.Sprintf("%.6f", float64(d/time.Microsecond)/1e6)
}

func BuildCommand(req BuildRequest) (model.RunPlan, error) {
	if req.FFmpegPath == "" {
		return model.RunPlan{}, errors.New("FFmpeg path is empty")
	}
	if req.InputPath == "" || req.OutputPath == "" {
		return model.RunPlan{}, errors.New("input and output paths are required")
	}
	if len(req.Trim.Segments) > 1 {
		return model.RunPlan{}, errors.New("BuildCommand applies a single trim segment; build per-segment plans via the export job")
	}

	p := req.Profile
	args := []string{"-hide_banner"}
	if req.Overwrite {
		args = append(args, "-y")
	} else {
		args = append(args, "-n")
	}
	args = append(args, "-progress", "pipe:1", "-nostats")

	ve, err := lookupVideoEncoder(p.Video.Encoder)
	if err != nil {
		return model.RunPlan{}, err
	}

	// Per-encoder pre-input flags (e.g. -vaapi_device, -init_hw_device qsv,
	// -gpu N) are emitted before -i so that ffmpeg can resolve the hw
	// context the encoder pipeline references.
	var preInput []string
	if ve != nil {
		build, err := ve.BuildVideoArgs(p.Video)
		if err != nil {
			return model.RunPlan{}, err
		}
		preInput = build.PreInputArgs
	}
	args = append(args, preInput...)

	// Input-side seek: ffmpeg seeks to the nearest keyframe at or before
	// seg.Start and decodes forward from there. For re-encode this is
	// frame-accurate on modern builds; for stream copy the cut snaps to
	// keyframes (validation warns). Both preInput flags and -ss are
	// input-side options, so this position is deterministic rather than
	// functionally significant.
	if seg, ok := req.Trim.First(); ok {
		args = append(args, "-ss", formatSeconds(seg.Start))
	}

	args = append(args, "-i", req.InputPath, "-map", "0:v:0")
	if p.Audio.CopyAllStreams {
		args = append(args, "-map", "0:a?")
	} else {
		args = append(args, "-map", "0:a:0?")
	}

	warnings := make([]string, 0)
	if p.Subtitles.Mode == model.SubtitleCopy {
		subtitleArgs, subtitleWarnings := subtitleMapping(p.Container, req.Media.SubtitleStreams)
		if req.Trim.Active() && len(subtitleArgs) > 0 {
			// Copied subtitle events cannot be cut at trim boundaries:
			// ffmpeg delivers an event active at the seek point in full
			// and -t gates on packet start times only. A 0–60s event in
			// a [1s,3s) cut measures the Matroska output at 60s (the
			// event sets the container duration) and lands mistimed in
			// MP4 either way. Skip the streams with a warning
			// (mirroring the incompatible-codec handling in
			// subtitleMapping) instead of producing a wrong output; only
			// the mapped, container-compatible streams are dropped — the
			// skip warnings below would report streams that were never
			// mapped anyway.
			warnings = append(warnings,
				"trimming is active: copied subtitle streams are skipped because subtitle events cannot be cut at trim boundaries; switch subtitles to none or convert without trimming to keep them")
		} else {
			args = append(args, subtitleArgs...)
			warnings = append(warnings, subtitleWarnings...)
		}
	}

	videoArgs, err := buildVideoArgs(ve, p.Video, req.Media)
	if err != nil {
		return model.RunPlan{}, err
	}
	args = append(args, videoArgs...)

	audioArgs, err := buildAudioArgs(p.Audio)
	if err != nil {
		return model.RunPlan{}, err
	}
	args = append(args, audioArgs...)

	if p.Subtitles.Mode == model.SubtitleCopy && hasMappedSubtitles(args) {
		args = append(args, "-c:s", "copy")
	}
	// Output-side zero seek. With the input-side -ss above, streams
	// copied from before seg.Start arrive with negative rebased
	// timestamps — copied audio has no seek-accurate drop, so a [2s,3s)
	// cut of a 3s-GOP file carried ~2s of preroll audio that muxers
	// resolve by shifting the whole timeline (Matroska has no edit
	// lists): the output measured 3.018s with its video starting at
	// 1.977s. -ss 0 discards exactly those negative packets while
	// keeping the segment-relative out_time -progress reports. Safe
	// only with re-encoded video, whose frames already start at zero:
	// dropped negative packets would strand a copied video stream
	// mid-GOP, which is why that stream keeps the keyframe-snap
	// behavior the copy warning describes.
	if _, ok := req.Trim.First(); ok && ve != nil {
		args = append(args, "-ss", formatSeconds(0))
	}
	// Output-side duration cap. With the input-side -ss above,
	// -progress's out_time is segment-relative, so progress computed
	// against seg.End-seg.Start stays correct.
	if seg, ok := req.Trim.First(); ok {
		args = append(args, "-t", formatSeconds(seg.End-seg.Start))
	}
	if p.Container == model.ContainerMP4 {
		args = append(args, "-movflags", "+faststart")
	}

	args = append(args, req.OutputPath)
	return model.RunPlan{
		Executable: req.FFmpegPath,
		Args:       args,
		Preview:    ShellPreview(req.FFmpegPath, args),
		OutputPath: req.OutputPath,
		Warnings:   warnings,
	}, nil
}

// lookupVideoEncoder resolves the encoder name from a profile. The
// "copy" pseudo-encoder returns (nil, nil) — callers handle it as a
// passthrough. Unknown encoder names return a non-nil error.
func lookupVideoEncoder(name string) (encoder.VideoEncoder, error) {
	if strings.EqualFold(strings.TrimSpace(name), "copy") {
		return nil, nil
	}
	raw, ok := encoder.DefaultRegistry().Get(name)
	if !ok {
		return nil, fmt.Errorf("unsupported video encoder %q", name)
	}
	ve, ok := raw.(encoder.VideoEncoder)
	if !ok {
		return nil, fmt.Errorf("encoder %q is not a video encoder", name)
	}
	return ve, nil
}

// buildVideoArgs composes the post -c:v flags for a profile. When the
// encoder is the "copy" passthrough, no -vf is emitted and the only
// output is `-c:v copy`. Otherwise the encoder's VideoFilter and
// BuildVideoArgs are used to produce the filter chain and per-encoder
// args in the same order as the prior ffmpeg.buildVideoArgs switch.
func buildVideoArgs(ve encoder.VideoEncoder, v model.VideoSettings, media model.MediaInfo) ([]string, error) {
	if ve == nil {
		return []string{"-c:v", "copy"}, nil
	}
	filter, err := ve.VideoFilter(v, media)
	if err != nil {
		return nil, err
	}
	build, err := ve.BuildVideoArgs(v)
	if err != nil {
		return nil, err
	}
	args := make([]string, 0, 2+len(build.Args))
	if filter != "" {
		args = append(args, "-vf", filter)
	}
	args = append(args, build.Args...)
	return args, nil
}

// buildAudioArgs composes the post -c:a flags for a profile. Stream
// copy (per model.AudioSettings.IsStreamCopy, the predicate validation
// shares) dispatches to a stub `-c:a copy`; every other encoder goes
// through the registry.
func buildAudioArgs(a model.AudioSettings) ([]string, error) {
	if a.IsStreamCopy() {
		return []string{"-c:a", "copy"}, nil
	}
	raw, ok := encoder.DefaultRegistry().Get(a.Encoder)
	if !ok {
		return nil, fmt.Errorf("unsupported audio encoder %q", a.Encoder)
	}
	ae, ok := raw.(encoder.AudioEncoder)
	if !ok {
		return nil, fmt.Errorf("encoder %q is not an audio encoder", a.Encoder)
	}
	return ae.BuildAudioArgs(a)
}

func subtitleMapping(container string, streams []model.SubtitleStream) ([]string, []string) {
	args := make([]string, 0)
	warnings := make([]string, 0)
	for _, stream := range streams {
		compatible := container == model.ContainerMKV ||
			(container == model.ContainerMP4 && stream.Codec == "mov_text") ||
			(container == model.ContainerWebM && stream.Codec == "webvtt")
		if compatible {
			args = append(args, "-map", fmt.Sprintf("0:%d?", stream.Index))
		} else {
			warnings = append(warnings, fmt.Sprintf("subtitle stream %d (%s) will be skipped", stream.Index, stream.Codec))
		}
	}
	return args, warnings
}

func hasMappedSubtitles(args []string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "-map" && strings.HasPrefix(args[i+1], "0:") &&
			args[i+1] != "0:v:0" && !strings.HasPrefix(args[i+1], "0:a") {
			return true
		}
	}
	return false
}

func ShellPreview(executable string, args []string) string {
	parts := make([]string, 0, len(args)+1)
	parts = append(parts, shellQuote(executable))
	for _, arg := range args {
		parts = append(parts, shellQuote(arg))
	}
	return strings.Join(parts, " ")
}

func shellQuote(value string) string {
	if runtime.GOOS == "windows" {
		if value == "" || strings.ContainsAny(value, " \t\"") {
			return `"` + strings.ReplaceAll(value, `"`, `\"`) + `"`
		}
		return value
	}
	if value != "" && !strings.ContainsAny(value, " \t\n'\"\\$&;|<>*?()[]{}!") {
		return value
	}
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}

func SameFilePath(a, b string) bool {
	aa, errA := filepath.Abs(filepath.Clean(a))
	bb, errB := filepath.Abs(filepath.Clean(b))
	return errA == nil && errB == nil && aa == bb
}

// ConcatRequest describes the join pass of a multi-segment export:
// concatenating the staged per-segment intermediates listed in ListPath
// into one stream-copied output. Overwrite applies to the staged final
// (owned by the export orchestrator), never to a user-visible path.
type ConcatRequest struct {
	FFmpegPath string
	ListPath   string
	OutputPath string
	// Container selects the output muxer explicitly, so the staged
	// final's format never depends on filename inference. One of the
	// model.Container* values.
	Container string
	Overwrite bool
}

// containerMuxer maps a curated container to the explicit ffmpeg output
// muxer name used by the concat pass.
func containerMuxer(container string) (string, error) {
	switch container {
	case model.ContainerMP4:
		return "mp4", nil
	case model.ContainerMKV:
		return "matroska", nil
	case model.ContainerWebM:
		return "webm", nil
	default:
		return "", fmt.Errorf("unsupported output container %q", container)
	}
}

// BuildConcatCommand builds the join pass: a concat-demuxer invocation
// that stream-copies every listed intermediate into one output. The
// intermediates' compatibility is verified separately (ffprobe stream
// layout comparison) before this command runs.
func BuildConcatCommand(req ConcatRequest) (model.RunPlan, error) {
	if req.FFmpegPath == "" {
		return model.RunPlan{}, errors.New("FFmpeg path is empty")
	}
	if req.ListPath == "" || req.OutputPath == "" {
		return model.RunPlan{}, errors.New("concat list and output paths are required")
	}
	muxer, err := containerMuxer(req.Container)
	if err != nil {
		return model.RunPlan{}, err
	}

	args := []string{"-hide_banner"}
	if req.Overwrite {
		args = append(args, "-y")
	} else {
		args = append(args, "-n")
	}
	args = append(args, "-progress", "pipe:1", "-nostats")
	args = append(args, "-f", "concat", "-safe", "0", "-i", req.ListPath)
	args = append(args, "-map", "0", "-c", "copy")
	if req.Container == model.ContainerMP4 {
		args = append(args, "-movflags", "+faststart")
	}
	args = append(args, "-f", muxer, req.OutputPath)
	return model.RunPlan{
		Executable: req.FFmpegPath,
		Args:       args,
		Preview:    ShellPreview(req.FFmpegPath, args),
		OutputPath: req.OutputPath,
	}, nil
}

// WriteConcatList writes the concat demuxer's list file: one
// "file '<escaped path>'" line per part, with a mode that keeps the
// list private to the user. Paths must not contain newlines — the
// format is line-based — and single quotes are escaped the way the
// demuxer's own parser expects.
func WriteConcatList(path string, parts []string) error {
	if len(parts) == 0 {
		return errors.New("concat list requires at least one segment")
	}
	var b strings.Builder
	for _, part := range parts {
		if strings.ContainsAny(part, "\n") {
			return fmt.Errorf("concat segment path contains a newline: %q", part)
		}
		b.WriteString("file '")
		b.WriteString(escapeConcatPath(part))
		b.WriteString("'\n")
	}
	return os.WriteFile(path, []byte(b.String()), 0o600)
}

// escapeConcatPath renders one path for a concat-list "file '…'" line:
// each single quote closes the quoting, escapes itself, and reopens
// ('\” — the convention ffmpeg's concat demuxer documents).
func escapeConcatPath(path string) string {
	return strings.ReplaceAll(path, "'", `'\''`)
}
