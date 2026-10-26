package encoder

import (
	"fmt"
	"strconv"
	"strings"

	"git.skobk.in/skobkin/yave/internal/model"
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
func (e *vaapiEncoder) DefaultPreset() string             { return "" }
func (e *vaapiEncoder) Profiles() []string                { return e.profiles }
func (e *vaapiEncoder) RateControlModes() []RateControl {
	return []RateControl{RCAuto, RCCQP, RCVBR, RCCBR}
}
func (e *vaapiEncoder) DefaultRateControl() RateControl { return RCAuto }

// VideoFilter emits the canonical VAAPI hwupload chain followed by an
// optional scale_vaapi filter. The frame-rate conversion step is
// prepended when requested, matching the prior ffmpeg.videoFilter
// ordering.
//
// The output string is byte-identical to the prior implementation:
//
//	format=nv12,hwupload[,scale_vaapi=w=W:h=H[:force_original_aspect_ratio=decrease:force_divisible_by=2]]
func (e *vaapiEncoder) VideoFilter(v model.VideoSettings, media model.MediaInfo) (string, error) {
	filters, err := appendFrameRateFilter(v)
	if err != nil {
		return "", err
	}
	width, height, scale := targetDimensions(v, media)
	filters = append(filters, "format=nv12", "hwupload")
	if scale {
		part := fmt.Sprintf("scale_vaapi=w=%d:h=%d", width, height)
		if v.KeepAspectRatio {
			part += ":force_original_aspect_ratio=decrease:force_divisible_by=2"
		}
		filters = append(filters, part)
	}
	return strings.Join(filters, ","), nil
}

// BuildVideoArgs emits the pre-input -vaapi_device flag plus the
// per-stream rate-control args. The switch mirrors the prior
// ffmpeg.buildVideoArgs VAAPI branch.
//
// A blank VAAPIDevice is a configuration error, not a "use default"
// case: -vaapi_device is required for ffmpeg to attach a VAAPI context
// to the filter graph, and silently dropping it produces an unusable
// command that fails only when the user runs it. Surface the error
// here so command generation matches the validation UI.
func (e *vaapiEncoder) BuildVideoArgs(v model.VideoSettings) (BuildResult, error) {
	if v.Encoder == "" {
		return BuildResult{}, fmt.Errorf("video encoder is empty")
	}
	if strings.TrimSpace(v.VAAPIDevice) == "" {
		return BuildResult{}, fmt.Errorf("VAAPI device path is empty")
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
	if v.Profile != "" {
		args = append(args, "-profile:v", v.Profile)
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
// actually encode through yave's fixed 8-bit 4:2:0 upload chain
// (format=nv12,hwupload): ffmpeg matches -profile:v against an
// internal profile↔surface-format table before the driver is reached,
// so a 10-bit or non-4:2:0 profile (h264 high10, hevc main10, av1
// high/professional, vp9 profile 2) can never match an nv12 surface
// and fails every conversion with "No usable encoding profile found."
// regardless of hardware. hevc rext was encode-verified to work
// through the nv12 chain. vp9_vaapi exposes no -h enumeration, but the
// generic profile_idc integer 0 parses and reaches the driver.
//
// Restoring the dropped profiles needs a 10-bit chain (p010 upload /
// vpp format conversion) — tracked in the issue tracker.
func vaapiVideoEncoders() []Encoder {
	return []Encoder{
		&vaapiEncoder{
			name: "h264_vaapi", label: "H.264 VA-API", codecFamily: "h264",
			priority: 60, qpRange: IntegerRange{Min: 1, Max: 51},
			profiles: []string{"constrained_baseline", "main", "high"}, // high10 needs a 10-bit surface
		},
		&vaapiEncoder{
			name: "hevc_vaapi", label: "H.265 VA-API", codecFamily: "hevc",
			priority: 50, qpRange: IntegerRange{Min: 1, Max: 51},
			profiles: []string{"main", "rext"}, // main10 needs a 10-bit surface
		},
		&vaapiEncoder{
			name: "av1_vaapi", label: "AV1 VA-API", codecFamily: "av1",
			priority: 40, qpRange: IntegerRange{Min: 0, Max: 255},
			profiles: []string{"main"}, // high/professional need 4:4:4/4:2:2 surfaces
		},
		&vaapiEncoder{
			name: "vp9_vaapi", label: "VP9 VA-API", codecFamily: "vp9",
			priority: 30, qpRange: IntegerRange{Min: 0, Max: 255},
			profiles: []string{"0"}, // profile 2 is 10-bit
		},
	}
}
