package transcode

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"git.skobk.in/skobkin/yave/internal/model"
)

func TestRunnerHelperProcess(t *testing.T) {
	separator := -1
	for i, arg := range os.Args {
		if arg == "--" {
			separator = i
			break
		}
	}
	if separator < 0 || separator+1 >= len(os.Args) {
		return
	}

	mode := os.Args[separator+1]
	_, _ = fmt.Fprintln(os.Stderr, "ready")
	switch mode {
	case "complete":
		return
	case "graceful":
		line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		if strings.TrimSpace(line) != "q" {
			t.Fatalf("control input = %q, want q", line)
		}
	case "ignore":
		for {
			time.Sleep(time.Second)
		}
	case "progress":
		// Two complete progress blocks: 1s then 2s of out_time.
		for _, micros := range []string{"1000000", "2000000"} {
			fmt.Printf("out_time_us=%s\nprogress=continue\n", micros)
		}
	case "fail":
		os.Exit(1)
	default:
		t.Fatalf("unknown helper mode %q", mode)
	}
}

func TestRunnerCompletesNormally(t *testing.T) {
	err := (Runner{}).Run(NewRunControl(), helperPlan("complete"), 0, Callbacks{})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
}

func TestRunnerGracefulCancelWritesQuitCommand(t *testing.T) {
	control := NewRunControl()
	ready := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- (Runner{}).Run(control, helperPlan("graceful"), 0, Callbacks{
			OnLog: func(line string) {
				if line == "ready" {
					select {
					case <-ready:
					default:
						close(ready)
					}
				}
			},
		})
	}()
	waitForReady(t, ready)
	control.RequestCancel()
	if err := waitForRun(t, done); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v, want context.Canceled", err)
	}
}

func TestRunnerTerminateKillsUnresponsiveProcess(t *testing.T) {
	control := NewRunControl()
	ready := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- (Runner{}).Run(control, helperPlan("ignore"), 0, Callbacks{
			OnLog: func(line string) {
				if line == "ready" {
					select {
					case <-ready:
					default:
						close(ready)
					}
				}
			},
		})
	}()
	waitForReady(t, ready)
	control.RequestCancel()
	select {
	case err := <-done:
		t.Fatalf("Run() returned before termination: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	control.Terminate()
	if err := waitForRun(t, done); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v, want context.Canceled", err)
	}
}

func TestRunnerHonorsControlRequestBeforeStart(t *testing.T) {
	tests := []struct {
		name    string
		request func(*RunControl)
	}{
		{name: "cancel", request: (*RunControl).RequestCancel},
		{name: "terminate", request: (*RunControl).Terminate},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			control := NewRunControl()
			tt.request(control)
			err := (Runner{}).Run(control, model.RunPlan{Executable: "/does/not/exist"}, 0, Callbacks{})
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("Run() error = %v, want context.Canceled", err)
			}
		})
	}
}

func helperPlan(mode string) model.RunPlan {
	return model.RunPlan{
		Executable: os.Args[0],
		Args:       []string{"-test.run=^TestRunnerHelperProcess$", "--", mode},
	}
}

func waitForReady(t *testing.T, ready <-chan struct{}) {
	t.Helper()
	select {
	case <-ready:
	case <-time.After(5 * time.Second):
		t.Fatal("helper process did not become ready")
	}
}

func waitForRun(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("Runner did not return")
		return nil
	}
}

func TestParseProgress(t *testing.T) {
	t.Parallel()
	p := ParseProgress(map[string]string{
		"frame":       "120",
		"out_time_us": "5000000",
		"speed":       "2.0x",
		"bitrate":     "900kbits/s",
		"progress":    "continue",
	}, 10*time.Second)
	if p.Frame != 120 || p.OutTime != 5*time.Second || p.Percentage != 0.5 {
		t.Fatalf("unexpected progress: %+v", p)
	}
}

// --- RunSequence ---

// stageRecorder collects ordered markers from Verify, OnLog, Publish,
// OnStageStart, and OnStageDone callbacks.
type stageRecorder struct {
	mu      sync.Mutex
	markers []string
	pubs    int
}

func (r *stageRecorder) record(marker string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.markers = append(r.markers, marker)
}

func (r *stageRecorder) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string{}, r.markers...)
}

func (r *stageRecorder) published() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.pubs
}

