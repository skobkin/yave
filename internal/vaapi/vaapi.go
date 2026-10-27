// Package vaapi implements native VA-API device enumeration and
// encoding-capability probing — what vainfo does, without the vainfo
// subprocess — plus the pure policy helpers that turn probe results
// into an encoder-device choice. The libva binding is loaded lazily
// through purego; when libva is unavailable every device is reported
// with unknown capabilities so callers can fail open.
package vaapi

import "fmt"

// Profile mirrors the C VAProfile enum. The values are ABI constants
// from libva's va.h, not a Go iota sequence.
type Profile int32

// Entrypoint mirrors the C VAEntrypoint enum (va.h ABI values).
type Entrypoint int32

// The VAProfile values yave's curated encoder set can require. Only
// profiles reachable through the fixed 8-bit 4:2:0 upload chain are
// listed; see internal/encoder/vaapi.go for the curation rationale.
const (
	ProfileH264ConstrainedBaseline Profile = 13
	ProfileH264Main                Profile = 6
	ProfileH264High                Profile = 7
	ProfileHEVCMain                Profile = 17
	ProfileHEVCMain10              Profile = 18
	ProfileVP9Profile0             Profile = 19
	ProfileAV1Profile0             Profile = 32
)

// The VAEntrypoint values the auto-selection logic distinguishes.
// EntrypointVLD is decode-only; it exists so messages can say "decode
// only" about devices that list profiles without encode entrypoints.
const (
	EntrypointVLD        Entrypoint = 1
	EntrypointEncSlice   Entrypoint = 6
	EntrypointEncPicture Entrypoint = 7
	EntrypointEncSliceLP Entrypoint = 8
)

// EncodeEntrypoints returns the entrypoints ffmpeg's VAAPI encoder
// accepts in normal (low_power=auto) mode; see the
// vaapi_encode_entrypoints_normal table in libavcodec/vaapi_encode.c.
func EncodeEntrypoints() []Entrypoint {
	return []Entrypoint{EntrypointEncSlice, EntrypointEncPicture, EntrypointEncSliceLP}
}

// DeviceInfo describes one render node as seen through libva. It is
// the vainfo-equivalent payload: the driver's vendor string plus every
// profile/entrypoint pair the device reported.
type DeviceInfo struct {
	// Path is the render-node path the probe was asked about
	// (/dev/dri/renderD<N>).
	Path string
	// Driver is vaQueryVendorString's output; empty when unknown.
	Driver string
	// EntryPoints maps every profile the device reported to all of its
	// entrypoints, decode ones included. nil when Err is set.
	EntryPoints map[Profile][]Entrypoint
	// Err is non-empty when the capabilities could not be probed (open
	// failure, libva unavailable, driver initialization error). The
	// device is still listed so the node inventory stays complete and
	// callers can fail open.
	Err string
}

// Known reports whether the device's capabilities were probed
// successfully.
func (d DeviceInfo) Known() bool { return d.Err == "" }

// Encodes reports whether the device offers an encoding entrypoint for
// any of the required profiles.
func (d DeviceInfo) Encodes(profiles ...Profile) bool {
	for _, p := range profiles {
		if d.SupportsEncoding(p) {
			return true
		}
	}
	return false
}

// SupportsEncoding reports whether the device offers an encoding
// entrypoint (any of EncodeEntrypoints) for the profile.
func (d DeviceInfo) SupportsEncoding(p Profile) bool {
	return hasEncodingEntrypoint(d.EntryPoints[p])
}

// HasAnyEncodingEntrypoint reports whether the device offers an
// encoding entrypoint for any profile it listed — not necessarily one
// of the required ones. Used to distinguish "cannot encode the
// requested profile" from "no encoding entrypoint at all" in messages.
func (d DeviceInfo) HasAnyEncodingEntrypoint() bool {
	for _, eps := range d.EntryPoints {
		if hasEncodingEntrypoint(eps) {
			return true
		}
	}
	return false
}

func hasEncodingEntrypoint(eps []Entrypoint) bool {
	for _, ep := range eps {
		for _, enc := range EncodeEntrypoints() {
			if ep == enc {
				return true
			}
		}
	}
	return false
}

