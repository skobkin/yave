package vaapi

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unsafe"
)

const fakeDisplay = 42

// fakeLibva builds a libvaHandle serving one synthetic device: every
// opened node maps to a single display with the given profiles,
// entrypoints, and vendor string. Call counters expose the cleanup
// ordering to tests.
type fakeLibva struct {
	profiles    []Profile
	entrypoints map[Profile][]Entrypoint
	vendor      string

	// Behavior knobs, applied from the fake functions.
	failDisplayDRM      bool
	initializeStatus    int32
	queryProfilesStatus int32
	queryProfilesNum    int32 // override; 0 means len(profiles)
	entrypointStatus    map[Profile]int32

	maxNumProfiles    int32
	maxNumEntrypoints int32

	terminates int
}

func (f *fakeLibva) handle() *libvaHandle {
	if f.maxNumProfiles == 0 {
		f.maxNumProfiles = 64
	}
	if f.maxNumEntrypoints == 0 {
		f.maxNumEntrypoints = 8
	}
	return &libvaHandle{
		getDisplayDRM: func(int) uintptr {
			if f.failDisplayDRM {
				return 0
			}
			return fakeDisplay
		},
		initialize: func(uintptr, *int32, *int32) int32 {
			return f.initializeStatus
		},
		terminate: func(uintptr) int32 {
			f.terminates++
			return 0
		},
		maxNumProfiles:    func(uintptr) int32 { return f.maxNumProfiles },
		maxNumEntrypoints: func(uintptr) int32 { return f.maxNumEntrypoints },
		queryConfigProfiles: func(_ uintptr, list *Profile, num *int32) int32 {
			if f.queryProfilesStatus != 0 {
				return f.queryProfilesStatus
			}
			n := int32(len(f.profiles))
			if f.queryProfilesNum != 0 {
				n = f.queryProfilesNum
			}
			slice := unsafe.Slice(list, n)
			for i := int32(0); i < n; i++ {
				slice[i] = f.profiles[i]
			}
			*num = n
			return 0
		},
		queryConfigEntrypoints: func(_ uintptr, profile Profile, list *Entrypoint, num *int32) int32 {
			if st, ok := f.entrypointStatus[profile]; ok && st != 0 {
				return st
			}
			eps := f.entrypoints[profile]
			slice := unsafe.Slice(list, len(eps))
			for i, ep := range eps {
				slice[i] = ep
			}
			*num = int32(len(eps))
			return 0
		},
		queryVendorString: func(uintptr) string { return f.vendor },
	}
}

// probeFile is the node stand-in: probeNode opens the path it is
// given, so a regular file exercises the open path without /dev/dri.
func probeFile(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "renderD128")
}

