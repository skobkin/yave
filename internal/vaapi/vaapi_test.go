package vaapi

import (
	"reflect"
	"testing"
)

func TestEncodes(t *testing.T) {
	encodeDevice := DeviceInfo{EntryPoints: map[Profile][]Entrypoint{
		ProfileHEVCMain: {EntrypointVLD, EntrypointEncSlice},
	}}
	decodeOnly := DeviceInfo{EntryPoints: map[Profile][]Entrypoint{
		ProfileHEVCMain: {EntrypointVLD},
	}}
	lowPowerOnly := DeviceInfo{EntryPoints: map[Profile][]Entrypoint{
		ProfileH264Main: {EntrypointEncSliceLP},
	}}
	unknown := DeviceInfo{Err: "open: permission denied"}

	if !encodeDevice.Encodes(ProfileHEVCMain) {
		t.Error("device with EncSlice for HEVCMain must encode")
	}
	if !encodeDevice.Encodes(ProfileH264Main, ProfileHEVCMain) {
		t.Error("Encodes must be satisfied by any of the required profiles")
	}
	if decodeOnly.Encodes(ProfileHEVCMain) {
		t.Error("VLD-only device must not count as encoding")
	}
	if !decodeOnly.Supports(ProfileHEVCMain) {
		t.Error("VLD-only device still supports the profile for decode")
	}
	if !lowPowerOnly.SupportsEncoding(ProfileH264Main) {
		t.Error("EncSliceLP must count as an encode entrypoint")
	}
	if unknown.Encodes(ProfileHEVCMain) {
		t.Error("device with unknown capabilities must not claim encoding")
	}
	if (DeviceInfo{}).Encodes(ProfileHEVCMain) {
		t.Error("empty DeviceInfo must not claim encoding")
	}
}

