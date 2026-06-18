package ffmpeg

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"sort"
	"strings"

	"git.skobk.in/skobkin/yave/internal/model"
)

const (
	MediaVideo = "video"
	MediaAudio = "audio"

	KindHardware    = "hardware"
	KindSoftware    = "software"
	KindEncode      = "encode"
	KindPassthrough = "passthrough"
)

var curatedEncoders = []model.Encoder{
	{Name: "hevc_vaapi", Label: "H.265 / HEVC VAAPI", Media: MediaVideo, Kind: KindHardware, Priority: 100},
	{Name: "h264_vaapi", Label: "H.264 VAAPI", Media: MediaVideo, Kind: KindHardware, Priority: 100},
	{Name: "libx265", Label: "H.265 / HEVC x265", Media: MediaVideo, Kind: KindSoftware, Priority: 90},
	{Name: "libx264", Label: "H.264 x264", Media: MediaVideo, Kind: KindSoftware, Priority: 90},
	{Name: "av1_vaapi", Label: "AV1 VAAPI", Media: MediaVideo, Kind: KindHardware, Priority: 70},
	{Name: "vp9_vaapi", Label: "VP9 VAAPI", Media: MediaVideo, Kind: KindHardware, Priority: 70},
	{Name: "libsvtav1", Label: "AV1 SVT-AV1", Media: MediaVideo, Kind: KindSoftware, Priority: 60},
	{Name: "libvpx-vp9", Label: "VP9 libvpx-vp9", Media: MediaVideo, Kind: KindSoftware, Priority: 60},
	{Name: "libopus", Label: "Opus", Media: MediaAudio, Kind: KindEncode, Priority: 100},
	{Name: "opus", Label: "Opus", Media: MediaAudio, Kind: KindEncode, Priority: 90},
	{Name: "aac", Label: "AAC", Media: MediaAudio, Kind: KindEncode, Priority: 100},
	{Name: "libmp3lame", Label: "MP3", Media: MediaAudio, Kind: KindEncode, Priority: 70},
	{Name: "mp3", Label: "MP3", Media: MediaAudio, Kind: KindEncode, Priority: 60},
	{Name: "flac", Label: "FLAC", Media: MediaAudio, Kind: KindEncode, Priority: 70},
}

// Discover resolves both programs and queries their versions and FFmpeg's
// encoder and hardware-acceleration capabilities.
func Discover(ctx context.Context, config model.FFmpegConfig) (model.Capabilities, error) {
	ffmpegPath, err := ResolveExecutable(config.FFmpegPath, "ffmpeg")
	if err != nil {
		return model.Capabilities{}, err
	}
	ffprobePath, err := ResolveExecutable(config.FFprobePath, "ffprobe")
	if err != nil {
		return model.Capabilities{}, err
	}

	ffmpegVersion, err := commandOutput(ctx, ffmpegPath, "-hide_banner", "-version")
	if err != nil {
		return model.Capabilities{}, fmt.Errorf("detect FFmpeg version: %w", err)
	}
	ffprobeVersion, err := commandOutput(ctx, ffprobePath, "-hide_banner", "-version")
	if err != nil {
		return model.Capabilities{}, fmt.Errorf("detect ffprobe version: %w", err)
	}
	encodersOutput, err := commandOutput(ctx, ffmpegPath, "-hide_banner", "-encoders")
	if err != nil {
		return model.Capabilities{}, fmt.Errorf("detect FFmpeg encoders: %w", err)
	}
	hwaccelsOutput, err := commandOutput(ctx, ffmpegPath, "-hide_banner", "-hwaccels")
	if err != nil {
		return model.Capabilities{}, fmt.Errorf("detect FFmpeg hardware accelerators: %w", err)
	}

	return model.Capabilities{
		FFmpegPath:     ffmpegPath,
		FFprobePath:    ffprobePath,
		FFmpegVersion:  ParseVersion(ffmpegVersion),
		FFprobeVersion: ParseVersion(ffprobeVersion),
		Encoders:       ParseEncoders(encodersOutput),
		HWAccels:       ParseHWAccels(hwaccelsOutput),
	}, nil
}

// ResolveExecutable resolves configured through PATH when it is a bare
// command, while also accepting explicit relative and absolute paths.
func ResolveExecutable(configured, fallback string) (string, error) {
	name := strings.TrimSpace(configured)
	if name == "" {
		name = fallback
	}
	path, err := exec.LookPath(name)
	if err != nil {
		return "", fmt.Errorf("%s executable %q not found: %w", fallback, name, err)
	}
	return path, nil
}

// ParseVersion returns the first non-empty output line. FFmpeg and ffprobe
// print their complete version identification there.
func ParseVersion(output string) string {
	for line := range strings.SplitSeq(output, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			return line
		}
	}
	return ""
}

// ParseEncoders extracts available encoder names and applies YAVE's curated
// whitelist and native/external encoder preferences.
func ParseEncoders(output string) map[string]model.Encoder {
	available := make(map[string]struct{})
	for line := range strings.SplitSeq(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || len(fields[0]) < 6 {
			continue
		}
		mediaFlag := fields[0][0]
		if mediaFlag != 'V' && mediaFlag != 'A' {
			continue
		}
		available[fields[1]] = struct{}{}
	}

	result := make(map[string]model.Encoder)
	for _, encoder := range curatedEncoders {
		if _, ok := available[encoder.Name]; ok {
			result[encoder.Name] = encoder
		}
	}
	preferEncoder(result, "libopus", "opus")
	preferEncoder(result, "libmp3lame", "mp3")

	// Copy is an FFmpeg pseudo-encoder valid for both media types and therefore
	// does not appear in `ffmpeg -encoders`.
	result["copy"] = model.Encoder{
		Name:     "copy",
		Label:    "Copy",
		Media:    "video,audio",
		Kind:     KindPassthrough,
		Priority: 100,
	}
	return result
}

func preferEncoder(encoders map[string]model.Encoder, preferred, fallback string) {
	if _, ok := encoders[preferred]; ok {
		delete(encoders, fallback)
	}
}

// ParseHWAccels parses the one-name-per-line section emitted by -hwaccels.
func ParseHWAccels(output string) []string {
	var result []string
	seen := make(map[string]struct{})
	inList := false
	for line := range strings.SplitSeq(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.EqualFold(strings.TrimSuffix(line, ":"), "Hardware acceleration methods") {
			inList = true
			continue
		}
		if !inList || strings.ContainsAny(line, " \t") {
			continue
		}
		if _, ok := seen[line]; ok {
			continue
		}
		seen[line] = struct{}{}
		result = append(result, line)
	}
	sort.Strings(result)
	return result
}

func commandOutput(ctx context.Context, executable string, args ...string) (string, error) {
	output, err := exec.CommandContext(ctx, executable, args...).CombinedOutput() //nolint:gosec // G204: executable resolved via exec.LookPath
	if err != nil {
		detail := strings.TrimSpace(string(output))
		if detail == "" {
			detail = err.Error()
		}
		if errors.Is(ctx.Err(), context.Canceled) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return "", ctx.Err()
		}
		return "", fmt.Errorf("%s %s failed: %s", executable, strings.Join(args, " "), detail)
	}
	return string(output), nil
}
