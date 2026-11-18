package encoder

import (
	"fmt"
	"strconv"
	"strings"

	"git.skobk.in/skobkin/yave/internal/model"
	"git.skobk.in/skobkin/yave/internal/vaapi"
)

// vaapiEncoder is the shared implementation for the VAAPI video
// encoders: h264_vaapi, hevc_vaapi, av1_vaapi, vp9_vaapi. The four
// encoders differ only in codec family and QP range; everything else
// is the VAAPI code path shared with the prior ffmpeg.buildVideoArgs
// switch.
type vaapiEncoder struct {
	name        string
	label       string
	codecFamily string
	priority    int
	qpRange     IntegerRange
	// profiles is the curated -profile:v value list; see Profiles().
	profiles []string
}

func (e *vaapiEncoder) Name() string         { return e.name }
func (e *vaapiEncoder) Label() string        { return e.label }
func (e *vaapiEncoder) Media() string        { return MediaVideo }
func (e *vaapiEncoder) Kind() string         { return KindHardware }
func (e *vaapiEncoder) Priority() int        { return e.priority }
func (e *vaapiEncoder) HWBackend() HWBackend { return HWVAAPI }
func (e *vaapiEncoder) CodecFamily() string  { return e.codecFamily }
func (e *vaapiEncoder) QPRange() (IntegerRange, bool) {
	return e.qpRange, e.qpRange.Max >= e.qpRange.Min
}
func (e *vaapiEncoder) CRFRange() (IntegerRange, bool)    { return IntegerRange{}, false }
func (e *vaapiEncoder) PresetRange() (IntegerRange, bool) { return IntegerRange{}, false }
func (e *vaapiEncoder) SupportsTune() bool                { return false }
func (e *vaapiEncoder) SupportsProfile() bool             { return true }
func (e *vaapiEncoder) SupportsPreset() bool              { return false }

// The VAAPI encoders have no preset or tune flags in ffmpeg, so there
// is nothing to curate.
func (e *vaapiEncoder) Presets() []string     { return nil }
func (e *vaapiEncoder) Tunes() []string       { return nil }
func (e *vaapiEncoder) DefaultPreset() string { return "" }
func (e *vaapiEncoder) Profiles() []string    { return e.profiles }
func (e *vaapiEncoder) RateControlModes() []RateControl {
	return []RateControl{RCAuto, RCCQP, RCVBR, RCCBR}
}
func (e *vaapiEncoder) DefaultRateControl() RateControl { return RCAuto }

// VideoFilter emits the canonical VAAPI hwupload chain followed by an
// optional scale_vaapi filter. The frame-rate conversion step is
// prepended when requested, matching the prior ffmpeg.videoFilter
// ordering.
//
// The upload format follows TenBitChain: the 8-bit chain is the
// historical string
//
//	format=nv12,hwupload[,scale_vaapi=w=W:h=H[...]]
//
// while a 10-bit chain uploads p010le instead of nv12 — same shape,
// deeper surface. scale_vaapi preserves the surface format (verified:
// a p010 chain through a real resize probes back as 10-bit), so the
// scaler needs no variant.
func (e *vaapiEncoder) VideoFilter(v model.VideoSettings, media model.MediaInfo) (string, error) {
	filters, err := appendFrameRateFilter(v)
	if err != nil {
		return "", err
	}
	width, height, scale := targetDimensions(v, media)
	uploadFormat := "nv12"
	if TenBitChain(e.name, v.Profile, media) {
		uploadFormat = "p010le"
	}
	filters = append(filters, "format="+uploadFormat, "hwupload")
	if scale {
		part := fmt.Sprintf("scale_vaapi=w=%d:h=%d", width, height)
		if v.KeepAspectRatio {
			part += ":force_original_aspect_ratio=decrease:force_divisible_by=2"
		}
		filters = append(filters, part)
	}
	return strings.Join(filters, ","), nil
}

