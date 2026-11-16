// Package frames extracts preview frames from a media file through the
// configured FFmpeg executable for the trim timeline UI. Every call
// runs one short-lived ffmpeg process with an input-side seek, so the
// cost per frame is roughly one GOP regardless of source length —
// multi-hour recordings stay cheap to thumbnail.
package frames

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"os/exec"
	"strings"
	"time"
)

const (
	// maxImageBytes caps captured JPEG output before it is buffered.
	// On overflow the subprocess is cancelled and residual output is
	// drained and discarded, so ffmpeg can never block on a full pipe
	// while the parent waits for EOF.
	maxImageBytes = 8 << 20 // 8 MiB
	// maxImageSide and maxPixelCount bound decoded allocations: a valid
	// JPEG header claiming absurd dimensions must not allocate them.
	maxImageSide  = 4096
	maxPixelCount = maxImageSide * maxImageSide
	// stderrTailSize bounds the retained diagnostics; stderr itself is
	// always drained continuously so the process never blocks on a full
	// stderr pipe.
	stderrTailSize = 4 << 10 // 4 KiB
	// pipeCloseGrace bounds post-cancellation draining: after this long
	// past context cancellation the pipe read-ends are force-closed so
	// a wrapper descendant holding the write-ends cannot extend the
	// wait. Mirrors cmd.WaitDelay.
	pipeCloseGrace = 2 * time.Second
)

// Extractor renders preview frames from an input file through the
// configured FFmpeg executable. Methods are safe for concurrent use;
// each call runs one ffmpeg process writing JPEG to stdout (no temp
// files) and honors ctx cancellation by killing the process.
type Extractor struct {
	ffmpegPath string
}

// NewExtractor returns an extractor bound to one FFmpeg executable.
// The UI rebuilds it whenever the configured executable changes and
// drops results produced by obsolete instances.
func NewExtractor(ffmpegPath string) *Extractor {
	return &Extractor{ffmpegPath: ffmpegPath}
}

// Frame is one extracted image. Index is the timeline strip slot the
// frame belongs to (-1 for standalone previews); Image is nil only when
// a caller explicitly tolerates per-slot failures.
type Frame struct {
	Index int
	Time  time.Duration
	Image image.Image
}

// squarePixels is the filter-chain prefix both extraction paths start
// with: anamorphic sources (sample aspect ratio != 1) are converted to
// their display geometry and tagged square-pixel before any further
// scaling. Downstream code (Go's JPEG decoder, Fyne) sees only pixel
// dimensions, so without this a 160x120 SAR 2:1 source would display
// as 4:3 instead of its true 8:3. The trunc-even keeps mjpeg's
// even-dimension requirement when iw*sar is odd.
const squarePixels = "scale=w='trunc(iw*sar/2)*2':h=ih,setsar=1"

// Thumbnail extracts a single frame at (approximately) at, scaled to
// the given height with the source display aspect ratio preserved
// (square-pixel normalization first, then scale=-2:<height>: the width
// is derived and kept even — an mjpeg requirement). The frame is taken
// from stream 0:v:0, the exact stream command generation maps for
// transcoding, and converted to full-range YUV (format=yuvj420p): the
// mjpeg encoder requires full range, and ffmpeg 8+ rejects
// limited-range input instead of auto-converting it.
func (e *Extractor) Thumbnail(ctx context.Context, inputPath string, at time.Duration, height int) (Frame, error) {
	if height <= 0 {
		return Frame{}, fmt.Errorf("thumbnail height must be positive, got %d", height)
	}
	return e.extract(ctx, inputPath, at, []string{
		"-map", "0:v:0",
		"-frames:v", "1",
		"-vf", fmt.Sprintf("%s,scale=-2:%d,format=yuvj420p", squarePixels, height),
		"-q:v", "3",
	})
}

