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
	"git.skobk.in/skobkin/yave/internal/vaapi"
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
	validateVAAPIDevice(&result, profile.Video, capabilities, videoEncoder)
	validateBuildProfile(&result, profile.Video, videoEncoder, capabilities)
	validateProfileBitDepth(&result, profile.Video, videoEncoder, media)
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

// validateBuildProfile rejects -profile:v values the detected FFmpeg
// build does not offer. capabilities.Encoders[name].Profiles is the
// curated list narrowed by `ffmpeg -h encoder=<name>` — the same
// source the UI dropdown uses — closing the gap the registry's static
// curated list cannot see (a value curated for the project but absent
// from this build's option constants fails at conversion time with
// "Undefined constant or missing '('"). Missing capabilities or an
// un-narrowed list defer to the registry check, so pre-detection
// calls and fail-open builds keep the old behavior.
func validateBuildProfile(result *Result, video model.VideoSettings, encoderName string, capabilities model.Capabilities) {
	value := strings.TrimSpace(video.Profile)
	if encoderName == "copy" || value == "" {
		return
	}
	m, ok := capabilities.Encoders[encoderName]
	if !ok || len(m.Profiles) == 0 {
		return
	}
	for _, p := range m.Profiles {
		if strings.EqualFold(p, value) {
			return
		}
	}
	result.error("profile.video.profile",
		"%s profile %q is not offered by the detected FFmpeg build; must be one of: %s",
		encoderName, value, strings.Join(m.Profiles, ", "))
}

// validateProfileBitDepth warns when the selected -profile:v cannot
// encode the source's bit depth. The software chains pass the source
// pixel format through, so an 8-bit-only profile on a 10-bit source
// turns a working conversion into a hard x264/x265 failure ("main
// profile doesn't support a bit depth of 10") that an empty profile —
// which lets the encoder auto-select — would not have. Only
// libx264/libx265 curate depth-constrained profiles: the hardware
// chains convert to nv12 and offer only chain-compatible values, and
// the AV1/VP9 Main profiles cover every depth. A warning, not an
// error: the pixel-format name heuristic can mis-parse exotic names,
// and the encoder's own error is loud.
func validateProfileBitDepth(result *Result, video model.VideoSettings, encoderName string, media *model.MediaInfo) {
	maxDepth := profileMaxBitDepth(encoderName, strings.TrimSpace(video.Profile))
	if maxDepth == 0 || media == nil {
		return
	}
	stream, ok := media.PrimaryVideo()
	if !ok {
		return
	}
	if depth := pixelFormatBitDepth(stream.PixelFormat); depth > maxDepth {
		result.warning("profile.video.profile",
			"%s profile %q does not support %d-bit sources; clear the profile to let %s auto-select a compatible one",
			encoderName, video.Profile, depth, encoderName)
	}
}

// profileMaxBitDepth reports the highest source bit depth the given
// -profile:v value accepts, or 0 when the combination carries no
// depth constraint (no profile selected, unknown encoder, or a
// profile that is not depth-limited).
func profileMaxBitDepth(encoderName, profile string) int {
	if profile == "" {
		return 0
	}
	switch strings.ToLower(encoderName) {
	case "libx264":
		// baseline/main/high are all 8-bit; a 10-bit source requires
		// the automatic High10, reachable only without -profile:v.
		return 8
	case "libx265":
		switch strings.ToLower(profile) {
		case "main10":
			return 10
		case "main12":
			return 12
		default:
			return 8
		}
	}
	return 0
}

// pixelFormatBitDepth reads the bit depth out of an ffprobe pixel
// format name (yuv420p10le → 10, yuv420p → 8). Unrecognized names
// return 8, which disables the warning rather than fabricating one.
func pixelFormatBitDepth(format string) int {
	f := strings.ToLower(strings.TrimSpace(format))
	switch {
	case strings.Contains(f, "16le"), strings.Contains(f, "16be"):
		return 16
	case strings.Contains(f, "14le"), strings.Contains(f, "14be"):
		return 14
	case strings.Contains(f, "12le"), strings.Contains(f, "12be"):
		return 12
	case strings.Contains(f, "10le"), strings.Contains(f, "10be"):
		return 10
	case strings.Contains(f, "9le"), strings.Contains(f, "9be"):
		return 9
	}
	return 8
}