// RequiredVAProfiles maps a curated -profile:v value to the VAProfile
// or profiles ffmpeg's VAAPI encoder accepts for it through the chain
// TenBitChain selects for the given media. The table mirrors the
// per-codec VAAPIEncodeProfile tables in libavcodec
// (vaapi_encode_h264.c, vaapi_encode_h265.c, vaapi_encode_av1.c,
// vaapi_encode_vp9.c). The depth-aware rows resolve through the shared
// TenBitChain decision — never a Main+Main10 union for the auto case:
// the device picker accepts any listed profile, so a union would let a
// Main-only device be selected for a p010 chain it cannot serve. The
// empty/auto profile maps to the single profile the chain will use
// (Main for an 8-bit chain, Main10 for a 10-bit one) — vaapi_encode
// auto-selects from the surface format the same way, and any one
// candidate keeps the conversion runnable. Unknown values return nil;
// curated membership is validated elsewhere.
func (e *vaapiEncoder) RequiredVAProfiles(profile string, media model.MediaInfo) []vaapi.Profile {
	p := strings.ToLower(strings.TrimSpace(profile))
	tenBit := TenBitChain(e.name, profile, media)
	switch e.codecFamily {
	case "h264":
		switch p {
		case "constrained_baseline":
			return []vaapi.Profile{vaapi.ProfileH264ConstrainedBaseline}
		case "main":
			return []vaapi.Profile{vaapi.ProfileH264Main}
		case "high":
			return []vaapi.Profile{vaapi.ProfileH264High}
		case "":
			return []vaapi.Profile{
				vaapi.ProfileH264ConstrainedBaseline, vaapi.ProfileH264Main, vaapi.ProfileH264High,
			}
		}
	case "hevc":
		switch {
		case tenBit:
			// Explicit main10, or auto on a 10-bit source: the chain
			// uploads p010le, and the profile table serves it with
			// VAProfileHEVCMain10 (vaapi_encode_h265.c).
			return []vaapi.Profile{vaapi.ProfileHEVCMain10}
		case p == "main", p == "rext", p == "":
			// 8-bit 4:2:0 rext is encoded as HEVCMain by ffmpeg.
			return []vaapi.Profile{vaapi.ProfileHEVCMain}
		}
	case "av1":
		switch p {
		case "main", "":
			// The AV1 table has only Main rows (8- and 10-bit both map
			// to VAProfileAV1Profile0), so one profile covers both
			// chains; high/professional have no table rows at all and
			// fail with "No usable encoding profile found." regardless
			// of surface — they stay out of the curated list.
			return []vaapi.Profile{vaapi.ProfileAV1Profile0}
		}
	case "vp9":
		switch {
		case tenBit:
			// vp9_vaapi "2" → VAProfileVP9Profile2 (10-bit 4:2:0;
			// vaapi_encode_vp9.c).
			return []vaapi.Profile{vaapi.ProfileVP9Profile2}
		case p == "0", p == "":
			return []vaapi.Profile{vaapi.ProfileVP9Profile0}
		}
	}
	return nil
}

// RequiredVAProfiles resolves the VAProfile requirement for an encoder
// name, curated -profile:v value, and source media through the default
// registry. ok is false for non-VAAPI encoders and unknown names, so
// callers can skip the device-compatibility logic for everything else.
func RequiredVAProfiles(encoderName, profile string, media model.MediaInfo) ([]vaapi.Profile, bool) {
	raw, ok := DefaultRegistry().Get(strings.ToLower(strings.TrimSpace(encoderName)))
	if !ok {
		return nil, false
	}
	va, ok := raw.(*vaapiEncoder)
	if !ok {
		return nil, false
	}
	return va.RequiredVAProfiles(profile, media), true
}

// BuildVideoArgs emits the pre-input -vaapi_device flag plus the
// per-stream rate-control args. The switch mirrors the prior
// ffmpeg.buildVideoArgs VAAPI branch.
//
// A blank VAAPIDevice means "automatic": validation and the UI resolve
// it against the probed devices and materialize a concrete path before
// command generation. Reaching the builder blank therefore means no
// resolution was possible (no detection, no render nodes), which is
// still a configuration error rather than a "use default" case —
// silently dropping the flag produces an unusable command that fails
// only when the user runs it. Surface the error here so command
// generation matches the validation UI.
func (e *vaapiEncoder) BuildVideoArgs(v model.VideoSettings) (BuildResult, error) {
	if v.Encoder == "" {
		return BuildResult{}, fmt.Errorf("video encoder is empty")
	}
	if strings.TrimSpace(v.VAAPIDevice) == "" {
		return BuildResult{}, fmt.Errorf("VAAPI device path is empty (automatic device selection requires capability detection)")
	}
	res := BuildResult{}
	res.PreInputArgs = []string{"-vaapi_device", v.VAAPIDevice}
	args := []string{"-c:v", e.name}
	mode := strings.ToUpper(strings.TrimSpace(v.RateControl))
	switch mode {
	case "", "AUTO":
		if v.Bitrate != "" {
			args = append(args, "-b:v", v.Bitrate)
		}
	case "CQP":
		args = append(args, "-rc_mode", "CQP", "-qp", strconv.Itoa(v.QP))
	case "VBR", "CBR":
		args = append(args, "-rc_mode", strings.ToUpper(v.RateControl))
		if v.Bitrate == "" {
			return BuildResult{}, fmt.Errorf("%s rate control requires a target bitrate", v.RateControl)
		}
		args = append(args, "-b:v", v.Bitrate)
		if v.Maxrate != "" {
			args = append(args, "-maxrate", v.Maxrate)
		}
		if v.Bufsize != "" {
			args = append(args, "-bufsize", v.Bufsize)
		}
	default:
		return BuildResult{}, fmt.Errorf("unsupported VAAPI rate control %q", v.RateControl)
	}
	// Emit the canonical curated spelling: validation accepts the
	// trimmed case-insensitive form, but ffmpeg's option parser does
	// not (see canonicalCurated).
	if p := canonicalCurated(e.profiles, v.Profile); p != "" {
		args = append(args, "-profile:v", p)
	}
	res.Args = args
	return res, nil
}

