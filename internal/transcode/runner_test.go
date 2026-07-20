package transcode

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
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
