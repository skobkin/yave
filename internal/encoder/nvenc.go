package encoder

import (
	"fmt"
	"strconv"
	"strings"

	"git.skobk.in/skobkin/yave/internal/model"
)

// nvencEncoder is the shared implementation for the NVIDIA NVENC video
// encoders: h264_nvenc, hevc_nvenc, av1_nvenc. The three encoders
// differ only in codec family, QP range, the supported preset set, and
// whether they accept -profile:v (av1_nvenc does not). Everything
// else — filter chain, rate-control mapping, multi-GPU selector — is
// shared.
//
// The multi-GPU selector -gpu N is emitted alongside the encoder args
// (after -c:v) because ffmpeg only exposes it as an nvenc encoder
// option; -gpu before -i fails with "Unrecognized option 'gpu'". When
// the user picks a non-default GPU, the same adapter is initialised
// before -i with `-init_hw_device cuda=cuda:N` and the filter graph is
// bound to it with `-filter_hw_device cuda`, so the hwupload_cuda /
// scale_cuda steps upload onto the same adapter the encoder uses;
// otherwise upload runs on ffmpeg's default CUDA device and the two
// can disagree on multi-GPU hosts. The filter chain is the modern CUDA
// path: format=yuv420p,format=nv12,hwupload_cuda followed by an
// optional scale_cuda. AV1 VBR is available from FFmpeg 5.0+ via the
// av1_nvenc encoder.
type nvencEncoder struct {
	name            string
	label           string
	codecFamily     string
	priority        int
	qpRange         IntegerRange
	presets         []string // categorical preset names
	supportsProfile bool
	presetFlag      string // "-preset"
	presetName      string // UI label
	presetDefault   string
}

func (e *nvencEncoder) Name() string         { return e.name }
func (e *nvencEncoder) Label() string        { return e.label }
func (e *nvencEncoder) Media() string        { return MediaVideo }
func (e *nvencEncoder) Kind() string         { return KindHardware }
func (e *nvencEncoder) Priority() int        { return e.priority }
func (e *nvencEncoder) HWBackend() HWBackend { return HWNVENC }
func (e *nvencEncoder) CodecFamily() string  { return e.codecFamily }
func (e *nvencEncoder) QPRange() (IntegerRange, bool) {
	return e.qpRange, e.qpRange.Max >= e.qpRange.Min
}
func (e *nvencEncoder) CRFRange() (IntegerRange, bool) { return IntegerRange{}, false }
func (e *nvencEncoder) PresetRange() (IntegerRange, bool) {
	// NVENC preset is categorical.
	return IntegerRange{}, false
}
func (e *nvencEncoder) SupportsTune() bool    { return false }
func (e *nvencEncoder) SupportsProfile() bool { return e.supportsProfile }
func (e *nvencEncoder) RateControlModes() []RateControl {
	return []RateControl{RCCQP, RCVBR, RCCBR}
}
func (e *nvencEncoder) DefaultRateControl() RateControl { return RCCQP }

// VideoFilter emits the modern NVENC hw upload chain. The
// format=yuv420p step ensures the source is in 8-bit 4:2:0 before
// nv12 conversion; not all sources arrive as yuv420p.
//
//	format=yuv420p,format=nv12,hwupload_cuda[,scale_cuda=w=W:h=H[:opts]]
func (e *nvencEncoder) VideoFilter(v model.VideoSettings, media model.MediaInfo) (string, error) {
	filters, err := appendFrameRateFilter(v)
	if err != nil {
		return "", err
	}
	width, height, scale := targetDimensions(v, media)
	filters = append(filters, "format=yuv420p", "format=nv12", "hwupload_cuda")
	if scale {
		part := fmt.Sprintf("scale_cuda=w=%d:h=%d", width, height)
		if v.KeepAspectRatio {
			part += ":force_original_aspect_ratio=decrease:force_divisible_by=2"
		}
		filters = append(filters, part)
	}
	return strings.Join(filters, ","), nil
}

