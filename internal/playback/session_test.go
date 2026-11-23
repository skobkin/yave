package playback

import (
	"context"
	"errors"
	"image"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// writeFakeFFmpeg writes a POSIX shell stand-in for ffmpeg. The video
// invocation is recognised by the yuv4mpegpipe muxer in its arguments,
// the audio one by s16le; body supplies the behaviour for each.
func writeFakeFFmpeg(t *testing.T, video, audio string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake ffmpeg is a POSIX shell script")
	}
	script := "#!/bin/sh\ncase \"$*\" in\n*yuv4mpegpipe*)\n" + video + "\n;;\n*s16le*)\n" + audio + "\n;;\nesac\n"
	path := filepath.Join(t.TempDir(), "ffmpeg")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil { //nolint:gosec // G306: test helper script must be executable
		t.Fatal(err)
	}
	return path
}

// threeFrames emits a 2x2 4:2:0 stream of three frames at 100 fps
// whose luma bytes are 1, 2 and 3.
const threeFrames = `printf 'YUV4MPEG2 W2 H2 F100:1 Ip A1:1 C420jpeg\n'
for i in 1 2 3; do
  printf 'FRAME\n'
  printf "\\00$i\\00$i\\00$i\\00$i\\010\\020"
done`

type fakeSink struct {
	err      error
	started  chan struct{}
	consumed chan int64
}

func newFakeSink() *fakeSink {
	return &fakeSink{started: make(chan struct{}), consumed: make(chan int64, 1)}
}

func (f *fakeSink) Start(r io.Reader) (AudioPlayback, error) {
	if f.err != nil {
		return nil, f.err
	}
	close(f.started)
	go func() {
		n, _ := io.Copy(io.Discard, r) //nolint:errcheck // test sink drains until the decoder ends
		f.consumed <- n
	}()
	return &fakePlayback{}, nil
}

func collect(t *testing.T, s *Session) []Frame {
	t.Helper()
	var got []Frame
	timeout := time.After(10 * time.Second)
	for {
		select {
		case f, ok := <-s.Frames():
			if !ok {
				return got
			}
			got = append(got, f)
		case <-timeout:
			t.Fatal("timed out waiting for the session to end")
		}
	}
}

func TestSessionDeliversFramesInOrder(t *testing.T) {
	ffmpeg := writeFakeFFmpeg(t, threeFrames, "head -c 4000 /dev/zero")
	sink := newFakeSink()

	s, err := Start(context.Background(), Options{FFmpegPath: ffmpeg, Input: "in.mkv", From: time.Second, HasAudio: true}, sink)
	if err != nil {
		t.Fatal(err)
	}
	got := collect(t, s)
	<-s.Done()

	if len(got) != 3 {
		t.Fatalf("got %d frames, want 3", len(got))
	}
	for i, f := range got {
		wantAt := time.Second + time.Duration(i)*10*time.Millisecond
		if f.At != wantAt {
			t.Errorf("frame %d At = %v, want %v", i, f.At, wantAt)
		}
		img, ok := f.Image.(*image.YCbCr)
		if !ok || img.Y[0] != byte(i+1) {
			t.Errorf("frame %d image = %#v, want a YCbCr frame with luma %d", i, f.Image, i+1)
		}
	}
	if err := s.Err(); err != nil {
		t.Errorf("Err() = %v, want nil", err)
	}
	if s.Notice() != "" {
		t.Errorf("Notice() = %q, want none", s.Notice())
	}
	select {
	case <-sink.started:
	default:
		t.Error("audio sink was never started")
	}
	if pos := s.Position(); pos != time.Second+20*time.Millisecond {
		t.Errorf("Position() = %v, want the last frame's time", pos)
	}
}

