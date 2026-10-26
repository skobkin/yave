package ffmpeg

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sort"

	"git.skobk.in/skobkin/yave/internal/encoder"
	"git.skobk.in/skobkin/yave/internal/model"
)

const (
	driRenderNode     = "/dev/dri/renderD128"
	driRenderPattern  = "/dev/dri/renderD*"
	nvidiaControlNode = "/dev/nvidia0"
	nvidiaPattern     = "/dev/nvidia[0-9]*"
)

// fsProbe is the minimal filesystem seam the hardware probe needs.
// osFSProbe is the real implementation; tests inject fakes.
type fsProbe struct {
	stat func(name string) error
	glob func(pattern string) ([]string, error)
}

var osFSProbe = fsProbe{
	stat: func(name string) error { _, err := os.Stat(name); return err },
	glob: filepath.Glob,
}

// hwAvailability records which hardware encoder backends have working
// device nodes. Copied into model.Capabilities for observability;
// encoder filtering happens in Discover via filterUnavailableHWEncoders.
type hwAvailability struct {
	vaapi     bool
	qsv       bool
	nvenc     bool
	nvencGPUs int
	// renderNodes lists the DRM render nodes found on the host, sorted.
	// Non-Linux and "no nodes" both leave it nil; the fail-open branch
	// (unreadable /dev) offers the canonical node so device dropdowns
	// stay populated.
	renderNodes []string
}

// probeHWBackends checks for the device nodes the curated hardware
// encoder families need. Non-Linux platforms skip the probe entirely:
// the render-node layout below is Linux-specific, and NVENC genuinely
// works on Windows without /dev entries.
func probeHWBackends(fs fsProbe) hwAvailability {
	if runtime.GOOS != "linux" {
		return hwAvailability{vaapi: true, qsv: true, nvenc: true}
	}
	return probeLinuxHWBackends(fs)
}

func probeLinuxHWBackends(fs fsProbe) hwAvailability {
	render, nodes := renderNodes(fs)
	hw := hwAvailability{vaapi: render, qsv: render, renderNodes: nodes}

	// TODO: nvidia-smi's exit code could distinguish a live driver+GPU
	// from a stale /dev/nvidia0 node; stat-only avoids subprocess cost
	// and permission surprises. Revisit if the stat probe produces
	// false positives.
	if gpus, err := fs.glob(nvidiaPattern); err == nil && len(gpus) > 0 {
		// Enumeration is authoritative: containers may expose only
		// nonzero GPU nodes (no /dev/nvidia0), so GPU 0 alone is not
		// the gate for availability.
		hw.nvenc = true
		hw.nvencGPUs = len(gpus)
	} else if devicePresent(fs, nvidiaControlNode) {
		// Enumeration empty or failed while the GPU 0 node exists:
		// keep NVENC visible and conservatively report one GPU.
		hw.nvenc = true
		hw.nvencGPUs = 1
	}
	return hw
}

// devicePresent reports whether a device node exists. A stat failure
// other than a clean not-exist (e.g. permission on a restricted /dev)
// is treated as present: hiding a working encoder is worse than
// showing one that errors at run time.
func devicePresent(fs fsProbe, path string) bool {
	err := fs.stat(path)
	switch {
	case err == nil:
		return true
	case errors.Is(err, os.ErrNotExist):
		return false
	default:
		return true
	}
}

// renderNodes checks for DRM render nodes and returns them sorted.
// QSV piggybacks on the same nodes on Linux, so this is an
// approximation: an AMD-only box (render node present, no Intel media
// driver) still reports QSV as available — the runtime FFmpeg error
// makes the real gap obvious.
//
// The bool reports availability with the same semantics the previous
// renderNodePresent probe had: a glob error fails open (devicePresent
// semantics — hiding a working encoder is worse than showing one that
// errors at run time) and the canonical node is offered even though it
// could not be verified. Nothing found at all reports (false, nil).
func renderNodes(fs fsProbe) (bool, []string) {
	nodes, err := fs.glob(driRenderPattern)
	if err != nil {
		// filepath.Glob only errors on malformed patterns; fail open.
		return true, []string{driRenderNode}
	}
	if len(nodes) == 0 {
		// No matches: only count the canonical node as present when a
		// stat confirms it (or the stat fails non-not-exist).
		if devicePresent(fs, driRenderNode) {
			return true, []string{driRenderNode}
		}
		return false, nil
	}
	sort.Strings(nodes)
	return true, nodes
}

// filterUnavailableHWEncoders drops encoders whose hardware backend has
// no device node. The map is freshly built by ParseEncoders, so it is
// mutated in place. Software, audio, and copy encoders are untouched.
func filterUnavailableHWEncoders(encoders map[string]model.Encoder, hw hwAvailability) map[string]model.Encoder {
	reg := encoder.DefaultRegistry()
	for name := range encoders {
		if name == "copy" {
			continue
		}
		ve, ok := reg.Get(name)
		if !ok {
			continue
		}
		switch ve.HWBackend() {
		case encoder.HWVAAPI:
			if !hw.vaapi {
				delete(encoders, name)
			}
		case encoder.HWQSV:
			if !hw.qsv {
				delete(encoders, name)
			}
		case encoder.HWNVENC:
			if !hw.nvenc {
				delete(encoders, name)
			}
		}
	}
	return encoders
}
