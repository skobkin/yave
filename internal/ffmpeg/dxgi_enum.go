package ffmpeg

import (
	"errors"

	"git.skobk.in/skobkin/yave/internal/model"
)

// PCI vendor IDs the adapter summaries distinguish. Declared untagged
// and exported so the UI hints can reference them from any platform.
const (
	VendorNVIDIA uint32 = 0x10DE
	VendorIntel  uint32 = 0x8086
)

// errDXGINotFound mirrors DXGI_ERROR_NOT_FOUND (0x887A0002): the
// normal end of an adapter enumeration, not a failure.
var errDXGINotFound = errors.New("dxgi: no more adapters")

// collectAdapters drives the adapter enumeration loop as pure logic so
// the completeness contract is testable off-Windows. next returns the
// next adapter, or errDXGINotFound once the enumeration is exhausted;
// any other error aborts. An exhausted enumeration returns the
// accumulated adapters with complete=true — including the empty list
// when the very first call reports NOT_FOUND — while any other
// failure returns (nil, false): an incomplete probe must never be
// published, because consumers use the list to decide that a saved
// device has vanished.
func collectAdapters(next func() (model.GPUAdapter, error)) ([]model.GPUAdapter, bool) {
	var adapters []model.GPUAdapter
	for {
		a, err := next()
		if errors.Is(err, errDXGINotFound) {
			return adapters, true
		}
		if err != nil {
			return nil, false
		}
		adapters = append(adapters, a)
	}
}

// countVendorAdapters counts the adapters with the given PCI vendor ID.
func countVendorAdapters(adapters []model.GPUAdapter, vendorID uint32) int {
	n := 0
	for _, a := range adapters {
		if a.VendorID == vendorID {
			n++
		}
	}
	return n
}