// recorderStage builds a Stage whose callbacks append markers to rec.
func recorderStage(rec *stageRecorder, name string, mode string, outputPath string) Stage {
	return Stage{
		Plan:       helperPlan(mode),
		Duration:   0,
		Label:      name,
		OutputPath: outputPath,
		Verify: func(ctx context.Context) error {
			rec.record(name + ".verify")
			return nil
		},
		Publish: func() error {
			rec.record(name + ".publish")
			rec.mu.Lock()
			rec.pubs++
			rec.mu.Unlock()
			return nil
		},
	}
}

// callbacksFor wires rec into OnStageStart, OnLog, OnStageDone.
func callbacksFor(rec *stageRecorder, cb Callbacks) Callbacks {
	cb.OnStageStart = func(index, total int, stage Stage) {
		rec.record(fmt.Sprintf("start%d", index))
	}
	cb.OnLog = func(string) {
		rec.record("log")
	}
	cb.OnStageDone = func(index, total int, stage Stage, err error) {
		if err != nil {
			rec.record(fmt.Sprintf("done%d.err=%v", index, err))
			return
		}
		rec.record(fmt.Sprintf("done%d", index))
	}
	return cb
}

func TestRunSequenceCompletesStagesInOrder(t *testing.T) {
	rec := &stageRecorder{}
	stages := []Stage{
		recorderStage(rec, "segment 1 of 2", "complete", "/out/part01.mkv"),
		recorderStage(rec, "segment 2 of 2", "complete", "/out/part02.mkv"),
	}
	err := (Runner{}).RunSequence(NewRunControl(), stages, callbacksFor(rec, Callbacks{}))
	if err != nil {
		t.Fatalf("RunSequence() error = %v", err)
	}
	want := []string{
		"start0", "segment 1 of 2.verify", "log", "segment 1 of 2.publish", "done0",
		"start1", "segment 2 of 2.verify", "log", "segment 2 of 2.publish", "done1",
	}
	if got := rec.snapshot(); !slices.Equal(got, want) {
		t.Fatalf("markers = %v, want %v", got, want)
	}
	if rec.published() != 2 {
		t.Fatalf("published = %d, want 2", rec.published())
	}
}

func TestRunSequenceVerifyFailureSkipsRun(t *testing.T) {
	rec := &stageRecorder{}
	stages := []Stage{recorderStage(rec, "joining segments", "complete", "/out/final.mkv")}
	stages[0].Verify = func(ctx context.Context) error {
		return errors.New("layout mismatch")
	}
	err := (Runner{}).RunSequence(NewRunControl(), stages, callbacksFor(rec, Callbacks{}))
	if err == nil || !strings.Contains(err.Error(), "joining segments") || !strings.Contains(err.Error(), "layout mismatch") {
		t.Fatalf("error = %v, want stage-labeled verify failure", err)
	}
	if errors.Is(err, context.Canceled) {
		t.Fatalf("verify failure must not look canceled: %v", err)
	}
	if rec.published() != 0 {
		t.Fatalf("published = %d, want 0", rec.published())
	}
	for _, marker := range rec.snapshot() {
		if marker == "log" {
			t.Fatal("stage process must not run when verification fails")
		}
	}
}

func TestRunSequencePublishFailureWrapsStageLabel(t *testing.T) {
	rec := &stageRecorder{}
	stages := []Stage{recorderStage(rec, "segment 1 of 2", "complete", "/out/part01.mkv")}
	stages[0].Publish = func() error { return errors.New("target appeared") }
	err := (Runner{}).RunSequence(NewRunControl(), stages, callbacksFor(rec, Callbacks{}))
	if err == nil || !strings.Contains(err.Error(), "segment 1 of 2: target appeared") {
		t.Fatalf("error = %v, want stage-labeled publish failure", err)
	}
	if errors.Is(err, context.Canceled) {
		t.Fatalf("publish failure must not look canceled: %v", err)
	}
}

func TestRunSequenceCancelBeforeStart(t *testing.T) {
	for name, request := range map[string]func(*RunControl){
		"cancel":    (*RunControl).RequestCancel,
		"terminate": (*RunControl).Terminate,
	} {
		t.Run(name, func(t *testing.T) {
			rec := &stageRecorder{}
			control := NewRunControl()
			request(control)
			stages := []Stage{recorderStage(rec, "segment 1 of 1", "complete", "/out/part01.mkv")}
			err := (Runner{}).RunSequence(control, stages, callbacksFor(rec, Callbacks{}))
			if !errors.Is(err, context.Canceled) || err != context.Canceled {
				t.Fatalf("error = %v, want bare context.Canceled", err)
			}
			for _, marker := range rec.snapshot() {
				if marker == "log" {
					t.Fatal("no stage process may run after a pre-start request")
				}
			}
		})
	}
}

