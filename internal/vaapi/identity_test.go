package vaapi

import (
	"os"
	"path/filepath"
	"testing"
)

const testPCIIDsContent = `10de  NVIDIA Corporation
	2191  TU116M [GeForce GTX 1660 Ti Mobile]
		1043 171f  -- TU116M [GeForce GTX 1660 Ti Mobile]
	21xx  malformed hex must be skipped
1002  Advanced Micro Devices, Inc. [AMD/ATI]
	1636  Renoir [Radeon Vega Series / Radeon Vega Mobile Series]
		1043 171f  -- Renoir subsystem entry
8086  Intel Corporation
	1636  Broadwell-U Integrated Graphics

# List of known device classes
C 03  Display controller
	00  VGA compatible controller
badline
# 10ff  Commented vendor must not match
`

func TestParsePCIIDs(t *testing.T) {
	db := parsePCIIDs(testPCIIDsContent)

	wantVendors := map[string]string{
		"10de": "NVIDIA Corporation",
		"1002": "Advanced Micro Devices, Inc. [AMD/ATI]",
		"8086": "Intel Corporation",
	}
	for id, name := range wantVendors {
		if got := db.vendors[id]; got != name {
			t.Errorf("vendors[%q] = %q, want %q", id, got, name)
		}
	}

	// The same device ID under different vendors must resolve
	// separately (1636 is both an AMD Renoir and an Intel part).
	wantDevices := map[string][2]string{
		"10de": {"2191", "TU116M [GeForce GTX 1660 Ti Mobile]"},
		"1002": {"1636", "Renoir [Radeon Vega Series / Radeon Vega Mobile Series]"},
		"8086": {"1636", "Broadwell-U Integrated Graphics"},
	}
	for vendor, want := range wantDevices {
		if got := db.devices[vendor][want[0]]; got != want[1] {
			t.Errorf("devices[%q][%q] = %q, want %q", vendor, want[0], got, want[1])
		}
	}

	if _, ok := db.devices["10de"]["1043"]; ok {
		t.Error("subsystem entry was stored as a device")
	}
	if _, ok := db.vendors["03"]; ok {
		t.Error("class header was stored as a vendor")
	}
	if _, ok := db.devices["03"]; ok {
		t.Error("class device line was stored")
	}
	if _, ok := db.vendors["10ff"]; ok {
		t.Error("commented vendor was stored")
	}
	if _, ok := db.devices["10de"]["21xx"]; ok {
		t.Error("malformed hex device was stored")
	}

	upper := parsePCIIDs("10DE  NVIDIA Corporation\n\t21AB  Uppercase IDs\n")
	if db.vendors["10de"] == "" && upper.vendors["10de"] != "NVIDIA Corporation" {
		t.Errorf("uppercase vendor ID not normalized: %q", upper.vendors["10de"])
	}
	if got := upper.devices["10de"]["21ab"]; got != "Uppercase IDs" {
		t.Errorf("uppercase device ID not normalized: %q", got)
	}

	empty := parsePCIIDs("")
	if len(empty.vendors) != 0 || len(empty.devices) != 0 {
		t.Error("empty content produced non-empty database")
	}
}

func TestCompactName(t *testing.T) {
	tests := []struct {
		full string
		want string
	}{
		{"Renoir [Radeon Vega Series / Radeon Vega Mobile Series]", "Renoir"},
		{"TU116M [GeForce GTX 1660 Ti Mobile]", "GeForce GTX 1660 Ti Mobile"},
		{"Simple Name", "Simple Name"},
		{"NV50 [GeForce 210]", "GeForce 210"},
		{"Chip  [A / B]", "Chip"},
		{"", ""},
	}
	for _, tt := range tests {
		if got := CompactName(tt.full); got != tt.want {
			t.Errorf("CompactName(%q) = %q, want %q", tt.full, got, tt.want)
		}
	}
}

func TestDeviceIdentityDisplayName(t *testing.T) {
	tests := []struct {
		name string
		id   DeviceIdentity
		want string
	}{
		{"pci.ids name wins", DeviceIdentity{Vendor: "10de", Device: "2191", Driver: "nvidia", Name: "TU116M [GeForce GTX 1660 Ti Mobile]"}, "GeForce GTX 1660 Ti Mobile"},
		{"driver and IDs", DeviceIdentity{Vendor: "10de", Device: "2191", Driver: "nvidia"}, "nvidia 10de:2191"},
		{"IDs only", DeviceIdentity{Vendor: "10de", Device: "2191"}, "10de:2191"},
		{"driver only", DeviceIdentity{Driver: "nvidia"}, "nvidia"},
		{"nothing", DeviceIdentity{}, ""},
	}
	for _, tt := range tests {
		if got := tt.id.DisplayName(); got != tt.want {
			t.Errorf("%s: DisplayName() = %q, want %q", tt.name, got, tt.want)
		}
	}
	if (&DeviceIdentity{Vendor: "10de"}).HasIDs() {
		t.Error("HasIDs() true with only the vendor set")
	}
}

