package transcode

import (
	"bufio"
	"context"
	"errors"
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
	// OnStageStart fires on the run goroutine before each stage begins,
	// including before its Verify.
	OnStageStart func(index, total int, stage Stage)
	// OnStageDone fires after every stage attempt, success or failure.
	// A stage whose OutputPath is non-empty and whose err is nil
	// published a user-visible output; OutputPath-empty stages are
	// processing-only regardless of err.
	OnStageDone func(index, total int, stage Stage, err error)
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
	cancelCtx          context.Context
	cancelFunc         context.CancelFunc
}

func NewRunControl() *RunControl {
	return &RunControl{}
}

// CancelContext returns a context that is canceled by RequestCancel and
// Terminate alike, created lazily. Auxiliary subprocesses of a staged run
// (intermediate verification) run under it so a hanging probe is
// interrupted instead of blocking the run; a request made before the
// first call is honored immediately.
func (c *RunControl) CancelContext() context.Context {
	if c == nil {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		return ctx
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cancelCtx == nil {
		c.cancelCtx, c.cancelFunc = context.WithCancel(context.Background())
		if c.cancelRequested || c.terminateRequested {
			c.cancelFunc()
		}
	}
	return c.cancelCtx
}

// triggerCancelContextLocked cancels the lazily created context, if any.
func (c *RunControl) triggerCancelContextLocked() {
	if c.cancelFunc != nil {
		c.cancelFunc()
	}
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
	c.triggerCancelContextLocked()
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
	c.triggerCancelContextLocked()
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

// Stage is one step of a multi-stage conversion: one FFmpeg invocation
// with optional pre-flight verification and post-success publication.
type Stage struct {
	Plan     model.RunPlan
	Duration time.Duration // progress denominator for this stage (segment length; Total for a join pass)
	Label    string        // human-readable stage name used in errors and the run log
	// OutputPath names the USER-VISIBLE output this stage publishes.
	// Empty for processing-only stages (join-mode intermediate
	// encodes): OnStageDone distinguishes successful processing
	// (err == nil) from successful publication (err == nil && OutputPath
	// != "") so the UI's published-output counter and log wording stay
	// honest.
	OutputPath string
	// Verify, when set, runs before the stage's process starts. A
	// verification failure skips the stage entirely. Implementations
	// should honor the context so cancellation interrupts a hung probe.
	Verify func(ctx context.Context) error
	// Publish, when set, runs after a successful Run and a cancellation
	// recheck. Publication is the commit point that makes the stage's
	// output user-visible; a stage without Publish is processing-only.
	Publish func() error
}

// afterRunHook, when non-nil, runs inside RunSequence between a stage's
// successful Run and the pre-publication cancellation recheck. Nil in
// production; tests install it to land a cancellation request in exactly
// that window deterministically.
var afterRunHook func(index int)

// RunSequence executes stages sequentially under one control. Progress
// is aggregated across stages, weighted by each stage's Duration, and
// only advances the completed base when a stage actually finishes (a
// stage's out_time is segment-relative and can be non-monotonic).
//
// Cancellation semantics: a graceful or forced request made at any point
// — before the sequence, during verification, during a stage's process,
// or between a successful Run and its publication — makes RunSequence
// return the bare context.Canceled sentinel; no further output is
// published and no further stage starts. Outputs already published stay
// published (a Publish that completed before the request counts as
// published via OnStageDone). Other errors are wrapped with the stage's
// Label for the run log and status line.
func (Runner) RunSequence(control *RunControl, stages []Stage, cb Callbacks) error {
	if len(stages) == 0 {
		return errors.New("RunSequence requires at least one stage")
	}
	if control == nil {
		control = NewRunControl()
	}
	total := time.Duration(0)
	for _, s := range stages {
		total += s.Duration
	}
	done := func(i int, stage Stage, err error) {
		if cb.OnStageDone != nil {
			cb.OnStageDone(i, len(stages), stage, err)
		}
	}
	fail := func(i int, stage Stage, err error) error {
		done(i, stage, err)
		if errors.Is(err, context.Canceled) {
			return context.Canceled
		}
		return fmt.Errorf("%s: %w", stage.Label, err)
	}
	ctx := control.CancelContext()
	var base time.Duration
	var lastPct float64
	for i, stage := range stages {
		if cancel, terminate := control.requested(); cancel || terminate {
			return fail(i, stage, context.Canceled)
		}
		if cb.OnStageStart != nil {
			cb.OnStageStart(i, len(stages), stage)
		}
		if stage.Verify != nil {
			if err := stage.Verify(ctx); err != nil {
				if errors.Is(err, context.Canceled) || ctx.Err() != nil {
					return fail(i, stage, context.Canceled)
				}
				return fail(i, stage, err)
			}
		}
		if cancel, terminate := control.requested(); cancel || terminate {
			return fail(i, stage, context.Canceled)
		}
		err := (Runner{}).Run(control, stage.Plan, stage.Duration, Callbacks{
			OnLog: cb.OnLog,
			OnProgress: func(p model.Progress) {
				if cb.OnProgress == nil {
					return
				}
				if total <= 0 {
					// Unknown total (e.g. ffprobe reported no duration):
					// there is no percentage, but the out-time, speed,
					// and elapsed updates still show real progress.
					// Forward them unchanged, preserving the
					// missing-metadata degradation the direct run
					// provides.
					cb.OnProgress(p)
					return
				}
				stageTime := p.OutTime
				if stage.Duration > 0 && stageTime > stage.Duration {
					stageTime = stage.Duration
				}
				if stageTime < 0 {
					stageTime = 0
				}
				agg := float64(base+stageTime) / float64(total)
				agg = min(1, max(lastPct, agg))
				lastPct = agg
				reported := p
				reported.OutTime = base + stageTime
				reported.Percentage = agg
				cb.OnProgress(reported)
			},
		})
		if err != nil {
			return fail(i, stage, err)
		}
		if hook := afterRunHook; hook != nil {
			hook(i)
		}
		if cancel, terminate := control.requested(); cancel || terminate {
			return fail(i, stage, context.Canceled)
		}
		if stage.Publish != nil {
			if err := stage.Publish(); err != nil {
				return fail(i, stage, err)
			}
		}
		done(i, stage, nil)
		base += stage.Duration
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
