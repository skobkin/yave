package vaapi

import (
	"context"
	"fmt"
	"os"
)

// statusUnsupportedProfile is VA_STATUS_ERROR_UNSUPPORTED_PROFILE from
// va.h: a listed profile can still reject an entrypoint query, and
// vainfo skips exactly this status instead of failing the device.
const statusUnsupportedProfile = 0x0c

// libvaHandle holds the libva entry points the probe needs. VADisplay
// is an opaque C pointer carried as uintptr, with 0 standing in for a
// failed vaGetDisplayDRM. Tests build fakes from plain Go funcs; the
// real instance is bound via purego in dlopen_linux.go.
type libvaHandle struct {
	getDisplayDRM          func(fd int) uintptr
	initialize             func(dpy uintptr, major, minor *int32) int32
	terminate              func(dpy uintptr) int32
	maxNumProfiles         func(dpy uintptr) int32
	maxNumEntrypoints      func(dpy uintptr) int32
	queryConfigProfiles    func(dpy uintptr, profiles *Profile, num *int32) int32
	queryConfigEntrypoints func(dpy uintptr, profile Profile, entrypoints *Entrypoint, num *int32) int32
	queryVendorString      func(dpy uintptr) string
}

// loadLibVA is the platform seam: the dlopen-backed loader on Linux,
// an error stub elsewhere. The handle is cached for the process
// lifetime — unloading libva while a driver may hold state is unsafe,
// and re-detections simply reuse it.
var loadLibVA func() (*libvaHandle, error)

// Probe records the VA-API capabilities of every render node,
// vainfo-style: open the node, vaGetDisplayDRM, vaInitialize,
// vaQueryConfigProfiles, per-profile vaQueryConfigEntrypoints, and
// vaQueryVendorString for the driver name. One DeviceInfo is returned
// per node, in input order; a failing device is reported with Err set
// and the loop continues.
//
// The context is checked between devices only — libva calls cannot be
// interrupted — so cancellation truncates the result to the devices
// probed so far. With discovery's detection budget this is practically
// unreachable; SelectDevice treats missing entries as unknown either
// way.
func Probe(ctx context.Context, nodes []string) []DeviceInfo {
	l, err := loadLibVA()
	if err != nil {
		// No libva: every device is listed with unknown capabilities so
		// the node inventory stays complete.
		devices := make([]DeviceInfo, 0, len(nodes))
		for _, node := range nodes {
			devices = append(devices, DeviceInfo{Path: node, Err: err.Error()})
		}
		return devices
	}
	return probeWith(ctx, l, nodes)
}

// probeWith runs the per-device probe loop against an already loaded
// handle. Split from Probe so tests can inject a fake handle without
// touching libva.
func probeWith(ctx context.Context, l *libvaHandle, nodes []string) []DeviceInfo {
	devices := make([]DeviceInfo, 0, len(nodes))
	for _, node := range nodes {
		if ctx.Err() != nil {
			break
		}
		devices = append(devices, probeNode(l, node))
	}
	return devices
}

// probeNode probes a single render node. It always returns a
// DeviceInfo with Path set; Err carries the failure reason when the
// capabilities could not be determined. Cleanup follows vainfo's
// ordering: vaTerminate whenever a display was obtained, then the fd
// is closed last.
func probeNode(l *libvaHandle, path string) DeviceInfo {
	info := DeviceInfo{Path: path}

	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		info.Err = fmt.Sprintf("open %s: %v", path, err)
		return info
	}
	defer f.Close()

	display := l.getDisplayDRM(int(f.Fd()))
	if display == 0 {
		info.Err = fmt.Sprintf("vaGetDisplayDRM failed for %s", path)
		return info
	}
	defer l.terminate(display) //nolint:errcheck // status ignored, like vainfo's unconditional cleanup

	var major, minor int32
	if st := l.initialize(display, &major, &minor); st != 0 {
		info.Err = fmt.Sprintf("vaInitialize failed for %s (status %d)", path, st)
		return info
	}

	maxProfiles := int(l.maxNumProfiles(display))
	if maxProfiles <= 0 {
		info.Err = fmt.Sprintf("vaMaxNumProfiles returned %d for %s", maxProfiles, path)
		return info
	}
	profiles := make([]Profile, maxProfiles)
	var numProfiles int32
	if st := l.queryConfigProfiles(display, &profiles[0], &numProfiles); st != 0 {
		info.Err = fmt.Sprintf("vaQueryConfigProfiles failed for %s (status %d)", path, st)
		return info
	}

	maxEntrypoints := int(l.maxNumEntrypoints(display))
	if maxEntrypoints <= 0 {
		info.Err = fmt.Sprintf("vaMaxNumEntrypoints returned %d for %s", maxEntrypoints, path)
		return info
	}
	entrypoints := make([]Entrypoint, maxEntrypoints)
	for _, profile := range profiles[:numProfiles] {
		var num int32
		st := l.queryConfigEntrypoints(display, profile, &entrypoints[0], &num)
		if st != 0 {
			if st == statusUnsupportedProfile {
				continue
			}
			info.Err = fmt.Sprintf("vaQueryConfigEntrypoints failed for %s profile %d (status %d)", path, profile, st)
			return info
		}
		listed := make([]Entrypoint, num)
		copy(listed, entrypoints[:num])
		if info.EntryPoints == nil {
			info.EntryPoints = make(map[Profile][]Entrypoint)
		}
		info.EntryPoints[profile] = listed
	}

	info.Driver = l.queryVendorString(display)
	return info
}
