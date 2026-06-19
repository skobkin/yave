package encoder

import (
	"fmt"
	"strconv"
	"strings"

	"git.skobk.in/skobkin/yave/internal/model"
)

// softwareEncoder is the shared implementation for the software video
// encoders: libx264, libx265, libsvtav1, libvpx-vp9, libaom-av1.
//
// Behavioural differences live in a small set of callbacks so each
// concrete encoder can be described in a few lines. The shape of the
// emitted args is byte-for-byte identical to the prior ffmpeg.buildVideoArgs
// switch so the existing command_test.go and validation_test.go tests
// continue to pass.
type softwareEncoder struct {
	name        string
	label       string
	codecFamily string
	priority    int
	// crfRange and presetRange are what the encoder advertises. Empty
	// ranges mean "not supported" (the UI hides the corresponding widget).
	crfRange    IntegerRange
	presetRange IntegerRange
	// presetFlag is the ffmpeg flag that follows the preset value
	// ("-preset" for libx264/libx265/libsvtav1/libaom-av1, "-deadline"
	// for libvpx-vp9).
	presetFlag string
	// presetName is the UI label used by the registry.Model adapter
	// (matches the previous preset name in updateControlState).
	presetName string
	// presetKind is "integer" for sliders, "categorical" for free text.
	presetKind string
	// presetDefault is the value used when the user has not picked one.
	presetDefault string
	// supportsTune gates the -tune flag (libx264/libx265 only today).
	supportsTune bool
	// supportsProfile gates the -profile:v flag.
	supportsProfile bool
	// rateControlModes lists the modes exposed by the UI dropdown.
	rateControlModes   []RateControl
	defaultRateControl RateControl
	// buildArgs is the per-encoder flag builder. It receives the parts
	// that are common to every software encoder (-c:v <name>) and is
	// expected to append the rate-control / preset / tune / profile
	// flags only.
	buildArgs func(v model.VideoSettings) ([]string, error)
}

func (e *softwareEncoder) Name() string         { return e.name }
func (e *softwareEncoder) Label() string        { return e.label }
func (e *softwareEncoder) Media() string        { return MediaVideo }
func (e *softwareEncoder) Kind() string         { return KindSoftware }
func (e *softwareEncoder) Priority() int        { return e.priority }
func (e *softwareEncoder) HWBackend() HWBackend { return HWNone }
func (e *softwareEncoder) CodecFamily() string  { return e.codecFamily }
func (e *softwareEncoder) RateControlModes() []RateControl {
	return e.rateControlModes
}
func (e *softwareEncoder) DefaultRateControl() RateControl {
	return e.defaultRateControl
}
func (e *softwareEncoder) QPRange() (IntegerRange, bool) { return IntegerRange{}, false }
func (e *softwareEncoder) CRFRange() (IntegerRange, bool) {
	return e.crfRange, e.crfRange.Max >= e.crfRange.Min
}
func (e *softwareEncoder) PresetRange() (IntegerRange, bool) {
	if e.presetKind == "integer" {
		return e.presetRange, true
	}
	return IntegerRange{}, false
}
func (e *softwareEncoder) SupportsTune() bool    { return e.supportsTune }
func (e *softwareEncoder) SupportsProfile() bool { return e.supportsProfile }

// VideoFilter returns the filter chain for software encoders. When
// scaling is requested a scale= filter is emitted; when frame-rate
// conversion is requested an fps= filter is prepended. There is no
// hw upload or pixel-format conversion because software encoders consume
// software frames.
func (e *softwareEncoder) VideoFilter(v model.VideoSettings, media model.MediaInfo) (string, error) {
	filters, err := appendFrameRateFilter(nil, v)
	if err != nil {
		return "", err
	}
	width, height, scale := targetDimensions(v, media)
	if scale {
		filters = append(filters, scalePart(width, height, v.KeepAspectRatio))
	}
	return strings.Join(filters, ","), nil
}

// BuildVideoArgs builds the post -c:v flag list. The pre-input args
// are empty for software encoders.
func (e *softwareEncoder) BuildVideoArgs(v model.VideoSettings) (BuildResult, error) {
	if v.Encoder == "" {
		return BuildResult{}, fmt.Errorf("video encoder is empty")
	}
	args := []string{"-c:v", e.name}
	if e.buildArgs != nil {
		extra, err := e.buildArgs(v)
		if err != nil {
			return BuildResult{}, err
		}
		args = append(args, extra...)
	}
	return BuildResult{Args: args}, nil
}

// Validate enforces encoder-specific rules that are not just
// range checks (CRF/preset bounds are checked at the validation
// package level from the registry, so we only enforce what is specific
// to a particular encoder — none of the software encoders require that
// at this stage).
func (e *softwareEncoder) Validate(v model.VideoSettings) ([]Issue, error) {
	return nil, nil
}

