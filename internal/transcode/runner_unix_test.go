//go:build unix

package transcode

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"git.skobk.in/skobkin/yave/internal/model"
)

// TestRunnerTerminateKillsProcessGroup pins that Terminate also kills
// grandchildren: a wrapper script that does not exec ffmpeg must not leave
// the real encoder running.
func TestRunnerTerminateKillsProcessGroup(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "child.pid")
	script := filepath.Join(dir, "wrapper.sh")
	body := "#!/bin/sh\nsleep 60 >/dev/null 2>&1 &\necho $! > " + pidFile + "\necho ready >&2\nwait\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil { //nolint:gosec // G306: test helper script must be executable
		t.Fatal(err)
	}
	control := NewRunControl()
	ready := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- (Runner{}).Run(control, model.RunPlan{Executable: script}, 0, Callbacks{
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
	raw, err := os.ReadFile(pidFile) //nolint:gosec // G304: path from t.TempDir
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })

	control.Terminate()
	if err := waitForRun(t, done); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v, want context.Canceled", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		// Signal 0 probes existence; a reaped or zombie-free pid errors.
		if err := syscall.Kill(pid, 0); err != nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("grandchild %d survived Terminate", pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