// Validate rejects out-of-list -profile:v values. VAAPI always accepts
// the flag itself (a generic AVCodecContext option), so only curated
// membership is enforced.
func (e *vaapiEncoder) Validate(v model.VideoSettings) ([]Issue, error) {
	return profileIssues(e.SupportsProfile(), e.profiles, e.name, v.Profile), nil
}

// vaapiVideoEncoders returns the curated VAAPI video encoder set.
//
// The profile lists start from `ffmpeg -h encoder=<name>` of the
// reference build and are then restricted to the values that can
// actually encode through the chain TenBitChain selects. ffmpeg's
// VAAPI encoder matches -profile:v against an internal
// profile↔surface-format table before the driver is reached, so a
// value without a matching table row fails every conversion with "No
// usable encoding profile found." regardless of hardware. Each chain
// serves exactly one surface format — nv12 (8-bit 4:2:0) or p010le
// (10-bit 4:2:0) — so a value is curatable when its table row accepts
// that chain's surface:
//
//   - h264: baseline/main/high are the 8-bit rows; high10 has a
//     10-bit row but no p010-served one in vaapi_encode_h264.c's
//     4:2:0 set — stays out.
//   - hevc: main and rext (rext@8 4:2:0 encodes as Main) through the
//     8-bit chain; main10 through the p010 chain
//     (VAProfileHEVCMain10). 12-bit/422/444 rows have no chain — out.
//   - av1: main only. The vaapi_encode_av1_profiles table has ONLY
//     AV1 Main rows (8- and 10-bit both → VAProfileAV1Profile0), so
//     "high"/"professional" fail profile lookup on ANY surface —
//     verified on ffmpeg n9.0.2: -profile:v high over a p010 chain
//     aborts with "No usable encoding profile found." Main 10-bit is
//     reachable through the p010 chain with the same "main" value.
//   - vp9: "0" through the 8-bit chain; "2" through the p010 chain
//     (VAProfileVP9Profile2, the 10-bit 4:2:0 profile). 1/3 are 4:4:4
//     — out. vp9_vaapi exposes no -h enumeration, but the generic
//     profile_idc integers parse and reach the driver.
//
// The 10-bit values were verified end-to-end on ffmpeg n9.0.2
// (hevc_vaapi main10 with and without scale_vaapi, 8-bit input
// included; the no-profile auto case picks Main 10 from a p010
// surface). This table mechanism is VAAPI-specific — QSV validates via
// MFX and NVENC via the driver (see their constructor comments).
func vaapiVideoEncoders() []Encoder {
	return []Encoder{
		&vaapiEncoder{
			name: "h264_vaapi", label: "H.264 VA-API", codecFamily: "h264",
			priority: 60, qpRange: IntegerRange{Min: 1, Max: 51},
			profiles: []string{"constrained_baseline", "main", "high"}, // high10 has no p010-served row
		},
		&vaapiEncoder{
			name: "hevc_vaapi", label: "H.265 VA-API", codecFamily: "hevc",
			priority: 50, qpRange: IntegerRange{Min: 1, Max: 51},
			// main10 rides the p010 chain (TenBitChain); main/rext the
			// 8-bit one.
			profiles: []string{"main", "main10", "rext"},
		},
		&vaapiEncoder{
			name: "av1_vaapi", label: "AV1 VA-API", codecFamily: "av1",
			priority: 40, qpRange: IntegerRange{Min: 0, Max: 255},
			// Only Main has profile-table rows at all; 10-bit output
			// needs no separate value — the p010 chain + "main" encodes
			// Main 10-bit.
			profiles: []string{"main"},
		},
		&vaapiEncoder{
			name: "vp9_vaapi", label: "VP9 VA-API", codecFamily: "vp9",
			priority: 30, qpRange: IntegerRange{Min: 0, Max: 255},
			// "2" is the 10-bit profile through the p010 chain; 1/3 are
			// 4:4:4.
			profiles: []string{"0", "2"},
		},
	}
}