// Supports reports whether the device lists the profile at all, decode
// entrypoints included. Used to phrase "decode only" messages.
func (d DeviceInfo) Supports(p Profile) bool {
	return len(d.EntryPoints[p]) > 0
}

// DeviceStatus classifies the outcome of automatic device selection.
type DeviceStatus int

const (
	// DeviceSelected means a probed device compatible with the
	// required profiles was chosen.
	DeviceSelected DeviceStatus = iota
	// DeviceFallback means no usable probe data existed (libva
	// missing, every probe failed); the first node is offered with
	// unknown capabilities — the historical default behavior.
	DeviceFallback
	// DeviceNoneCompatible means at least one device was probed but
	// none offers encoding for the required profiles.
	DeviceNoneCompatible
	// DeviceNoNodes means the host has no render nodes at all.
	DeviceNoNodes
)

// DeviceChoice is the outcome of automatic VAAPI device selection.
type DeviceChoice struct {
	// Path is the concrete -vaapi_device path; empty unless the status
	// is DeviceSelected or DeviceFallback.
	Path string
	// Driver is the chosen device's vendor string, when known.
	Driver string
	Status DeviceStatus
}

// SelectDevice picks the VAAPI device for automatic mode. It is pure
// and deterministic: the first known device in the given order that
// can encode any required profile wins. When known devices exist but
// none is compatible the status is DeviceNoneCompatible (callers turn
// that into an actionable error); with no usable probe data at all it
// falls back to nodes[0] (DeviceFallback, the historical default); an
// empty node list reports DeviceNoNodes.
//
// nodes is the sorted render-node list; devices may be partial (a node
// without a DeviceInfo entry is treated as unknown). Callers must guard
// against an empty required slice — an empty requirement can never
// match and is rejected upstream (unknown -profile:v values).
func SelectDevice(nodes []string, devices []DeviceInfo, required []Profile) DeviceChoice {
	if len(nodes) == 0 {
		return DeviceChoice{Status: DeviceNoNodes}
	}
	byPath := make(map[string]DeviceInfo, len(devices))
	for _, d := range devices {
		byPath[d.Path] = d
	}
	knownIncompatible := false
	for _, node := range nodes {
		d, ok := byPath[node]
		if !ok || !d.Known() {
			continue
		}
		knownIncompatible = true
		if d.Encodes(required...) {
			return DeviceChoice{Path: d.Path, Driver: d.Driver, Status: DeviceSelected}
		}
	}
	if knownIncompatible {
		return DeviceChoice{Status: DeviceNoneCompatible}
	}
	return DeviceChoice{Path: nodes[0], Status: DeviceFallback}
}

// DescribeDevice renders one device's capabilities relative to the
// required profiles for error and hint messages, e.g.
//
//	/dev/dri/renderD129 (Mesa Gallium driver ...) — encodes
//	/dev/dri/renderD128 (VA-API NVDEC driver) — decode only
//	/dev/dri/renderD130 (Other driver) — encodes other profiles
//	/dev/dri/renderD132 — no encoding entrypoint
//	/dev/dri/renderD131 — capabilities unknown (open: permission denied)
func DescribeDevice(d DeviceInfo, required []Profile) string {
	if !d.Known() {
		return fmt.Sprintf("%s — capabilities unknown (%s)", d.Path, d.Err)
	}
	// "no encoding entrypoint" is reserved for devices that list no
	// encoding entrypoint at all; a device that encodes other codecs
	// must not be described as if vainfo showed it decode-only.
	capability := "no encoding entrypoint"
	switch {
	case d.Encodes(required...):
		capability = "encodes"
	case hasAnySupport(d, required):
		capability = "decode only"
	case d.HasAnyEncodingEntrypoint():
		capability = "encodes other profiles"
	}
	if d.Driver == "" {
		return fmt.Sprintf("%s — %s", d.Path, capability)
	}
	return fmt.Sprintf("%s (%s) — %s", d.Path, d.Driver, capability)
}

func hasAnySupport(d DeviceInfo, profiles []Profile) bool {
	for _, p := range profiles {
		if d.Supports(p) {
			return true
		}
	}
	return false
}
