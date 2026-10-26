package model

import "time"

const ConfigVersion = 1

type Config struct {
	Version        int          `yaml:"version"`
	Profiles       []Profile    `yaml:"profiles"`
	DefaultProfile string       `yaml:"default_profile"`
	FFmpeg         FFmpegConfig `yaml:"ffmpeg"`
}

type FFmpegConfig struct {
	FFmpegPath  string `yaml:"ffmpeg_path"`
	FFprobePath string `yaml:"ffprobe_path"`
}

type Profile struct {
	Name           string           `yaml:"name"`
	Container      string           `yaml:"container"`
	OutputTemplate string           `yaml:"output_template"`
	Video          VideoSettings    `yaml:"video"`
	Audio          AudioSettings    `yaml:"audio"`
	Subtitles      SubtitleSettings `yaml:"subtitles"`
}

type VideoSettings struct {
	Encoder          string `yaml:"encoder"`
	RateControl      string `yaml:"rate_control"`
	QP               int    `yaml:"qp"`
	CRF              int    `yaml:"crf"`
	Bitrate          string `yaml:"bitrate"`
	Maxrate          string `yaml:"maxrate"`
	Bufsize          string `yaml:"bufsize"`
	Preset           string `yaml:"preset"`
	Tune             string `yaml:"tune"`
	Profile          string `yaml:"profile"`
	FrameRateMode    string `yaml:"frame_rate_mode"`
	FrameRate        string `yaml:"frame_rate"`
	MaxWidth         int    `yaml:"max_width"`
	MaxHeight        int    `yaml:"max_height"`
	KeepAspectRatio  bool   `yaml:"keep_aspect_ratio"`
	PreventUpscaling bool   `yaml:"prevent_upscaling"`
	VAAPIDevice      string `yaml:"vaapi_device"`
	// QSVDevice is the render-node path passed to -init_hw_device qsv=...
	// when the selected encoder is a QSV backend. Empty means "default".
	QSVDevice string `yaml:"qsv_device"`
	// GPUIndex is the NVENC multi-GPU selector passed to -gpu N.
	// Zero means "single-GPU / use the default adapter".
	GPUIndex int `yaml:"gpu_index"`
}

type AudioSettings struct {
	Mode           string `yaml:"mode"`
	Encoder        string `yaml:"encoder"`
	Bitrate        string `yaml:"bitrate"`
	Compression    int    `yaml:"compression"`
	CopyAllStreams bool   `yaml:"copy_all_streams"`
}

type SubtitleSettings struct {
	Mode string `yaml:"mode"`
}

type Encoder struct {
	Name     string
	Label    string
	Media    string
	Kind     string
	Priority int
	// Profiles lists the -profile:v values this build accepts for the
	// encoder (curated list narrowed by the build's own enumeration).
	// Empty means no profile values are offered for this encoder.
	Profiles []string
}

type Capabilities struct {
	FFmpegPath     string
	FFprobePath    string
	FFmpegVersion  string
	FFprobeVersion string
	Encoders       map[string]Encoder
	HWAccels       []string
	// Hardware-availability probe results. Encoders whose backend device
	// is absent are already filtered out of Encoders; these fields record
	// the probe outcome for display.
	HWVAAPIAvailable bool
	HWQSVAvailable   bool
	HWNVENCAvailable bool
	NVENCGPUCount    int
}

type MediaInfo struct {
	Path            string
	Format          string
	Duration        time.Duration
	Bitrate         int64
	VideoStreams    []VideoStream
	AudioStreams    []AudioStream
	SubtitleStreams []SubtitleStream
	ChapterCount    int
}

type VideoStream struct {
	Index       int
	Codec       string
	Width       int
	Height      int
	FrameRate   string
	PixelFormat string
	ColorRange  string
}

type AudioStream struct {
	Index      int
	Codec      string
	SampleRate int
	Channels   int
}

type SubtitleStream struct {
	Index int
	Codec string
}

func (m MediaInfo) PrimaryVideo() (VideoStream, bool) {
	if len(m.VideoStreams) == 0 {
		return VideoStream{}, false
	}
	return m.VideoStreams[0], true
}

type Progress struct {
	Frame      int64
	OutTime    time.Duration
	Speed      string
	Bitrate    string
	Elapsed    time.Duration
	Percentage float64
	State      string
}

type RunPlan struct {
	Executable string
	Args       []string
	Preview    string
	OutputPath string
	Warnings   []string
}
