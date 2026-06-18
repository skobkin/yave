package ffprobe

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"git.skobk.in/skobkin/yave/internal/model"
)

type probeDocument struct {
	Format   probeFormat    `json:"format"`
	Streams  []probeStream  `json:"streams"`
	Chapters []probeChapter `json:"chapters"`
}

type probeFormat struct {
	FormatName string            `json:"format_name"`
	Duration   string            `json:"duration"`
	BitRate    string            `json:"bit_rate"`
	Tags       map[string]string `json:"tags"`
}

type probeStream struct {
	Index       int               `json:"index"`
	CodecName   string            `json:"codec_name"`
	CodecType   string            `json:"codec_type"`
	Width       int               `json:"width"`
	Height      int               `json:"height"`
	AverageRate string            `json:"avg_frame_rate"`
	RealRate    string            `json:"r_frame_rate"`
	PixelFormat string            `json:"pix_fmt"`
	ColorRange  string            `json:"color_range"`
	SampleRate  string            `json:"sample_rate"`
	Channels    int               `json:"channels"`
	Tags        map[string]string `json:"tags"`
	Disposition map[string]int    `json:"disposition"`
}

type probeChapter struct {
	ID        int               `json:"id"`
	TimeBase  string            `json:"time_base"`
	StartTime string            `json:"start_time"`
	EndTime   string            `json:"end_time"`
	Tags      map[string]string `json:"tags"`
}

// Inspect runs ffprobe and converts its JSON output into the shared media model.
func Inspect(ctx context.Context, executable, inputPath string) (model.MediaInfo, error) {
	executable = strings.TrimSpace(executable)
	if executable == "" {
		executable = "ffprobe"
	}
	resolved, err := exec.LookPath(executable)
	if err != nil {
		return model.MediaInfo{}, fmt.Errorf("ffprobe executable %q not found: %w", executable, err)
	}
	if strings.TrimSpace(inputPath) == "" {
		return model.MediaInfo{}, fmt.Errorf("inspect media: input path is empty")
	}

	args := []string{
		"-v", "error",
		"-print_format", "json",
		"-show_format",
		"-show_streams",
		"-show_chapters",
		inputPath,
	}
	output, err := exec.CommandContext(ctx, resolved, args...).CombinedOutput() //nolint:gosec // G204: resolved via exec.LookPath
	if err != nil {
		if ctx.Err() != nil {
			return model.MediaInfo{}, ctx.Err()
		}
		detail := strings.TrimSpace(string(output))
		if detail == "" {
			detail = err.Error()
		}
		return model.MediaInfo{}, fmt.Errorf("inspect media %q with ffprobe: %s", inputPath, detail)
	}
	info, err := Parse(output)
	if err != nil {
		return model.MediaInfo{}, fmt.Errorf("inspect media %q: %w", inputPath, err)
	}
	info.Path = inputPath
	return info, nil
}

// Parse converts ffprobe's JSON document into model.MediaInfo. Numeric values
// represented as absent, empty, "N/A", or malformed strings are treated as
// unknown rather than making otherwise useful inspection data fail.
func Parse(data []byte) (model.MediaInfo, error) {
	var document probeDocument
	if err := json.Unmarshal(data, &document); err != nil {
		return model.MediaInfo{}, fmt.Errorf("decode ffprobe JSON: %w", err)
	}

	info := model.MediaInfo{
		Format:       document.Format.FormatName,
		Duration:     parseDuration(document.Format.Duration),
		Bitrate:      parseInt64(document.Format.BitRate),
		ChapterCount: len(document.Chapters),
	}
	for _, stream := range document.Streams {
		switch stream.CodecType {
		case "video":
			info.VideoStreams = append(info.VideoStreams, model.VideoStream{
				Index:       stream.Index,
				Codec:       stream.CodecName,
				Width:       stream.Width,
				Height:      stream.Height,
				FrameRate:   parseFrameRate(stream.AverageRate, stream.RealRate),
				PixelFormat: stream.PixelFormat,
				ColorRange:  stream.ColorRange,
			})
		case "audio":
			info.AudioStreams = append(info.AudioStreams, model.AudioStream{
				Index:      stream.Index,
				Codec:      stream.CodecName,
				SampleRate: parseInt(stream.SampleRate),
				Channels:   stream.Channels,
			})
		case "subtitle":
			info.SubtitleStreams = append(info.SubtitleStreams, model.SubtitleStream{
				Index: stream.Index,
				Codec: stream.CodecName,
			})
		}
	}
	return info, nil
}

func parseDuration(value string) time.Duration {
	seconds, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	if err != nil || seconds < 0 {
		return 0
	}
	return time.Duration(seconds * float64(time.Second))
}

func parseInt64(value string) int64 {
	number, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil || number < 0 {
		return 0
	}
	return number
}

func parseInt(value string) int {
	number, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || number < 0 {
		return 0
	}
	return number
}

func parseFrameRate(candidates ...string) string {
	for _, candidate := range candidates {
		candidate = strings.TrimSpace(candidate)
		parts := strings.Split(candidate, "/")
		if len(parts) != 2 {
			continue
		}
		numerator, numeratorErr := strconv.ParseInt(parts[0], 10, 64)
		denominator, denominatorErr := strconv.ParseInt(parts[1], 10, 64)
		if numeratorErr == nil && denominatorErr == nil && numerator > 0 && denominator > 0 {
			return candidate
		}
	}
	return ""
}
