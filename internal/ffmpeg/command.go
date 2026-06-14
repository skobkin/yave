package ffmpeg

import (
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

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

	if isVAAPI(p.Video.Encoder) {
		if p.Video.VAAPIDevice == "" {
			return model.RunPlan{}, errors.New("VAAPI device path is empty")
		}
		args = append(args, "-vaapi_device", p.Video.VAAPIDevice)
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
		args = append(args, subtitleArgs...)
		warnings = append(warnings, subtitleWarnings...)
	}

	videoArgs, err := buildVideoArgs(p.Video, req.Media)
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

func buildVideoArgs(v model.VideoSettings, media model.MediaInfo) ([]string, error) {
	if v.Encoder == "copy" {
		return []string{"-c:v", "copy"}, nil
	}

	args := make([]string, 0, 24)
	filter, err := videoFilter(v, media)
	if err != nil {
		return nil, err
	}
	if filter != "" {
		args = append(args, "-vf", filter)
	}
	args = append(args, "-c:v", v.Encoder)

	switch {
	case isVAAPI(v.Encoder):
		switch strings.ToUpper(v.RateControl) {
		case "", "AUTO":
			if v.Bitrate != "" {
				args = append(args, "-b:v", v.Bitrate)
			}
		case "CQP":
			args = append(args, "-rc_mode", "CQP", "-qp", strconv.Itoa(v.QP))
		case "VBR", "CBR":
			args = append(args, "-rc_mode", strings.ToUpper(v.RateControl))
			if v.Bitrate == "" {
				return nil, fmt.Errorf("%s rate control requires a target bitrate", v.RateControl)
			}
			args = append(args, "-b:v", v.Bitrate)
			if v.Maxrate != "" {
				args = append(args, "-maxrate", v.Maxrate)
			}
			if v.Bufsize != "" {
				args = append(args, "-bufsize", v.Bufsize)
			}
		default:
			return nil, fmt.Errorf("unsupported VAAPI rate control %q", v.RateControl)
		}
		if v.Profile != "" {
			args = append(args, "-profile:v", v.Profile)
		}

	case v.Encoder == "libx264" || v.Encoder == "libx265":
		args = append(args, "-crf", strconv.Itoa(v.CRF))
		if v.Preset != "" {
			args = append(args, "-preset", v.Preset)
		}
		if v.Tune != "" {
			args = append(args, "-tune", v.Tune)
		}
		if v.Profile != "" {
			args = append(args, "-profile:v", v.Profile)
		}

	case v.Encoder == "libsvtav1":
		if strings.EqualFold(v.RateControl, "bitrate") && v.Bitrate != "" {
			args = append(args, "-b:v", v.Bitrate)
		} else {
			args = append(args, "-crf", strconv.Itoa(v.CRF), "-b:v", "0")
		}
		args = append(args, "-preset", defaultString(v.Preset, "6"))

	case v.Encoder == "libvpx-vp9":
		if strings.EqualFold(v.RateControl, "bitrate") && v.Bitrate != "" {
			args = append(args, "-b:v", v.Bitrate)
		} else {
			args = append(args, "-crf", strconv.Itoa(v.CRF), "-b:v", "0")
		}
		if v.Preset != "" {
			args = append(args, "-deadline", v.Preset)
		}
	default:
		return nil, fmt.Errorf("unsupported video encoder %q", v.Encoder)
	}

	return args, nil
}

func buildAudioArgs(a model.AudioSettings) ([]string, error) {
	if a.Mode == model.AudioCopy || a.Encoder == "copy" {
		return []string{"-c:a", "copy"}, nil
	}
	if a.Encoder == "" {
		return nil, errors.New("audio encoder is empty")
	}
	args := []string{"-c:a", a.Encoder}
	switch a.Encoder {
	case "flac":
		args = append(args, "-compression_level", strconv.Itoa(a.Compression))
	case "libopus", "opus", "aac", "libmp3lame", "mp3":
		if a.Bitrate != "" {
			args = append(args, "-b:a", a.Bitrate)
		}
	default:
		return nil, fmt.Errorf("unsupported audio encoder %q", a.Encoder)
	}
	return args, nil
}

func videoFilter(v model.VideoSettings, media model.MediaInfo) (string, error) {
	filters := make([]string, 0, 3)
	if strings.EqualFold(v.FrameRateMode, "convert") {
		if v.FrameRate == "" {
			return "", errors.New("frame rate is required when conversion is enabled")
		}
		filters = append(filters, "fps="+v.FrameRate)
	}

	width, height, scale := targetDimensions(v, media)
	if isVAAPI(v.Encoder) {
		filters = append(filters, "format=nv12", "hwupload")
		if scale {
			part := fmt.Sprintf("scale_vaapi=w=%d:h=%d", width, height)
			if v.KeepAspectRatio {
				part += ":force_original_aspect_ratio=decrease:force_divisible_by=2"
			}
			filters = append(filters, part)
		}
	} else if scale {
		part := fmt.Sprintf("scale=w=%d:h=%d", width, height)
		if v.KeepAspectRatio {
			part += ":force_original_aspect_ratio=decrease:force_divisible_by=2"
		}
		filters = append(filters, part)
	}
	return strings.Join(filters, ","), nil
}

func targetDimensions(v model.VideoSettings, media model.MediaInfo) (int, int, bool) {
	if v.MaxWidth <= 0 && v.MaxHeight <= 0 {
		return 0, 0, false
	}
	src, ok := media.PrimaryVideo()
	if !ok || src.Width <= 0 || src.Height <= 0 {
		return even(v.MaxWidth), even(v.MaxHeight), v.MaxWidth > 0 && v.MaxHeight > 0
	}

	maxW, maxH := v.MaxWidth, v.MaxHeight
	if maxW <= 0 {
		maxW = src.Width
	}
	if maxH <= 0 {
		maxH = src.Height
	}
	if v.PreventUpscaling && src.Width <= maxW && src.Height <= maxH {
		return src.Width, src.Height, false
	}
	if !v.KeepAspectRatio {
		return even(maxW), even(maxH), src.Width != maxW || src.Height != maxH
	}

	ratio := math.Min(float64(maxW)/float64(src.Width), float64(maxH)/float64(src.Height))
	if v.PreventUpscaling {
		ratio = math.Min(ratio, 1)
	}
	w := even(int(math.Floor(float64(src.Width) * ratio)))
	h := even(int(math.Floor(float64(src.Height) * ratio)))
	return w, h, w != src.Width || h != src.Height
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

func isVAAPI(encoder string) bool {
	return strings.HasSuffix(encoder, "_vaapi")
}

func even(v int) int {
	if v <= 0 {
		return v
	}
	return v - v%2
}

func defaultString(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
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
