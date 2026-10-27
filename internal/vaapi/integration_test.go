//go:build linux

package vaapi

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// TestVAAPIDeviceProbeIntegration exercises the real libva binding
// against the host's render nodes. It is opt-in because AGENTS.md
// forbids unit tests from depending on the host's device set: run it
// on a machine with VA-API devices via
//
//	YAVE_VAAPI_INTEGRATION=1 go test ./internal/vaapi -run Integration -v
//
// The output is meant to be correlated with
// `vainfo --display drm --device <node>`; no capability assertions are
// made so the test stays meaningful on any hardware.
func TestVAAPIDeviceProbeIntegration(t *testing.T) {
	if os.Getenv("YAVE_VAAPI_INTEGRATION") != "1" {
		t.Skip("set YAVE_VAAPI_INTEGRATION=1 to probe the host's VA-API devices")
	}
	nodes, err := filepath.Glob("/dev/dri/renderD*")
	if err != nil || len(nodes) == 0 {
		t.Skip("no DRM render nodes on this host")
	}

	devices := Probe(context.Background(), nodes)

	if len(devices) != len(nodes) {
		t.Fatalf("got %d devices for %d nodes", len(devices), len(nodes))
	}
	for _, d := range devices {
		t.Logf("%s driver=%q err=%q", d.Path, d.Driver, d.Err)
		if !d.Known() {
			continue
		}
		for profile, eps := range d.EntryPoints {
			t.Logf("  profile %d: %v", profile, eps)
		}
		if d.Encodes(ProfileHEVCMain) {
			fmt.Printf("%s encodes HEVC\n", d.Path)
		}
	}
}
