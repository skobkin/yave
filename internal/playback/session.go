package playback

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"image"
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"
)

const (
	stderrTailSize = 4 << 10
	pipeCloseGrace = 2 * time.Second
)

// Frame is one presented video frame.
type Frame struct {
	// At is the frame's media position.
	At    time.Duration
	Image image.Image
}

// Session is one running playback window. Create it with Start; it
// runs until the window ends, Stop is called, or decoding fails.
type Session struct {
	frames chan Frame
	done   chan struct{}
	cancel context.CancelFunc

	mu     sync.Mutex
	pos    time.Duration
	err    error
	notice string
}

// Frames delivers frames in presentation order and is closed when the
// session ends. Consumers must keep receiving until it closes or call
// Stop; a stalled consumer makes the session drop late frames.
func (s *Session) Frames() <-chan Frame { return s.frames }

// Done is closed once both decoders are reaped.
func (s *Session) Done() <-chan struct{} { return s.done }

// Cancel asks playback to end without waiting: the decoders are killed
// and reaped in the background, and Done closes when that finished.
// UI code uses it so the interface thread never waits on a subprocess.
func (s *Session) Cancel() { s.cancel() }

// Stop cancels playback and waits for the decoders to exit.
func (s *Session) Stop() {
	s.cancel()
	<-s.done
}

// Position is the media time of the last presented frame, or the start
// position before the first one.
func (s *Session) Position() time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pos
}

// Err is the failure that ended the session; nil for a normal end or a
// cancellation. Valid after Done is closed.
func (s *Session) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

// Notice is a non-fatal condition worth telling the user, such as
// playing without audio because no output device is available.
func (s *Session) Notice() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.notice
}

func (s *Session) setErr(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err == nil {
		s.err = err
	}
}

// Start launches the decoders for one playback window. A nil sink
// plays video only. Start returns an error only when the video decoder
// cannot be launched; later failures surface through Err.
func Start(ctx context.Context, o Options, sink AudioSink) (*Session, error) {
	if strings.TrimSpace(o.FFmpegPath) == "" {
		return nil, errors.New("start playback: FFmpeg path is empty")
	}
	if strings.TrimSpace(o.Input) == "" {
		return nil, errors.New("start playback: input path is empty")
	}
	runCtx, cancel := context.WithCancel(ctx)
	s := &Session{
		frames: make(chan Frame, 2),
		done:   make(chan struct{}),
		cancel: cancel,
		pos:    o.From,
	}

	video, err := startDecoder(runCtx, o.FFmpegPath, VideoArgs(o))
	if err != nil {
		cancel()
		return nil, fmt.Errorf("start video playback: %w", err)
	}

	var clk clock
	var audio *decoder
	var playing AudioPlayback
	audioCtx, audioCancel := context.WithCancel(runCtx)
	switch {
	case !o.HasAudio:
	case sink == nil:
		s.notice = "Playing without audio: no audio output is available."
	default:
		audio, playing, clk, s.notice = startAudio(audioCtx, o, sink)
	}
	if clk == nil {
		clk = newWallClock(o.From)
	}

	go func() {
		defer close(s.done)
		defer cancel()
		perr := s.present(runCtx, video, clk, o)
		// The window is over: silence the audio side whatever happened.
		audioCancel()
		if playing != nil {
			_ = playing.Close()
		}
		if audio != nil {
			_ = audio.wait() //nolint:errcheck // audio failures after video end are not actionable
		}
		s.finishVideo(runCtx, cancel, video, perr)
		close(s.frames)
	}()
	return s, nil
}

// finishVideo reaps the video decoder and records why the session
// ended. A decoder that already exited on its own is waited for
// normally so its exit status and stderr become the error; a stream
// the parser rejected while the process still runs is cancelled first.
func (s *Session) finishVideo(ctx context.Context, cancel context.CancelFunc, video *decoder, perr error) {
	// Sample caller cancellation before cancelling ourselves, otherwise a
	// parse error would be mistaken for a caller stop and lost.
	stoppedByCaller := ctx.Err() != nil
	processEnded := perr == nil || errors.Is(perr, io.EOF) || errors.Is(perr, io.ErrUnexpectedEOF)
	if !processEnded {
		cancel()
	}
	waitErr := video.wait()
	if stoppedByCaller {
		return
	}
	if !processEnded && perr != nil {
		// We killed the process ourselves; its exit status is noise.
		s.setErr(fmt.Errorf("%w%s", perr, video.diagnostics()))
		return
	}
	switch {
	case waitErr != nil:
		s.setErr(waitErr)
	case perr != nil:
		s.setErr(fmt.Errorf("%w%s", perr, video.diagnostics()))
	}
}

