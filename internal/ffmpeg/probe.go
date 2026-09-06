package ffmpeg

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"

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
	render := renderNodePresent(fs)
	hw := hwAvailability{vaapi: render, qsv: render}

	// TODO: nvidia-smi's exit code could distinguish a live driver+GPU
	// from a stale /dev/nvidia0 node; stat-only avoids subprocess cost
	// and permission surprises. Revisit if the stat probe produces
	// false positives.
	if devicePresent(fs, nvidiaControlNode) {
		hw.nvenc = true
		if gpus, err := fs.glob(nvidiaPattern); err != nil || len(gpus) == 0 {
			hw.nvencGPUs = 1
		} else {
			hw.nvencGPUs = len(gpus)
		}
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

// renderNodePresent checks for a DRM render node. QSV piggybacks on the
// same nodes on Linux, so this is an approximation: an AMD-only box
// (render node present, no Intel media driver) still reports QSV as
// available — the runtime FFmpeg error makes the real gap obvious.
func renderNodePresent(fs fsProbe) bool {
	if devicePresent(fs, driRenderNode) {
		return true
	}
	nodes, err := fs.glob(driRenderPattern)
	if err != nil {
		// filepath.Glob only errors on malformed patterns; fail open.
		return true
	}
	return len(nodes) > 0
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