// validateVideoForEncoder dispatches per-encoder checks via the
// encoder.VideoEncoder interface. It preserves the field names and
// error messages from the prior validateVideo switch so existing tests
// continue to match.
func validateVideoForEncoder(result *Result, video model.VideoSettings, ve encoder.VideoEncoder, name string) {
	mode := strings.ToUpper(strings.TrimSpace(video.RateControl))

	// Rate-control mode membership. Empty mode is accepted when the
	// encoder's DefaultRateControl is among its advertised
	// RateControlModes; see videoModeAllowed.
	if !videoModeAllowed(ve, mode) {
		allowed := make([]string, 0, len(ve.RateControlModes()))
		for _, m := range ve.RateControlModes() {
			allowed = append(allowed, string(m))
		}
		result.error("profile.video.rate_control",
			"%s rate control must be one of: %s", name, strings.Join(allowed, ", "))
		return
	}

	// Normalize empty mode to the encoder's default before the per-mode
	// switch. Without this, an empty rate_control on a default-CQP
	// (NVENC/QSV) or default-CRF (libx264/libx265/libsvtav1/...)
	// encoder would skip the QP/CRF range check: the switch falls
	// into the AUTO branch which only validates optional bitrate
	// fields, while BuildVideoArgs emits -qp/-crf with the un-checked
	// value. Substituting the default routes the empty branch through
	// the same checks an explicit selection gets. An empty encoder
	// default (defensive — no encoder currently returns "") falls back
	// to AUTO.
	if mode == "" {
		def := string(ve.DefaultRateControl())
		if def == "" {
			mode = string(encoder.RCAuto)
		} else {
			mode = def
		}
	}

	// VAAPI device semantics (automatic vs manual, per-device
	// compatibility) are checked against the capabilities in
	// validateVAAPIDevice, called from validateProfile.

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
	case "AUTO":
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

// validateVAAPIDevice applies the automatic/manual VAAPI device
// semantics against the probed per-device capabilities. It runs for
// every encoder (the VAAPI check is gated inside) so the device check
// also fires when an invalid rate control would have short-circuited
// the old in-encoder check.
//
// An empty device means automatic: with probe data, a compatible
// device must exist (hard, actionable error otherwise); without probe
// data (libva missing, non-Linux, every probe failed) validation fails
// open, mirroring the fail-open device probing in internal/ffmpeg. A
// manual device that the probe says cannot encode the selected codec
// and profile is a warning, not an error: the pick is explicit and the
// runtime error is loud, so blocking Start would be hostile.
func validateVAAPIDevice(result *Result, video model.VideoSettings, capabilities model.Capabilities, encoderName string) {
	required, ok := encoder.RequiredVAProfiles(encoderName, video.Profile)
	if !ok || len(required) == 0 {
		// Non-VAAPI encoders and unknown -profile:v values (already
		// reported by the curated-membership check) have no device
		// requirement to verify.
		return
	}

	device := strings.TrimSpace(video.VAAPIDevice)
	if device != "" {
		for _, d := range capabilities.VAAPIDevices {
			if d.Path != device || !d.Known() {
				continue
			}
			if !d.Encodes(required...) {
				result.warning("profile.video.vaapi_device",
					"the VAAPI device %s (%s) does not offer encoding for %s profile %s; switch the VAAPI device to automatic or pick a device that can encode",
					d.Path, d.Driver, encoderName, displayProfile(video.Profile))
			}
			return
		}
		// The device is absent from the probe data or its capabilities
		// are unknown: no noise for an explicit pick.
		return
	}

	switch choice := vaapi.SelectDevice(capabilities.RenderNodes, capabilities.VAAPIDevices, required); choice.Status {
	case vaapi.DeviceNoneCompatible:
		descriptions := make([]string, 0, len(capabilities.VAAPIDevices))
		for _, d := range capabilities.VAAPIDevices {
			descriptions = append(descriptions, vaapi.DescribeDevice(d, required))
		}
		result.error("profile.video.vaapi_device",
			"no detected VAAPI device can encode %s profile %s: %s. Pick a VAAPI device that offers encoding, switch to a software encoder, or install a driver with VA-API encoding support",
			encoderName, displayProfile(video.Profile), strings.Join(descriptions, "; "))
	case vaapi.DeviceNoNodes:
		result.error("profile.video.vaapi_device",
			"no VAAPI render device was detected; set a VAAPI device explicitly or use another video encoder")
	}
}

// displayProfile renders a curated -profile:v value for messages, with
// the empty (automatic) value spelled out.
func displayProfile(profile string) string {
	trimmed := strings.TrimSpace(profile)
	if trimmed == "" {
		return "(automatic)"
	}
	return strconv.Quote(trimmed)
}

// videoModeAllowed reports whether mode is one of the modes the
// encoder advertises. Empty mode is treated as the encoder's
// default; if that default appears in the advertised RateControlModes
// set the empty value is accepted, because the encoder's builder
// would substitute the default and emit a runnable command
// (e.g. NVENC/QSV: "" -> CQP; VAAPI: "" -> Auto; libx264/libx265:
// "" -> CRF, the builder always emits -crf regardless). Mirrors the
// preset fix that consults DefaultPreset() to keep validation in
// sync with the builders.
func videoModeAllowed(ve encoder.VideoEncoder, mode string) bool {
	modes := ve.RateControlModes()
	if mode == "" {
		def := string(ve.DefaultRateControl())
		for _, m := range modes {
			if string(m) == def {
				return true
			}
		}
		return false
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

	// Bitrate and compression checks read the encoder's own choices
	// from the registry — the same source the UI dropdowns use — so a
	// new audio encoder only needs registering to validate correctly.
	if ae, ok := lookupAudioEncoder(encoder); ok {
		if choices := ae.BitrateChoices(); len(choices) > 0 {
			validateChoice(result, "profile.audio.bitrate", audio.Bitrate, choices...)
		} else if r, ok := ae.AudioCompressionRange(); ok && !r.Contains(audio.Compression) {
			result.error("profile.audio.compression", "%s compression level must be between %d and %d", strings.ToUpper(encoder), r.Min, r.Max)
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
