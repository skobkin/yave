package validation_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git.skobk.in/skobkin/yave/internal/model"
	"git.skobk.in/skobkin/yave/internal/validation"
)

func TestValidateAcceptsRunnableProfile(t *testing.T) {
	input := createInput(t)
	profile := validProfile()
	media := validMedia()

	result := validation.Validate(profile, &media, capabilities(), input, filepath.Join(t.TempDir(), "output.mkv"))

	if !result.Valid() {
		t.Fatalf("expected valid result, got errors: %#v", result.Errors)
	}
	if len(result.Warnings) != 0 {
		t.Fatalf("expected no warnings, got: %#v", result.Warnings)
	}
}

func TestValidateRequiredFieldsAndPaths(t *testing.T) {
	profile := validProfile()
	profile.Name = ""
	profile.Container = ""
	profile.OutputTemplate = ""
	profile.Video.Encoder = ""
	profile.Audio.Mode = ""
	profile.Subtitles.Mode = ""

	result := validation.Validate(profile, nil, model.Capabilities{}, "", "")

	assertErrorFields(t, result,
		"profile.name",
		"profile.container",
		"profile.output_template",
		"profile.video.encoder",
		"profile.audio.mode",
		"profile.subtitles.mode",
		"input_path",
		"output_path",
	)
}

func TestValidateRejectsUnavailableEncoders(t *testing.T) {
	profile := validProfile()
	caps := capabilities()
	delete(caps.Encoders, "libx265")
	delete(caps.Encoders, "aac")
	profile.Audio.Mode = model.AudioEncode
	profile.Audio.Encoder = "aac"

	result := validation.ValidateProfile(profile, caps, nil)

	assertErrorContains(t, result, "profile.video.encoder", "not available")
	assertErrorContains(t, result, "profile.audio.encoder", "not available")
}

func TestValidateProfileAllowsCopyBeforeMediaIsLoaded(t *testing.T) {
	profile := validProfile()
	profile.Video.Encoder = "copy"

	result := validation.ValidateProfile(profile, capabilities(), nil)

	if !result.Valid() {
		t.Fatalf("profile-only validation should defer source codec checks: %#v", result.Errors)
	}
}

func TestValidateRunRequiresMetadataForCopyCompatibility(t *testing.T) {
	input := createInput(t)
	profile := validProfile()
	profile.Video.Encoder = "copy"

	result := validation.Validate(profile, nil, capabilities(), input, filepath.Join(t.TempDir(), "output.mkv"))

	assertErrorContains(t, result, "profile.video.encoder", "without source video codec metadata")
	assertErrorContains(t, result, "profile.audio.encoder", "without source audio codec metadata")
}

func TestContainerCompatibility(t *testing.T) {
	tests := []struct {
		name       string
		container  string
		video      string
		audioMode  string
		audio      string
		videoCodec string
		audioCodec string
		valid      bool
	}{
		{name: "mkv modern codecs", container: "mkv", video: "libx265", audioMode: "encode", audio: "flac", valid: true},
		{name: "mp4 h264 aac", container: "mp4", video: "libx264", audioMode: "encode", audio: "aac", valid: true},
		{name: "webm vp9 opus", container: "webm", video: "libvpx-vp9", audioMode: "encode", audio: "libopus", valid: true},
		{name: "webm rejects h264", container: "webm", video: "libx264", audioMode: "encode", audio: "libopus", valid: false},
		{name: "mp4 rejects flac", container: "mp4", video: "libx264", audioMode: "encode", audio: "flac", valid: false},
		{name: "copy compatible source", container: "webm", video: "copy", audioMode: "copy", audio: "copy", videoCodec: "vp9", audioCodec: "opus", valid: true},
		{name: "copy incompatible video", container: "mp4", video: "copy", audioMode: "copy", audio: "copy", videoCodec: "vp9", audioCodec: "aac", valid: false},
		{name: "copy incompatible audio", container: "webm", video: "copy", audioMode: "copy", audio: "copy", videoCodec: "av1", audioCodec: "aac", valid: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			profile := validProfile()
			profile.Container = tt.container
			profile.Video.Encoder = tt.video
			if tt.video == "libx264" {
				profile.Video.CRF = 23
			}
			if tt.video == "libvpx-vp9" {
				profile.Video.CRF = 31
			}
			profile.Audio.Mode = tt.audioMode
			profile.Audio.Encoder = tt.audio
			if tt.audio == "libopus" {
				profile.Audio.Bitrate = "160k"
			}
			if tt.audio == "flac" {
				profile.Audio.Compression = 8
			}
			media := validMedia()
			if tt.videoCodec != "" {
				media.VideoStreams[0].Codec = tt.videoCodec
			}
			if tt.audioCodec != "" {
				media.AudioStreams[0].Codec = tt.audioCodec
			}

			result := validation.ValidateProfile(profile, capabilities(), &media)
			if result.Valid() != tt.valid {
				t.Fatalf("valid=%v, want %v; errors: %#v", result.Valid(), tt.valid, result.Errors)
			}
		})
	}
}

