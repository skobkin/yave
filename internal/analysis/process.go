package analysis

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"

	"git.skobk.in/skobkin/yave/internal/procgroup"
)

const (
	stderrTailSize = 4 << 10
	pipeCloseGrace = 2 * time.Second
	maxLineBytes   = 1 << 20
)

// tail retains only the last limit bytes written to it, so a chatty
// FFmpeg cannot grow an error message without bound.
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

// run executes FFmpeg and feeds its output to the line callbacks: onOut
// for every stdout line, onErr for every stderr line. Lines that onErr
// does not claim (it returns false) are kept in a bounded tail that
// becomes the diagnostic of a failure. The subprocess runs in its own
// process group and is killed when ctx ends; pipes are force-closed after
// a grace period so a wrapper script whose descendants inherited them
// cannot block the caller. A cancelled context returns ctx.Err().
func run(ctx context.Context, path string, args []string, onOut func(line string), onErr func(line string) bool) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("FFmpeg path is empty")
	}
	cmd := exec.CommandContext(ctx, path, args...) //nolint:gosec // G204: executable comes from capability detection
	cmd.WaitDelay = pipeCloseGrace
	procgroup.Configure(cmd)
	cmd.Cancel = func() error { return procgroup.Kill(cmd.Process) }
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("attach FFmpeg output: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("attach FFmpeg diagnostics: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start FFmpeg: %w", err)
	}

	diagnostics := &tail{limit: stderrTailSize}
	var readers sync.WaitGroup
	readers.Add(2)
	go func() {
		defer readers.Done()
		scanLines(stdout, func(line string) { onOut(line) })
	}()
	go func() {
		defer readers.Done()
		scanLines(stderr, func(line string) {
			if onErr != nil && onErr(line) {
				return
			}
			_, _ = io.WriteString(diagnostics, line+"\n") //nolint:errcheck // tail.Write never fails
		})
	}()
	// The pipes must be drained before Wait closes them.
	readers.Wait()
	waitErr := cmd.Wait()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if waitErr != nil {
		detail := strings.TrimSpace(diagnostics.String())
		if detail != "" {
			detail = ": " + detail
		}
		return fmt.Errorf("FFmpeg failed: %w%s", waitErr, detail)
	}
	return nil
}

// scanLines calls fn for every line of r, however long: an oversized line
// is truncated rather than aborting the scan.
func scanLines(r io.Reader, fn func(string)) {
	br := bufio.NewReaderSize(r, 64<<10)
	for {
		line, isPrefix, err := br.ReadLine()
		if len(line) > 0 {
			fn(string(line))
		}
		for isPrefix { // drop the rest of an oversized line
			_, isPrefix, err = br.ReadLine()
			if err != nil {
				return
			}
		}
		if err != nil {
			return
		}
	}
}
