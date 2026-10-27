package validation_test

import (
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"git.skobk.in/skobkin/yave/internal/model"
	"git.skobk.in/skobkin/yave/internal/vaapi"
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

// TestValidateRejectsProfilesOutsideDetectedBuild pins the
// build-narrowed profile check: when capabilities carry a narrowed
// profile list (what `ffmpeg -h encoder=` enumerated), a value
// curated for the project but absent from this build is rejected
// with a clear message instead of failing at conversion time.
// Missing capabilities or un-narrowed lists keep failing open.
func TestValidateRejectsProfilesOutsideDetectedBuild(t *testing.T) {
	profile := validProfile()
	profile.Video.Encoder = "hevc_vaapi"
	profile.Video.RateControl = "CQP"
	profile.Video.QP = 26
	profile.Video.VAAPIDevice = "/dev/dri/renderD128"

	caps := capabilities()
	caps.Encoders["hevc_vaapi"] = model.Encoder{Name: "hevc_vaapi", Profiles: []string{"main", "main10"}}

	// A narrowed-list value passes (case-insensitively).
	pass := profile
	pass.Video.Profile = "MAIN"
	if result := validation.ValidateProfile(pass, caps, nil); !result.Valid() {
		t.Errorf("build-offered profile rejected: %#v", result.Errors)
	}

	// Curated for the project but absent from this build fails.
	reject := profile
	reject.Video.Profile = "rext"
	result := validation.ValidateProfile(reject, caps, nil)
	assertErrorContains(t, result, "profile.video.profile", "not offered by the detected FFmpeg build")

	// No narrowing information (pre-detection, fail-open probe):
	// curated values keep passing.
	if result := validation.ValidateProfile(reject, capabilities(), nil); !result.Valid() {
		t.Errorf("validation without build data must not reject curated values: %#v", result.Errors)
	}
}

// TestValidateWarnsOnProfileBitDepthMismatch pins the source-aware
// profile warning: the software chains pass the source pixel format
// through, so an 8-bit-only profile on a 10-bit source guarantees a
// conversion failure the "(default)" selection would not have.
// Hardware chains are exempt — their lists are nv12-compatible — and
// an empty profile never warns.
func TestValidateWarnsOnProfileBitDepthMismatch(t *testing.T) {
	tenBit := validMedia()
	tenBit.VideoStreams[0].PixelFormat = "yuv420p10le"
	eightBit := validMedia()
	eightBit.VideoStreams[0].PixelFormat = "yuv420p"

	caps := capabilities()

	x264 := validProfile()
	x264.Video.Encoder = "libx264"
	x264.Video.RateControl = "CRF"
	x264.Video.Preset = ""
	x264.Video.Profile = "main"

	if result := validation.ValidateProfile(x264, caps, &tenBit); !result.Valid() {
		t.Fatalf("profile must stay valid (warning, not error): %#v", result.Errors)
	} else {
		assertWarningContains(t, result, "profile.video.profile", "10-bit")
	}

	noProfile := x264
	noProfile.Video.Profile = ""
	if result := validation.ValidateProfile(noProfile, caps, &tenBit); len(result.Warnings) > 0 {
		t.Errorf("empty profile must not warn: %#v", result.Warnings)
	}

	if result := validation.ValidateProfile(x264, caps, &eightBit); len(result.Warnings) > 0 {
		t.Errorf("8-bit source with an 8-bit profile must not warn: %#v", result.Warnings)
	}

	x265main10 := x264
	x265main10.Video.Encoder = "libx265"
	x265main10.Video.Profile = "main10"
	if result := validation.ValidateProfile(x265main10, caps, &tenBit); len(result.Warnings) > 0 {
		t.Errorf("libx265 main10 accepts a 10-bit source: %#v", result.Warnings)
	}

	x265main := x265main10
	x265main.Video.Profile = "main"
	if result := validation.ValidateProfile(x265main, caps, &tenBit); !result.Valid() {
		t.Fatalf("profile must stay valid (warning, not error): %#v", result.Errors)
	} else {
		assertWarningContains(t, result, "profile.video.profile", "10-bit")
	}

	vaapi := x264
	vaapi.Video.Encoder = "hevc_vaapi"
	vaapi.Video.RateControl = "CQP"
	vaapi.Video.QP = 26
	vaapi.Video.VAAPIDevice = "/dev/dri/renderD128"
	vaapi.Video.Profile = "main"
	if result := validation.ValidateProfile(vaapi, caps, &tenBit); len(result.Warnings) > 0 {
		t.Errorf("hardware chain profiles must not trigger the depth warning: %#v", result.Warnings)
	}
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

// TestEmptyRateControlStillValidatesRanges guards the rate-control
// range checks for profiles that omit rate_control. With rate_control
// empty, validation must still apply the encoder's default range
// checks (QP for default-CQP, CRF for default-CRF) — otherwise an
// imported profile with an out-of-range value would pass validation
// even though BuildVideoArgs emits the invalid -qp/-crf.
//
// Before the fix, validateVideoForEncoder routed an empty mode into
// the AUTO branch, which only validates optional bitrate fields. So
// NVENC/QSV profiles with rate_control="" could carry a QP outside
// {0,51}/{0,255} past validation; libx264/libx265/libsvtav1 profiles
// with rate_control="" could carry a CRF outside {0,51}/{0,63}. The
// encoder builders substitute the default for the empty mode and emit
// the un-checked value to ffmpeg, which then rejects it at run time
// instead of failing the validation step.
func TestEmptyRateControlStillValidatesRanges(t *testing.T) {
	tests := []struct {
		name    string
		encoder string
		mutate  func(*model.VideoSettings)
		wantErr string // empty -> expect valid
	}{
		{
			name:    "nvenc empty mode, qp too high",
			encoder: "hevc_nvenc",
			mutate:  func(v *model.VideoSettings) { v.QP = 999; v.Preset = "p4" },
			wantErr: "profile.video.qp",
		},
		{
			name:    "qsv empty mode, qp too high",
			encoder: "hevc_qsv",
			mutate:  func(v *model.VideoSettings) { v.QP = 999; v.Preset = "medium" },
			wantErr: "profile.video.qp",
		},
		{
			name:    "libx264 empty mode, crf too high",
			encoder: "libx264",
			mutate:  func(v *model.VideoSettings) { v.CRF = 999; v.Preset = "medium" },
			wantErr: "profile.video.crf",
		},
		{
			name:    "libx265 empty mode, crf too high",
			encoder: "libx265",
			mutate:  func(v *model.VideoSettings) { v.CRF = 999; v.Preset = "medium" },
			wantErr: "profile.video.crf",
		},
		{
			name:    "libsvtav1 empty mode, crf too high",
			encoder: "libsvtav1",
			mutate:  func(v *model.VideoSettings) { v.CRF = 999; v.Preset = "" },
			wantErr: "profile.video.crf",
		},
		// Sanity: an in-range value still passes after the
		// normalization.
		{
			name:    "nvenc empty mode, in-range qp",
			encoder: "hevc_nvenc",
			mutate:  func(v *model.VideoSettings) { v.QP = 28; v.Preset = "p4" },
			wantErr: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			profile := validProfile()
			profile.Video.Encoder = tt.encoder
			profile.Video.RateControl = "" // the path under test
			profile.Video.Bitrate = ""
			profile.Video.VAAPIDevice = "/dev/dri/renderD128"
			tt.mutate(&profile.Video)

			result := validation.ValidateProfile(profile, capabilities(), nil)
			if tt.wantErr == "" {
				if !result.Valid() {
					t.Fatalf("expected valid, got errors: %#v", result.Errors)
				}
				return
			}
			if result.Valid() {
				t.Fatalf("expected error on %q, got none", tt.wantErr)
			}
			found := false
			for _, e := range result.Errors {
				if e.Field == tt.wantErr {
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("missing error on %q, got: %#v", tt.wantErr, result.Errors)
			}
		})
	}
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
		{name: "svt empty preset (builder defaults to 6)", encoder: "libsvtav1", mode: "CRF", mutate: func(v *model.VideoSettings) { v.CRF = 35; v.Preset = "" }, valid: true},
		{name: "aom empty preset (builder defaults to 4)", encoder: "libaom-av1", mode: "CRF", mutate: func(v *model.VideoSettings) { v.CRF = 30; v.Preset = "" }, valid: true},
		{name: "aom explicit preset", encoder: "libaom-av1", mode: "CRF", mutate: func(v *model.VideoSettings) { v.CRF = 30; v.Preset = "3" }, valid: true},
		{name: "aom preset out of range", encoder: "libaom-av1", mode: "CRF", mutate: func(v *model.VideoSettings) { v.CRF = 30; v.Preset = "9" }, valid: false},
		{name: "vp9 bitrate", encoder: "libvpx-vp9", mode: "BITRATE", mutate: func(v *model.VideoSettings) { v.Bitrate = "2400k" }, valid: true},
		{name: "vp9 unsupported mode", encoder: "libvpx-vp9", mode: "CQP", valid: false},
		// Empty rate_control is treated as the encoder's default; the
		// builders substitute the default so an imported profile that
		// omits RateControl should still validate when the default is
		// an advertised mode. Mirrors the preset fix that consults
		// DefaultPreset().
		{name: "nvenc empty mode (default CQP)", encoder: "hevc_nvenc", mode: "", mutate: func(v *model.VideoSettings) { v.QP = 28; v.Preset = "p4" }, valid: true},
		{name: "qsv empty mode (default CQP)", encoder: "hevc_qsv", mode: "", mutate: func(v *model.VideoSettings) { v.QP = 28; v.Preset = "medium" }, valid: true},
		{name: "libx264 empty mode (default CRF)", encoder: "libx264", mode: "", mutate: func(v *model.VideoSettings) { v.CRF = 23; v.Preset = "medium" }, valid: true},
		{name: "libx265 empty mode (default CRF)", encoder: "libx265", mode: "", mutate: func(v *model.VideoSettings) { v.CRF = 28; v.Preset = "medium" }, valid: true},
		{name: "vaapi empty mode (default Auto)", encoder: "hevc_vaapi", mode: "", mutate: func(v *model.VideoSettings) { v.VAAPIDevice = "/dev/dri/renderD128" }, valid: true},
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

func TestCodecSpecificIntegerRangeValidation(t *testing.T) {
	tests := []struct {
		name    string
		encoder string
		field   string
		min     int
		max     int
		apply   func(*model.Profile, int)
	}{
		{name: "H.264 VAAPI QP", encoder: "h264_vaapi", field: "profile.video.qp", min: 1, max: 51, apply: applyQP},
		{name: "HEVC VAAPI QP", encoder: "hevc_vaapi", field: "profile.video.qp", min: 1, max: 51, apply: applyQP},
		{name: "AV1 VAAPI QP", encoder: "av1_vaapi", field: "profile.video.qp", min: 0, max: 255, apply: applyQP},
		{name: "VP9 VAAPI QP", encoder: "vp9_vaapi", field: "profile.video.qp", min: 0, max: 255, apply: applyQP},
		{name: "x264 CRF", encoder: "libx264", field: "profile.video.crf", min: 0, max: 51, apply: applyCRF},
		{name: "x265 CRF", encoder: "libx265", field: "profile.video.crf", min: 0, max: 51, apply: applyCRF},
		{name: "SVT-AV1 CRF", encoder: "libsvtav1", field: "profile.video.crf", min: 0, max: 63, apply: applyCRF},
		{name: "VP9 CRF", encoder: "libvpx-vp9", field: "profile.video.crf", min: 0, max: 63, apply: applyCRF},
		{name: "SVT-AV1 preset", encoder: "libsvtav1", field: "profile.video.preset", min: 0, max: 13, apply: applyPreset},
		{name: "FLAC compression", encoder: "libx265", field: "profile.audio.compression", min: 0, max: 12, apply: applyCompression},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, value := range []struct {
				name  string
				value int
				valid bool
			}{
				{name: "below", value: tt.min - 1, valid: false},
				{name: "minimum", value: tt.min, valid: true},
				{name: "maximum", value: tt.max, valid: true},
				{name: "above", value: tt.max + 1, valid: false},
			} {
				t.Run(value.name, func(t *testing.T) {
					profile := validProfile()
					profile.Video.Encoder = tt.encoder
					profile.Video.VAAPIDevice = "/dev/dri/renderD128"
					tt.apply(&profile, value.value)

					result := validation.ValidateProfile(profile, capabilities(), nil)
					hasFieldError := false
					for _, issue := range result.Errors {
						if issue.Field == tt.field {
							hasFieldError = true
							break
						}
					}
					if hasFieldError == value.valid {
						t.Fatalf("field error = %v, want %v; errors: %#v", hasFieldError, !value.valid, result.Errors)
					}
				})
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

func applyQP(profile *model.Profile, value int) {
	profile.Video.RateControl = "CQP"
	profile.Video.QP = value
}

func applyCRF(profile *model.Profile, value int) {
	profile.Video.RateControl = "CRF"
	profile.Video.CRF = value
	if profile.Video.Encoder == "libsvtav1" {
		profile.Video.Preset = "6"
	}
}

func applyPreset(profile *model.Profile, value int) {
	profile.Video.RateControl = "CRF"
	profile.Video.CRF = 30
	profile.Video.Preset = strconv.Itoa(value)
}

func applyCompression(profile *model.Profile, value int) {
	profile.Audio.Mode = model.AudioEncode
	profile.Audio.Encoder = "flac"
	profile.Audio.Compression = value
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
		"h264_nvenc", "hevc_nvenc", "av1_nvenc",
		"h264_qsv", "hevc_qsv", "av1_qsv", "vp9_qsv",
		"libx265", "libx264", "libsvtav1", "libvpx-vp9", "libaom-av1",
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

// vaapiDeviceFixture mirrors the owner's dual-GPU host from issue #23:
// renderD128 is an NVIDIA GPU whose VA-API driver decodes only, while
// renderD129 is an AMD iGPU that encodes H.264 and HEVC.
func vaapiDeviceFixture() model.Capabilities {
	caps := capabilities()
	caps.RenderNodes = []string{"/dev/dri/renderD128", "/dev/dri/renderD129"}
	caps.VAAPIDevices = []vaapi.DeviceInfo{
		{
			Path:   "/dev/dri/renderD128",
			Driver: "VA-API NVDEC driver [direct backend]",
			EntryPoints: map[vaapi.Profile][]vaapi.Entrypoint{
				vaapi.ProfileH264Main: {vaapi.EntrypointVLD},
				vaapi.ProfileHEVCMain: {vaapi.EntrypointVLD},
			},
		},
		{
			Path:   "/dev/dri/renderD129",
			Driver: "Mesa Gallium driver (radeonsi)",
			EntryPoints: map[vaapi.Profile][]vaapi.Entrypoint{
				vaapi.ProfileH264Main: {vaapi.EntrypointVLD, vaapi.EntrypointEncSlice},
				vaapi.ProfileHEVCMain: {vaapi.EntrypointVLD, vaapi.EntrypointEncSlice},
			},
		},
	}
	return caps
}

func TestValidateVAAPIDevice(t *testing.T) {
	vaapiProfile := func(p *model.Profile, encoderName, device, profile string) {
		p.Video.Encoder = encoderName
		p.Video.VAAPIDevice = device
		p.Video.RateControl = "CQP"
		p.Video.QP = 28
		p.Video.Profile = profile
	}

	tests := []struct {
		name       string
		caps       func() model.Capabilities
		encoder    string
		device     string
		profile    string
		wantErr    string
		wantWarn   string
		wantNoFlag bool
	}{
		{
			name:       "auto picks the encode-capable device",
			caps:       vaapiDeviceFixture,
			encoder:    "hevc_vaapi",
			device:     "",
			profile:    "main",
			wantNoFlag: true,
		},
		{
			name:       "auto with h264 auto profile also resolves",
			caps:       vaapiDeviceFixture,
			encoder:    "h264_vaapi",
			device:     "",
			profile:    "",
			wantNoFlag: true,
		},
		{
			name:    "auto with no compatible device is an error naming both",
			caps:    vaapiDeviceFixture,
			encoder: "vp9_vaapi",
			device:  "",
			profile: "0",
			wantErr: "no detected VAAPI device can encode vp9_vaapi profile \"0\"",
		},
		{
			name: "auto without probe data fails open",
			caps: func() model.Capabilities {
				caps := capabilities()
				caps.RenderNodes = []string{"/dev/dri/renderD128"}
				return caps
			},
			encoder:    "hevc_vaapi",
			device:     "",
			profile:    "main",
			wantNoFlag: true,
		},
		{
			name: "auto with all probes failed fails open",
			caps: func() model.Capabilities {
				caps := capabilities()
				caps.RenderNodes = []string{"/dev/dri/renderD128"}
				caps.VAAPIDevices = []vaapi.DeviceInfo{{Path: "/dev/dri/renderD128", Err: "open: permission denied"}}
				return caps
			},
			encoder:    "hevc_vaapi",
			device:     "",
			profile:    "main",
			wantNoFlag: true,
		},
		{
			name: "auto without render nodes is an error",
			caps: func() model.Capabilities {
				caps := capabilities()
				caps.RenderNodes = nil
				return caps
			},
			encoder: "hevc_vaapi",
			device:  "",
			profile: "main",
			wantErr: "no VAAPI render device was detected",
		},
		{
			name:    "manual decode-only device is a warning suggesting automatic",
			caps:    vaapiDeviceFixture,
			encoder: "hevc_vaapi",
			device:  "/dev/dri/renderD128",
			profile: "main",
			wantWarn: "the VAAPI device /dev/dri/renderD128 (VA-API NVDEC driver [direct backend]) " +
				"does not offer encoding for hevc_vaapi profile \"main\"; switch the VAAPI device to automatic",
		},
		{
			name: "manual decode-only device with identity warns identically",
			caps: func() model.Capabilities {
				caps := vaapiDeviceFixture()
				caps.VAAPIDevices[0].Identity = vaapi.DeviceIdentity{
					Vendor: "10de", Device: "2191", Driver: "nvidia",
					Name: "TU116M [GeForce GTX 1660 Ti Mobile]", VendorName: "NVIDIA Corporation",
				}
				return caps
			},
			encoder: "hevc_vaapi",
			device:  "/dev/dri/renderD128",
			profile: "main",
			wantWarn: "the VAAPI device /dev/dri/renderD128 (VA-API NVDEC driver [direct backend]) " +
				"does not offer encoding for hevc_vaapi profile \"main\"; switch the VAAPI device to automatic",
		},
		{
			name:       "manual encode-capable device is clean",
			caps:       vaapiDeviceFixture,
			encoder:    "hevc_vaapi",
			device:     "/dev/dri/renderD129",
			profile:    "main",
			wantNoFlag: true,
		},
		{
			name:       "manual rext requirement resolves via HEVCMain",
			caps:       vaapiDeviceFixture,
			encoder:    "hevc_vaapi",
			device:     "/dev/dri/renderD129",
			profile:    "rext",
			wantNoFlag: true,
		},
		{
			name:       "manual device without probe data is clean",
			caps:       capabilities,
			encoder:    "hevc_vaapi",
			device:     "/dev/dri/renderD128",
			profile:    "main",
			wantNoFlag: true,
		},
		{
			name: "manual device not among probed devices is clean",
			caps: func() model.Capabilities {
				caps := capabilities()
				caps.RenderNodes = []string{"/dev/dri/renderD130"}
				caps.VAAPIDevices = []vaapi.DeviceInfo{{Path: "/dev/dri/renderD130", Err: "open: denied"}}
				return caps
			},
			encoder:    "hevc_vaapi",
			device:     "/dev/dri/renderD131",
			profile:    "main",
			wantNoFlag: true,
		},
		{
			name:       "unknown profile value skips the device check",
			caps:       vaapiDeviceFixture,
			encoder:    "hevc_vaapi",
			device:     "",
			profile:    "main10",
			wantNoFlag: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			profile := validProfile()
			vaapiProfile(&profile, tt.encoder, tt.device, tt.profile)
			result := validation.ValidateProfile(profile, tt.caps(), nil)

			if tt.wantErr != "" {
				assertErrorContains(t, result, "profile.video.vaapi_device", tt.wantErr)
			}
			if tt.wantWarn != "" {
				assertWarningContains(t, result, "profile.video.vaapi_device", tt.wantWarn)
			}
			if tt.wantNoFlag {
				for _, issue := range append(slices.Clone(result.Errors), result.Warnings...) {
					if issue.Field == "profile.video.vaapi_device" {
						t.Errorf("unexpected device issue: %+v", issue)
					}
				}
			}
		})
	}
}
