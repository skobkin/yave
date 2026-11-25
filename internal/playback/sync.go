package playback

import (
	"io"
	"sync"
	"time"
)

// lateDrop is how far behind the clock a frame may be before it is
// discarded instead of shown, so a slow consumer catches up instead of
// drifting away from the audio.
const lateDrop = 120 * time.Millisecond

// action is the scheduler's verdict for one decoded frame.
type action int

const (
	actPresent action = iota
	actWait
	actDrop
)

// decide compares a frame's media time with the clock: wait (returning
// how long) while it is early, drop it when it is too late, present it
// otherwise.
func decide(frameAt, now time.Duration) (action, time.Duration) {
	switch {
	case frameAt > now:
		return actWait, frameAt - now
	case now-frameAt > lateDrop:
		return actDrop, 0
	default:
		return actPresent, 0
	}
}

// AudioSink plays a PCM stream (AudioSampleRate, AudioChannels, s16le)
// to an output device. Implementations must not block Start. gain is the
// initial linear volume (1 is unity, 0 silence); it is applied before the
// first sample plays, so a muted session never blips at full volume.
type AudioSink interface {
	Start(r io.Reader, gain float64) (AudioPlayback, error)
}

// AudioPlayback is one running audio stream.
type AudioPlayback interface {
	// Buffered is the number of bytes already read from the stream but
	// not yet played.
	Buffered() int
	// SetVolume changes the linear volume of the running stream (1 is
	// unity, 0 silence).
	SetVolume(gain float64)
	// Close stops playback and releases the stream.
	Close() error
}

// clock reports the current media position.
type clock interface {
	Now() time.Duration
}

// wallClock advances with real time from a media-position origin.
// The origin is pinned by begin (the first decoded frame); until then it
// reads as the origin itself.
type wallClock struct {
	from time.Duration
	now  func() time.Time

	mu      sync.Mutex
	start   time.Time
	started bool
}

func newWallClock(from time.Duration) *wallClock {
	return &wallClock{from: from, now: time.Now}
}

// begin starts the clock; later calls are no-ops.
func (c *wallClock) begin() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.started {
		c.start, c.started = c.now(), true
	}
}

func (c *wallClock) Now() time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.started {
		return c.from
	}
	return c.from + c.now().Sub(c.start)
}

// countingReader counts bytes consumed by the audio sink and notes
// when the stream ends.
type countingReader struct {
	r io.Reader

	mu  sync.Mutex
	n   int64
	eof bool
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.mu.Lock()
	c.n += int64(n)
	if err != nil {
		c.eof = true
	}
	c.mu.Unlock()
	return n, err
}

func (c *countingReader) snapshot() (n int64, eof bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n, c.eof
}

// audioClock derives the media position from the audio device: bytes
// handed to the sink minus what it still buffers. Once the audio
// stream has ended (shorter than the video, or the window's end) and
// the sink has played all of it, it continues on the wall clock from
// that moment so video never stalls. The sink reads ahead, so the
// moment the source ended is too early to extend from: the clock would
// jump forward by the read-ahead and drop the last frames.
type audioClock struct {
	from    time.Duration
	reader  *countingReader
	playing AudioPlayback
	now     func() time.Time

	mu        sync.Mutex
	drainedAt time.Time // when everything read was first seen played
}

func (c *audioClock) Now() time.Duration {
	n, eof := c.reader.snapshot()
	buffered := int64(c.playing.Buffered())
	c.mu.Lock()
	defer c.mu.Unlock()
	if eof && buffered == 0 {
		if c.drainedAt.IsZero() {
			c.drainedAt = c.now()
		}
		return c.from + bytesToDuration(n) + c.now().Sub(c.drainedAt)
	}
	return c.from + bytesToDuration(max(n-buffered, 0))
}

func bytesToDuration(n int64) time.Duration {
	return time.Duration(n) * time.Second / (AudioSampleRate * AudioBytesPerFrame)
}
