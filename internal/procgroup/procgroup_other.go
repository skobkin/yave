//go:build !unix

// Package procgroup runs child processes in their own process group so that
// killing a child also kills anything it spawned. On this platform it only
// kills the direct child.
package procgroup

import (
	"os"
	"os/exec"
)

// Configure is a no-op on this platform.
func Configure(*exec.Cmd) {}

// Kill kills p.
func Kill(p *os.Process) error {
	if p == nil {
		return nil
	}
	return p.Kill()
}
