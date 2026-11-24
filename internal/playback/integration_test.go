package playback

import (
	"context"
	"image"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// TestSessionIntegration drives a real FFmpeg through the Y4M and
// s16le pipelines on a generated clip, without opening an audio
// device (a counting sink stands in). Opt-in: set
// YAVE_PLAYBACK_INTEGRATION=1; it also needs ffmpeg in PATH.
func TestSessionIntegration(t *testing.T) {
	if os.Getenv("YAVE_PLAYBACK_INTEGRATION") != "1" {
		t.Skip("set YAVE_PLAYBACK_INTEGRATION=1 to run against a real FFmpeg")
	}
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skipf("ffmpeg not found: %v", err)
	}
	clip := filepath.Join(t.TempDir(), "clip.mkv")
	gen := exec.CommandContext(context.Background(), ffmpeg, "-hide_banner", "-loglevel", "error", //nolint:gosec // G204: locally discovered FFmpeg, fixed arguments
		"-f", "lavfi", "-i", "testsrc=size=1280x720:rate=25",
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=44100",
		"-t", "2", "-c:v", "mpeg4", "-c:a", "pcm_s16le", clip)
	if out, err := gen.CombinedOutput(); err != nil {
		t.Skipf("cannot generate the fixture clip: %v: %s", err, out)
	}

	sink := &pacedSink{consumed: make(chan int64, 1)}
	opts := Options{FFmpegPath: ffmpeg, Input: clip, From: 500 * time.Millisecond, Until: 1500 * time.Millisecond, FrameRate: "25/1", HasAudio: true}
	s, err := Start(context.Background(), opts, sink)
	if err != nil {
		t.Fatal(err)
	}
	frames := collect(t, s)
	<-s.Done()

	if err := s.Err(); err != nil {
		t.Fatalf("session failed: %v", err)
	}
	// One second at 25 fps; allow decimation slack at the window edges.
	if len(frames) < 20 || len(frames) > 26 {
		t.Errorf("got %d frames for a 1s window at 25 fps, want about 25", len(frames))
	}
	first := frames[0].Image.(*image.YCbCr)
	if b := first.Bounds(); b.Dx() != DefaultMaxWidth || b.Dy() != 360 {
		t.Errorf("frame size = %v, want %dx360 (1280x720 capped to the default width)", b, DefaultMaxWidth)
	}
	if frames[0].At != opts.From {
		t.Errorf("first frame At = %v, want %v", frames[0].At, opts.From)
	}
	// 1s of 48 kHz stereo s16le is 192000 bytes.
	if n := <-sink.consumed; n < 180000 || n > 200000 {
		t.Errorf("audio sink consumed %d bytes, want about 192000", n)
	}
}

// pacedSink consumes audio at the real-time rate like an output
// device does; an instantly draining sink would run the audio clock
// ahead and make every video frame look late.
type pacedSink struct{ consumed chan int64 }

func (p *pacedSink) Start(r io.Reader, _ float64) (AudioPlayback, error) {
	go func() {
		const chunk = AudioSampleRate * AudioBytesPerFrame / 40 // 25ms
		buf := make([]byte, chunk)
		var total int64
		for {
			n, err := io.ReadFull(r, buf)
			total += int64(n)
			if err != nil {
				break
			}
			time.Sleep(25 * time.Millisecond)
		}
		p.consumed <- total
	}()
	return &fakePlayback{}, nil
}
