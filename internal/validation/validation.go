package validation

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"git.skobk.in/skobkin/yave/internal/model"
)

type Severity string

const (
	SeverityError   Severity = "error"
	SeverityWarning Severity = "warning"
)

type Issue struct {
	Severity Severity
	Field    string
	Message  string
}

type Result struct {
	Errors   []Issue
	Warnings []Issue
}

func (r Result) Valid() bool {
	return len(r.Errors) == 0
}

// Validate performs all checks needed before building or starting a run.
func Validate(
	profile model.Profile,
	media *model.MediaInfo,
	capabilities model.Capabilities,
	inputPath string,
	outputPath string,
) Result {
	result := validateProfile(profile, capabilities, media, true)
	validatePaths(&result, inputPath, outputPath)
	return result
}

// ValidateProfile checks whether a profile is complete and runnable with the
// current FFmpeg capabilities. Media may be nil when no input has been loaded.
func ValidateProfile(profile model.Profile, capabilities model.Capabilities, media *model.MediaInfo) Result {
	return validateProfile(profile, capabilities, media, false)
}

func validateProfile(profile model.Profile, capabilities model.Capabilities, media *model.MediaInfo, requireSourceMetadata bool) Result {
	var result Result

	required(&result, "profile.name", profile.Name, "profile name is required")
	required(&result, "profile.output_template", profile.OutputTemplate, "output template is required")

	container := strings.ToLower(strings.TrimSpace(profile.Container))
	if !oneOf(container, model.ContainerMKV, model.ContainerMP4, model.ContainerWebM) {
		result.error("profile.container", "unsupported output container %q", profile.Container)
	}

	videoEncoder := strings.ToLower(strings.TrimSpace(profile.Video.Encoder))
	if videoEncoder == "" {
		result.error("profile.video.encoder", "video encoder is required")
	} else if !supportedVideoEncoder(videoEncoder) {
		result.error("profile.video.encoder", "unsupported video encoder %q", profile.Video.Encoder)
	} else if videoEncoder != "copy" && !encoderAvailable(capabilities, videoEncoder) {
		result.error("profile.video.encoder", "video encoder %q is not available in the detected FFmpeg build", profile.Video.Encoder)
	}

	validateVideo(&result, profile.Video, videoEncoder)
	validateAudio(&result, profile.Audio, capabilities)
	validateSubtitles(&result, profile.Subtitles)

	if oneOf(container, model.ContainerMKV, model.ContainerMP4, model.ContainerWebM) {
		validateCompatibility(&result, profile, container, media, requireSourceMetadata)
	}

	return result
}

func validatePaths(result *Result, inputPath, outputPath string) {
	inputPath = strings.TrimSpace(inputPath)
	outputPath = strings.TrimSpace(outputPath)

	if inputPath == "" {
		result.error("input_path", "input path is required")
	} else if info, err := os.Stat(inputPath); err != nil {
		if os.IsNotExist(err) {
			result.error("input_path", "input file does not exist: %s", inputPath)
		} else {
			result.error("input_path", "cannot access input file %q: %v", inputPath, err)
		}
	} else if !info.Mode().IsRegular() {
		result.error("input_path", "input path is not a regular file: %s", inputPath)
	}

	if outputPath == "" {
		result.error("output_path", "output path is required")
		return
	}
	if samePath(inputPath, outputPath) {
		result.error("output_path", "output path must differ from the input path")
	}
	parent := filepath.Dir(outputPath)
	if info, err := os.Stat(parent); err != nil {
		if os.IsNotExist(err) {
			result.error("output_path", "output directory does not exist: %s", parent)
		} else {
			result.error("output_path", "cannot access output directory %q: %v", parent, err)
		}
	} else if !info.IsDir() {
		result.error("output_path", "output parent is not a directory: %s", parent)
	}
	if info, err := os.Stat(outputPath); err == nil && !info.Mode().IsRegular() {
		result.error("output_path", "existing output is not a regular file: %s", outputPath)
	} else if err != nil && !os.IsNotExist(err) {
		result.error("output_path", "cannot access output path %q: %v", outputPath, err)
	}
}