// startAudio launches the audio decoder and hands its PCM to the sink.
// Any failure degrades to video-only with a notice.
func startAudio(ctx context.Context, o Options, sink AudioSink) (*decoder, AudioPlayback, clock, string) {
	dec, err := startDecoder(ctx, o.FFmpegPath, AudioArgs(o))
	if err != nil {
		return nil, nil, nil, "Playing without audio: " + err.Error()
	}
	reader := &countingReader{r: dec.stdout}
	playing, err := sink.Start(reader)
	if err != nil {
		_ = dec.stop() //nolint:errcheck // abandoning the audio path
		return nil, nil, nil, "Playing without audio: " + err.Error()
	}
	return dec, playing, &audioClock{from: o.From, reader: reader, playing: playing, now: time.Now}, ""
}

// present parses video frames and releases them on the clock. It
// returns nil at a clean end of stream or when ctx is cancelled.
func (s *Session) present(ctx context.Context, video *decoder, clk clock, o Options) error {
	br := bufio.NewReaderSize(video.stdout, 1<<20)
	hdr, err := ReadHeader(br)
	if err != nil {
		return streamErr(ctx, err)
	}
	interval := time.Duration(hdr.FrameDuration() * float64(time.Second))
	if interval <= 0 {
		interval = time.Second / MaxFPS
	}
	for k := 0; ; k++ {
		img, err := ReadFrame(br, hdr)
		if err != nil {
			return streamErr(ctx, err)
		}
		at := o.From + time.Duration(k)*interval
		if k == 0 {
			// Decoder start-up latency must not count as playback time.
			if b, ok := clk.(interface{ begin() }); ok {
				b.begin()
			}
		}
		for {
			act, wait := decide(at, clk.Now())
			if act == actWait {
				if !sleep(ctx, wait) {
					return nil
				}
				continue
			}
			if act == actPresent {
				select {
				case s.frames <- Frame{At: at, Image: img}:
					s.mu.Lock()
					s.pos = at
					s.mu.Unlock()
				case <-ctx.Done():
					return nil
				}
			}
			break
		}
	}
}

// streamErr turns a read failure into the value present returns:
// nil for a cancellation or ReadFrame's bare io.EOF (a clean end of
// stream), the error otherwise.
func streamErr(ctx context.Context, err error) error {
	if ctx.Err() != nil || err == io.EOF { //nolint:errorlint // ReadFrame returns the bare sentinel at a clean end
		return nil //nolint:nilerr // a cancellation or clean end is not a failure
	}
	return err
}

func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// decoder is one FFmpeg subprocess with a bounded stderr tail.
type decoder struct {
	cmd     *exec.Cmd
	stdout  io.ReadCloser
	tail    *tail
	errDone chan struct{}
}

func startDecoder(ctx context.Context, path string, args []string) (*decoder, error) {
	cmd := exec.CommandContext(ctx, path, args...) //nolint:gosec // G204: executable comes from capability detection
	cmd.WaitDelay = pipeCloseGrace
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	d := &decoder{cmd: cmd, stdout: stdout, tail: &tail{limit: stderrTailSize}, errDone: make(chan struct{})}
	go func() {
		defer close(d.errDone)
		_, _ = io.Copy(d.tail, stderr) //nolint:errcheck // draining diagnostics; errors surface via Wait
	}()
	// Killing the direct child closes its pipe write-ends, but a
	// descendant of a configured wrapper script can inherit them and
	// keep the pipes open, which would block the reader (and so Stop)
	// until that descendant exits. After a grace period past
	// cancellation, close the read-ends so blocked reads fail and Wait
	// can reap what is left. Mirrors internal/frames.
	go func() {
		<-ctx.Done()
		grace := time.NewTimer(pipeCloseGrace)
		defer grace.Stop()
		select {
		case <-d.errDone:
		case <-grace.C:
		}
		_ = stdout.Close() //nolint:errcheck // best effort; Wait closes it again
		_ = stderr.Close() //nolint:errcheck // best effort; Wait closes it again
	}()
	return d, nil
}

// wait reaps the process after its pipes drained; stderr must be fully
// read before cmd.Wait closes it.
func (d *decoder) wait() error {
	<-d.errDone
	if err := d.cmd.Wait(); err != nil {
		return fmt.Errorf("ffmpeg playback decoder failed: %w%s", err, d.diagnostics())
	}
	return nil
}

func (d *decoder) stop() error {
	if d.cmd.Process != nil {
		_ = d.cmd.Process.Kill() //nolint:errcheck // already exited is fine
	}
	_, _ = io.Copy(io.Discard, d.stdout) //nolint:errcheck // drain so Wait cannot block
	return d.wait()
}

func (d *decoder) diagnostics() string {
	if s := strings.TrimSpace(d.tail.String()); s != "" {
		return ": " + s
	}
	return ""
}

// tail retains only the last limit bytes written to it.
type tail struct {
	mu    sync.Mutex
	limit int
	buf   []byte
	trunc bool
}

func (t *tail) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if excess := len(t.buf) - t.limit; excess > 0 {
		t.buf = append(t.buf[:0], t.buf[excess:]...)
		t.trunc = true
	}
	return len(p), nil
}

func (t *tail) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.trunc {
		return "…" + string(t.buf)
	}
	return string(t.buf)
}
