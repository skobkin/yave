//go:build linux

package vaapi

import (
	"fmt"
	"sync"

	"github.com/ebitengine/purego"
)

func init() {
	loadLibVA = loadLibVAOnce
}

var (
	libvaOnce   sync.Once
	libvaLoaded *libvaHandle
	libvaErr    error
)

// loadLibVAOnce dlopens libva on first use and caches the outcome for
// the process lifetime: repeated detections reuse the handle, and a
// failed load keeps failing fast without re-probing the loader.
func loadLibVAOnce() (*libvaHandle, error) {
	libvaOnce.Do(func() {
		libvaLoaded, libvaErr = openLibVA()
	})
	return libvaLoaded, libvaErr
}

// openLibVA loads libva.so.2 and libva-drm.so.2 and binds the symbols
// the probe needs. Every symbol is resolved explicitly so an
// unexpectedly stripped build degrades to "capabilities unknown"
// instead of panicking inside RegisterLibFunc.
func openLibVA() (*libvaHandle, error) {
	libva, err := purego.Dlopen("libva.so.2", purego.RTLD_NOW)
	if err != nil {
		return nil, fmt.Errorf("load libva.so.2: %w", err)
	}
	libvaDRM, err := purego.Dlopen("libva-drm.so.2", purego.RTLD_NOW)
	if err != nil {
		return nil, fmt.Errorf("load libva-drm.so.2: %w", err)
	}

	h := &libvaHandle{}
	binds := []struct {
		name   string
		fn     any
		handle uintptr
	}{
		{"vaGetDisplayDRM", &h.getDisplayDRM, libvaDRM},
		{"vaInitialize", &h.initialize, libva},
		{"vaTerminate", &h.terminate, libva},
		{"vaMaxNumProfiles", &h.maxNumProfiles, libva},
		{"vaMaxNumEntrypoints", &h.maxNumEntrypoints, libva},
		{"vaQueryConfigProfiles", &h.queryConfigProfiles, libva},
		{"vaQueryConfigEntrypoints", &h.queryConfigEntrypoints, libva},
		{"vaQueryVendorString", &h.queryVendorString, libva},
	}
	for _, b := range binds {
		sym, err := purego.Dlsym(b.handle, b.name)
		if err != nil {
			return nil, fmt.Errorf("resolve %s: %w", b.name, err)
		}
		purego.RegisterFunc(b.fn, sym)
	}
	return h, nil
}