// Preview extracts a single frame at (approximately) at, never upscaled
// and capped at the given width, with the source display aspect ratio
// preserved (square-pixel normalization first, then
// scale='min(<maxWidth>,iw)':-2). The min() expression is
// single-quoted because ffmpeg's filtergraph parser splits filters on
// commas without tracking parentheses — an unquoted min(W,iw) would be
// parsed as two filters. Quotes inside the argv element are
// filtergraph-level quoting; no shell is involved. The full-range
// conversion matches Thumbnail (see there).
func (e *Extractor) Preview(ctx context.Context, inputPath string, at time.Duration, maxWidth int) (Frame, error) {
	if maxWidth <= 0 {
		return Frame{}, fmt.Errorf("preview width must be positive, got %d", maxWidth)
	}
	return e.extract(ctx, inputPath, at, []string{
		"-map", "0:v:0",
		"-frames:v", "1",
		"-vf", fmt.Sprintf("%s,scale='min(%d,iw)':-2,format=yuvj420p", squarePixels, maxWidth),
		"-q:v", "3",
	})
}

// extract runs one ffmpeg invocation: an input-side seek to at, the
// caller's filter/encoder options, and raw MJPEG to stdout.
func (e *Extractor) extract(ctx context.Context, inputPath string, at time.Duration, tail []string) (Frame, error) {
	executable := strings.TrimSpace(e.ffmpegPath)
	if executable == "" {
		return Frame{}, errors.New("FFmpeg path is empty")
	}
	if strings.TrimSpace(inputPath) == "" {
		return Frame{}, errors.New("input path is empty")
	}
	args := []string{
		"-hide_banner",
		"-loglevel", "error",
		"-ss", formatSeconds(at),
		"-i", inputPath,
	}
	args = append(args, tail...)
	args = append(args, "-c:v", "mjpeg", "-f", "mjpeg", "pipe:1")

	// A derived context lets extraction kill the subprocess early (the
	// stdout-overflow path) without mutating the caller's context.
	runCtx, runCancel := context.WithCancel(ctx)
	defer runCancel()
	cmd := exec.CommandContext(runCtx, executable, args...) //nolint:gosec // G204: executable comes from capability detection
	// Cancelling the context kills the direct child; a descendant that
	// keeps a pipe write-end open must not extend the wait forever.
	// WaitDelay caps cmd.Wait itself — but the manual drains below run
	// BEFORE Wait, so they need their own bound: pipeCloseGrace.
	cmd.WaitDelay = pipeCloseGrace
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return Frame{}, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return Frame{}, err
	}
	if err := cmd.Start(); err != nil {
		return Frame{}, fmt.Errorf("start ffmpeg frame extraction: %w", err)
	}

	// Drain stderr continuously, retaining only a bounded tail: the
	// process must never block on a full stderr pipe, including while
	// it is being killed.
	diagnostics := &limitedTail{limit: stderrTailSize}
	stderrDone := make(chan struct{})
	go func() {
		defer close(stderrDone)
		_, _ = io.Copy(diagnostics, stderr) //nolint:errcheck // draining diagnostics; errors surface via cmd.Wait
	}()

	// Watchdog bounding the drains after cancellation. Killing the
	// direct child closes its pipe write-ends — but a descendant of a
	// configured wrapper script can inherit them and keep the pipes
	// open well past the kill, which would block the drains (and thus
	// Wait) until that descendant exits. Give the readers a grace
	// period mirroring WaitDelay, then close both read-ends: blocked
	// reads fail immediately and Wait reaps what is left. One deadline
	// covers both pipes — a timer tick is consumed once, so the drains
	// are tracked in a helper and the expiry select stays single-shot.
	stdoutDone := make(chan struct{})
	drainsDone := make(chan struct{})
	go func() {
		<-stdoutDone
		<-stderrDone
		close(drainsDone)
	}()
	go func() {
		<-runCtx.Done()
		grace := time.NewTimer(pipeCloseGrace)
		defer grace.Stop()
		select {
		case <-drainsDone:
		case <-grace.C:
		}
		_ = stdout.Close()
		_ = stderr.Close()
	}()

	data, overflow := readBounded(stdout, maxImageBytes)
	close(stdoutDone)
	if overflow {
		// Stop buffering: kill the subprocess so the pipes close, then
		// keep draining until EOF (WaitDelay also caps a descendant
		// still holding a write-end). Without the kill the writer could
		// stay blocked on a full pipe while the parent waits.
		runCancel()
		_, _ = io.Copy(io.Discard, stdout) //nolint:errcheck // best-effort drain until EOF
	}

	// Wait only after both pipes reached EOF (process exit or ctx kill
	// closes them), so Wait cannot deadlock on full pipes.
	<-stderrDone
	waitErr := cmd.Wait()

	if overflow {
		return Frame{}, fmt.Errorf("frame at %s: output exceeded the %d MiB image limit", at, maxImageBytes>>20)
	}
	if ctx.Err() != nil {
		return Frame{}, ctx.Err()
	}
	if waitErr != nil {
		detail := strings.TrimSpace(diagnostics.String())
		if detail == "" {
			detail = waitErr.Error()
		}
		return Frame{}, fmt.Errorf("extract frame at %s: %s", at, detail)
	}
	img, err := decodeBounded(data)
	if err != nil {
		return Frame{}, fmt.Errorf("frame at %s: %w", at, err)
	}
	return Frame{Index: -1, Time: at, Image: img}, nil
}

