package encoder

import (
	"fmt"
	"strings"

	"git.skobk.in/skobkin/yave/internal/model"
)

// SettingsSummary serializes the most important profile settings into
// short filename tokens for the {video_settings} / {audio_settings}
// output placeholders, so experiments with quality, preset, codec
// profile, or tuning produce distinguishable filenames without manual
// renaming. The tokens live next to the arg builders and consult the
// same registry traits, so a token mirrors what the emitted command
// actually carries — the summary must never disagree with the command
// it describes.
//
// Device indices (VAAPIDevice/GPUIndex) are deliberately excluded:
// they change nothing about the encode result and would churn
// filenames across machines. The serialization is underscore-joined
// and safe to embed in a filename, but the caller still runs it
// through the template sanitizer.
//
// The video token's fixed order: encoder, codec profile, rate-control
// token, preset (when the encoder supports one), tune (when set),
// frame rate (only when conversion is on), dimensions. Empty
// components are omitted.
func SettingsSummary(encoderName string, v model.VideoSettings, a model.AudioSettings) (video, audio string) {
	return videoSummary(encoderName, v), audioSummary(a)
}

// videoSummary renders the {video_settings} token.
func videoSummary(encoderName string, v model.VideoSettings) string {
	name := strings.ToLower(strings.TrimSpace(encoderName))
	if name == "copy" {
		return "copy"
	}
	var parts []string
	if name != "" {
		parts = append(parts, name)
	}
	// The codec profile is serialized separately from {profile}: the
	// saved profile's NAME never changes when the user edits main →
	// main10 inside it, and one saved profile producing both variants
	// must not collide on one filename.
	if p := canonicalProfile(name, strings.TrimSpace(v.Profile)); p != "" {
		parts = append(parts, p)
	}
	if rc := rateControlToken(name, v); rc != "" {
		parts = append(parts, rc)
	}
	if preset := effectivePreset(name, v); preset != "" {
		parts = append(parts, preset)
	}
	if t := strings.TrimSpace(v.Tune); t != "" && supportsTune(name) {
		parts = append(parts, t)
	}
	if strings.EqualFold(strings.TrimSpace(v.FrameRateMode), "convert") {
		if fr := strings.TrimSpace(v.FrameRate); fr != "" {
			parts = append(parts, fr+"fps")
		}
	}
	// Dimensions render what the profile requests, not the resolved
	// target: a lone maximum side is representable ("1280w") rather
	// than silently dropped, and no source media is consulted.
	switch {
	case v.MaxWidth > 0 && v.MaxHeight > 0:
		parts = append(parts, fmt.Sprintf("%dx%d", v.MaxWidth, v.MaxHeight))
	case v.MaxWidth > 0:
		parts = append(parts, fmt.Sprintf("%dw", v.MaxWidth))
	case v.MaxHeight > 0:
		parts = append(parts, fmt.Sprintf("%dh", v.MaxHeight))
	}
	return strings.Join(parts, "_")
}

// audioSummary renders the {audio_settings} token: encoder plus
// bitrate, except compression-based encoders (flac) which render the
// compression level ALWAYS — mirroring the builder, which emits
// -compression_level unconditionally — so a stale retained bitrate
// never leaks into the name of a lossless encode. Stream copy is
// "copy".
func audioSummary(a model.AudioSettings) string {
	if a.IsStreamCopy() {
		return "copy"
	}
	name := strings.ToLower(strings.TrimSpace(a.Encoder))
	var parts []string
	if name != "" {
		parts = append(parts, name)
	}
	if hasAudioCompression(name) {
		parts = append(parts, fmt.Sprintf("c%d", a.Compression))
		return strings.Join(parts, "_")
	}
	if b := strings.TrimSpace(a.Bitrate); b != "" {
		parts = append(parts, b)
	}
	return strings.Join(parts, "_")
}

// rateControlToken renders the effective rate-control knob the builder
// emits: crf<N> / qp<N> / cbr<bitrate> / vbr<bitrate>. An empty mode
// falls back to the encoder's default (CQP for nvenc/qsv, AUTO for
// vaapi, CRF for software) so the token distinguishes the modes even
// when the profile stores the empty pre-selection. VAAPI's AUTO mode
// emits no rate-control flags until a bitrate is set, so its token is
// empty too, and "b<bitrate>" when one is — the honest shape of a
// bare -b:v.
func rateControlToken(encoderName string, v model.VideoSettings) string {
	mode := strings.ToUpper(strings.TrimSpace(v.RateControl))
	if mode == "" {
		if raw, ok := DefaultRegistry().Get(encoderName); ok {
			if ve, ok := raw.(VideoEncoder); ok {
				mode = string(ve.DefaultRateControl())
			}
		}
	}
	bitrate := strings.ToLower(strings.TrimSpace(v.Bitrate))
	switch mode {
	case "CRF":
		return fmt.Sprintf("crf%d", v.CRF)
	case "CQP", "QP":
		return fmt.Sprintf("qp%d", v.QP)
	case "VBR", "BITRATE":
		if bitrate == "" {
			return "vbr"
		}
		return "vbr" + bitrate
	case "CBR":
		if bitrate == "" {
			return "cbr"
		}
		return "cbr" + bitrate
	case "AUTO":
		if bitrate == "" {
			return ""
		}
		return "b" + bitrate
	}
	return ""
}

// canonicalProfile resolves the -profile:v spelling the command will
// carry: the curated-list spelling when the registry knows one (the
// same normalization the builders apply — "MAIN10" and " main10 " both
// serialize as "main10"), otherwise the trimmed value.
func canonicalProfile(encoderName, profile string) string {
	if profile == "" {
		return ""
	}
	raw, ok := DefaultRegistry().Get(encoderName)
	if !ok {
		return profile
	}
	ve, ok := raw.(VideoEncoder)
	if !ok {
		return profile
	}
	return canonicalCurated(ve.Profiles(), profile)
}

// effectivePreset renders the preset the command will carry: the
// user's pick, or the encoder's default when the builder substitutes
// one (nvenc p4, qsv medium, svtav1 6). Encoders without a preset —
// the VAAPI family — render nothing, mirroring their builders.
func effectivePreset(encoderName string, v model.VideoSettings) string {
	raw, ok := DefaultRegistry().Get(encoderName)
	if !ok {
		return ""
	}
	ve, ok := raw.(VideoEncoder)
	if !ok || !ve.SupportsPreset() {
		return ""
	}
	return defaultString(strings.TrimSpace(v.Preset), ve.DefaultPreset())
}

func supportsTune(encoderName string) bool {
	raw, ok := DefaultRegistry().Get(encoderName)
	if !ok {
		return false
	}
	ve, ok := raw.(VideoEncoder)
	return ok && ve.SupportsTune()
}

// hasAudioCompression reports whether the audio encoder consumes a
// compression level (flac today), matching the builder's
// compression-vs-bitrate dispatch.
func hasAudioCompression(encoderName string) bool {
	raw, ok := DefaultRegistry().Get(encoderName)
	if !ok {
		return false
	}
	ae, ok := raw.(interface {
		AudioCompressionRange() (IntegerRange, bool)
	})
	if !ok {
		return false
	}
	_, has := ae.AudioCompressionRange()
	return has
}
