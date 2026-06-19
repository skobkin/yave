package encoder

import (
	"fmt"
	"strconv"
	"strings"

	"git.skobk.in/skobkin/yave/internal/model"
)

// qsvEncoder is the shared implementation for the Intel QSV video
// encoders: h264_qsv, hevc_qsv, av1_qsv, vp9_qsv. The four encoders
// differ in codec family, QP range, and the supported preset set;
// everything else — pre-input args, filter chain, rate-control
// mapping — is shared.
//
// Pre-input flags are always emitted: the QSV device is initialized
// with `-init_hw_device qsv=qsv` (or `-init_hw_device qsv=qsv:hw,child_device=<path>`
// when the user has picked a render node) under the explicit name
// "qsv", and the filter graph is bound to that same device with
// `-filter_hw_device qsv`. Without `-filter_hw_device`, the
// `format=nv12,hwupload,...` filter chain has no hw context to upload
// software-decoded frames into, so QSV profiles would fail at filter
// initialization. The `=` after `qsv` would set the device label, not
// the child device, so the render-node path uses `child_device=` instead.
// The filter chain is the QSV hw upload path:
// format=nv12,hwupload,format=qsv followed by an optional scale_qsv.
// `format=nv12` is required first so hwupload has a software pixel
// format it can ingest; the QSV retag (format=qsv) must come *after*
// hwupload, since format=qsv labels a frame as the QSV hardware pixfmt
// and the upload only succeeds on a software pixfmt.
type qsvEncoder struct {
	name            string
	label           string
	codecFamily     string
	priority        int
	qpRange         IntegerRange
	presets         []string // categorical preset names
	supportsProfile bool
	presetFlag      string
	presetName      string
	presetDefault   string
}

func (e *qsvEncoder) Name() string         { return e.name }
func (e *qsvEncoder) Label() string        { return e.label }
func (e *qsvEncoder) Media() string        { return MediaVideo }
func (e *qsvEncoder) Kind() string         { return KindHardware }
func (e *qsvEncoder) Priority() int        { return e.priority }
func (e *qsvEncoder) HWBackend() HWBackend { return HWQSV }
func (e *qsvEncoder) CodecFamily() string  { return e.codecFamily }
func (e *qsvEncoder) QPRange() (IntegerRange, bool) {
	return e.qpRange, e.qpRange.Max >= e.qpRange.Min
}
func (e *qsvEncoder) CRFRange() (IntegerRange, bool) { return IntegerRange{}, false }
func (e *qsvEncoder) PresetRange() (IntegerRange, bool) {
	// QSV preset is categorical.
	return IntegerRange{}, false
}
func (e *qsvEncoder) SupportsTune() bool    { return false }
func (e *qsvEncoder) SupportsProfile() bool { return e.supportsProfile }
func (e *qsvEncoder) SupportsPreset() bool  { return e.presetFlag != "" }
func (e *qsvEncoder) DefaultPreset() string { return e.presetDefault }
func (e *qsvEncoder) RateControlModes() []RateControl {
	return []RateControl{RCCQP, RCVBR, RCCBR}
}
func (e *qsvEncoder) DefaultRateControl() RateControl { return RCCQP }

