// Package playback decodes a window of a media file through the
// configured FFmpeg executable and presents it in real time: video as
// 4:2:0 frames parsed from a YUV4MPEG2 pipe, audio as raw PCM fed to
// an optional audio sink, with the audio device position acting as the
// master clock when present. It has no GUI dependency and is separate
// from the transcoding runner; the trim UI treats it as optional.
package playback

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

const (
	// DefaultMaxWidth caps decoded frame width, matching the static
	// trim preview.
	DefaultMaxWidth = 640
	// MaxFPS caps the presentation rate; high-frame-rate sources are
	// decimated so decode and UI updates stay cheap.
	MaxFPS = 30

	// AudioSampleRate is the PCM sample rate playback converts to.
	AudioSampleRate = 48000
	// AudioChannels is the PCM channel count playback converts to.
	AudioChannels = 2
	// AudioBytesPerFrame is the size of one signed 16-bit LE sample
	// frame; with the two constants above it fixes the single PCM
	// format, keeping the sink and clock arithmetic format-independent.
	AudioBytesPerFrame = AudioChannels * 2
)

// squarePixels converts anamorphic sources to display geometry before
// scaling, like the static preview extractor does.
const squarePixels = "scale=w='trunc(iw*sar/2)*2':h=ih,setsar=1"

// Options describe one playback window.
type Options struct {
	FFmpegPath string
	Input      string
	// From is the media position playback starts at.
	From time.Duration
	// Until is the media position playback stops at; zero plays to the
	// end of the input.
	Until time.Duration
	// MaxWidth caps the decoded width; zero means DefaultMaxWidth.
	MaxWidth int
	// FrameRate is the source's ffprobe r_frame_rate-style value
	// ("30000/1001", "25"); empty or invalid means MaxFPS.
	FrameRate string
	// HasAudio reports that the input has an audio stream to play.
	HasAudio bool
	// Gain is the initial linear audio volume (1 is unity, 0 silence);
	// nil means unity.
	Gain *float64
}

// gain returns the initial audio gain: unity unless Options set one,
// never negative.
func (o Options) gain() float64 {
	if o.Gain == nil {
		return 1
	}
	return clampGain(*o.Gain)
}

// clampGain keeps a gain in the range the audio backends accept.
func clampGain(gain float64) float64 {
	if gain < 0 || math.IsNaN(gain) {
		return 0
	}
	return gain
}

func (o Options) window() (time.Duration, bool) {
	if o.Until > o.From {
		return o.Until - o.From, true
	}
	return 0, false
}

func (o Options) inputArgs() []string {
	args := []string{"-hide_banner", "-nostdin", "-loglevel", "error", "-ss", formatSeconds(o.From), "-i", o.Input}
	if d, ok := o.window(); ok {
		args = append(args, "-t", formatSeconds(d))
	}
	return args
}

// VideoArgs builds the video-decode argument slice: square-pixel,
// width-capped, rate-capped 4:2:0 frames as YUV4MPEG2 on stdout.
func VideoArgs(o Options) []string {
	width := o.MaxWidth
	if width <= 0 {
		width = DefaultMaxWidth
	}
	args := o.inputArgs()
	return append(args,
		"-map", "0:v:0", "-an", "-sn", "-dn",
		"-vf", fmt.Sprintf("%s,scale='min(%d,iw)':-2,fps=%s,format=yuv420p", squarePixels, width, ChooseFPS(o.FrameRate)),
		"-f", "yuv4mpegpipe", "pipe:1",
	)
}

// AudioArgs builds the audio-decode argument slice: first audio stream
// as 48 kHz stereo s16le on stdout.
func AudioArgs(o Options) []string {
	args := o.inputArgs()
	return append(args,
		"-map", "0:a:0", "-vn", "-sn", "-dn",
		"-ac", strconv.Itoa(AudioChannels), "-ar", strconv.Itoa(AudioSampleRate),
		"-f", "s16le", "pipe:1",
	)
}

// ChooseFPS returns an ffmpeg fps-filter value for a source frame rate:
// the source rate when it is a valid fraction no higher than MaxFPS,
// otherwise MaxFPS.
func ChooseFPS(frameRate string) string {
	fallback := strconv.Itoa(MaxFPS)
	num, den, ok := strings.Cut(strings.TrimSpace(frameRate), "/")
	if !ok {
		den = "1"
	}
	n, errN := strconv.Atoi(num)
	d, errD := strconv.Atoi(den)
	if errN != nil || errD != nil || n <= 0 || d <= 0 || n > MaxFPS*d {
		return fallback
	}
	return fmt.Sprintf("%d/%d", n, d)
}

// formatSeconds renders fractional seconds with microsecond precision.
func formatSeconds(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	return fmt.Sprintf("%d.%06d", d/time.Second, (d%time.Second)/time.Microsecond)
}