func TestSessionWithoutSinkPlaysVideoOnlyWithNotice(t *testing.T) {
	ffmpeg := writeFakeFFmpeg(t, threeFrames, "head -c 4000 /dev/zero")
	s, err := Start(context.Background(), Options{FFmpegPath: ffmpeg, Input: "in.mkv", HasAudio: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := collect(t, s); len(got) != 3 {
		t.Fatalf("got %d frames, want 3", len(got))
	}
	<-s.Done()
	if !strings.Contains(s.Notice(), "without audio") {
		t.Errorf("Notice() = %q, want a video-only notice", s.Notice())
	}
}

func TestSessionSinkFailureFallsBackToVideoOnly(t *testing.T) {
	ffmpeg := writeFakeFFmpeg(t, threeFrames, "head -c 4000 /dev/zero")
	sink := newFakeSink()
	sink.err = errors.New("open audio output: no such device")

	s, err := Start(context.Background(), Options{FFmpegPath: ffmpeg, Input: "in.mkv", HasAudio: true}, sink)
	if err != nil {
		t.Fatal(err)
	}
	if got := collect(t, s); len(got) != 3 {
		t.Fatalf("got %d frames, want 3", len(got))
	}
	<-s.Done()
	if !strings.Contains(s.Notice(), "no such device") {
		t.Errorf("Notice() = %q, want the sink error", s.Notice())
	}
	if s.Err() != nil {
		t.Errorf("a failed sink must not fail the session: %v", s.Err())
	}
}

func TestSessionStopKillsDecoders(t *testing.T) {
	ffmpeg := writeFakeFFmpeg(t,
		"printf 'YUV4MPEG2 W2 H2 F100:1 C420jpeg\\n'\nsleep 30",
		"sleep 30")
	s, err := Start(context.Background(), Options{FFmpegPath: ffmpeg, Input: "in.mkv", HasAudio: true}, newFakeSink())
	if err != nil {
		t.Fatal(err)
	}
	stopped := make(chan struct{})
	go func() { s.Stop(); close(stopped) }()
	select {
	case <-stopped:
	case <-time.After(10 * time.Second):
		t.Fatal("Stop did not return; decoders were not killed")
	}
	if s.Err() != nil {
		t.Errorf("a requested stop must not be an error: %v", s.Err())
	}
	if _, ok := <-s.Frames(); ok {
		t.Error("Frames() must be closed after Stop")
	}
}

func TestSessionReportsDecoderFailureWithStderr(t *testing.T) {
	ffmpeg := writeFakeFFmpeg(t, "echo 'Invalid data found when processing input' >&2\nexit 1", "exit 1")
	s, err := Start(context.Background(), Options{FFmpegPath: ffmpeg, Input: "bad.mkv"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := collect(t, s); len(got) != 0 {
		t.Fatalf("got %d frames, want 0", len(got))
	}
	<-s.Done()
	if err := s.Err(); err == nil || !strings.Contains(err.Error(), "Invalid data found") {
		t.Errorf("Err() = %v, want the decoder's stderr", err)
	}
}

func TestSessionTruncatedStreamIsAnError(t *testing.T) {
	ffmpeg := writeFakeFFmpeg(t, "printf 'YUV4MPEG2 W2 H2 F100:1 C420jpeg\\n'\nprintf 'FRAME\\n'\nprintf '\\001\\002'", "exit 0")
	s, err := Start(context.Background(), Options{FFmpegPath: ffmpeg, Input: "in.mkv"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	collect(t, s)
	<-s.Done()
	if err := s.Err(); err == nil || !strings.Contains(err.Error(), "unexpected EOF") {
		t.Errorf("Err() = %v, want an unexpected-EOF error", err)
	}
}

func TestStartValidatesOptions(t *testing.T) {
	if _, err := Start(context.Background(), Options{Input: "in.mkv"}, nil); err == nil {
		t.Error("empty FFmpeg path must be rejected")
	}
	if _, err := Start(context.Background(), Options{FFmpegPath: "ffmpeg"}, nil); err == nil {
		t.Error("empty input must be rejected")
	}
	missing := filepath.Join(t.TempDir(), "no-such-ffmpeg")
	if _, err := Start(context.Background(), Options{FFmpegPath: missing, Input: "in.mkv"}, nil); err == nil {
		t.Error("an unlaunchable FFmpeg must fail Start")
	}
}