func validateVideo(result *Result, video model.VideoSettings, encoder string) {
	if video.MaxWidth < 0 {
		result.error("profile.video.max_width", "maximum width cannot be negative")
	}
	if video.MaxHeight < 0 {
		result.error("profile.video.max_height", "maximum height cannot be negative")
	}

	frameRateMode := strings.ToLower(strings.TrimSpace(video.FrameRateMode))
	if frameRateMode != "" && !oneOf(frameRateMode, "keep", "convert") {
		result.error("profile.video.frame_rate_mode", "frame rate mode must be keep or convert")
	}
	if frameRateMode == "convert" {
		if strings.TrimSpace(video.FrameRate) == "" {
			result.error("profile.video.frame_rate", "frame rate is required when frame rate conversion is enabled")
		} else if !validFrameRate(video.FrameRate) {
			result.error("profile.video.frame_rate", "frame rate must be a positive number or fraction")
		}
	}

	if encoder == "copy" {
		if video.MaxWidth != 0 || video.MaxHeight != 0 {
			result.error("profile.video.encoder", "video copy cannot be combined with scaling")
		}
		if frameRateMode == "convert" || strings.TrimSpace(video.FrameRate) != "" {
			result.error("profile.video.encoder", "video copy cannot be combined with frame rate conversion")
		}
		return
	}

	switch {
	case strings.HasSuffix(encoder, "_vaapi"):
		validateVAAPI(result, video, encoder)
	case encoder == "libx264" || encoder == "libx265":
		if !strings.EqualFold(strings.TrimSpace(video.RateControl), "CRF") {
			result.error("profile.video.rate_control", "%s supports CRF rate control only", encoder)
		}
		if r, ok := CRFRange(encoder); ok && !r.Contains(video.CRF) {
			result.error("profile.video.crf", "CRF for %s must be between %d and %d", encoder, r.Min, r.Max)
		}
	case encoder == "libsvtav1":
		validateCRFOrBitrate(result, video, encoder)
		r, _ := PresetRange(encoder)
		if preset, err := strconv.Atoi(strings.TrimSpace(video.Preset)); err != nil || !r.Contains(preset) {
			result.error("profile.video.preset", "SVT-AV1 preset must be an integer between %d and %d", r.Min, r.Max)
		}
	case encoder == "libvpx-vp9":
		validateCRFOrBitrate(result, video, encoder)
	}
}

func validateVAAPI(result *Result, video model.VideoSettings, encoder string) {
	mode := strings.ToUpper(strings.TrimSpace(video.RateControl))
	if !oneOf(mode, "AUTO", "CQP", "VBR", "CBR") {
		result.error("profile.video.rate_control", "VAAPI rate control must be Auto, CQP, VBR, or CBR")
		return
	}
	if strings.TrimSpace(video.VAAPIDevice) == "" {
		result.error("profile.video.vaapi_device", "VAAPI device path is required")
	}

	switch mode {
	case "CQP":
		if r, ok := QPRange(encoder); ok && !r.Contains(video.QP) {
			result.error("profile.video.qp", "VAAPI QP for %s must be between %d and %d", encoder, r.Min, r.Max)
		}
	case "VBR", "CBR":
		validateRequiredBitrate(result, "profile.video.bitrate", video.Bitrate, mode+" target bitrate")
	case "AUTO":
		validateOptionalBitrate(result, "profile.video.bitrate", video.Bitrate)
	}
	validateOptionalBitrate(result, "profile.video.maxrate", video.Maxrate)
	validateOptionalBitrate(result, "profile.video.bufsize", video.Bufsize)
}

func validateCRFOrBitrate(result *Result, video model.VideoSettings, encoder string) {
	mode := strings.ToUpper(strings.TrimSpace(video.RateControl))
	switch mode {
	case "CRF":
		if r, ok := CRFRange(encoder); ok && !r.Contains(video.CRF) {
			result.error("profile.video.crf", "CRF for %s must be between %d and %d", encoder, r.Min, r.Max)
		}
		validateOptionalBitrate(result, "profile.video.bitrate", video.Bitrate)
	case "VBR", "BITRATE":
		validateRequiredBitrate(result, "profile.video.bitrate", video.Bitrate, encoder+" target bitrate")
	default:
		result.error("profile.video.rate_control", "%s rate control must be CRF or bitrate", encoder)
	}
}