// BuildVideoArgs emits the pre-input CUDA init / filter binding plus
// the per-stream NVENC args. When the user picks a non-default GPU,
// the encoder gets -gpu N (folded into the output args because ffmpeg
// only recognises -gpu as an nvenc encoder option), and the matching
// adapter is brought up before -i with
// `-init_hw_device cuda=cuda:N -filter_hw_device cuda` so the
// hwupload_cuda / scale_cuda steps upload onto the same GPU the
// encoder uses.
//
// The rate-control mapping is -rc constqp/vbr/cbr with the matching
// -qp / -b:v / -maxrate flags. CBR forces -maxrate = -b:v when the
// user did not specify a maxrate, matching the ffmpeg requirement
// that CBR targets must equal the maximum rate.
func (e *nvencEncoder) BuildVideoArgs(v model.VideoSettings) (BuildResult, error) {
	if v.Encoder == "" {
		return BuildResult{}, fmt.Errorf("video encoder is empty")
	}
	res := BuildResult{}
	if v.GPUIndex > 0 {
		// Bind the filter graph's CUDA context to the same adapter
		// the encoder targets. Without this, hwupload_cuda /
		// scale_cuda run on ffmpeg's default CUDA device and may
		// disagree with the -gpu N the encoder receives.
		res.PreInputArgs = []string{
			"-init_hw_device", "cuda=cuda:" + strconv.Itoa(v.GPUIndex),
			"-filter_hw_device", "cuda",
		}
	}
	args := []string{"-c:v", e.name}
	if v.GPUIndex > 0 {
		args = append(args, "-gpu", strconv.Itoa(v.GPUIndex))
	}
	preset := defaultString(v.Preset, e.presetDefault)
	if preset != "" {
		args = append(args, e.presetFlag, preset)
	}
	mode := rateControlMode(v)
	switch mode {
	case "CQP", "":
		args = append(args, "-rc", "constqp", "-qp", strconv.Itoa(v.QP))
	case "VBR":
		if strings.TrimSpace(v.Bitrate) == "" {
			return BuildResult{}, fmt.Errorf("VBR rate control requires a target bitrate")
		}
		args = append(args, "-rc", "vbr", "-b:v", v.Bitrate)
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
		args = append(args, "-rc", "cbr", "-b:v", v.Bitrate)
		// CBR requires b:v == maxrate; auto-promote when missing.
		maxrate := v.Maxrate
		if maxrate == "" {
			maxrate = v.Bitrate
		}
		args = append(args, "-maxrate", maxrate)
		if v.Bufsize != "" {
			args = append(args, "-bufsize", v.Bufsize)
		}
	default:
		return BuildResult{}, fmt.Errorf("unsupported NVENC rate control %q", v.RateControl)
	}
	if e.supportsProfile && v.Profile != "" {
		args = append(args, "-profile:v", v.Profile)
	}
	res.Args = args
	return res, nil
}

// Validate checks the categorical preset set. av1_nvenc rejects
// -profile:v before BuildVideoArgs can be reached (av1 has no
// profile flag in ffmpeg) so we surface it as a hard error here.
func (e *nvencEncoder) Validate(v model.VideoSettings) ([]Issue, error) {
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
	if !e.supportsProfile && strings.TrimSpace(v.Profile) != "" {
		issues = append(issues, Issue{
			Level:   IssueError,
			Field:   "profile.video.profile",
			Message: fmt.Sprintf("%s does not support -profile:v", e.name),
		})
	}
	return issues, nil
}

// nvencVideoEncoders returns the curated NVENC video encoder set.
func nvencVideoEncoders() []Encoder {
	// h264 / hevc accept the modern p1..p7 set plus the legacy names
	// (slow / medium / fast / hp / hq / bd) for compatibility with
	// older ffmpeg builds. av1_nvenc (FFmpeg 5.0+) only accepts p1..p7.
	h264HevcPresets := []string{
		"p1", "p2", "p3", "p4", "p5", "p6", "p7",
		"slow", "medium", "fast",
		"hp", "hq", "bd",
	}
	av1Presets := []string{"p1", "p2", "p3", "p4", "p5", "p6", "p7"}

	return []Encoder{
		&nvencEncoder{
			name: "h264_nvenc", label: "H.264 NVENC", codecFamily: "h264",
			priority:        50,
			qpRange:         IntegerRange{Min: 0, Max: 51},
			presets:         h264HevcPresets,
			supportsProfile: true,
			presetFlag:      "-preset", presetName: "preset", presetDefault: "p4",
		},
		&nvencEncoder{
			name: "hevc_nvenc", label: "H.265 NVENC", codecFamily: "hevc",
			priority:        40,
			qpRange:         IntegerRange{Min: 0, Max: 51},
			presets:         h264HevcPresets,
			supportsProfile: true,
			presetFlag:      "-preset", presetName: "preset", presetDefault: "p4",
		},
		&nvencEncoder{
			name: "av1_nvenc", label: "AV1 NVENC", codecFamily: "av1",
			priority:        30,
			qpRange:         IntegerRange{Min: 0, Max: 255},
			presets:         av1Presets,
			supportsProfile: false, // ffmpeg's av1_nvenc rejects -profile:v
			presetFlag:      "-preset", presetName: "preset", presetDefault: "p4",
		},
	}
}
