//go:build windows

package ffmpeg

import (
	"os"
	"testing"
)

// TestDXGIAdapterEnumeration is an opt-in integration test for the raw
// COM binding: it runs the real dxgi.dll enumeration and asserts at
// least one adapter with a non-empty description. No vendor whitelist
// — valid machines carry virtio/Parallels/basic-render adapters. A
// wrong vtable slot yields HRESULTs instead of adapters and fails
// here. Cross-compilation cannot catch vtable errors, so this must run
// on real Windows:
//
//	YAVE_TEST_DXGI=1 go test ./internal/ffmpeg/
func TestDXGIAdapterEnumeration(t *testing.T) {
	if os.Getenv("YAVE_TEST_DXGI") == "" {
		t.Skip("set YAVE_TEST_DXGI=1 to run the real DXGI enumeration")
	}
	adapters, complete := enumerateDXGIAdapters()
	if !complete {
		t.Fatalf("enumeration reported incomplete; refusing to judge the binding on a partial run")
	}
	if len(adapters) == 0 {
		t.Fatalf("no adapters enumerated — the binding is wrong or the machine has no display adapters")
	}
	for _, a := range adapters {
		if a.Name == "" {
			t.Errorf("adapter %d has an empty description", a.Index)
		}
		t.Logf("adapter %d: %s (vendor 0x%04x)", a.Index, a.Name, a.VendorID)
	}
}