func validateAudio(result *Result, audio model.AudioSettings, capabilities model.Capabilities) {
	mode := strings.ToLower(strings.TrimSpace(audio.Mode))
	encoder := strings.ToLower(strings.TrimSpace(audio.Encoder))
	if !oneOf(mode, model.AudioCopy, model.AudioEncode) {
		result.error("profile.audio.mode", "audio mode must be copy or encode")
		return
	}
	if mode == model.AudioCopy {
		if encoder == "" {
			result.error("profile.audio.encoder", "audio encoder is required")
		} else if encoder != "copy" {
			result.error("profile.audio.encoder", "audio copy mode requires the copy encoder")
		}
		return
	}

	if encoder == "" {
		result.error("profile.audio.encoder", "audio encoder is required in encode mode")
		return
	}
	if !supportedAudioEncoder(encoder) {
		result.error("profile.audio.encoder", "unsupported audio encoder %q", audio.Encoder)
		return
	}
	if !encoderAvailable(capabilities, encoder) {
		result.error("profile.audio.encoder", "audio encoder %q is not available in the detected FFmpeg build", audio.Encoder)
	}

	switch encoder {
	case "libopus", "opus":
		validateChoice(result, "profile.audio.bitrate", audio.Bitrate, "96k", "128k", "160k", "192k", "256k")
	case "aac":
		validateChoice(result, "profile.audio.bitrate", audio.Bitrate, "128k", "160k", "192k", "256k")
	case "libmp3lame", "mp3":
		validateChoice(result, "profile.audio.bitrate", audio.Bitrate, "128k", "192k", "256k", "320k")
	case "flac":
		r, _ := AudioCompressionRange(encoder)
		if !r.Contains(audio.Compression) {
			result.error("profile.audio.compression", "FLAC compression level must be between %d and %d", r.Min, r.Max)
		}
	}
}

func validateSubtitles(result *Result, subtitles model.SubtitleSettings) {
	mode := strings.ToLower(strings.TrimSpace(subtitles.Mode))
	if !oneOf(mode, model.SubtitleNone, model.SubtitleCopy) {
		result.error("profile.subtitles.mode", "subtitle mode must be none or copy_if_compatible")
	}
}

func required(result *Result, field, value, message string) {
	if strings.TrimSpace(value) == "" {
		result.error(field, "%s", message)
	}
}

func (r *Result) error(field, format string, args ...any) {
	r.Errors = append(r.Errors, Issue{Severity: SeverityError, Field: field, Message: fmt.Sprintf(format, args...)})
}

func (r *Result) warning(field, format string, args ...any) {
	r.Warnings = append(r.Warnings, Issue{Severity: SeverityWarning, Field: field, Message: fmt.Sprintf(format, args...)})
}

func oneOf(value string, choices ...string) bool {
	for _, choice := range choices {
		if value == choice {
			return true
		}
	}
	return false
}

func encoderAvailable(capabilities model.Capabilities, encoder string) bool {
	_, ok := capabilities.Encoders[encoder]
	return ok
}

func samePath(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	absA, errA := filepath.Abs(a)
	absB, errB := filepath.Abs(b)
	if errA != nil || errB != nil {
		return filepath.Clean(a) == filepath.Clean(b)
	}
	return filepath.Clean(absA) == filepath.Clean(absB)
}

var (
	bitratePattern   = regexp.MustCompile(`^[1-9][0-9]*(?:[kKmM])?$`)
	frameRatePattern = regexp.MustCompile(`^[0-9]+(?:\.[0-9]+)?(?:/[1-9][0-9]*)?$`)
)

func validateRequiredBitrate(result *Result, field, value, label string) {
	if strings.TrimSpace(value) == "" {
		result.error(field, "%s is required", label)
		return
	}
	validateOptionalBitrate(result, field, value)
}

func validateOptionalBitrate(result *Result, field, value string) {
	value = strings.TrimSpace(value)
	if value != "" && !bitratePattern.MatchString(value) {
		result.error(field, "bitrate must be a positive integer with an optional k or M suffix")
	}
}

func validateChoice(result *Result, field, value string, choices ...string) {
	if !oneOf(strings.ToLower(strings.TrimSpace(value)), choices...) {
		result.error(field, "bitrate must be one of: %s", strings.Join(choices, ", "))
	}
}

func validFrameRate(value string) bool {
	value = strings.TrimSpace(value)
	if !frameRatePattern.MatchString(value) {
		return false
	}
	parts := strings.Split(value, "/")
	numerator, err := strconv.ParseFloat(parts[0], 64)
	return err == nil && numerator > 0
}

func supportedVideoEncoder(encoder string) bool {
	return oneOf(encoder,
		"hevc_vaapi", "h264_vaapi", "av1_vaapi", "vp9_vaapi",
		"libx265", "libx264", "libsvtav1", "libvpx-vp9", "copy",
	)
}

func supportedAudioEncoder(encoder string) bool {
	return oneOf(encoder, "libopus", "opus", "aac", "libmp3lame", "mp3", "flac")
}
