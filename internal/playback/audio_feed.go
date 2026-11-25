package playback

import (
	"encoding/binary"
	"io"
	"math"
	"sync"
	"time"
)

// pcmFeed carries PCM (AudioSampleRate, AudioChannels, s16le) from a
// source reader to an output stream that pulls it, and keeps the
// accounting the audio clock needs. It holds no platform code: an output
// backend pulls with pull, reports the server's playback position with
// observe, and the clock reads buffered.
//
// The source is read ahead by fill on its own goroutine into a bounded
// queue, so a pull never waits on the decoder longer than it takes the
// decoder to produce data, and close always wakes a waiting pull.
type pcmFeed struct {
	mu   sync.Mutex
	cond *sync.Cond

	queue []byte // read from the source, not yet pulled
	limit int    // fill pauses while the queue holds this much
	eof   bool   // the source has ended
	// closed stops fill and makes pull end once the queue is empty; data
	// already queued is still handed out, so an output that was being
	// started gets its prebuffer.
	closed bool
	gain   float64
	handed int64 // bytes pulled by the output

	pos    streamPos // the latest server report
	lost   bool      // the output is gone; pulls are paced by the caller
	played int64     // highest played estimate handed out, keeps it monotonic
	now    func() time.Time
}

// streamPos is an output server's report of a stream's position, all
// in bytes of this feed's format.
type streamPos struct {
	read    int64         // bytes the server has taken from the stream
	write   int64         // bytes written to the stream
	latency time.Duration // from the server's read position to the speaker
	running bool          // the server is playing (not prebuffering or drained)
	at      time.Time     // when the report was taken
}

func newPCMFeed(gain float64, limit int) *pcmFeed {
	f := &pcmFeed{limit: limit, gain: clampGain(gain), now: time.Now}
	f.cond = sync.NewCond(&f.mu)
	return f
}

// fill reads the source into the queue until it ends or the feed closes.
func (f *pcmFeed) fill(r io.Reader) {
	chunk := make([]byte, 16<<10)
	for {
		f.mu.Lock()
		for len(f.queue) >= f.limit && !f.closed {
			f.cond.Wait()
		}
		closed := f.closed
		f.mu.Unlock()
		if closed {
			return
		}
		n, err := r.Read(chunk)
		f.mu.Lock()
		f.queue = append(f.queue, chunk[:n]...)
		if err != nil {
			f.eof = true
		}
		f.cond.Broadcast()
		f.mu.Unlock()
		if err != nil {
			return
		}
	}
}

// waitStart blocks until the queue holds target bytes, the source ends
// or the feed closes, and reports whether the output should start: it
// has at least least bytes to play. Otherwise the queue is dropped
// (too short to hear), so the clock sees nothing pending.
func (f *pcmFeed) waitStart(target, least int) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for len(f.queue) < target && !f.eof && !f.closed {
		f.cond.Wait()
	}
	if len(f.queue) >= least && !f.closed {
		return true
	}
	f.queue = nil
	f.cond.Broadcast()
	return false
}

// pull moves whole frames into p, with the gain applied, waiting while
// none are queued. It reports false once nothing more will come: the
// source ended or the feed closed, and the queue is empty (a trailing
// partial frame is dropped).
func (f *pcmFeed) pull(p []byte) (int, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for len(f.queue) < AudioBytesPerFrame && !f.eof && !f.closed {
		f.cond.Wait()
	}
	n := min(len(p), len(f.queue)) / AudioBytesPerFrame * AudioBytesPerFrame
	if n == 0 {
		return 0, false
	}
	scaleS16(p[:n], f.queue[:n], f.gain)
	f.queue = f.queue[n:]
	f.handed += int64(n)
	f.cond.Broadcast() // room for fill
	return n, true
}

// scaleS16 copies s16le samples from src to dst multiplied by gain,
// saturating at the sample range.
func scaleS16(dst, src []byte, gain float64) {
	if gain == 1 {
		copy(dst, src)
		return
	}
	for i := 0; i+1 < len(src); i += 2 {
		v := float64(int16(binary.LittleEndian.Uint16(src[i:]))) * gain //nolint:gosec // G115: s16le sample bits
		v = math.Max(math.MinInt16, math.Min(math.MaxInt16, math.Round(v)))
		binary.LittleEndian.PutUint16(dst[i:], uint16(int16(v))) //nolint:gosec // G115: v is clamped to the int16 range; uint16 holds its bits
	}
}

func (f *pcmFeed) setGain(gain float64) {
	f.mu.Lock()
	f.gain = clampGain(gain)
	f.mu.Unlock()
}

// observe records the server's latest position report. While the
// server is stopped at the same read position, the first report's time
// is kept: after a drain the tail is extrapolated from when it stopped,
// not from each later report.
func (f *pcmFeed) observe(pos streamPos) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !pos.running && !f.pos.running && !f.pos.at.IsZero() && pos.read == f.pos.read {
		pos.at = f.pos.at
	}
	f.pos = pos
}

// freeRun marks the output as gone: from now on what is pulled counts as
// played at once, and the caller paces the pulls in real time.
func (f *pcmFeed) freeRun() {
	f.mu.Lock()
	f.lost = true
	f.mu.Unlock()
}

// close stops reading the source and wakes every waiter.
func (f *pcmFeed) close() {
	f.mu.Lock()
	f.closed = true
	f.cond.Broadcast()
	f.mu.Unlock()
}

// buffered is the number of bytes read from the source but not yet
// heard: the queue, plus what the output took that has not reached the
// speaker according to the server's last report. Between reports the
// position advances in real time while the server is playing.
func (f *pcmFeed) buffered() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	var est int64
	switch {
	case f.lost:
		est = f.handed
	case f.pos.at.IsZero():
		est = 0 // no report yet: nothing is audible
	case !f.pos.running && f.eof && len(f.queue) == 0 && f.pos.write >= f.handed:
		// Drained: the server took the last byte and stopped (its read
		// index may stop a block short of the write index). The tail is
		// still on its way to the speaker.
		est = f.pos.write + durationToBytes(f.now().Sub(f.pos.at)) - durationToBytes(f.pos.latency)
	case f.pos.running:
		est = f.pos.read + durationToBytes(f.now().Sub(f.pos.at)) - durationToBytes(f.pos.latency)
	default:
		est = f.pos.read - durationToBytes(f.pos.latency)
	}
	est = min(max(est, f.played), f.handed)
	f.played = est
	return len(f.queue) + int(f.handed-est)
}

// durationToBytes converts a duration to whole frames of PCM, in bytes.
func durationToBytes(d time.Duration) int64 {
	return int64(d) * AudioSampleRate / int64(time.Second) * AudioBytesPerFrame
}
