package validation

import (
	"fmt"
	"strings"

	"git.skobk.in/skobkin/yave/internal/encoder"
	"git.skobk.in/skobkin/yave/internal/model"
)

var containerCodecs = map[string]struct {
	video map[string]bool
	audio map[string]bool
}{
	model.ContainerMKV: {
		video: set("h264", "hevc", "av1", "vp9"),
		audio: set("aac", "opus", "mp3", "flac"),
	},
	model.ContainerMP4: {
		video: set("h264", "hevc", "av1"),
		audio: set("aac", "opus"),
	},
	model.ContainerWebM: {
		video: set("vp9", "av1"),
		audio: set("opus"),
	},
}

func validateCompatibility(result *Result, profile model.Profile, container string, media *model.MediaInfo, requireSourceMetadata bool) {
	codecs := containerCodecs[container]
	videoCodec, known := selectedVideoCodec(profile.Video.Encoder, media)
	if known && !codecs.video[videoCodec] {
		result.error("profile.video.encoder", "%s video is not compatible with the %s container", displayCodec(videoCodec), strings.ToUpper(container))
	}
	if requireSourceMetadata && !known && strings.EqualFold(profile.Video.Encoder, "copy") {
		result.error("profile.video.encoder", "cannot validate video copy compatibility without source video codec metadata")
	}

	audioCodecs, known := selectedAudioCodecs(profile.Audio, media)
	if requireSourceMetadata && !known && strings.EqualFold(profile.Audio.Mode, model.AudioCopy) {
		result.error("profile.audio.encoder", "cannot validate audio copy compatibility without source audio codec metadata")
	}
	for _, codec := range audioCodecs {
		if !codecs.audio[codec] {
			result.error("profile.audio.encoder", "%s audio is not compatible with the %s container", displayCodec(codec), strings.ToUpper(container))
		}
	}

	if container == model.ContainerMP4 && contains(audioCodecs, "opus") {
		result.warning("profile.audio.encoder", "Opus in MP4 has limited player compatibility")
	}
	validateSubtitleCompatibility(result, profile.Subtitles, container, media)
}

func validateSubtitleCompatibility(result *Result, subtitles model.SubtitleSettings, container string, media *model.MediaInfo) {
	if !strings.EqualFold(subtitles.Mode, model.SubtitleCopy) || media == nil || len(media.SubtitleStreams) == 0 {
		return
	}
	if container == model.ContainerMKV {
		return
	}

	compatibleCodec := ""
	switch container {
	case model.ContainerMP4:
		compatibleCodec = "mov_text"
	case model.ContainerWebM:
		compatibleCodec = "webvtt"
	default:
		return
	}

	skipped := make([]string, 0)
	for _, stream := range media.SubtitleStreams {
		codec := canonicalCodec(stream.Codec)
		if codec != compatibleCodec {
			skipped = append(skipped, fmt.Sprintf("#%d %s", stream.Index, displayCodec(codec)))
		}
	}
	if len(skipped) > 0 {
		result.warning(
			"profile.subtitles.mode",
			"%s supports only %s subtitle copy; incompatible streams will be skipped: %s",
			strings.ToUpper(container),
			compatibleCodec,
			strings.Join(skipped, ", "),
		)
	}
}

func selectedVideoCodec(encoderName string, media *model.MediaInfo) (string, bool) {
	name := strings.ToLower(strings.TrimSpace(encoderName))
	if name == "copy" {
		if media == nil {
			return "", false
		}
		stream, ok := media.PrimaryVideo()
		if !ok || strings.TrimSpace(stream.Codec) == "" {
			return "", false
		}
		return canonicalCodec(stream.Codec), true
	}
	for _, e := range encoder.DefaultRegistry().All() {
		if !strings.EqualFold(e.Name(), name) {
			continue
		}
		family := strings.ToLower(strings.TrimSpace(e.CodecFamily()))
		if family == "" {
			return "", false
		}
		return family, true
	}
	return "", false
}

func selectedAudioCodecs(audio model.AudioSettings, media *model.MediaInfo) ([]string, bool) {
	if !strings.EqualFold(audio.Mode, model.AudioCopy) {
		codec := canonicalCodec(audio.Encoder)
		return []string{codec}, codec != ""
	}
	if media == nil {
		return nil, false
	}
	if len(media.AudioStreams) == 0 {
		return nil, true
	}
	streams := media.AudioStreams
	if !audio.CopyAllStreams {
		streams = streams[:1]
	}
	codecs := make([]string, 0, len(streams))
	for _, stream := range streams {
		if strings.TrimSpace(stream.Codec) == "" {
			return nil, false
		}
		codecs = append(codecs, canonicalCodec(stream.Codec))
	}
	return codecs, true
}

func canonicalCodec(codec string) string {
	switch strings.ToLower(strings.TrimSpace(codec)) {
	case "libx264", "h264_vaapi", "avc", "avc1":
		return "h264"
	case "libx265", "hevc_vaapi", "h265", "hev1", "hvc1":
		return "hevc"
	case "libsvtav1", "av1_vaapi":
		return "av1"
	case "libvpx-vp9", "vp9_vaapi":
		return "vp9"
	case "libopus":
		return "opus"
	case "libmp3lame":
		return "mp3"
	case "subrip", "srt":
		return "subrip"
	case "webvtt":
		return "webvtt"
	case "mov_text":
		return "mov_text"
	default:
		return strings.ToLower(strings.TrimSpace(codec))
	}
}

func displayCodec(codec string) string {
	if codec == "" {
		return "unknown"
	}
	switch codec {
	case "h264", "hevc", "av1", "vp9", "aac", "opus", "mp3", "flac":
		return strings.ToUpper(codec)
	}
	return codec
}

func set(values ...string) map[string]bool {
	result := make(map[string]bool, len(values))
	for _, value := range values {
		result[value] = true
	}
	return result
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