func TestProbeNodeHappyPath(t *testing.T) {
	node := probeFile(t)
	if err := os.WriteFile(node, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	f := &fakeLibva{
		profiles: []Profile{ProfileH264Main, ProfileHEVCMain},
		entrypoints: map[Profile][]Entrypoint{
			ProfileH264Main: {EntrypointVLD, EntrypointEncSlice},
			ProfileHEVCMain: {EntrypointVLD},
		},
		vendor: "Test Driver 1.0",
	}

	info := probeNode(f.handle(), node)

	if info.Err != "" {
		t.Fatalf("unexpected Err: %s", info.Err)
	}
	if info.Path != node || info.Driver != "Test Driver 1.0" {
		t.Errorf("unexpected DeviceInfo: %+v", info)
	}
	want := map[Profile][]Entrypoint{
		ProfileH264Main: {EntrypointVLD, EntrypointEncSlice},
		ProfileHEVCMain: {EntrypointVLD},
	}
	for profile, eps := range want {
		if got := info.EntryPoints[profile]; len(got) != len(eps) {
			t.Errorf("profile %d entrypoints = %v, want %v", profile, got, eps)
		}
	}
	if f.terminates != 1 {
		t.Errorf("terminate calls = %d, want 1", f.terminates)
	}
	if !info.Known() || !info.Encodes(ProfileH264Main) || info.Encodes(ProfileHEVCMain) {
		t.Errorf("capability methods disagree with the fixture: %+v", info)
	}
}

func TestProbeNodeOpenFailure(t *testing.T) {
	info := probeNode((&fakeLibva{}).handle(), filepath.Join(t.TempDir(), "missing"))
	if info.Err == "" {
		t.Fatal("open failure must set Err")
	}
	if info.EntryPoints != nil {
		t.Errorf("failed probe must not report entrypoints: %+v", info)
	}
}

func TestProbeNodeDisplayFailureSkipsTerminate(t *testing.T) {
	node := probeFile(t)
	if err := os.WriteFile(node, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	f := &fakeLibva{failDisplayDRM: true}

	info := probeNode(f.handle(), node)

	if info.Err == "" || !strings.Contains(info.Err, "vaGetDisplayDRM") {
		t.Fatalf("Err = %q, want a vaGetDisplayDRM failure", info.Err)
	}
	if f.terminates != 0 {
		t.Errorf("terminate must not run without a display, got %d calls", f.terminates)
	}
}

func TestProbeNodeInitializeFailureStillTerminates(t *testing.T) {
	node := probeFile(t)
	if err := os.WriteFile(node, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	f := &fakeLibva{initializeStatus: 3}

	info := probeNode(f.handle(), node)

	if info.Err == "" || !strings.Contains(info.Err, "vaInitialize") {
		t.Fatalf("Err = %q, want a vaInitialize failure", info.Err)
	}
	if f.terminates != 1 {
		t.Errorf("terminate calls = %d, want 1 after a failed initialize", f.terminates)
	}
}

func TestProbeNodeQueryProfilesFailure(t *testing.T) {
	node := probeFile(t)
	if err := os.WriteFile(node, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	f := &fakeLibva{queryProfilesStatus: 5}

	info := probeNode(f.handle(), node)

	if info.Err == "" || !strings.Contains(info.Err, "vaQueryConfigProfiles") {
		t.Fatalf("Err = %q, want a vaQueryConfigProfiles failure", info.Err)
	}
}

func TestProbeNodeZeroProfilesIsKnown(t *testing.T) {
	node := probeFile(t)
	if err := os.WriteFile(node, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	f := &fakeLibva{}

	info := probeNode(f.handle(), node)

	if info.Err != "" {
		t.Fatalf("zero profiles is not an error, got %q", info.Err)
	}
	if info.EntryPoints != nil {
		t.Errorf("EntryPoints = %v, want nil", info.EntryPoints)
	}
	if !info.Known() {
		t.Error("device with zero profiles must count as known")
	}
}

func TestProbeNodeEntrypointQueryStatuses(t *testing.T) {
	node := probeFile(t)
	if err := os.WriteFile(node, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name    string
		status  int32
		wantErr bool
	}{
		{name: "unsupported profile is skipped", status: statusUnsupportedProfile},
		{name: "other statuses fail the device", status: 2, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &fakeLibva{
				profiles: []Profile{ProfileVP9Profile0, ProfileHEVCMain},
				entrypoints: map[Profile][]Entrypoint{
					ProfileHEVCMain: {EntrypointEncSlice},
				},
				entrypointStatus: map[Profile]int32{ProfileVP9Profile0: tt.status},
			}
			info := probeNode(f.handle(), node)
			if tt.wantErr {
				if info.Err == "" || !strings.Contains(info.Err, "vaQueryConfigEntrypoints") {
					t.Fatalf("Err = %q, want an entrypoint query failure", info.Err)
				}
				return
			}
			if info.Err != "" {
				t.Fatalf("unexpected Err: %q", info.Err)
			}
			if _, ok := info.EntryPoints[ProfileVP9Profile0]; ok {
				t.Error("skipped profile must not be recorded")
			}
			if !info.Encodes(ProfileHEVCMain) {
				t.Error("remaining profile must be recorded")
			}
		})
	}
}

func TestProbeWithContinuesAfterDeviceFailure(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "renderD128") // never created
	node := probeFile(t)
	if err := os.WriteFile(node, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	f := &fakeLibva{
		profiles: []Profile{ProfileHEVCMain},
		entrypoints: map[Profile][]Entrypoint{
			ProfileHEVCMain: {EntrypointEncSlice},
		},
		vendor: "Test Driver",
	}

	got := probeWith(context.Background(), f.handle(), []string{missing, node})

	if len(got) != 2 {
		t.Fatalf("got %d devices, want 2", len(got))
	}
	if got[0].Path != missing || got[0].Err == "" {
		t.Errorf("first device = %+v, want the failed open recorded", got[0])
	}
	if got[1].Path != node || got[1].Err != "" {
		t.Errorf("second device = %+v, want a clean probe", got[1])
	}
}

func TestProbeWithStopsWhenContextCanceled(t *testing.T) {
	node := probeFile(t)
	if err := os.WriteFile(node, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	f := &fakeLibva{
		profiles: []Profile{ProfileHEVCMain},
		entrypoints: map[Profile][]Entrypoint{
			ProfileHEVCMain: {EntrypointEncSlice},
		},
	}
	// Cancel from inside the first probe: the loop must stop before the
	// second device.
	handle := f.handle()
	origInitialize := handle.initialize
	handle.initialize = func(dpy uintptr, major, minor *int32) int32 {
		cancel()
		return origInitialize(dpy, major, minor)
	}

	got := probeWith(ctx, handle, []string{node, probeFile(t)})

	if len(got) != 1 {
		t.Fatalf("got %d devices, want the 1 probed before cancellation", len(got))
	}

	got = probeWith(context.Background(), handle, []string{node, node})
	if len(got) != 2 {
		t.Fatalf("fresh context: got %d devices, want 2", len(got))
	}
}

func TestProbeWithoutLibvaListsAllNodesAsUnknown(t *testing.T) {
	orig := loadLibVA
	loadLibVA = func() (*libvaHandle, error) { return nil, errors.New("libva.so.2: cannot open shared object file") }
	t.Cleanup(func() { loadLibVA = orig })

	nodes := []string{"/dev/dri/renderD128", "/dev/dri/renderD129"}
	got := Probe(context.Background(), nodes)

	if len(got) != len(nodes) {
		t.Fatalf("got %d devices, want %d", len(got), len(nodes))
	}
	for i, d := range got {
		if d.Path != nodes[i] || d.Known() {
			t.Errorf("device %d = %+v, want the node path with unknown capabilities", i, d)
		}
	}
}
