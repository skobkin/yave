//go:build linux

package playback

import (
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/jfreymuth/pulse"
	"github.com/jfreymuth/pulse/proto"
)

// NewAudioSink returns the Linux audio output: a PulseAudio stream per
// playback (PipeWire serves the same protocol), created by Start and
// deleted by Close. oto keeps one stream open for the whole process and
// never corks it, so desktops (Plasma's task-bar speaker) showed YAVE as
// playing audio long after playback stopped. Without a PulseAudio server
// it falls back to oto, which then uses ALSA.
func NewAudioSink() AudioSink { return pulseSink{fallback: otoSink{}} }

const (
	// pulseLatency is the server-side buffer asked for. The A/V clock
	// accounts for it, so it trades only start-up delay for robustness.
	pulseLatency = 100 * time.Millisecond
	// pulsePrebuffer is how much the server holds before it starts
	// playing; small, so a very short window still starts. Start feeds a
	// whole pulseLatency at once anyway.
	pulsePrebuffer = 10 * time.Millisecond
	// pulseQueue bounds the decoded audio read ahead of the server.
	pulseQueue = 500 * time.Millisecond
	// pulsePoll is how often the playback position is asked for.
	pulsePoll = 20 * time.Millisecond
	// pulseStartWait bounds how long Close waits for a stream start in
	// progress, which only hangs if the server vanished meanwhile.
	pulseStartWait = 2 * time.Second
)

// errNoPulseServer means no PulseAudio server could ever be reached;
// the sink then uses its fallback for the rest of the process.
var errNoPulseServer = errors.New("no PulseAudio server")

var pulseShared struct {
	mu        sync.Mutex
	client    *pulse.Client
	connected bool // a client was ever created
	missing   bool // the first connection failed
}

// pulseClient returns the process's PulseAudio connection, connecting on
// first use. A failed first connection is final (errNoPulseServer); a
// lost one is retried on the next call.
func pulseClient() (*pulse.Client, error) {
	s := &pulseShared
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.missing {
		return nil, errNoPulseServer
	}
	if s.client != nil {
		return s.client, nil
	}
	c, err := pulse.NewClient(pulse.ClientApplicationName("YAVE"))
	if err != nil {
		if !s.connected {
			s.missing = true
			return nil, errNoPulseServer
		}
		return nil, fmt.Errorf("connect to PulseAudio: %w", err)
	}
	s.client, s.connected = c, true
	return c, nil
}

// forgetPulseClient drops a connection that failed, so the next playback
// reconnects. The library closes a lost connection itself.
func forgetPulseClient(c *pulse.Client) {
	s := &pulseShared
	s.mu.Lock()
	if s.client == c {
		s.client = nil
	}
	s.mu.Unlock()
}

type pulseSink struct{ fallback AudioSink }

func (s pulseSink) Start(r io.Reader, gain float64) (AudioPlayback, error) {
	client, err := pulseClient()
	if errors.Is(err, errNoPulseServer) {
		return s.fallback.Start(r, gain)
	}
	if err != nil {
		return nil, err
	}
	feed := newPCMFeed(gain, int(durationToBytes(pulseQueue)))
	stream, err := client.NewPlayback(pulseSource{feed},
		pulse.PlaybackStereo,
		pulse.PlaybackSampleRate(AudioSampleRate),
		pulse.PlaybackLatency(pulseLatency.Seconds()), // after rate and channels
		pulse.PlaybackMediaName("Trim preview"),
		pulse.PlaybackRawOption(func(c *proto.CreatePlaybackStream) {
			c.BufferPrebufferLength = uint32(durationToBytes(pulsePrebuffer)) //nolint:gosec // G115: 1920 bytes
		}),
	)
	if err != nil {
		forgetPulseClient(client)
		return nil, fmt.Errorf("open PulseAudio stream: %w", err)
	}
	p := &pulsePlayback{
		client:  client,
		stream:  stream,
		feed:    feed,
		started: make(chan struct{}),
		done:    make(chan struct{}),
	}
	go feed.fill(r)
	go p.run()
	return p, nil
}

