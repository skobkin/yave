package playback

import (
	"fmt"
	"io"
	"sync"

	"github.com/ebitengine/oto/v3"
)

var (
	otoOnce sync.Once
	otoCtx  *oto.Context
	otoErr  error
)

// NewAudioSink returns the platform audio output backed by oto. The
// output device is opened lazily on the first Start — one context per
// process, as oto requires — and a failure to open it is reported by
// Start, so callers can fall back to video-only playback.
func NewAudioSink() AudioSink { return otoSink{} }

type otoSink struct{}

func (otoSink) Start(r io.Reader, gain float64) (AudioPlayback, error) {
	otoOnce.Do(func() {
		ctx, ready, err := oto.NewContext(&oto.NewContextOptions{
			SampleRate:   AudioSampleRate,
			ChannelCount: AudioChannels,
			Format:       oto.FormatSignedInt16LE,
		})
		if err != nil {
			otoErr = fmt.Errorf("open audio output: %w", err)
			return
		}
		<-ready
		otoCtx = ctx
	})
	if otoErr != nil {
		return nil, otoErr
	}
	p := otoCtx.NewPlayer(r)
	p.SetVolume(clampGain(gain))
	p.Play()
	return otoPlayback{p}, nil
}

type otoPlayback struct{ p *oto.Player }

func (o otoPlayback) Buffered() int { return o.p.BufferedSize() }

func (o otoPlayback) SetVolume(gain float64) { o.p.SetVolume(clampGain(gain)) }

// Close stops playback and the read-ahead from the source. oto needs
// no further release: since v3.4 the player's finalizer does it.
func (o otoPlayback) Close() error {
	o.p.PauseAndStopReading()
	return nil
}
