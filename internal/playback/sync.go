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
// to an output device. Implementations must not block Start.
type AudioSink interface {
	Start(r io.Reader) (AudioPlayback, error)
}

// AudioPlayback is one running audio stream.
type AudioPlayback interface {
	// Buffered is the number of bytes already read from the stream but
	// not yet played.
	Buffered() int
	// Close stops playback and releases the stream.
	Close() error
}

// clock reports the current media position.
type clock interface {
	Now() time.Duration
}

// wallClock advances with real time from a media-position origin.
type wallClock struct {
	from  time.Duration
	start time.Time
	now   func() time.Time
}

func newWallClock(from time.Duration) *wallClock {
	return &wallClock{from: from, start: time.Now(), now: time.Now}
}

func (c *wallClock) Now() time.Duration { return c.from + c.now().Sub(c.start) }

// countingReader counts bytes consumed by the audio sink and notes
// when the stream ends.
type countingReader struct {
	r io.Reader

	mu    sync.Mutex
	n     int64
	eof   bool
	eofAt time.Time
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.mu.Lock()
	c.n += int64(n)
	if err != nil && !c.eof {
		c.eof, c.eofAt = true, time.Now()
	}
	c.mu.Unlock()
	return n, err
}

func (c *countingReader) snapshot() (n int64, eof bool, eofAt time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n, c.eof, c.eofAt
}

// audioClock derives the media position from the audio device: bytes
// handed to the sink minus what it still buffers. Once the audio
// stream has ended (shorter than the video, or the window's end) it
// continues on the wall clock from that point so video never stalls.
type audioClock struct {
	from    time.Duration
	reader  *countingReader
	playing AudioPlayback
	now     func() time.Time
}

func (c *audioClock) Now() time.Duration {
	n, eof, eofAt := c.reader.snapshot()
	played := n - int64(c.playing.Buffered())
	if played < 0 {
		played = 0
	}
	pos := c.from + bytesToDuration(played)
	if eof && c.playing.Buffered() == 0 {
		// Everything has played; extend with real time since the end.
		return c.from + bytesToDuration(n) + c.now().Sub(eofAt)
	}
	return pos
}

func bytesToDuration(n int64) time.Duration {
	return time.Duration(n) * time.Second / (AudioSampleRate * AudioBytesPerFrame)
}