// pulseSource hands the feed to the stream's callback, which the
// library calls on its own goroutine.
type pulseSource struct{ feed *pcmFeed }

func (s pulseSource) Format() byte { return proto.FormatInt16LE }

func (s pulseSource) Read(p []byte) (int, error) {
	n, ok := s.feed.pull(p)
	if !ok {
		return 0, pulse.EndOfData
	}
	return n, nil
}

type pulsePlayback struct {
	client *pulse.Client
	stream *pulse.PlaybackStream
	feed   *pcmFeed

	started chan struct{} // closed once run has started the stream or given up
	done    chan struct{} // closed by Close
	once    sync.Once
}

// run starts the stream once enough audio is queued, then reports the
// server's position to the feed until Close. If the server goes away
// mid-playback the audio is consumed silently in real time instead, so
// the clock, and with it the video, keeps going.
func (p *pulsePlayback) run() {
	start := p.feed.waitStart(int(durationToBytes(pulseLatency)), int(durationToBytes(pulsePrebuffer)))
	if start {
		// Blocks until the server reports it is playing, which the queued
		// audio (at least the prebuffer) guarantees.
		p.stream.Start()
	}
	close(p.started)
	if !start {
		return
	}
	tick := time.NewTicker(pulsePoll)
	defer tick.Stop()
	for {
		pos, err := p.position()
		select {
		case <-p.done:
			return
		default:
		}
		if err != nil {
			forgetPulseClient(p.client)
			p.feed.freeRun()
			p.consumeSilently(tick)
			return
		}
		p.feed.observe(pos)
		select {
		case <-p.done:
			return
		case <-tick.C:
		}
	}
}

func (p *pulsePlayback) position() (streamPos, error) {
	if err := p.stream.Error(); err != nil {
		return streamPos{}, err
	}
	if p.stream.Closed() {
		return streamPos{}, pulse.ErrConnectionClosed
	}
	var r proto.GetPlaybackLatencyReply
	now := time.Now()
	err := p.client.RawRequest(&proto.GetPlaybackLatency{
		StreamIndex: p.stream.StreamIndex(),
		// Echoed back by the server for latency interpolation, unused here.
		Time: proto.Time{Seconds: uint32(now.Unix()), Microseconds: uint32(now.Nanosecond() / 1000)}, //nolint:gosec // G115: protocol field width
	}, &r)
	if err != nil {
		return streamPos{}, err
	}
	return streamPos{
		read:    r.ReadIndex,
		write:   r.WriteIndex,
		latency: time.Duration(r.Latency) * time.Microsecond, //nolint:gosec // G115: a sink latency in µs is nowhere near 2^63
		running: r.Running,
		at:      time.Now(),
	}, nil
}

// consumeSilently pulls one poll interval of audio per tick and drops it.
func (p *pulsePlayback) consumeSilently(tick *time.Ticker) {
	buf := make([]byte, durationToBytes(pulsePoll))
	for {
		if _, ok := p.feed.pull(buf); !ok {
			return
		}
		select {
		case <-p.done:
			return
		case <-tick.C:
		}
	}
}

func (p *pulsePlayback) Buffered() int { return p.feed.buffered() }

func (p *pulsePlayback) SetVolume(gain float64) { p.feed.setGain(gain) }

// Close deletes the stream, which removes it from the desktop's mixer
// and its "playing audio" indicators.
func (p *pulsePlayback) Close() error {
	p.once.Do(func() {
		p.feed.close()
		// Closing the stream while Start is still handing it its first
		// request would send on a closed channel inside the library.
		select {
		case <-p.started:
		case <-time.After(pulseStartWait):
		}
		close(p.done)
		p.stream.Close()
	})
	return nil
}