func TestAudioCopyAllStreamsValidatesEverySourceCodec(t *testing.T) {
	profile := validProfile()
	profile.Container = model.ContainerWebM
	profile.Video.Encoder = "libvpx-vp9"
	profile.Video.CRF = 31
	media := validMedia()
	media.VideoStreams[0].Codec = "vp9"
	media.AudioStreams = append(media.AudioStreams, model.AudioStream{Index: 2, Codec: "aac"})

	firstOnly := validation.ValidateProfile(profile, capabilities(), &media)
	if !firstOnly.Valid() {
		t.Fatalf("first compatible audio stream should be accepted: %#v", firstOnly.Errors)
	}

	profile.Audio.CopyAllStreams = true
	allStreams := validation.ValidateProfile(profile, capabilities(), &media)
	assertErrorContains(t, allStreams, "profile.audio.encoder", "AAC audio is not compatible")
}

func TestSubtitleCompatibilityWarnings(t *testing.T) {
	tests := []struct {
		container string
		streams   []model.SubtitleStream
		warnings  int
		contains  string
	}{
		{container: "mkv", streams: []model.SubtitleStream{{Index: 3, Codec: "subrip"}}, warnings: 0},
		{container: "mp4", streams: []model.SubtitleStream{{Index: 3, Codec: "mov_text"}}, warnings: 0},
		{container: "mp4", streams: []model.SubtitleStream{{Index: 3, Codec: "subrip"}}, warnings: 1, contains: "mov_text"},
		{container: "webm", streams: []model.SubtitleStream{{Index: 3, Codec: "webvtt"}}, warnings: 0},
		{container: "webm", streams: []model.SubtitleStream{{Index: 3, Codec: "ass"}}, warnings: 1, contains: "webvtt"},
	}

	for _, tt := range tests {
		t.Run(tt.container+"_"+tt.contains, func(t *testing.T) {
			profile := validProfile()
			profile.Container = tt.container
			if tt.container == "webm" {
				profile.Video.Encoder = "libvpx-vp9"
				profile.Video.CRF = 31
				profile.Audio.Mode = model.AudioEncode
				profile.Audio.Encoder = "libopus"
			}
			media := validMedia()
			if tt.container == "mp4" {
				media.AudioStreams[0].Codec = "aac"
			}
			media.SubtitleStreams = tt.streams

			result := validation.ValidateProfile(profile, capabilities(), &media)
			if len(result.Warnings) != tt.warnings {
				t.Fatalf("warnings=%#v, want %d", result.Warnings, tt.warnings)
			}
			if tt.contains != "" && !strings.Contains(result.Warnings[0].Message, tt.contains) {
				t.Fatalf("warning %q does not contain %q", result.Warnings[0].Message, tt.contains)
			}
		})
	}
}

func TestMP4OpusProducesWarning(t *testing.T) {
	profile := validProfile()
	profile.Container = model.ContainerMP4
	profile.Video.Encoder = "libx264"
	profile.Video.CRF = 23
	profile.Audio.Mode = model.AudioEncode
	profile.Audio.Encoder = "libopus"

	result := validation.ValidateProfile(profile, capabilities(), nil)

	if !result.Valid() {
		t.Fatalf("MP4 Opus should be allowed with a warning: %#v", result.Errors)
	}
	assertWarningContains(t, result, "profile.audio.encoder", "limited player compatibility")
}

func TestVideoRateControlValidation(t *testing.T) {
	tests := []struct {
		name    string
		encoder string
		mode    string
		mutate  func(*model.VideoSettings)
		valid   bool
	}{
		{name: "vaapi cqp", encoder: "hevc_vaapi", mode: "CQP", mutate: func(v *model.VideoSettings) { v.QP = 30 }, valid: true},
		{name: "vaapi qp too low", encoder: "hevc_vaapi", mode: "CQP", mutate: func(v *model.VideoSettings) { v.QP = 0 }, valid: false},
		{name: "vaapi vbr bitrate", encoder: "hevc_vaapi", mode: "VBR", mutate: func(v *model.VideoSettings) { v.Bitrate = "6000k" }, valid: true},
		{name: "vaapi vbr missing bitrate", encoder: "hevc_vaapi", mode: "VBR", valid: false},
		{name: "vaapi invalid bitrate", encoder: "hevc_vaapi", mode: "AUTO", mutate: func(v *model.VideoSettings) { v.Bitrate = "fast" }, valid: false},
		{name: "x264 crf", encoder: "libx264", mode: "CRF", mutate: func(v *model.VideoSettings) { v.CRF = 23 }, valid: true},
		{name: "x265 crf too high", encoder: "libx265", mode: "CRF", mutate: func(v *model.VideoSettings) { v.CRF = 52 }, valid: false},
		{name: "svt crf and preset", encoder: "libsvtav1", mode: "CRF", mutate: func(v *model.VideoSettings) { v.CRF = 35; v.Preset = "6" }, valid: true},
		{name: "svt invalid preset", encoder: "libsvtav1", mode: "CRF", mutate: func(v *model.VideoSettings) { v.CRF = 35; v.Preset = "14" }, valid: false},
		{name: "vp9 bitrate", encoder: "libvpx-vp9", mode: "BITRATE", mutate: func(v *model.VideoSettings) { v.Bitrate = "2400k" }, valid: true},
		{name: "vp9 unsupported mode", encoder: "libvpx-vp9", mode: "CQP", valid: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			profile := validProfile()
			profile.Video.Encoder = tt.encoder
			profile.Video.RateControl = tt.mode
			profile.Video.Bitrate = ""
			profile.Video.VAAPIDevice = "/dev/dri/renderD128"
			if tt.mutate != nil {
				tt.mutate(&profile.Video)
			}

			result := validation.ValidateProfile(profile, capabilities(), nil)
			if result.Valid() != tt.valid {
				t.Fatalf("valid=%v, want %v; errors: %#v", result.Valid(), tt.valid, result.Errors)
			}
		})
	}
}

