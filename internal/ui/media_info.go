package ui

import (
	"fmt"
	"strings"

	"git.skobk.in/skobkin/yave/internal/model"
)

const unknownText = "unknown"

// formatMediaInfo renders the Media information card. Absent metadata is
// printed as "unknown" rather than blanks or zero values.
func formatMediaInfo(info model.MediaInfo) string {
	duration := unknownText
	if info.Duration > 0 {
		duration = formatClock(info.Duration)
	}
	size := unknownText
	if info.Size > 0 {
		size = formatByteSize(info.Size)
	}
	format := info.Format
	if format == "" {
		format = unknownText
	}
	lines := []string{fmt.Sprintf("Format: %s | Duration: %s | Size: %s | Bitrate: %s",
		format, duration, size, formatBitrate(info.Bitrate))}
	for i, v := range info.VideoStreams {
		label := "Video"
		if len(info.VideoStreams) > 1 {
			label = fmt.Sprintf("Video %d", i+1)
		}
		lines = append(lines, label+": "+describeVideo(v))
	}
	for _, v := range info.CoverArtStreams {
		lines = append(lines, fmt.Sprintf("Cover art: %s, %s", orUnknown(v.Codec), dimensionsText(v.Width, v.Height)))
	}
	for i, a := range info.AudioStreams {
		lines = append(lines, fmt.Sprintf("Audio %d: %s", i+1, describeAudio(a)))
	}
	for i, s := range info.SubtitleStreams {
		lines = append(lines, fmt.Sprintf("Subtitle %d: %s", i+1, describeSubtitle(s)))
	}
	if len(info.SubtitleStreams) == 0 {
		lines = append(lines, "Subtitles: none")
	}
	lines = append(lines, fmt.Sprintf("Chapters: %d", info.ChapterCount))
	return strings.Join(lines, "\n")
}

func describeVideo(v model.VideoStream) string {
	parts := []string{
		orUnknown(v.Codec),
		dimensionsText(v.Width, v.Height),
		frameRateText(v.FrameRate),
		orUnknown(v.PixelFormat),
	}
	if v.ColorRange != "" {
		parts = append(parts, v.ColorRange+" range")
	}
	if color := colorText(v); color != "" {
		parts = append(parts, color)
	}
	if v.Rotation != 0 {
		parts = append(parts, fmt.Sprintf("rotated %d°", v.Rotation))
	}
	if v.Bitrate > 0 {
		parts = append(parts, formatBitrate(v.Bitrate))
	}
	return strings.Join(parts, ", ") + streamNameSuffix(v.Language, v.Title)
}

func describeAudio(a model.AudioStream) string {
	rate := unknownText
	if a.SampleRate > 0 {
		rate = fmt.Sprintf("%d Hz", a.SampleRate)
	}
	channels := unknownText + " channels"
	switch {
	case a.ChannelLayout != "" && a.Channels > 0:
		channels = fmt.Sprintf("%s (%d ch)", a.ChannelLayout, a.Channels)
	case a.ChannelLayout != "":
		channels = a.ChannelLayout
	case a.Channels > 0:
		channels = fmt.Sprintf("%d channels", a.Channels)
	}
	parts := []string{orUnknown(a.Codec), rate, channels}
	if a.Bitrate > 0 {
		parts = append(parts, formatBitrate(a.Bitrate))
	}
	return strings.Join(parts, ", ") + streamNameSuffix(a.Language, a.Title)
}

func describeSubtitle(s model.SubtitleStream) string {
	return orUnknown(s.Codec) + streamNameSuffix(s.Language, s.Title)
}

// colorText summarizes HDR / colorimetry: "HDR10 (PQ), bt2020" style.
func colorText(v model.VideoStream) string {
	var parts []string
	switch v.ColorTransfer {
	case "smpte2084":
		parts = append(parts, "HDR (PQ)")
	case "arib-std-b67":
		parts = append(parts, "HDR (HLG)")
	}
	if v.ColorPrimaries != "" && v.ColorPrimaries != "unknown" {
		parts = append(parts, v.ColorPrimaries)
	} else if v.ColorSpace != "" && v.ColorSpace != "unknown" {
		parts = append(parts, v.ColorSpace)
	}
	return strings.Join(parts, ", ")
}

func streamNameSuffix(language, title string) string {
	var parts []string
	if language != "" && language != "und" {
		parts = append(parts, language)
	}
	if title != "" {
		parts = append(parts, fmt.Sprintf("%q", title))
	}
	if len(parts) == 0 {
		return ""
	}
	return " — " + strings.Join(parts, " ")
}

func dimensionsText(width, height int) string {
	if width <= 0 || height <= 0 {
		return unknownText + " size"
	}
	return fmt.Sprintf("%dx%d", width, height)
}

func frameRateText(rate string) string {
	rate = strings.TrimSpace(rate)
	if rate == "" || rate == "0" || rate == "0/0" {
		return unknownText + " fps"
	}
	return rate + " fps"
}

func orUnknown(value string) string {
	if strings.TrimSpace(value) == "" {
		return unknownText
	}
	return value
}

// formatBitrate renders bits per second in the most readable unit.
func formatBitrate(value int64) string {
	switch {
	case value <= 0:
		return unknownText
	case value >= 1_000_000:
		return fmt.Sprintf("%.1f Mbps", float64(value)/1_000_000)
	default:
		return fmt.Sprintf("%d kbps", (value+500)/1000)
	}
}
