package model

import (
	"fmt"
	"strings"
	"time"

	"git.skobk.in/skobkin/yave/internal/vaapi"
)

const ConfigVersion = 1

type Config struct {
	Version        int                 `yaml:"version"`
	Profiles       []Profile           `yaml:"profiles"`
	DefaultProfile string              `yaml:"default_profile"`
	FFmpeg         FFmpegConfig        `yaml:"ffmpeg"`
	Updates        UpdatesConfig       `yaml:"updates"`
	UI             UIConfig            `yaml:"ui"`
	Notifications  NotificationsConfig `yaml:"notifications"`
}

// NotificationsConfig controls desktop notifications. Every flag is a
// "disable" switch so a config written before this section existed keeps
// all notifications on.
type NotificationsConfig struct {
	DisableAll       bool `yaml:"disable_all"`
	DisableCompleted bool `yaml:"disable_completed"`
	DisableFailed    bool `yaml:"disable_failed"`
	DisableCanceled  bool `yaml:"disable_canceled"`
}

// UIConfig holds window behavior settings. The zero value keeps the
// historical behavior (closing the window hides it to the tray), so configs
// written before these keys existed are unchanged.
type UIConfig struct {
	// QuitOnClose makes the window close button quit the application
	// instead of hiding the window to the system tray.
	QuitOnClose bool `yaml:"quit_on_close"`
	// TrayNoticeShown records that the one-time "keeps running in the
	// tray" notice has been shown.
	TrayNoticeShown bool `yaml:"tray_notice_shown"`
	// WindowWidth / WindowHeight remember the main window's size in
	// device-independent pixels; zero means "use the default".
	// LastInputDir is where the open-file dialog starts; RecentFiles lists
	// the most recently opened inputs, newest first.
	LastInputDir string   `yaml:"last_input_dir,omitempty"`
	RecentFiles  []string `yaml:"recent_files,omitempty"`

	WindowWidth  int `yaml:"window_width,omitempty"`
	WindowHeight int `yaml:"window_height,omitempty"`
}

type FFmpegConfig struct {
	FFmpegPath  string `yaml:"ffmpeg_path"`
	FFprobePath string `yaml:"ffprobe_path"`
}

// UpdatesConfig controls automatic update checks against the release forge.
type UpdatesConfig struct {
	// CheckAutomatically enables the startup and periodic update checks.
	CheckAutomatically bool `yaml:"check_automatically"`
}

type Profile struct {
	Name           string           `yaml:"name"`
	Container      string           `yaml:"container"`
	OutputTemplate string           `yaml:"output_template"`
	Video          VideoSettings    `yaml:"video"`
	Audio          AudioSettings    `yaml:"audio"`
	Subtitles      SubtitleSettings `yaml:"subtitles"`
	// ExcludeVideo / ExcludeAudio drop that stream type from the output
	// (audio extraction, muted video). They are stored inverted so a
	// config written before these keys existed keeps including both.
	ExcludeVideo bool `yaml:"exclude_video"`
	ExcludeAudio bool `yaml:"exclude_audio"`
}