func TestAudioRanges(t *testing.T) {
	profile := validProfile()
	profile.Audio.Mode = model.AudioEncode
	profile.Audio.Encoder = "aac"
	profile.Audio.Bitrate = "96k"

	result := validation.ValidateProfile(profile, capabilities(), nil)
	assertErrorContains(t, result, "profile.audio.bitrate", "must be one of")

	profile.Audio.Encoder = "flac"
	profile.Audio.Compression = 13
	result = validation.ValidateProfile(profile, capabilities(), nil)
	assertErrorContains(t, result, "profile.audio.compression", "between 0 and 12")
}

func TestVideoCopyForbidsTransforms(t *testing.T) {
	profile := validProfile()
	profile.Video.Encoder = "copy"
	profile.Video.MaxWidth = 1280
	profile.Video.FrameRateMode = "convert"
	profile.Video.FrameRate = "24"

	result := validation.ValidateProfile(profile, capabilities(), nil)

	assertErrorContains(t, result, "profile.video.encoder", "scaling")
	assertErrorContains(t, result, "profile.video.encoder", "frame rate conversion")
}

func TestOutputMustDifferFromInput(t *testing.T) {
	input := createInput(t)
	profile := validProfile()
	media := validMedia()

	result := validation.Validate(profile, &media, capabilities(), input, filepath.Join(filepath.Dir(input), ".", filepath.Base(input)))

	assertErrorContains(t, result, "output_path", "must differ")
}

func TestOutputDirectoryMustExist(t *testing.T) {
	input := createInput(t)
	profile := validProfile()
	media := validMedia()
	output := filepath.Join(t.TempDir(), "missing", "output.mkv")

	result := validation.Validate(profile, &media, capabilities(), input, output)

	assertErrorContains(t, result, "output_path", "directory does not exist")
}

func validProfile() model.Profile {
	return model.Profile{
		Name:           "Test",
		Container:      model.ContainerMKV,
		OutputTemplate: "{dir}/{name}_out.{ext}",
		Video: model.VideoSettings{
			Encoder:          "libx265",
			RateControl:      "CRF",
			CRF:              28,
			Preset:           "medium",
			FrameRateMode:    "keep",
			KeepAspectRatio:  true,
			PreventUpscaling: true,
		},
		Audio: model.AudioSettings{
			Mode:    model.AudioCopy,
			Encoder: "copy",
			Bitrate: "160k",
		},
		Subtitles: model.SubtitleSettings{Mode: model.SubtitleCopy},
	}
}

func validMedia() model.MediaInfo {
	return model.MediaInfo{
		VideoStreams: []model.VideoStream{{Index: 0, Codec: "hevc", Width: 1920, Height: 1080}},
		AudioStreams: []model.AudioStream{{Index: 1, Codec: "opus", Channels: 2}},
	}
}

func capabilities() model.Capabilities {
	names := []string{
		"hevc_vaapi", "h264_vaapi", "av1_vaapi", "vp9_vaapi",
		"libx265", "libx264", "libsvtav1", "libvpx-vp9",
		"libopus", "opus", "aac", "libmp3lame", "mp3", "flac",
	}
	encoders := make(map[string]model.Encoder, len(names))
	for _, name := range names {
		encoders[name] = model.Encoder{Name: name}
	}
	return model.Capabilities{Encoders: encoders}
}

func createInput(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "input.mkv")
	if err := os.WriteFile(path, []byte("input"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func assertErrorFields(t *testing.T, result validation.Result, fields ...string) {
	t.Helper()
	for _, field := range fields {
		found := false
		for _, issue := range result.Errors {
			if issue.Field == field {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("missing error for %s; errors: %#v", field, result.Errors)
		}
	}
}

func assertErrorContains(t *testing.T, result validation.Result, field, text string) {
	t.Helper()
	for _, issue := range result.Errors {
		if issue.Field == field && strings.Contains(issue.Message, text) {
			return
		}
	}
	t.Fatalf("missing %s error containing %q; errors: %#v", field, text, result.Errors)
}

func assertWarningContains(t *testing.T, result validation.Result, field, text string) {
	t.Helper()
	for _, issue := range result.Warnings {
		if issue.Field == field && strings.Contains(issue.Message, text) {
			return
		}
	}
	t.Fatalf("missing %s warning containing %q; warnings: %#v", field, text, result.Warnings)
}
