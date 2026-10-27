package vaapi

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// DeviceIdentity is what sysfs and the PCI ID database say about the
// GPU behind a render node. It exists so the UI can talk about
// graphics cards instead of device paths: a node that resolves to
// "GeForce GTX 1660 Ti Mobile" no longer asks the user to trust
// /dev/dri/renderD128. Every field degrades independently to "" when
// unknown (non-Linux hosts, missing sysfs attributes, missing pci.ids
// database), and consumers must treat the zero value as "unidentified"
// rather than an error.
type DeviceIdentity struct {
	// Vendor is the PCI vendor ID, 4 lowercase hex chars ("10de").
	Vendor string
	// Device is the PCI device ID, 4 lowercase hex chars ("2191").
	Device string
	// Driver is the kernel driver bound to the device ("nvidia",
	// "amdgpu"). Not to be confused with DeviceInfo.Driver, the VA-API
	// vendor string reported by libva.
	Driver string
	// Name is the pci.ids device name, verbatim
	// ("TU116M [GeForce GTX 1660 Ti Mobile]"). "" when the PCI IDs are
	// unknown or no pci.ids database is installed.
	Name string
	// VendorName is the pci.ids vendor name ("NVIDIA Corporation").
	VendorName string
}

// HasIDs reports whether both PCI IDs were resolved.
func (id DeviceIdentity) HasIDs() bool { return id.Vendor != "" && id.Device != "" }

// DisplayName renders the identity as a compact label, most
// informative tier first:
//
//	"GeForce GTX 1660 Ti Mobile"   (pci.ids name, compacted)
//	"nvidia 10de:2191"             (kernel driver + raw IDs)
//	"10de:2191"                    (raw IDs only)
//	"nvidia"                       (kernel driver only)
//	""                             (nothing known)
func (id DeviceIdentity) DisplayName() string {
	switch {
	case id.Name != "":
		return CompactName(id.Name)
	case id.HasIDs() && id.Driver != "":
		return id.Driver + " " + id.Vendor + ":" + id.Device
	case id.HasIDs():
		return id.Vendor + ":" + id.Device
	case id.Driver != "":
		return id.Driver
	default:
		return ""
	}
}

// CompactName shortens a pci.ids device name for tight layouts. Names
// either put the marketing name in brackets after a chip name nobody
// knows ("TU116M [GeForce GTX 1660 Ti Mobile]" → the bracket
// content), or list a long family in brackets after a short chip name
// ("Renoir [Radeon Vega Series / Radeon Vega Mobile Series]" →
// "Renoir"). The first bracket pair wins; names without brackets are
// kept verbatim.
func CompactName(full string) string {
	open := strings.Index(full, " [")
	if open < 0 {
		return full
	}
	end := strings.Index(full[open+1:], "]")
	if end < 0 {
		return full
	}
	inner := full[open+2 : open+1+end]
	if strings.Contains(inner, " / ") {
		return strings.TrimSpace(full[:open])
	}
	return inner
}

// sysfsDRMRoot is where the kernel exposes DRM devices. A variable so
// tests can point it at a fixture tree.
var sysfsDRMRoot = "/sys/class/drm"

// readSysfsIdentity reads the PCI IDs and kernel driver of one render
// node from sysfs. root is the DRM class directory (sysfsDRMRoot), node
// the render-node path as probed (/dev/dri/renderD129); only its
// basename is used. Every step fails soft: an unreadable attribute
// leaves its field empty and the walk continues.
func readSysfsIdentity(root, node string) DeviceIdentity {
	var id DeviceIdentity
	dir := filepath.Join(root, filepath.Base(node), "device")
	// uevent carries every attribute at once; parse it before falling
	// back to the per-attribute files.
	if b, err := os.ReadFile(filepath.Join(dir, "uevent")); err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			key, value, ok := strings.Cut(line, "=")
			if !ok {
				continue
			}
			switch key {
			case "DRIVER":
				id.Driver = strings.TrimSpace(value)
			case "PCI_ID":
				if vendor, device, ok := strings.Cut(strings.TrimSpace(value), ":"); ok {
					id.Vendor = strings.ToLower(vendor)
					id.Device = strings.ToLower(device)
				}
			}
		}
	}
	if !id.HasIDs() {
		id.Vendor = readSysfsHex(filepath.Join(dir, "vendor"))
		id.Device = readSysfsHex(filepath.Join(dir, "device"))
	}
	if id.Driver == "" {
		if link, err := os.Readlink(filepath.Join(dir, "driver")); err == nil {
			id.Driver = filepath.Base(link)
		}
	}
	return id
}

// readSysfsHex reads a sysfs attribute holding a value like "0x10de"
// and returns the lowercase bare hex digits; "" on any error.
func readSysfsHex(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.ToLower(strings.TrimPrefix(strings.TrimSpace(string(b)), "0x"))
}

// pciIDs is the parsed pciutils ID database. Device IDs repeat across
// vendors (1636 is both an AMD Renoir and an Intel Broadwell-U part),
// so every device lookup goes through its vendor.
type pciIDs struct {
	vendors map[string]string            // vendor ID -> vendor name
	devices map[string]map[string]string // vendor ID -> device ID -> device name
}

// pciIDsPaths lists the standard locations of the pciutils database,
// tried in order. Distributions disagree about where it lives, and
// plenty of systems ship none at all.
var pciIDsPaths = []string{
	"/usr/share/hwdata/pci.ids",
	"/usr/share/misc/pci.ids",
	"/usr/share/pci.ids",
	"/var/lib/pciutils/pci.ids",
}

