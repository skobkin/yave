//go:build !windows

package ffmpeg

import "git.skobk.in/skobkin/yave/internal/model"

// osGPUAdapterProbe is the seam for the platform display-adapter
// enumeration. The second result reports completeness: only a
// completed enumeration yields an authoritative list. Non-Windows
// platforms have no probe here (Linux discovers VAAPI/QSV devices via
// DRM render nodes; macOS has no VAAPI/QSV targets); tests inject
// fakes through this variable.
var osGPUAdapterProbe = func() ([]model.GPUAdapter, bool) { return nil, false }