// VideoFilter emits the QSV hw upload chain. The frame first has to be
// converted to a software YUV420 pixel format (nv12) — that is what
// hwupload can ingest from CPU memory — then hwupload actually moves
// the frame onto the QSV device, and finally the frame is retagged with
// the QSV hardware pixfmt via format=qsv so scale_qsv and the QSV
// encoder see it as a QSV hw frame. Retagging to the QSV hw pixfmt
// *before* hwupload (format=nv12,format=qsv,hwupload) asks ffmpeg to
// label a software-decoded CPU frame as QSV hardware, and filter
// negotiation fails because no such conversion exists.
//
//	format=nv12,hwupload,format=qsv[,scale_qsv=w=W:h=H[:opts]]
func (e *qsvEncoder) VideoFilter(v model.VideoSettings, media model.MediaInfo) (string, error) {
	filters, err := appendFrameRateFilter(v)
	if err != nil {
		return "", err
	}
	width, height, scale := targetDimensions(v, media)
	filters = append(filters, "format=nv12", "hwupload", "format=qsv")
	if scale {
		// scale_qsv does not accept force_original_aspect_ratio or
		// force_divisible_by (those are software-scale options).
		// targetDimensions has already computed the aspect-fit and
		// even-aligned width/height for us, so the plain form is
		// correct for the common "scale + keep aspect ratio" profile.
		filters = append(filters, fmt.Sprintf("scale_qsv=w=%d:h=%d", width, height))
	}
	return strings.Join(filters, ","), nil
}

// BuildVideoArgs emits the pre-input -init_hw_device / -filter_hw_device
// pair plus the per-stream QSV args. The QSV device is initialised under
// the explicit name "qsv" so the filter graph can be bound to it via
// -filter_hw_device qsv; the hwupload filter in the QSV chain needs
// that binding when frames arrive in software memory (e.g. decoded by
// a software decoder).
//
// When the user picks a render node the init flag becomes
// -init_hw_device qsv:hw,child_device=<path> so ffmpeg opens the
// underlying DRM node; using qsv=<path> would just label the device
// and leave the child device unspecified.
//
// The rate-control mapping is -rc_mode CQP/VBR/CBR with the matching
// -qp / -b:v / -maxrate flags. CBR forces -maxrate = -b:v when the
// user did not specify a maxrate, matching the ffmpeg requirement
// that CBR targets must equal the maximum rate.
func (e *qsvEncoder) BuildVideoArgs(v model.VideoSettings) (BuildResult, error) {
	if v.Encoder == "" {
		return BuildResult{}, fmt.Errorf("video encoder is empty")
	}
	res := BuildResult{}
	// Name the QSV device explicitly ("qsv") so the filter graph can
	// bind to it via -filter_hw_device; without that, format=qsv,hwupload
	// has no hw context and the filter chain fails to initialise. The
	// "=name" part of `-init_hw_device type[=name][:device,...]` sets
	// the device label, while the ":device,key=val" part picks the
	// underlying child (render node here).
	const deviceName = "qsv"
	var initSpec string
	if strings.TrimSpace(v.QSVDevice) == "" {
		initSpec = "qsv=" + deviceName
	} else {
		initSpec = "qsv=" + deviceName + ":hw,child_device=" + v.QSVDevice
	}
	res.PreInputArgs = []string{"-init_hw_device", initSpec, "-filter_hw_device", deviceName}
	args := []string{"-c:v", e.name}
	preset := defaultString(v.Preset, e.presetDefault)
	if preset != "" {
		args = append(args, e.presetFlag, preset)
	}
	mode := rateControlMode(v)
	switch mode {
	case "CQP", "":
		args = append(args, "-rc_mode", "CQP", "-qp", strconv.Itoa(v.QP))
	case "VBR":
		if strings.TrimSpace(v.Bitrate) == "" {
			return BuildResult{}, fmt.Errorf("VBR rate control requires a target bitrate")
		}
		args = append(args, "-rc_mode", "VBR", "-b:v", v.Bitrate)
		if v.Maxrate != "" {
			args = append(args, "-maxrate", v.Maxrate)
		}
		if v.Bufsize != "" {
			args = append(args, "-bufsize", v.Bufsize)
		}
	case "CBR":
		if strings.TrimSpace(v.Bitrate) == "" {
			return BuildResult{}, fmt.Errorf("CBR rate control requires a target bitrate")
		}
		args = append(args, "-rc_mode", "CBR", "-b:v", v.Bitrate)
		maxrate := v.Maxrate
		if maxrate == "" {
			maxrate = v.Bitrate
		}
		args = append(args, "-maxrate", maxrate)
		if v.Bufsize != "" {
			args = append(args, "-bufsize", v.Bufsize)
		}
	default:
		return BuildResult{}, fmt.Errorf("unsupported QSV rate control %q", v.RateControl)
	}
	if e.supportsProfile && v.Profile != "" {
		args = append(args, "-profile:v", v.Profile)
	}
	res.Args = args
	return res, nil
}