// writeSysfsFixture builds a sysfs DRM class tree for one render node
// and returns the class root.
func writeSysfsFixture(t *testing.T, files map[string]string, links map[string]string) string {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "renderD129", "device")
	for name, content := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for name, target := range links {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, path); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestReadSysfsIdentity(t *testing.T) {
	t.Run("uevent carries everything", func(t *testing.T) {
		root := writeSysfsFixture(t, map[string]string{
			"uevent": "DRIVER=nvidia\nPCI_ID=10DE:2191\nPCI_SUBSYS_ID=1043:171F\n",
		}, nil)
		id := readSysfsIdentity(root, "/dev/dri/renderD129")
		want := DeviceIdentity{Vendor: "10de", Device: "2191", Driver: "nvidia"}
		if id != want {
			t.Errorf("readSysfsIdentity() = %+v, want %+v", id, want)
		}
	})

	t.Run("vendor and device files fill missing IDs", func(t *testing.T) {
		root := writeSysfsFixture(t, map[string]string{
			"uevent": "DRIVER=amdgpu\n",
			"vendor": "0x1002\n",
			"device": "0x1636\n",
		}, nil)
		id := readSysfsIdentity(root, "/dev/dri/renderD129")
		want := DeviceIdentity{Vendor: "1002", Device: "1636", Driver: "amdgpu"}
		if id != want {
			t.Errorf("readSysfsIdentity() = %+v, want %+v", id, want)
		}
	})

	t.Run("driver symlink fills missing driver", func(t *testing.T) {
		root := writeSysfsFixture(t, map[string]string{
			"vendor": "0x10DE\n",
			"device": "0x2191\n",
		}, map[string]string{
			"driver": "../../../../bus/pci/drivers/nvidia",
		})
		id := readSysfsIdentity(root, "/dev/dri/renderD129")
		want := DeviceIdentity{Vendor: "10de", Device: "2191", Driver: "nvidia"}
		if id != want {
			t.Errorf("readSysfsIdentity() = %+v, want %+v", id, want)
		}
	})

	t.Run("empty device directory yields zero identity", func(t *testing.T) {
		root := writeSysfsFixture(t, nil, nil)
		if id := (readSysfsIdentity(root, "/dev/dri/renderD129")); id != (DeviceIdentity{}) {
			t.Errorf("readSysfsIdentity() = %+v, want zero value", id)
		}
	})

	t.Run("malformed PCI_ID falls back to files", func(t *testing.T) {
		root := writeSysfsFixture(t, map[string]string{
			"uevent": "DRIVER=nvidia\nPCI_ID=10DE2191\n",
		}, nil)
		id := readSysfsIdentity(root, "/dev/dri/renderD129")
		want := DeviceIdentity{Driver: "nvidia"}
		if id != want {
			t.Errorf("readSysfsIdentity() = %+v, want %+v", id, want)
		}
	})
}

func TestLoadPCIIDsFrom(t *testing.T) {
	newReader := func(available map[string]string) func(string) ([]byte, error) {
		return func(path string) ([]byte, error) {
			if content, ok := available[path]; ok {
				return []byte(content), nil
			}
			return nil, os.ErrNotExist
		}
	}

	t.Run("first readable path wins", func(t *testing.T) {
		db, ok := loadPCIIDsFrom([]string{"/a", "/b"}, newReader(map[string]string{
			"/a": "10de  NVIDIA Corporation\n",
			"/b": "1002  AMD\n",
		}))
		if !ok || db.vendors["10de"] != "NVIDIA Corporation" {
			t.Errorf("loadPCIIDsFrom() = %+v, %v; want the first database", db, ok)
		}
	})

	t.Run("falls back to later paths", func(t *testing.T) {
		db, ok := loadPCIIDsFrom([]string{"/missing", "/b"}, newReader(map[string]string{
			"/b": "1002  AMD\n",
		}))
		if !ok || db.vendors["1002"] != "AMD" {
			t.Errorf("loadPCIIDsFrom() = %+v, %v; want the fallback database", db, ok)
		}
	})

	t.Run("no readable path", func(t *testing.T) {
		if _, ok := loadPCIIDsFrom([]string{"/missing"}, newReader(nil)); ok {
			t.Error("loadPCIIDsFrom() reported success without any database")
		}
	})
}

func TestLookupPCIName(t *testing.T) {
	original := pciIDsForLookup
	pciIDsForLookup = func() *pciIDs { return parsePCIIDs(testPCIIDsContent) }
	t.Cleanup(func() { pciIDsForLookup = original })

	device, vendor := lookupPCIName("10DE", "2191")
	if device != "TU116M [GeForce GTX 1660 Ti Mobile]" || vendor != "NVIDIA Corporation" {
		t.Errorf("lookupPCIName(10DE, 2191) = %q, %q", device, vendor)
	}

	// A known vendor with an unknown device still names the vendor:
	// callers use it for branding regardless of the device match.
	device, vendor = lookupPCIName("1002", "9999")
	if device != "" {
		t.Errorf("unknown device resolved to %q", device)
	}
	if vendor == "" {
		t.Error("known vendor resolved to an empty vendor name")
	}

	device, vendor = lookupPCIName("dead", "2191")
	if device != "" || vendor != "" {
		t.Errorf("unknown vendor resolved to %q, %q", device, vendor)
	}
}

func TestIdentifyRenderNodeOS(t *testing.T) {
	originalRoot, originalDB := sysfsDRMRoot, pciIDsForLookup
	t.Cleanup(func() { sysfsDRMRoot, pciIDsForLookup = originalRoot, originalDB })

	sysfsDRMRoot = writeSysfsFixture(t, map[string]string{
		"uevent": "DRIVER=nvidia\nPCI_ID=10DE:2191\n",
	}, nil)
	pciIDsForLookup = func() *pciIDs { return parsePCIIDs(testPCIIDsContent) }

	id := identifyRenderNodeOS("/dev/dri/renderD129")
	want := DeviceIdentity{
		Vendor:     "10de",
		Device:     "2191",
		Driver:     "nvidia",
		Name:       "TU116M [GeForce GTX 1660 Ti Mobile]",
		VendorName: "NVIDIA Corporation",
	}
	if id != want {
		t.Errorf("identifyRenderNodeOS() = %+v, want %+v", id, want)
	}
}
