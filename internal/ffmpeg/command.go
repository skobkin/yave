package ffmpeg

import (
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"

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
}

func BuildCommand(req BuildRequest) (model.RunPlan, error) {
	if req.FFmpegPath == "" {
		return model.RunPlan{}, errors.New("FFmpeg path is empty")
	}
	if req.InputPath == "" || req.OutputPath == "" {
		return model.RunPlan{}, errors.New("input and output paths are required")
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

	args = append(args, "-i", req.InputPath, "-map", "0:v:0")
	if p.Audio.CopyAllStreams {
		args = append(args, "-map", "0:a?")
	} else {
		args = append(args, "-map", "0:a:0?")
	}

	warnings := make([]string, 0)
	if p.Subtitles.Mode == model.SubtitleCopy {
		subtitleArgs, subtitleWarnings := subtitleMapping(p.Container, req.Media.SubtitleStreams)
		args = append(args, subtitleArgs...)
		warnings = append(warnings, subtitleWarnings...)
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

// buildAudioArgs composes the post -c:a flags for a profile. The
// "copy" mode is treated as a passthrough and dispatches to a stub
// `-c:a copy`; every other encoder goes through the registry.
func buildAudioArgs(a model.AudioSettings) ([]string, error) {
	if a.Mode == model.AudioCopy || strings.EqualFold(strings.TrimSpace(a.Encoder), "copy") {
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
