package transcode

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"git.skobk.in/skobkin/yave/internal/model"
)

type Callbacks struct {
	OnLog      func(string)
	OnProgress func(model.Progress)
}

type Runner struct{}

// RunControl coordinates graceful and forced cancellation with a running
// FFmpeg process. Requests made before the process starts are retained and
// applied as soon as it is attached.
type RunControl struct {
	mu                 sync.Mutex
	process            *os.Process
	stdin              io.WriteCloser
	cancelRequested    bool
	terminateRequested bool
}

func NewRunControl() *RunControl {
	return &RunControl{}
}

// RequestCancel asks FFmpeg to finish its current work and close its output
// cleanly using FFmpeg's interactive quit command.
func (c *RunControl) RequestCancel() {
	if c == nil {
		return
	}
	c.mu.Lock()
	if c.cancelRequested || c.terminateRequested {
		c.mu.Unlock()
		return
	}
	c.cancelRequested = true
	stdin := c.stdin
	c.mu.Unlock()
	if stdin != nil {
		_, _ = io.WriteString(stdin, "q\n")
	}
}

// Terminate forcefully stops FFmpeg. It is safe to call before the process has
// started; in that case Runner will not start it.
func (c *RunControl) Terminate() {
	if c == nil {
		return
	}
	c.mu.Lock()
	if c.terminateRequested {
		c.mu.Unlock()
		return
	}
	c.terminateRequested = true
	process := c.process
	c.mu.Unlock()
	if process != nil {
		_ = process.Kill()
	}
}

func (c *RunControl) requested() (cancel, terminate bool) {
	if c == nil {
		return false, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cancelRequested, c.terminateRequested
}

func (c *RunControl) attach(process *os.Process, stdin io.WriteCloser) {
	c.mu.Lock()
	c.process = process
	c.stdin = stdin
	cancel := c.cancelRequested
	terminate := c.terminateRequested
	c.mu.Unlock()

	if terminate {
		_ = process.Kill()
		return
	}
	if cancel {
		_, _ = io.WriteString(stdin, "q\n")
	}
}

func (c *RunControl) detach(process *os.Process) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.process == process {
		c.process = nil
		c.stdin = nil
	}
}

func (Runner) Run(control *RunControl, plan model.RunPlan, duration time.Duration, cb Callbacks) error {
	if control == nil {
		control = NewRunControl()
	}
	if cancel, terminate := control.requested(); cancel || terminate {
		return context.Canceled
	}

	cmd := exec.Command(plan.Executable, plan.Args...) //nolint:gosec // G204: executable path validated by ffmpeg.ResolveExecutable
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("open FFmpeg control pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("open FFmpeg progress pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("open FFmpeg log pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		if cancel, terminate := control.requested(); cancel || terminate {
			return context.Canceled
		}
		return fmt.Errorf("start FFmpeg: %w", err)
	}
	control.attach(cmd.Process, stdin)
	defer control.detach(cmd.Process)

	started := time.Now()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		scanLogs(stderr, cb.OnLog)
	}()
	go func() {
		defer wg.Done()
		scanProgress(stdout, duration, started, cb.OnProgress)
	}()

	waitErr := cmd.Wait()
	wg.Wait()
	if cancel, terminate := control.requested(); cancel || terminate {
		return context.Canceled
	}
	if waitErr != nil {
		return fmt.Errorf("FFmpeg failed: %w", waitErr)
	}
	return nil
}

func scanLogs(r io.Reader, onLog func(string)) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		if onLog != nil {
			onLog(scanner.Text())
		}
	}
}

func scanProgress(r io.Reader, duration time.Duration, started time.Time, onProgress func(model.Progress)) {
	scanner := bufio.NewScanner(r)
	values := make(map[string]string)
	for scanner.Scan() {
		key, value, ok := strings.Cut(scanner.Text(), "=")
		if !ok {
			continue
		}
		values[key] = value
		if key != "progress" {
			continue
		}
		progress := ParseProgress(values, duration)
		progress.Elapsed = time.Since(started)
		if onProgress != nil {
			onProgress(progress)
		}
		clear(values)
	}
}

func ParseProgress(values map[string]string, duration time.Duration) model.Progress {
	p := model.Progress{
		Speed:   values["speed"],
		Bitrate: values["bitrate"],
		State:   values["progress"],
	}
	p.Frame, _ = strconv.ParseInt(values["frame"], 10, 64)
	if raw := values["out_time_us"]; raw != "" {
		if micros, err := strconv.ParseInt(raw, 10, 64); err == nil {
			p.OutTime = time.Duration(micros) * time.Microsecond
		}
	} else if raw := values["out_time"]; raw != "" {
		p.OutTime = parseClock(raw)
	}
	if duration > 0 {
		p.Percentage = min(1, max(0, float64(p.OutTime)/float64(duration)))
	}
	return p
}

func parseClock(value string) time.Duration {
	parts := strings.Split(value, ":")
	if len(parts) != 3 {
		return 0
	}
	hours, errH := strconv.ParseFloat(parts[0], 64)
	minutes, errM := strconv.ParseFloat(parts[1], 64)
	seconds, errS := strconv.ParseFloat(parts[2], 64)
	if errH != nil || errM != nil || errS != nil {
		return 0
	}
	return time.Duration((hours*3600 + minutes*60 + seconds) * float64(time.Second))
}