// OutputExtension returns the file extension (without the dot) for the
// profile's output: the container name, or the audio-only variant when
// video is excluded (mka for Matroska, m4a for MP4; WebM keeps webm).
func (p Profile) OutputExtension() string {
	if p.ExcludeVideo && !p.ExcludeAudio {
		switch p.Container {
		case ContainerMKV:
			return "mka"
		case ContainerMP4:
			return "m4a"
		}
	}
	return p.Container
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
	// VAAPIDevice is the render-node path passed to -vaapi_device when
	// the selected encoder is a VAAPI backend. Empty means "automatic":
	// yave picks the first probed device that can encode the selected
	// codec and profile.
	VAAPIDevice string `yaml:"vaapi_device"`
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

// IsStreamCopy reports whether the audio settings select stream copy.
// ffmpeg argument generation and validation must agree on this predicate,
// so both go through this method: copy is selected either via the mode or
// via the encoder name, case- and whitespace-insensitively.
func (a AudioSettings) IsStreamCopy() bool {
	if a.Mode == AudioCopy {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(a.Encoder), "copy")
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
	// RenderNodes lists the DRM render nodes found on the host, sorted.
	// Linux-only: nil on other platforms and when nothing was found.
	RenderNodes []string
	// VAAPIDevices records the per-render-node VA-API probe (driver
	// string and per-profile entrypoints). Linux-only: nil when probing
	// was skipped or libva is unavailable. Devices whose capabilities
	// could not be probed appear with Err set — the node inventory
	// stays complete either way.
	VAAPIDevices []vaapi.DeviceInfo
	// GPUAdapters lists the display adapters from a complete system
	// enumeration (DXGI on Windows), in enumeration order. Index is the
	// adapter ordinal ffmpeg accepts as a device string on Windows —
	// both the VAAPI win32 display and the QSV d3d11va child device
	// take atoi(device) against this order. Nil means no authoritative
	// inventory exists on this platform or the enumeration did not
	// complete: a partial list is never published, because consumers
	// use it to decide that a saved device has vanished.
	GPUAdapters []GPUAdapter
}

// GPUAdapter is one entry of Capabilities.GPUAdapters.
type GPUAdapter struct {
	Index    int
	Name     string
	VendorID uint32 // PCI vendor ID: 0x10DE NVIDIA, 0x8086 Intel, 0x1002/0x1022 AMD
}

type MediaInfo struct {
	Path     string
	Format   string
	Duration time.Duration
	Bitrate  int64
	// Size is the file size in bytes from the container, 0 when unknown.
	Size         int64
	VideoStreams []VideoStream
	// CoverArtStreams holds attached pictures (disposition attached_pic,
	// e.g. MP3/M4A cover art). They are video streams to ffmpeg but not
	// video content, so they are kept out of VideoStreams: an audio file
	// with a cover is an audio-only input.
	CoverArtStreams []VideoStream
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
	Bitrate     int64 // bits per second, 0 when unknown
	Language    string
	Title       string
	// ColorSpace / ColorTransfer / ColorPrimaries are ffprobe's color
	// metadata names ("bt2020nc", "smpte2084", …), empty when absent.
	ColorSpace     string
	ColorTransfer  string
	ColorPrimaries string
	// Rotation is the display rotation in degrees, normalized to
	// [0, 360), from the display matrix side data or the legacy "rotate"
	// tag. ffmpeg autorotates frames before filters run, so 90 and 270
	// swap the width and height the filter chain sees.
	Rotation int
}

// DisplaySize returns the frame dimensions after autorotation: Width and
// Height swapped for 90 and 270 degree rotations.
func (v VideoStream) DisplaySize() (width, height int) {
	if v.Rotation == 90 || v.Rotation == 270 {
		return v.Height, v.Width
	}
	return v.Width, v.Height
}

type AudioStream struct {
	Index         int
	Codec         string
	SampleRate    int
	Channels      int
	ChannelLayout string
	Bitrate       int64
	Language      string
	Title         string
	// BitsPerSample is the stream's coded sample depth (ffprobe's
	// bits_per_raw_sample, falling back to bits_per_sample); 0 when the
	// probe does not report one. The join pass uses it to decide whether
	// rebuilding copied FLAC audio needs more than the encoder's default
	// depth (see ffmpeg.JoinAudioArgs).
	BitsPerSample int
}

type SubtitleStream struct {
	Index    int
	Codec    string
	Language string
	Title    string
}

// IsHDR reports whether the stream's transfer characteristic is PQ or HLG.
func (v VideoStream) IsHDR() bool {
	return v.ColorTransfer == "smpte2084" || v.ColorTransfer == "arib-std-b67"
}

func (m MediaInfo) PrimaryVideo() (VideoStream, bool) {
	if len(m.VideoStreams) == 0 {
		return VideoStream{}, false
	}
	return m.VideoStreams[0], true
}

// VideoMapSpecifier returns the ffmpeg -map specifier for the primary video
// stream. It is "0:v:0" unless attached pictures precede the primary video
// in the input, in which case the absolute stream index is used so that
// cover art is never selected as the video.
func (m MediaInfo) VideoMapSpecifier() string {
	primary, ok := m.PrimaryVideo()
	if !ok {
		return "0:v:0"
	}
	for _, cover := range m.CoverArtStreams {
		if cover.Index < primary.Index {
			return fmt.Sprintf("0:%d", primary.Index)
		}
	}
	return "0:v:0"
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

// TimeRange is a half-open [Start, End) interval on the media timeline,
// expressed relative to the input start (the same timeline ffmpeg's
// input-side -ss seeks on).
type TimeRange struct {
	Start time.Duration
	End   time.Duration
}

// Length returns the segment's duration (End - Start).
func (r TimeRange) Length() time.Duration {
	return r.End - r.Start
}

// TrimExportMode selects how a multi-segment trim selection is exported.
// Job-scoped UI state: never persisted into profiles or config.
type TrimExportMode uint8

const (
	// TrimExportSeparate writes every selected segment to its own output
	// file, named by the output template plus a _partNN suffix.
	TrimExportSeparate TrimExportMode = iota
	// TrimExportJoin concatenates all selected segments, in chronological
	// order, into a single output file.
	TrimExportJoin
)

func (m TrimExportMode) String() string {
	switch m {
	case TrimExportJoin:
		return "Join into one file"
	default:
		return "Separate files"
	}
}

// TrimSelection holds the cut ranges applied to the current input. It
// belongs to the input/conversion job, not to saved profiles, so it has
// no yaml tags and is never persisted (Profile/Config stay untouched).
// An empty selection means "no trimming".
//
// Segments are half-open intervals kept in chronological order;
// validation rejects overlaps and reordering. Single-segment callers
// (ffmpeg.BuildCommand) use First().
type TrimSelection struct {
	Segments []TimeRange
}

// Active reports whether any trimming is selected.
func (t TrimSelection) Active() bool {
	return len(t.Segments) > 0
}

// First returns the first segment, for single-segment consumers such as
// ffmpeg.BuildCommand (which deliberately accepts exactly one segment
// per invocation; multi-segment exports build one plan per segment).
func (t TrimSelection) First() (TimeRange, bool) {
	if len(t.Segments) == 0 {
		return TimeRange{}, false
	}
	return t.Segments[0], true
}

// Total sums all segment durations.
func (t TrimSelection) Total() time.Duration {
	var total time.Duration
	for _, s := range t.Segments {
		total += s.Length()
	}
	return total
}

type RunPlan struct {
	Executable string
	Args       []string
	Preview    string
	OutputPath string
	Warnings   []string
}
