package playback

import (
	"bytes"
	"encoding/binary"
	"io"
	"testing"
	"time"
)

// s16 encodes samples as s16le.
func s16(samples ...int16) []byte {
	b := make([]byte, 2*len(samples))
	for i, v := range samples {
		binary.LittleEndian.PutUint16(b[2*i:], uint16(v)) //nolint:gosec // G115: s16le sample bits
	}
	return b
}

// filledFeed returns a feed whose source (data) has been read to its end.
func filledFeed(t *testing.T, gain float64, data []byte) *pcmFeed {
	t.Helper()
	f := newPCMFeed(gain, 1<<20)
	f.fill(bytes.NewReader(data))
	return f
}

func TestPCMFeedPullAppliesGainAndWholeFrames(t *testing.T) {
	f := filledFeed(t, 0.5, s16(1000, -1000, 32767, -32768, 7, 7))
	p := make([]byte, 6) // room for one and a half frames
	n, ok := f.pull(p)
	if !ok || n != AudioBytesPerFrame {
		t.Fatalf("pull = %d, %v; want one frame", n, ok)
	}
	if got, want := p[:n], s16(500, -500); !bytes.Equal(got, want) {
		t.Errorf("samples = %v, want %v", got, want)
	}
	f.setGain(4)
	n, _ = f.pull(p)
	if got, want := p[:n], s16(32767, -32768); !bytes.Equal(got, want) {
		t.Errorf("amplified samples = %v, want saturated %v", got, want)
	}
}

func TestPCMFeedPullEndsAfterSourceAndDropsPartialFrame(t *testing.T) {
	f := filledFeed(t, 1, append(s16(1, 2), 9)) // a frame and a stray byte
	p := make([]byte, 64)
	if n, ok := f.pull(p); !ok || n != AudioBytesPerFrame {
		t.Fatalf("first pull = %d, %v", n, ok)
	}
	if n, ok := f.pull(p); ok || n != 0 {
		t.Errorf("pull after the end = %d, %v; want 0, false", n, ok)
	}
}

func TestPCMFeedCloseWakesAWaitingPull(t *testing.T) {
	f := newPCMFeed(1, 1<<20)
	r, w := io.Pipe()
	defer w.Close()
	go f.fill(r)
	result := make(chan bool)
	go func() {
		_, ok := f.pull(make([]byte, 16))
		result <- ok
	}()
	select {
	case <-result:
		t.Fatal("pull returned with nothing queued")
	case <-time.After(20 * time.Millisecond):
	}
	f.close()
	select {
	case ok := <-result:
		if ok {
			t.Error("pull after close reported data")
		}
	case <-time.After(time.Second):
		t.Fatal("close did not wake the pull")
	}
}

func TestPCMFeedHandsOutQueuedAudioAfterClose(t *testing.T) {
	// An output being started when playback stops still needs its
	// prebuffer, or the server never reports it started.
	f := newPCMFeed(1, 1<<20)
	r, w := io.Pipe()
	go f.fill(r)
	go func() { _, _ = w.Write(s16(1, 2, 3, 4)) }()
	if !f.waitStart(8, 4) {
		t.Fatal("waitStart refused a full prebuffer")
	}
	f.close()
	if n, ok := f.pull(make([]byte, 64)); !ok || n != 8 {
		t.Errorf("pull after close = %d, %v; want the queued 8 bytes", n, ok)
	}
	_ = w.Close()
}

func TestPCMFeedWaitStart(t *testing.T) {
	t.Run("enough audio", func(t *testing.T) {
		f := filledFeed(t, 1, make([]byte, 400))
		if !f.waitStart(100, 40) {
			t.Error("waitStart = false with the target queued")
		}
	})
	t.Run("short source starts above the minimum", func(t *testing.T) {
		f := filledFeed(t, 1, make([]byte, 60))
		if !f.waitStart(100, 40) {
			t.Error("waitStart = false with more than the minimum queued")
		}
	})
	t.Run("too short to hear is dropped", func(t *testing.T) {
		f := filledFeed(t, 1, make([]byte, 20))
		if f.waitStart(100, 40) {
			t.Error("waitStart = true below the minimum")
		}
		if got := f.buffered(); got != 0 {
			t.Errorf("buffered after dropping = %d, want 0 (the clock must not wait for it)", got)
		}
	})
	t.Run("closed while waiting", func(t *testing.T) {
		f := newPCMFeed(1, 1<<20)
		f.close()
		if f.waitStart(100, 40) {
			t.Error("waitStart = true on a closed feed")
		}
	})
}

