// Package encoder defines the curated set of FFmpeg encoders that yave
// exposes to the user plus a typed Encoder interface that replaces the
// flat string-switches that used to live in ffmpeg.BuildCommand,
// validation.ValidateProfile, and the UI controller.
//
// The package is intentionally leaf-level: it depends only on
// internal/model (for VideoSettings / MediaInfo / AudioSettings). The
// validation package imports this one for the registry; this package
// never imports validation.
package encoder

import (
	"git.skobk.in/skobkin/yave/internal/model"
)

// Media kind values returned by Encoder.Media(). They are kept as plain
// strings to match the existing model.Encoder.Media field so the
// Registry.Model adapter can build a model.Encoder without losing the
// media discriminator that the UI uses to split the video and audio
// dropdowns.
const (
	MediaVideo = "video"
	MediaAudio = "audio"
	MediaCopy  = "video,audio" // pseudo-encoder "copy"
)

// Encoder kind values returned by Encoder.Kind(). Same rationale as Media.
const (
	KindHardware    = "hardware"
	KindSoftware    = "software"
	KindEncode      = "encode"
	KindPassthrough = "passthrough"
)

// HWBackend identifies the hardware acceleration family of an encoder.
// BuildCommand dispatches on this to emit global flags like
// -init_hw_device or -gpu N. HWNone means software / passthrough.
type HWBackend int

const (
	HWNone HWBackend = iota
	HWVAAPI
	HWNVENC
	HWQSV
)

// RateControl is a rate-control mode exposed by an encoder. The mapping
// to encoder-specific ffmpeg flags (e.g. -rc_mode vs -rc) happens inside
// each encoder's BuildVideoArgs.
type RateControl string

const (
	RCAuto    RateControl = "AUTO"
	RCCQP     RateControl = "CQP"
	RCVBR     RateControl = "VBR"
	RCCBR     RateControl = "CBR"
	RCCRF     RateControl = "CRF"
	RCBitrate RateControl = "BITRATE"
)

// IntegerRange is an inclusive numeric range used for QP, CRF, preset,
// and audio compression values. Defined here (rather than in validation)
// so this package can stay leaf-level and so encoders do not need to
// import validation.
type IntegerRange struct {
	Min int
	Max int
}

// Contains reports whether value is in [Min, Max].
func (r IntegerRange) Contains(value int) bool {
	return value >= r.Min && value <= r.Max
}

// Clamp returns value constrained to [Min, Max].
func (r IntegerRange) Clamp(value int) int {
	if value < r.Min {
		return r.Min
	}
	if value > r.Max {
		return r.Max
	}
	return value
}

// ProportionalConvert maps value from the inclusive range from to the
// inclusive range to using a best-effort linear remap with endpoints
// preserved. A degenerate from-range (Min == Max) is treated as "keep
// value, clamp to to".
//
// This is identical to the prior validation.ProportionalConvert
// implementation; it lives here so Encoder methods can use it without
// importing validation.
func ProportionalConvert(value int, from, to IntegerRange) int {
	if from.Max == from.Min {
		return to.Clamp(value)
	}
	if from == to {
		return to.Clamp(value)
	}
	spread := from.Max - from.Min
	offset := value - from.Min
	mapped := to.Min + (offset*(to.Max-to.Min)+spread/2)/spread
	return to.Clamp(mapped)
}

// BuildResult is what VideoEncoder.BuildVideoArgs returns for a single
// video stream. PreInputArgs go before -i (e.g. -init_hw_device,
// -gpu N, or -vaapi_device). Args is everything after -c:v (rate
// control, preset, profile, ...).
type BuildResult struct {
	PreInputArgs []string
	Args         []string
}

// IssueLevel discriminates an encoder-emitted validation issue between
// a hard error and a soft warning.
type IssueLevel int

const (
	IssueError IssueLevel = iota
	IssueWarning
)

// Issue is an encoder-emitted validation finding. validation.validateVideo
// converts each Issue into the right slot of *validation.Result.
type Issue struct {
	Level   IssueLevel
	Field   string
	Message string
}

// Encoder is the minimal contract every curated encoder satisfies —
// audio or video. The richer VideoEncoder / AudioEncoder interfaces add
// the media-specific methods.
type Encoder interface {
	// Name is the ffmpeg encoder identifier (e.g. "hevc_vaapi").
	Name() string
	// Label is the human-readable name for the UI dropdown.
	Label() string
	// Media is one of MediaVideo, MediaAudio, or MediaCopy.
	Media() string
	// Kind is one of KindHardware, KindSoftware, KindEncode, KindPassthrough.
	Kind() string
	// Priority is the sort key for the UI dropdown (higher first).
	Priority() int
	// HWBackend reports the hardware acceleration family, or HWNone for
	// software or passthrough encoders.
	HWBackend() HWBackend
	// CodecFamily is the short codec identifier used for container
	// compatibility ("h264", "hevc", "av1", "vp9", "opus", ...).
	CodecFamily() string
}

// VideoEncoder is an Encoder that produces a video stream.
type VideoEncoder interface {
	Encoder

	// RateControlModes lists the modes exposed by the UI rate-control
	// dropdown when this encoder is selected. An empty list hides the
	// dropdown.
	RateControlModes() []RateControl

	// DefaultRateControl is what the UI should pre-select when the user
	// switches to this encoder.
	DefaultRateControl() RateControl

	// Integer range lookups. A return of ok=false means the encoder does
	// not expose the corresponding control; the UI hides the widget and
	// BuildVideoArgs never emits the flag.
	QPRange() (IntegerRange, bool)
	CRFRange() (IntegerRange, bool)
	PresetRange() (IntegerRange, bool) // ok=false -> categorical preset string

	// SupportsTune reports whether -tune is meaningful for this encoder.
	SupportsTune() bool

	// SupportsProfile reports whether -profile:v is meaningful for this
	// encoder. Most encoders accept profile; some (e.g. av1_nvenc) do not.
	SupportsProfile() bool

	// VideoFilter returns the filter chain to follow -vf, including any
	// frame-rate conversion, soft scaling, format conversion, hw upload,
	// and hardware scaling. Empty string means no -vf is emitted.
	VideoFilter(v model.VideoSettings, media model.MediaInfo) (string, error)

	// BuildVideoArgs emits the per-encoder args plus the global pre-input
	// flags. A returned non-nil error is fatal (e.g. missing device path).
	BuildVideoArgs(v model.VideoSettings) (BuildResult, error)

	// Validate returns encoder-specific findings beyond the integer range
	// checks. The validation package folds each Issue into its Result.
	Validate(v model.VideoSettings) ([]Issue, error)
}

// AudioEncoder is an Encoder that produces an audio stream.
type AudioEncoder interface {
	Encoder

	// AudioCompressionRange returns the FLAC compression level range.
	// ok=false means the encoder does not accept a compression level.
	AudioCompressionRange() (IntegerRange, bool)

	// BuildAudioArgs emits the -c:a and per-encoder flags (e.g.
	// -compression_level, -b:a). A returned non-nil error is fatal.
	BuildAudioArgs(a model.AudioSettings) ([]string, error)

	// Validate returns encoder-specific findings (bitrate choices etc.).
	Validate(a model.AudioSettings) ([]Issue, error)
}
