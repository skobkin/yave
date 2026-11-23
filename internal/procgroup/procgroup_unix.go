//go:build unix

// Package procgroup runs child processes in their own process group so that
// killing a child also kills anything it spawned. A wrapper script that does
// not exec ffmpeg would otherwise leave the real ffmpeg running after
// Terminate.
package procgroup

import (
	"os"
	"os/exec"
	"syscall"
)

// Configure starts cmd in a new process group.
func Configure(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

// Kill sends SIGKILL to the whole process group led by p, falling back to p
// alone when the group cannot be signalled. Use it only for processes started
// after Configure.
func Kill(p *os.Process) error {
	if p == nil {
		return nil
	}
	if err := syscall.Kill(-p.Pid, syscall.SIGKILL); err != nil {
		return p.Kill()
	}
	return nil
}