func TestRunSequenceCancelDuringHangingVerify(t *testing.T) {
	for name, request := range map[string]func(*RunControl){
		"cancel":    (*RunControl).RequestCancel,
		"terminate": (*RunControl).Terminate,
	} {
		t.Run(name, func(t *testing.T) {
			rec := &stageRecorder{}
			control := NewRunControl()
			stages := []Stage{recorderStage(rec, "joining segments", "complete", "/out/final.mkv")}
			verifyCtx := make(chan context.Context, 1)
			stages[0].Verify = func(ctx context.Context) error {
				rec.record("verify.entered")
				select {
				case verifyCtx <- ctx:
				default:
				}
				<-ctx.Done() // hang until cancellation
				return ctx.Err()
			}
			done := make(chan error, 1)
			go func() { done <- (Runner{}).RunSequence(control, stages, callbacksFor(rec, Callbacks{})) }()
			var got context.Context
			select {
			case got = <-verifyCtx:
			case <-time.After(5 * time.Second):
				t.Fatal("verify never started")
			}
			if got != control.CancelContext() {
				t.Fatal("verify must run under the control's cancel context")
			}
			request(control)
			if err := waitForRun(t, done); err != context.Canceled {
				t.Fatalf("error = %v, want bare context.Canceled", err)
			}
			if rec.published() != 0 {
				t.Fatalf("published = %d, want 0", rec.published())
			}
			for _, marker := range rec.snapshot() {
				if marker == "log" {
					t.Fatal("stage process must not start while verification hangs")
				}
			}
		})
	}
}

func TestRunSequenceCancelBetweenStages(t *testing.T) {
	rec := &stageRecorder{}
	control := NewRunControl()
	stages := []Stage{
		recorderStage(rec, "segment 1 of 2", "complete", "/out/part01.mkv"),
		recorderStage(rec, "segment 2 of 2", "complete", "/out/part02.mkv"),
	}
	base := stages[0].Publish
	stages[0].Publish = func() error {
		if err := base(); err != nil {
			return err
		}
		control.RequestCancel()
		return nil
	}
	err := (Runner{}).RunSequence(control, stages, callbacksFor(rec, Callbacks{}))
	if err != context.Canceled {
		t.Fatalf("error = %v, want bare context.Canceled", err)
	}
	if rec.published() != 1 {
		t.Fatalf("published = %d, want 1 (stage 1's output stays)", rec.published())
	}
	for _, marker := range rec.snapshot() {
		if strings.HasPrefix(marker, "start1") {
			t.Fatal("stage 2 must not start after cancellation")
		}
	}
}

func TestRunSequenceCancelDuringRun(t *testing.T) {
	rec := &stageRecorder{}
	control := NewRunControl()
	ready := make(chan struct{})
	cb := callbacksFor(rec, Callbacks{})
	cb.OnLog = func(line string) {
		if line == "ready" {
			select {
			case <-ready:
			default:
				close(ready)
			}
		}
		rec.record("log")
	}
	stages := []Stage{recorderStage(rec, "segment 1 of 1", "graceful", "/out/part01.mkv")}
	done := make(chan error, 1)
	go func() { done <- (Runner{}).RunSequence(control, stages, cb) }()
	waitForReady(t, ready)
	control.RequestCancel()
	if err := waitForRun(t, done); err != context.Canceled {
		t.Fatalf("error = %v, want bare context.Canceled", err)
	}
	if rec.published() != 0 {
		t.Fatalf("published = %d, want 0", rec.published())
	}
}