// Validate checks the categorical preset set. The QSV presets mirror
// x264's "veryfast..veryslow" plus the slow/fast/medium aliases.
//
// QSV's CBR mode requires -b:v == -maxrate (ffmpeg silently ignores
// the maxrate otherwise, producing a VBR-ish stream). The builder
// auto-promotes Maxrate when it is blank, but does not catch a
// user-supplied mismatch, so we surface it here.
func (e *qsvEncoder) Validate(v model.VideoSettings) ([]Issue, error) {
	var issues []Issue
	if len(e.presets) > 0 && strings.TrimSpace(v.Preset) != "" {
		want := strings.ToLower(strings.TrimSpace(v.Preset))
		ok := false
		for _, p := range e.presets {
			if strings.ToLower(p) == want {
				ok = true
				break
			}
		}
		if !ok {
			issues = append(issues, Issue{
				Level:   IssueError,
				Field:   "profile.video.preset",
				Message: fmt.Sprintf("%s preset must be one of: %s", e.name, strings.Join(e.presets, ", ")),
			})
		}
	}
	if strings.EqualFold(strings.TrimSpace(v.RateControl), "CBR") &&
		strings.TrimSpace(v.Maxrate) != "" &&
		strings.TrimSpace(v.Bitrate) != "" &&
		!strings.EqualFold(strings.TrimSpace(v.Bitrate), strings.TrimSpace(v.Maxrate)) {
		issues = append(issues, Issue{
			Level:   IssueError,
			Field:   "profile.video.maxrate",
			Message: fmt.Sprintf("%s CBR requires maxrate to equal bitrate (%s); got %s", e.name, v.Bitrate, v.Maxrate),
		})
	}
	return issues, nil
}

// qsvVideoEncoders returns the curated QSV video encoder set.
func qsvVideoEncoders() []Encoder {
	// QSV presets mirror the libx264 categorical set the codebase
	// already exposes; av1_qsv / vp9_qsv accept the same names per
	// the ffmpeg source.
	qsvPresets := []string{
		"veryfast", "faster", "fast", "medium", "slow", "slower", "veryslow",
	}

	return []Encoder{
		&qsvEncoder{
			name: "h264_qsv", label: "H.264 QSV", codecFamily: "h264",
			priority:        45,
			qpRange:         IntegerRange{Min: 1, Max: 51},
			presets:         qsvPresets,
			supportsProfile: true,
			presetFlag:      "-preset", presetName: "preset", presetDefault: "medium",
		},
		&qsvEncoder{
			name: "hevc_qsv", label: "H.265 QSV", codecFamily: "hevc",
			priority:        35,
			qpRange:         IntegerRange{Min: 1, Max: 51},
			presets:         qsvPresets,
			supportsProfile: true,
			presetFlag:      "-preset", presetName: "preset", presetDefault: "medium",
		},
		&qsvEncoder{
			name: "av1_qsv", label: "AV1 QSV", codecFamily: "av1",
			priority:        25,
			qpRange:         IntegerRange{Min: 0, Max: 255},
			presets:         qsvPresets,
			supportsProfile: true,
			presetFlag:      "-preset", presetName: "preset", presetDefault: "medium",
		},
		&qsvEncoder{
			name: "vp9_qsv", label: "VP9 QSV", codecFamily: "vp9",
			priority:        20,
			qpRange:         IntegerRange{Min: 0, Max: 255},
			presets:         qsvPresets,
			supportsProfile: true,
			presetFlag:      "-preset", presetName: "preset", presetDefault: "medium",
		},
	}
}