// parsePCIIDs scans pci.ids content into a vendor-scoped lookup. A
// device line belongs to the vendor line most recently seen; subsystem
// entries (two leading tabs), class definitions ("C 03  ..."),
// comments, and malformed lines are skipped. Hex IDs are normalized to
// lowercase; names stay verbatim. Trailing sections never disturb the
// result because only vendor-scoped lookups read the map.
func parsePCIIDs(content string) *pciIDs {
	db := &pciIDs{
		vendors: map[string]string{},
		devices: map[string]map[string]string{},
	}
	vendor := ""
	for _, line := range strings.Split(content, "\n") {
		switch {
		case strings.HasPrefix(line, "\t\t"):
			// Subsystem entry: skip without disturbing the current vendor.
		case strings.HasPrefix(line, "\t"):
			id, name, ok := cutIDName(line[1:])
			if ok && vendor != "" {
				if db.devices[vendor] == nil {
					db.devices[vendor] = map[string]string{}
				}
				db.devices[vendor][id] = name
			}
		default:
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			// Vendor line; class headers ("C 03  ...") and noise fail
			// the hex check and leave the current vendor alone.
			if id, name, ok := cutIDName(line); ok {
				vendor = id
				db.vendors[id] = name
			}
		}
	}
	return db
}

// cutIDName splits a pci.ids line into its 4-hex-digit ID (lowercased)
// and name, requiring the two-space separator the format uses. ok is
// false for anything else: class device lines, subsystem entries, and
// malformed input.
func cutIDName(s string) (id, name string, ok bool) {
	if len(s) < 6 || !isHex4(s[:4]) || s[4:6] != "  " {
		return "", "", false
	}
	return strings.ToLower(s[:4]), s[6:], true
}

// isHex4 reports whether s is exactly four hexadecimal digits.
func isHex4(s string) bool {
	if len(s) != 4 {
		return false
	}
	for _, c := range s {
		switch {
		case c >= '0' && c <= '9', c >= 'a' && c <= 'f', c >= 'A' && c <= 'F':
		default:
			return false
		}
	}
	return true
}

// loadPCIIDsFrom reads the first readable pci.ids database among paths,
// in order, and returns it parsed; ok is false when none of them can be
// read.
func loadPCIIDsFrom(paths []string, readFile func(string) ([]byte, error)) (*pciIDs, bool) {
	for _, path := range paths {
		b, err := readFile(path)
		if err != nil {
			continue
		}
		return parsePCIIDs(string(b)), true
	}
	return nil, false
}

// The parsed database (or its absence) is cached for the process
// lifetime: the file is over a megabyte and GPU identities never change
// under a running yave.
var (
	pciIDsOnce  sync.Once
	pciIDsCache *pciIDs
)

// pciIDsForLookup is the seam lookupPCIName resolves through; tests
// stub it like identifyRenderNode.
var pciIDsForLookup = loadPCIIDsDB

// loadPCIIDsDB reads and parses pci.idsPaths at most once; hosts
// without the database cache the miss and resolve nothing.
func loadPCIIDsDB() *pciIDs {
	pciIDsOnce.Do(func() {
		db, ok := loadPCIIDsFrom(pciIDsPaths, os.ReadFile)
		if ok {
			pciIDsCache = db
		}
	})
	return pciIDsCache
}

// lookupPCIName resolves a vendor:device pair against the pci.ids
// database, returning the device and vendor names ("" on any miss).
// Inputs are matched case-insensitively.
func lookupPCIName(vendor, device string) (deviceName, vendorName string) {
	db := pciIDsForLookup()
	if db == nil {
		return "", ""
	}
	vendor = strings.ToLower(vendor)
	device = strings.ToLower(device)
	if db.devices[vendor] == nil {
		return "", ""
	}
	return db.devices[vendor][device], db.vendors[vendor]
}

// knownVendorBrands carries short brands for the PCI vendors that
// typically back VA-API devices; pci.ids vendor names are wordier
// ("NVIDIA Corporation", "Advanced Micro Devices, Inc. [AMD/ATI]").
var knownVendorBrands = map[string]string{
	"10de": "NVIDIA",
	"8086": "Intel",
	"1002": "AMD",
}

// brand picks the display brand for the PCI vendor: the short known
// brand, else the pci.ids vendor name, else "".
func (id DeviceIdentity) brand() string {
	if brand, ok := knownVendorBrands[id.Vendor]; ok {
		return brand
	}
	return id.VendorName
}

// FullNameWithBrand renders the full pci.ids device name with a short
// vendor brand when one resolves
// ("NVIDIA TU116M [GeForce GTX 1660 Ti Mobile]"), degrading to the
// tiered DisplayName for identities without a resolved name
// ("amdgpu 1002:1636") and to "" when nothing is known.
func (id DeviceIdentity) FullNameWithBrand() string {
	if id.Name == "" {
		return id.DisplayName()
	}
	if brand := id.brand(); brand != "" {
		return brand + " " + id.Name
	}
	return id.Name
}

// identifyRenderNode is the seam Probe enriches devices through; tests
// stub it to stay host-independent.
var identifyRenderNode = identifyRenderNodeOS

// identifyRenderNodeOS combines sysfs attributes with the pci.ids
// database into one identity record for a render-node path.
func identifyRenderNodeOS(node string) DeviceIdentity {
	id := readSysfsIdentity(sysfsDRMRoot, node)
	if id.HasIDs() {
		id.Name, id.VendorName = lookupPCIName(id.Vendor, id.Device)
	}
	return id
}