func TestPCMFeedFillStopsAtTheLimit(t *testing.T) {
	f := newPCMFeed(1, 1000)
	r, w := io.Pipe()
	go f.fill(r)
	go func() { _, _ = w.Write(make([]byte, 64<<10)) }()
	time.Sleep(50 * time.Millisecond)
	f.mu.Lock()
	queued := len(f.queue)
	f.mu.Unlock()
	if queued < 1000 || queued > 1000+16<<10 {
		t.Errorf("queued %d bytes, want the limit plus at most one read", queued)
	}
	f.close()
	_ = w.Close()
}

func TestPCMFeedBuffered(t *testing.T) {
	second := durationToBytes(time.Second)
	base := time.Unix(1000, 0)
	setup := func(t *testing.T) (*pcmFeed, *time.Time) {
		t.Helper()
		f := filledFeed(t, 1, make([]byte, 2*second))
		now := base
		f.now = func() time.Time { return now }
		if n, ok := f.pull(make([]byte, second)); !ok || int64(n) != second {
			t.Fatalf("pull = %d, %v", n, ok)
		}
		return f, &now
	}

	t.Run("nothing heard before the first report", func(t *testing.T) {
		f, _ := setup(t)
		if got := f.buffered(); int64(got) != 2*second {
			t.Errorf("buffered = %d, want everything read (%d)", got, 2*second)
		}
	})
	t.Run("server position minus its latency, advancing in real time", func(t *testing.T) {
		f, now := setup(t)
		f.observe(streamPos{read: second / 2, write: second, latency: 250 * time.Millisecond, running: true, at: base})
		// heard: 0.5s read - 0.25s latency = 0.25s
		if got, want := int64(f.buffered()), 2*second-second/4; got != want {
			t.Errorf("buffered = %d, want %d", got, want)
		}
		*now = base.Add(100 * time.Millisecond)
		if got, want := int64(f.buffered()), 2*second-second/4-durationToBytes(100*time.Millisecond); got != want {
			t.Errorf("buffered 100ms later = %d, want %d", got, want)
		}
		*now = base.Add(10 * time.Second)
		if got := int64(f.buffered()); got != second {
			t.Errorf("buffered long after = %d, want the unpulled %d (never past what the server got)", got, second)
		}
	})
	t.Run("never goes back", func(t *testing.T) {
		f, _ := setup(t)
		f.observe(streamPos{read: second / 2, running: true, at: base})
		before := f.buffered()
		f.observe(streamPos{read: second / 4, running: true, at: base})
		if got := f.buffered(); got > before {
			t.Errorf("buffered rose from %d to %d on a lower report", before, got)
		}
	})
	t.Run("stalled server freezes the position", func(t *testing.T) {
		f, now := setup(t)
		f.observe(streamPos{read: second / 2, write: second, running: false, at: base})
		*now = base.Add(time.Second)
		if got, want := int64(f.buffered()), 2*second-second/2; got != want {
			t.Errorf("buffered = %d, want %d", got, want)
		}
	})
	t.Run("drained after the end", func(t *testing.T) {
		f, now := setup(t)
		if _, ok := f.pull(make([]byte, second)); !ok {
			t.Fatal("second pull failed")
		}
		// The read index stops a block short of the write index.
		f.observe(streamPos{read: 2*second - 4096, write: 2 * second, latency: 250 * time.Millisecond, running: false, at: base})
		if got, want := int64(f.buffered()), durationToBytes(250*time.Millisecond); got != want {
			t.Errorf("buffered once drained = %d, want the latency still in flight (%d)", got, want)
		}
		// Later polls see the same stopped position; the tail keeps
		// draining from when it stopped.
		f.observe(streamPos{read: 2*second - 4096, write: 2 * second, latency: 250 * time.Millisecond, running: false, at: base.Add(200 * time.Millisecond)})
		*now = base.Add(250 * time.Millisecond)
		if got := f.buffered(); got != 0 {
			t.Errorf("buffered after the latency = %d, want 0 so the clock moves on", got)
		}
	})
	t.Run("lost output counts pulls as heard", func(t *testing.T) {
		f, _ := setup(t)
		f.freeRun()
		if got := int64(f.buffered()); got != second {
			t.Errorf("buffered = %d, want only the queue (%d)", got, second)
		}
	})
}