func TestSelectDevice(t *testing.T) {
	// The owner's dual-GPU fixture from issue #23: renderD128 is an
	// NVIDIA GPU whose VA-API driver decodes only, renderD129 is an AMD
	// iGPU that encodes H.264 and HEVC.
	nvidia := DeviceInfo{
		Path:   "/dev/dri/renderD128",
		Driver: "VA-API NVDEC driver [direct backend]",
		EntryPoints: map[Profile][]Entrypoint{
			ProfileHEVCMain: {EntrypointVLD},
			ProfileH264Main: {EntrypointVLD},
		},
	}
	amd := DeviceInfo{
		Path:   "/dev/dri/renderD129",
		Driver: "Mesa Gallium driver for AMD Radeon Graphics (radeonsi)",
		EntryPoints: map[Profile][]Entrypoint{
			ProfileH264ConstrainedBaseline: {EntrypointVLD, EntrypointEncSlice},
			ProfileH264Main:                {EntrypointVLD, EntrypointEncSlice},
			ProfileH264High:                {EntrypointVLD, EntrypointEncSlice},
			ProfileHEVCMain:                {EntrypointVLD, EntrypointEncSlice},
		},
	}
	nodes := []string{"/dev/dri/renderD128", "/dev/dri/renderD129"}

	tests := []struct {
		name     string
		nodes    []string
		devices  []DeviceInfo
		required []Profile
		want     DeviceChoice
	}{
		{
			name:     "compatible device wins over decode-only first node",
			nodes:    nodes,
			devices:  []DeviceInfo{nvidia, amd},
			required: []Profile{ProfileHEVCMain},
			want:     DeviceChoice{Path: amd.Path, Driver: amd.Driver, Status: DeviceSelected},
		},
		{
			name:     "selection order is deterministic",
			nodes:    nodes,
			devices:  []DeviceInfo{nvidia, amd},
			required: []Profile{ProfileH264Main},
			want:     DeviceChoice{Path: amd.Path, Driver: amd.Driver, Status: DeviceSelected},
		},
		{
			name:     "reversed node order reverses the pick",
			nodes:    []string{"/dev/dri/renderD129", "/dev/dri/renderD128"},
			devices:  []DeviceInfo{amd, nvidia},
			required: []Profile{ProfileH264High},
			want:     DeviceChoice{Path: amd.Path, Driver: amd.Driver, Status: DeviceSelected},
		},
		{
			name:     "none compatible when every known device lacks encoding",
			nodes:    nodes,
			devices:  []DeviceInfo{nvidia},
			required: []Profile{ProfileVP9Profile0},
			want:     DeviceChoice{Status: DeviceNoneCompatible},
		},
		{
			name:     "all probes failed falls back to first node",
			nodes:    nodes,
			devices:  []DeviceInfo{{Path: nvidia.Path, Err: "open: denied"}, {Path: amd.Path, Err: "open: denied"}},
			required: []Profile{ProfileHEVCMain},
			want:     DeviceChoice{Path: nodes[0], Status: DeviceFallback},
		},
		{
			name:     "no probe data at all falls back to first node",
			nodes:    nodes,
			devices:  nil,
			required: []Profile{ProfileHEVCMain},
			want:     DeviceChoice{Path: nodes[0], Status: DeviceFallback},
		},
		{
			name:     "no nodes reports DeviceNoNodes",
			nodes:    nil,
			devices:  []DeviceInfo{amd},
			required: []Profile{ProfileHEVCMain},
			want:     DeviceChoice{Status: DeviceNoNodes},
		},
		{
			name:     "known incompatible mixed with unknown is none-compatible",
			nodes:    nodes,
			devices:  []DeviceInfo{nvidia, {Path: amd.Path, Err: "open: denied"}},
			required: []Profile{ProfileHEVCMain},
			want:     DeviceChoice{Status: DeviceNoneCompatible},
		},
		{
			name:     "h264 auto requirement matches any curated h264 profile",
			nodes:    nodes,
			devices:  []DeviceInfo{nvidia, amd},
			required: []Profile{ProfileH264ConstrainedBaseline, ProfileH264Main, ProfileH264High},
			want:     DeviceChoice{Path: amd.Path, Driver: amd.Driver, Status: DeviceSelected},
		},
		{
			name:  "vp9 and av1 requirements resolve",
			nodes: []string{"/dev/dri/renderD130"},
			devices: []DeviceInfo{{Path: "/dev/dri/renderD130", EntryPoints: map[Profile][]Entrypoint{
				ProfileAV1Profile0: {EntrypointEncSlice},
			}}},
			required: []Profile{ProfileAV1Profile0},
			want:     DeviceChoice{Path: "/dev/dri/renderD130", Status: DeviceSelected},
		},
		{
			name:     "device entry for a foreign path is ignored",
			nodes:    nodes,
			devices:  []DeviceInfo{{Path: "/dev/dri/renderD199", EntryPoints: map[Profile][]Entrypoint{ProfileHEVCMain: {EntrypointEncSlice}}}},
			required: []Profile{ProfileHEVCMain},
			want:     DeviceChoice{Path: nodes[0], Status: DeviceFallback},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SelectDevice(tt.nodes, tt.devices, tt.required)
			if got != tt.want {
				t.Errorf("SelectDevice() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestDescribeDevice(t *testing.T) {
	required := []Profile{ProfileHEVCMain}
	tests := []struct {
		name    string
		device  DeviceInfo
		require []Profile
		want    string
	}{
		{
			name: "encodes",
			device: DeviceInfo{
				Path: "/dev/dri/renderD129", Driver: "radeonsi",
				EntryPoints: map[Profile][]Entrypoint{ProfileHEVCMain: {EntrypointVLD, EntrypointEncSlice}},
			},
			require: required,
			want:    "/dev/dri/renderD129 (radeonsi) — encodes",
		},
		{
			name: "decode only",
			device: DeviceInfo{
				Path: "/dev/dri/renderD128", Driver: "NVDEC",
				EntryPoints: map[Profile][]Entrypoint{ProfileHEVCMain: {EntrypointVLD}},
			},
			require: required,
			want:    "/dev/dri/renderD128 (NVDEC) — decode only",
		},
		{
			name: "encodes other profiles is not reported as lacking an entrypoint",
			device: DeviceInfo{
				Path: "/dev/dri/renderD129", Driver: "radeonsi",
				EntryPoints: map[Profile][]Entrypoint{
					ProfileH264Main: {EntrypointVLD, EntrypointEncSlice},
					ProfileHEVCMain: {EntrypointVLD},
				},
			},
			require: []Profile{ProfileAV1Profile0},
			want:    "/dev/dri/renderD129 (radeonsi) — encodes other profiles",
		},
		{
			name: "encodes other profiles wins over decode-only for the required list",
			device: DeviceInfo{
				Path: "/dev/dri/renderD128", Driver: "NVDEC",
				EntryPoints: map[Profile][]Entrypoint{
					ProfileHEVCMain: {EntrypointVLD},
					ProfileH264Main: {EntrypointVLD, EntrypointEncSlice},
				},
			},
			require: []Profile{ProfileHEVCMain},
			want:    "/dev/dri/renderD128 (NVDEC) — encodes other profiles",
		},
		{
			name: "no encoding entrypoint",
			device: DeviceInfo{
				Path: "/dev/dri/renderD130", Driver: "",
				EntryPoints: map[Profile][]Entrypoint{ProfileH264Main: {EntrypointVLD}},
			},
			require: required,
			want:    "/dev/dri/renderD130 — no encoding entrypoint",
		},
		{
			name:    "capabilities unknown",
			device:  DeviceInfo{Path: "/dev/dri/renderD131", Err: "open: permission denied"},
			require: required,
			want:    "/dev/dri/renderD131 — capabilities unknown (open: permission denied)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := DescribeDevice(tt.device, tt.require); got != tt.want {
				t.Errorf("DescribeDevice() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestEncodeEntrypointsMatchFFmpeg(t *testing.T) {
	want := []Entrypoint{EntrypointEncSlice, EntrypointEncPicture, EntrypointEncSliceLP}
	if got := EncodeEntrypoints(); !reflect.DeepEqual(got, want) {
		t.Errorf("EncodeEntrypoints() = %v, want %v", got, want)
	}
}
