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
func (e *vaapiEncoder) BuildVideoArgs(v model.VideoSettings) (BuildResult, error) {
	if v.Encoder == "" {
		return BuildResult{}, fmt.Errorf("video encoder is empty")
	}
	res := BuildResult{}
	if strings.TrimSpace(v.VAAPIDevice) != "" {
		res.PreInputArgs = []string{"-vaapi_device", v.VAAPIDevice}
	}
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

// Validate is a no-op for VAAPI encoders; encoder-specific rules are
// covered by the validation package's range checks.
func (e *vaapiEncoder) Validate(v model.VideoSettings) ([]Issue, error) {
	return nil, nil
}

// vaapiVideoEncoders returns the curated VAAPI video encoder set.
func vaapiVideoEncoders() []Encoder {
	return []Encoder{
		&vaapiEncoder{
			name: "h264_vaapi", label: "H.264 VA-API", codecFamily: "h264",
			priority: 60, qpRange: IntegerRange{Min: 1, Max: 51},
		},
		&vaapiEncoder{
			name: "hevc_vaapi", label: "H.265 VA-API", codecFamily: "hevc",
			priority: 50, qpRange: IntegerRange{Min: 1, Max: 51},
		},
		&vaapiEncoder{
			name: "av1_vaapi", label: "AV1 VA-API", codecFamily: "av1",
			priority: 40, qpRange: IntegerRange{Min: 0, Max: 255},
		},
		&vaapiEncoder{
			name: "vp9_vaapi", label: "VP9 VA-API", codecFamily: "vp9",
			priority: 30, qpRange: IntegerRange{Min: 0, Max: 255},
		},
	}
}
