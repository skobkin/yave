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
	StartTime  string            `json:"start_time"`
	BitRate    string            `json:"bit_rate"`
	Tags       map[string]string `json:"tags"`
}

type probeStream struct {
	Index            int               `json:"index"`
	CodecName        string            `json:"codec_name"`
	CodecType        string            `json:"codec_type"`
	Width            int               `json:"width"`
	Height           int               `json:"height"`
	AverageRate      string            `json:"avg_frame_rate"`
	RealRate         string            `json:"r_frame_rate"`
	PixelFormat      string            `json:"pix_fmt"`
	ColorRange       string            `json:"color_range"`
	SampleRate       string            `json:"sample_rate"`
	Channels         int               `json:"channels"`
	BitsPerRawSample string            `json:"bits_per_raw_sample"`
	BitsPerSample    int               `json:"bits_per_sample"`
	Tags             map[string]string `json:"tags"`
	Disposition      map[string]int    `json:"disposition"`
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

	duration := normalizeDuration(document.Format.Duration, document.Format.StartTime, document.Format.FormatName)
	info := model.MediaInfo{
		Format:       document.Format.FormatName,
		Duration:     duration,
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
				Index:         stream.Index,
				Codec:         stream.CodecName,
				SampleRate:    parseInt(stream.SampleRate),
				Channels:      stream.Channels,
				BitsPerSample: parseBitsPerSample(stream),
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

// normalizeDuration reports the input-relative playable end — the span
// ffmpeg's input-side -ss and the normalized frame index measure — from
// ffprobe's format.duration and format.start_time. The shift is needed
// only for Matroska: its Duration element stores the maximum block
// timestamp, so with a nonzero origin it sits on the container's
// absolute timestamp line. A positive origin (broadcast recordings with
// a PCR offset, -output_ts_offset sources) overshoots the playable span
// (an 8s MKV with start_time 10 reports 18.023s); a negative origin
// (-copyts/-itsoffset recordings, whose first frames sit before zero)
// undershoots it (a 4s MKV with start_time -1.028 reports 3.009s, and
// seeding the trim end with that silently drops the leading second).
// Span-based containers already report the playable span — MP4/MOV and
// MPEG-TS fixtures with -output_ts_offset 2 report duration 8.000 with
// start_time 2.000 and 3.400 — and subtracting there would shorten
// valid inputs by the origin (8s → 6s, 8s → 4.6s). Both Matroska
// variants identify as "matroska,webm". A zero origin needs no shift,
// and a Matroska duration not past its origin is already span-shaped.
func normalizeDuration(raw, start, formatName string) time.Duration {
	duration := parseDuration(raw)
	if strings.Contains(formatName, "matroska") {
		// Signed parse: the origin can be negative. The duration guard
		// keeps a missing duration (0) from fabricating a span out of a
		// negative origin (0 − (−1.5) would read as 1.5s of media).
		if origin, ok := parseSeconds(start); ok && origin != 0 && duration > 0 && duration > origin {
			duration -= origin
		}
	}
	return duration
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

// parseBitsPerSample reports a stream's coded sample depth in bits.
// ffprobe exposes the codec-level depth as bits_per_raw_sample — a
// string like sample_rate, "N/A" when the codec does not report one —
// and the container-level depth as bits_per_sample. FLAC publishes only
// the raw field, which is the one the join's FLAC depth decision
// (ffmpeg.JoinAudioArgs) needs; 0 when neither is present, matching the
// optional-metadata tolerance of the rest of the parser.
func parseBitsPerSample(stream probeStream) int {
	if raw := parseInt(stream.BitsPerRawSample); raw > 0 {
		return raw
	}
	return stream.BitsPerSample
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
