package validation

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"git.skobk.in/skobkin/yave/internal/encoder"
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
	switch {
	case videoEncoder == "":
		result.error("profile.video.encoder", "video encoder is required")
	case !supportedVideoEncoder(videoEncoder):
		result.error("profile.video.encoder", "unsupported video encoder %q", profile.Video.Encoder)
	case videoEncoder != "copy" && !encoderAvailable(capabilities, videoEncoder):
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

func validateVideo(result *Result, video model.VideoSettings, encoderName string) {
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

	if encoderName == "copy" {
		if video.MaxWidth != 0 || video.MaxHeight != 0 {
			result.error("profile.video.encoder", "video copy cannot be combined with scaling")
		}
		if frameRateMode == "convert" || strings.TrimSpace(video.FrameRate) != "" {
			result.error("profile.video.encoder", "video copy cannot be combined with frame rate conversion")
		}
		return
	}

	ve, ok := lookupVideoEncoder(encoderName)
	if !ok {
		// Unknown encoder — supportedVideoEncoder catches this earlier.
		return
	}
	validateVideoForEncoder(result, video, ve, encoderName)
}

// validateVideoForEncoder dispatches per-encoder checks via the
// encoder.VideoEncoder interface. It preserves the field names and
// error messages from the prior validateVideo switch so existing tests
// continue to match.
func validateVideoForEncoder(result *Result, video model.VideoSettings, ve encoder.VideoEncoder, name string) {
	mode := strings.ToUpper(strings.TrimSpace(video.RateControl))

	// Rate-control mode membership. Empty mode is accepted for encoders
	// that advertise AUTO (VAAPI) and rejected otherwise.
	if !videoModeAllowed(ve, mode) {
		allowed := make([]string, 0, len(ve.RateControlModes()))
		for _, m := range ve.RateControlModes() {
			allowed = append(allowed, string(m))
		}
		result.error("profile.video.rate_control",
			"%s rate control must be one of: %s", name, strings.Join(allowed, ", "))
		return
	}

	// VAAPI device must be present when the encoder needs one.
	if ve.HWBackend() == encoder.HWVAAPI && strings.TrimSpace(video.VAAPIDevice) == "" {
		result.error("profile.video.vaapi_device", "VAAPI device path is required")
	}

	switch mode {
	case "CQP":
		if r, ok := ve.QPRange(); ok && !r.Contains(video.QP) {
			result.error("profile.video.qp", "VAAPI QP for %s must be between %d and %d", name, r.Min, r.Max)
		}
	case "VBR", "CBR":
		validateRequiredBitrate(result, "profile.video.bitrate", video.Bitrate, mode+" target bitrate")
		validateOptionalBitrate(result, "profile.video.maxrate", video.Maxrate)
		validateOptionalBitrate(result, "profile.video.bufsize", video.Bufsize)
	case "BITRATE":
		validateRequiredBitrate(result, "profile.video.bitrate", video.Bitrate, name+" target bitrate")
	case "CRF":
		if r, ok := ve.CRFRange(); ok && !r.Contains(video.CRF) {
			result.error("profile.video.crf", "CRF for %s must be between %d and %d", name, r.Min, r.Max)
		}
		validateOptionalBitrate(result, "profile.video.bitrate", video.Bitrate)
	case "AUTO", "":
		validateOptionalBitrate(result, "profile.video.bitrate", video.Bitrate)
		validateOptionalBitrate(result, "profile.video.maxrate", video.Maxrate)
		validateOptionalBitrate(result, "profile.video.bufsize", video.Bufsize)
	}

	// Integer preset check (SVT-AV1, libaom-av1). We accept an empty
	// preset when the encoder's builder has a default (libsvtav1 -> 6,
	// libaom-av1 -> 4) so that hand-edited and imported profiles with
	// preset="" can still validate and reach command generation. The
	// builder substitutes the default via defaultString(...); without
	// this allowance, those profiles would be rejected here even
	// though BuildVideoArgs would emit a runnable command.
	if r, ok := ve.PresetRange(); ok {
		if strings.TrimSpace(video.Preset) == "" && ve.DefaultPreset() != "" {
			// builder will substitute the default; nothing to check
		} else if preset, err := strconv.Atoi(strings.TrimSpace(video.Preset)); err != nil || !r.Contains(preset) {
			result.error("profile.video.preset", "%s preset must be an integer between %d and %d", name, r.Min, r.Max)
		}
	}

	// Fold encoder-specific Validate() findings into the result.
	if issues, err := ve.Validate(video); err == nil {
		for _, issue := range issues {
			if issue.Level == encoder.IssueError {
				result.error(issue.Field, "%s", issue.Message)
			} else {
				result.warning(issue.Field, "%s", issue.Message)
			}
		}
	}
}

// videoModeAllowed reports whether mode is one of the modes the
// encoder advertises. Empty mode is treated as the encoder's
// default only when that default exists in the advertised set.
func videoModeAllowed(ve encoder.VideoEncoder, mode string) bool {
	modes := ve.RateControlModes()
	if mode == "" {
		// Empty mode is allowed if the encoder's default is "AUTO"
		// (e.g. VAAPI), since callers commonly leave it unset.
		return ve.DefaultRateControl() == encoder.RCAuto
	}
	for _, m := range modes {
		if string(m) == mode {
			return true
		}
	}
	return false
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
	return slices.Contains(choices, value)
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

func supportedVideoEncoder(encoderName string) bool {
	if strings.EqualFold(strings.TrimSpace(encoderName), "copy") {
		return true
	}
	_, ok := lookupVideoEncoder(encoderName)
	return ok
}

func supportedAudioEncoder(encoder string) bool {
	return oneOf(encoder, "libopus", "opus", "aac", "libmp3lame", "mp3", "flac")
}