// softwareVideoEncoders returns the curated software video encoder
// set. Registration order does not matter; the registry sorts by
// priority then name.
func softwareVideoEncoders() []Encoder {
	return []Encoder{
		&softwareEncoder{
			name: "libx264", label: "H.264 (libx264)", codecFamily: "h264",
			priority: 100, crfRange: IntegerRange{0, 51},
			presetFlag: "-preset", presetKind: "categorical", presetName: "preset",
			supportsTune: true, supportsProfile: true,
			// libx264 supports CRF only — the prior buildVideoArgs switch
			// did not emit -b:v for libx264, so any other mode must be
			// rejected by validation.
			rateControlModes:   []RateControl{RCCRF},
			defaultRateControl: RCCRF,
			buildArgs:          x264OrX265Args,
		},
		&softwareEncoder{
			name: "libx265", label: "H.265 (libx265)", codecFamily: "hevc",
			priority: 90, crfRange: IntegerRange{0, 51},
			presetFlag: "-preset", presetKind: "categorical", presetName: "preset",
			supportsTune: true, supportsProfile: true,
			// libx265 supports CRF only — matches prior behaviour.
			rateControlModes:   []RateControl{RCCRF},
			defaultRateControl: RCCRF,
			buildArgs:          x264OrX265Args,
		},
		&softwareEncoder{
			name: "libsvtav1", label: "AV1 (libsvtav1)", codecFamily: "av1",
			priority: 80, crfRange: IntegerRange{0, 63},
			presetRange: IntegerRange{0, 13}, presetFlag: "-preset",
			presetKind: "integer", presetName: "preset", presetDefault: "6",
			supportsProfile:    true,
			rateControlModes:   []RateControl{RCCRF, RCBitrate},
			defaultRateControl: RCCRF,
			buildArgs:          svtAV1Args,
		},
		&softwareEncoder{
			name: "libvpx-vp9", label: "VP9 (libvpx-vp9)", codecFamily: "vp9",
			priority: 70, crfRange: IntegerRange{0, 63},
			presetFlag: "-deadline", presetKind: "categorical", presetName: "deadline",
			// libvpx-vp9 has no -profile:v flag in the prior code path;
			// SupportsProfile reports what BuildVideoArgs actually emits.
			supportsProfile:    false,
			rateControlModes:   []RateControl{RCCRF, RCBitrate},
			defaultRateControl: RCCRF,
			buildArgs:          vpxVP9Args,
		},
		&softwareEncoder{
			name: "libaom-av1", label: "AV1 (libaom-av1)", codecFamily: "av1",
			priority: 60, crfRange: IntegerRange{0, 63},
			presetRange: IntegerRange{0, 8}, presetFlag: "-cpu-used",
			presetKind: "integer", presetName: "cpu-used", presetDefault: "4",
			supportsProfile:    true,
			rateControlModes:   []RateControl{RCCRF, RCBitrate},
			defaultRateControl: RCCRF,
			buildArgs:          libaomAV1Args,
		},
	}
}

// x264OrX265Args builds the args for libx264 / libx265. Both encoders
// always emit -crf; preset/tune/profile are emitted only when set.
//
// The pre-refactor code accepted a single switch on encoder name and
// used the same code path for both. Keeping the helper shared prevents
// drift between the two implementations.
func x264OrX265Args(v model.VideoSettings) ([]string, error) {
	args := []string{"-crf", strconv.Itoa(v.CRF)}
	if v.Preset != "" {
		args = append(args, "-preset", v.Preset)
	}
	if v.Tune != "" {
		args = append(args, "-tune", v.Tune)
	}
	if v.Profile != "" {
		args = append(args, "-profile:v", v.Profile)
	}
	return args, nil
}

// svtAV1Args builds the args for libsvtav1. -b:v 0 is the constant-rate
// marker for CRF mode; -b:v <bitrate> is emitted for BITRATE mode.
// -preset defaults to 6 when the user has not picked a value, matching
// the prior behaviour.
func svtAV1Args(v model.VideoSettings) ([]string, error) {
	args := make([]string, 0, 4)
	if strings.EqualFold(v.RateControl, "BITRATE") && v.Bitrate != "" {
		args = append(args, "-b:v", v.Bitrate)
	} else {
		args = append(args, "-crf", strconv.Itoa(v.CRF), "-b:v", "0")
	}
	args = append(args, "-preset", defaultString(v.Preset, "6"))
	if v.Profile != "" {
		args = append(args, "-profile:v", v.Profile)
	}
	return args, nil
}

// vpxVP9Args builds the args for libvpx-vp9. Same CRF / BITRATE split
// as svtAV1Args; preset is emitted as -deadline so the categorical
// value ("good", "best", "realtime") survives unchanged.
func vpxVP9Args(v model.VideoSettings) ([]string, error) {
	args := make([]string, 0, 4)
	if strings.EqualFold(v.RateControl, "BITRATE") && v.Bitrate != "" {
		args = append(args, "-b:v", v.Bitrate)
	} else {
		args = append(args, "-crf", strconv.Itoa(v.CRF), "-b:v", "0")
	}
	if v.Preset != "" {
		args = append(args, "-deadline", v.Preset)
	}
	return args, nil
}

// libaomAV1Args builds the args for libaom-av1. -b:v 0 is the CRF mode
// marker for libaom; -b:v <bitrate> for BITRATE; -cpu-used replaces the
// libsvtav1 -preset flag and accepts an integer 0..8.
func libaomAV1Args(v model.VideoSettings) ([]string, error) {
	args := make([]string, 0, 4)
	if strings.EqualFold(v.RateControl, "BITRATE") && v.Bitrate != "" {
		args = append(args, "-b:v", v.Bitrate)
	} else {
		args = append(args, "-crf", strconv.Itoa(v.CRF), "-b:v", "0")
	}
	args = append(args, "-cpu-used", defaultString(v.Preset, "4"))
	if v.Profile != "" {
		args = append(args, "-profile:v", v.Profile)
	}
	return args, nil
}