// readBounded reads at most limit+1 bytes; the extra byte reports
// overflow without ever buffering more than the limit.
func readBounded(r io.Reader, limit int) (data []byte, overflow bool) {
	data, err := io.ReadAll(io.LimitReader(r, int64(limit)+1))
	if err != nil {
		// A read error after data means a truncated image; the
		// subsequent decode rejects it. Surface the error only when
		// nothing was read at all.
		if len(data) == 0 {
			return nil, false
		}
	}
	return data, len(data) > limit
}

// decodeBounded validates the image dimensions before and after
// decoding so malformed or huge output cannot allocate unbounded
// memory.
func decodeBounded(data []byte) (image.Image, error) {
	if len(data) == 0 {
		return nil, errors.New("ffmpeg produced no image data")
	}
	config, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("decode frame image: %w", err)
	}
	if config.Width <= 0 || config.Height <= 0 {
		return nil, fmt.Errorf("frame image has invalid dimensions %dx%d", config.Width, config.Height)
	}
	if config.Width > maxImageSide || config.Height > maxImageSide || config.Width*config.Height > maxPixelCount {
		return nil, fmt.Errorf("frame image %dx%d exceeds the %d pixel limit", config.Width, config.Height, maxPixelCount)
	}
	img, err := jpeg.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("decode frame image: %w", err)
	}
	if b := img.Bounds(); b.Dx() <= 0 || b.Dy() <= 0 || b.Dx()*b.Dy() > maxPixelCount {
		return nil, fmt.Errorf("decoded frame %dx%d exceeds the %d pixel limit", b.Dx(), b.Dy(), maxPixelCount)
	}
	return img, nil
}

// limitedTail retains only the last `limit` bytes written to it,
// keeping memory bounded for chatty stderr.
type limitedTail struct {
	limit int
	buf   []byte
	trunc bool
}

func (t *limitedTail) Write(p []byte) (int, error) {
	t.buf = append(t.buf, p...)
	if excess := len(t.buf) - t.limit; excess > 0 {
		t.buf = append(t.buf[:0], t.buf[excess:]...)
		t.trunc = true
	}
	return len(p), nil
}

// String returns the retained tail, prefixed with an ellipsis when
// anything was dropped.
func (t *limitedTail) String() string {
	if t.trunc {
		return "…" + string(t.buf)
	}
	return string(t.buf)
}

// formatSeconds renders a duration as fractional seconds with
// microsecond precision, truncating below the microsecond. Millisecond
// rounding is not enough for preview seeks: ffmpeg's input-side -ss
// drops frames whose timestamps are strictly before the target, so
// rounding a 66.667 ms frame to 0.067 skips it and extracts the next
// frame instead — truncation lands at or before the frame's own
// timestamp and selects it exactly.
func formatSeconds(d time.Duration) string {
	return fmt.Sprintf("%.6f", float64(d/time.Microsecond)/1e6)
}