func TestRunSequenceCancelBetweenRunAndPublish(t *testing.T) {
	rec := &stageRecorder{}
	control := NewRunControl()
	entered := make(chan struct{})
	release := make(chan struct{})
	prev := afterRunHook
	afterRunHook = func(index int) {
		close(entered) // signal: parked after Run, before Publish
		<-release
	}
	defer func() { afterRunHook = prev }()
	stages := []Stage{
		recorderStage(rec, "segment 1 of 2", "complete", "/out/part01.mkv"),
		recorderStage(rec, "segment 2 of 2", "complete", "/out/part02.mkv"),
	}
	done := make(chan error, 1)
	go func() { done <- (Runner{}).RunSequence(control, stages, callbacksFor(rec, Callbacks{})) }()
	<-entered // stage 0's Run finished; RunSequence is parked before Publish
	control.RequestCancel()
	close(release)
	if err := waitForRun(t, done); err != context.Canceled {
		t.Fatalf("error = %v, want bare context.Canceled", err)
	}
	if rec.published() != 0 {
		t.Fatalf("published = %d, want 0 (publish must be skipped)", rec.published())
	}
	for _, marker := range rec.snapshot() {
		if strings.Contains(marker, ".publish") {
			t.Fatal("no output may publish after cancellation was requested")
		}
		if strings.HasPrefix(marker, "start1") {
			t.Fatal("stage 2 must not start")
		}
	}
}

func TestRunSequenceCancelAfterPublishCountsOutput(t *testing.T) {
	rec := &stageRecorder{}
	control := NewRunControl()
	stages := []Stage{
		recorderStage(rec, "segment 1 of 2", "complete", "/out/part01.mkv"),
		recorderStage(rec, "segment 2 of 2", "complete", "/out/part02.mkv"),
	}
	base := stages[1].Publish
	stages[1].Publish = func() error {
		if err := base(); err != nil {
			return err
		}
		control.RequestCancel() // arrives after stage 2 published
		return nil
	}
	var doneCount int
	cb := callbacksFor(rec, Callbacks{})
	cb.OnStageDone = func(index, total int, stage Stage, err error) {
		if err == nil {
			doneCount++
		}
	}
	if err := (Runner{}).RunSequence(control, stages, cb); err != nil {
		t.Fatalf("error = %v, want nil: the run completed before the cancel arrived", err)
	}
	if doneCount != 2 || rec.published() != 2 {
		t.Fatalf("done = %d published = %d, want 2/2", doneCount, rec.published())
	}
}

func TestRunSequenceEmptyStages(t *testing.T) {
	err := (Runner{}).RunSequence(NewRunControl(), nil, Callbacks{})
	if err == nil {
		t.Fatal("empty stage list must fail")
	}
}

func TestRunSequenceAggregateProgress(t *testing.T) {
	stages := []Stage{
		{Plan: helperPlan("progress"), Duration: 2 * time.Second, Label: "segment 1 of 2"},
		{Plan: helperPlan("progress"), Duration: 2 * time.Second, Label: "segment 2 of 2"},
	}
	var mu sync.Mutex
	var seen []model.Progress
	cb := Callbacks{
		OnProgress: func(p model.Progress) {
			mu.Lock()
			seen = append(seen, p)
			mu.Unlock()
		},
	}
	if err := (Runner{}).RunSequence(NewRunControl(), stages, cb); err != nil {
		t.Fatalf("RunSequence() error = %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(seen) == 0 {
		t.Fatal("no aggregate progress reported")
	}
	lastPct := 0.0
	for i, p := range seen {
		if p.Percentage < lastPct {
			t.Fatalf("progress regressed at %d: %v < %v", i, p.Percentage, lastPct)
		}
		lastPct = p.Percentage
	}
	if last := seen[len(seen)-1]; last.Percentage != 1 {
		t.Fatalf("final percentage = %v, want 1", last.Percentage)
	}
	// Stage 1's out_time (1s, 2s) maps onto the 4s total; stage 2's
	// continues from the 2s completed base (3s, 4s).
	if seen[0].OutTime != time.Second {
		t.Fatalf("first cumulative out_time = %v, want 1s", seen[0].OutTime)
	}
	if seen[2].OutTime != 3*time.Second {
		t.Fatalf("stage 2 cumulative out_time = %v, want 3s", seen[2].OutTime)
	}
}

func TestRunSequenceHelperProcessModes(t *testing.T) {
	// fail: exits non-zero -> FFmpeg failed error wrapped with the label.
	stages := []Stage{{Plan: helperPlan("fail"), Duration: time.Second, Label: "segment 1 of 1"}}
	err := (Runner{}).RunSequence(NewRunControl(), stages, Callbacks{})
	if err == nil || !strings.Contains(err.Error(), "segment 1 of 1: FFmpeg failed") {
		t.Fatalf("error = %v, want labeled FFmpeg failure", err)
	}
}
