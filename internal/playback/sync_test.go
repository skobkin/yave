package playback

import (
	"bytes"
	"io"
	"sync"
	"testing"
	"time"
)

func TestDecide(t *testing.T) {
	tests := []struct {
		name     string
		frameAt  time.Duration
		now      time.Duration
		want     action
		wantWait time.Duration
	}{
		{"early frame waits", 2 * time.Second, 1900 * time.Millisecond, actWait, 100 * time.Millisecond},
		{"on time presents", 2 * time.Second, 2 * time.Second, actPresent, 0},
		{"slightly late presents", 2 * time.Second, 2*time.Second + lateDrop, actPresent, 0},
		{"too late drops", 2 * time.Second, 2*time.Second + lateDrop + time.Millisecond, actDrop, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, wait := decide(tt.frameAt, tt.now)
			if got != tt.want || wait != tt.wantWait {
				t.Errorf("decide() = (%v, %v), want (%v, %v)", got, wait, tt.want, tt.wantWait)
			}
		})
	}
}

type fakePlayback struct {
	mu       sync.Mutex
	buffered int
	volumes  []float64
}

func (f *fakePlayback) Buffered() int { return f.buffered }
func (f *fakePlayback) Close() error  { return nil }

func (f *fakePlayback) SetVolume(gain float64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.volumes = append(f.volumes, gain)
}

func (f *fakePlayback) setVolumes() []float64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]float64(nil), f.volumes...)
}

func TestAudioClock(t *testing.T) {
	oneSecond := AudioSampleRate * AudioBytesPerFrame
	reader := &countingReader{r: bytes.NewReader(make([]byte, 2*oneSecond))}
	playing := &fakePlayback{}
	clk := &audioClock{from: 10 * time.Second, reader: reader, playing: playing, now: time.Now}

	if got := clk.Now(); got != 10*time.Second {
		t.Fatalf("before any read Now() = %v, want 10s", got)
	}
	buf := make([]byte, oneSecond)
	if _, err := io.ReadFull(reader, buf); err != nil {
		t.Fatal(err)
	}
	playing.buffered = oneSecond / 4 // a quarter second is read but not yet audible
	if got, want := clk.Now(), 10*time.Second+750*time.Millisecond; got != want {
		t.Errorf("Now() = %v, want %v (read minus buffered)", got, want)
	}
	playing.buffered = oneSecond * 5 // over-reporting must not go before the origin
	if got := clk.Now(); got != 10*time.Second {
		t.Errorf("Now() with excess buffer = %v, want clamp to 10s", got)
	}
}

func TestAudioClockContinuesOnWallClockAfterEOF(t *testing.T) {
	oneSecond := AudioSampleRate * AudioBytesPerFrame
	reader := &countingReader{r: bytes.NewReader(make([]byte, oneSecond))}
	if _, err := io.ReadAll(reader); err != nil {
		t.Fatal(err)
	}
	base := time.Unix(1000, 0)
	now := base
	// The sink read the whole stream ahead; half of it is still to play.
	playing := &fakePlayback{buffered: oneSecond / 2}
	clk := &audioClock{from: 0, reader: reader, playing: playing, now: func() time.Time { return now }}
	now = base.Add(5 * time.Second) // long after the source ended
	if got, want := clk.Now(), 500*time.Millisecond; got != want {
		t.Errorf("Now() while the sink still plays = %v, want %v (no jump to the source's end)", got, want)
	}
	playing.buffered = 0
	if got, want := clk.Now(), time.Second; got != want {
		t.Errorf("Now() once played out = %v, want %v", got, want)
	}
	now = now.Add(300 * time.Millisecond)
	if got, want := clk.Now(), 1300*time.Millisecond; got != want {
		t.Errorf("Now() 300ms after playing out = %v, want %v", got, want)
	}
}
