package ffmpeg

import (
	"context"
	"os"
	"runtime"
	"slices"
	"testing"

	"git.skobk.in/skobkin/yave/internal/model"
	"git.skobk.in/skobkin/yave/internal/vaapi"
)

func notExist(path string) error {
	return &os.PathError{Op: "stat", Path: path, Err: os.ErrNotExist}
}

func permDenied(path string) error {
	return &os.PathError{Op: "stat", Path: path, Err: os.ErrPermission}
}

// fakeFS builds an fsProbe seam. stats maps a path to nil (present) or
// an error; paths missing from the map behave as not-exist. globs maps
// a pattern to its matches (missing pattern: no matches); globErrs
// forces an error for a pattern.
func fakeFS(stats map[string]error, globs map[string][]string, globErrs map[string]error) fsProbe {
	return fsProbe{
		stat: func(name string) error {
			if err, ok := stats[name]; ok {
				return err
			}
			return notExist(name)
		},
		glob: func(pattern string) ([]string, error) {
			if err, ok := globErrs[pattern]; ok {
				return nil, err
			}
			if res, ok := globs[pattern]; ok {
				return res, nil
			}
			return nil, nil
		},
	}
}

func TestProbeLinuxVAAPIViaRenderD128(t *testing.T) {
	fs := fakeFS(map[string]error{driRenderNode: nil}, nil, nil)
	got := probeLinuxHWBackends(fs)
	if !got.vaapi || !got.qsv {
		t.Errorf("vaapi=%v qsv=%v, want both true", got.vaapi, got.qsv)
	}
	if got.nvenc {
		t.Errorf("nvenc = true, want false")
	}
}

func TestProbeLinuxVAAPIViaFallbackRenderNode(t *testing.T) {
	fs := fakeFS(nil, map[string][]string{driRenderPattern: {"/dev/dri/renderD129"}}, nil)
	got := probeLinuxHWBackends(fs)
	if !got.vaapi || !got.qsv {
		t.Errorf("vaapi=%v qsv=%v, want both true via renderD129 fallback", got.vaapi, got.qsv)
	}
}

func TestProbeLinuxBackendsMissing(t *testing.T) {
	got := probeLinuxHWBackends(fakeFS(nil, nil, nil))
	if got.vaapi || got.qsv || got.nvenc || got.nvencGPUs != 0 {
		t.Errorf("got %+v, want all backends unavailable", got)
	}
}

func TestProbeLinuxFailsOpenOnStatPermission(t *testing.T) {
	fs := fakeFS(map[string]error{driRenderNode: permDenied(driRenderNode)}, nil, nil)
	got := probeLinuxHWBackends(fs)
	if !got.vaapi || !got.qsv {
		t.Errorf("vaapi=%v qsv=%v, want both true (permission denied must fail open)", got.vaapi, got.qsv)
	}
}

func TestProbeLinuxFailsOpenOnGlobError(t *testing.T) {
	fs := fakeFS(nil, nil, map[string]error{driRenderPattern: os.ErrPermission})
	got := probeLinuxHWBackends(fs)
	if !got.vaapi || !got.qsv {
		t.Errorf("vaapi=%v qsv=%v, want both true (glob error must fail open)", got.vaapi, got.qsv)
	}
}

func TestProbeLinuxNVENCAbsent(t *testing.T) {
	got := probeLinuxHWBackends(fakeFS(nil, nil, nil))
	if got.nvenc || got.nvencGPUs != 0 {
		t.Errorf("nvenc=%v gpus=%d, want false/0", got.nvenc, got.nvencGPUs)
	}
}

func TestProbeLinuxNVENCSingleGPU(t *testing.T) {
	fs := fakeFS(
		map[string]error{nvidiaControlNode: nil},
		map[string][]string{nvidiaPattern: {"/dev/nvidia0"}},
		nil,
	)
	got := probeLinuxHWBackends(fs)
	if !got.nvenc || got.nvencGPUs != 1 {
		t.Errorf("nvenc=%v gpus=%d, want true/1", got.nvenc, got.nvencGPUs)
	}
}

func TestProbeLinuxNVENCMultiGPU(t *testing.T) {
	fs := fakeFS(
		map[string]error{nvidiaControlNode: nil},
		map[string][]string{nvidiaPattern: {"/dev/nvidia0", "/dev/nvidia1", "/dev/nvidia2"}},
		nil,
	)
	got := probeLinuxHWBackends(fs)
	if !got.nvenc || got.nvencGPUs != 3 {
		t.Errorf("nvenc=%v gpus=%d, want true/3", got.nvenc, got.nvencGPUs)
	}
}

func TestProbeLinuxNVENCGlobEmptyFailsOpen(t *testing.T) {
	// Device node exists but the glob comes back empty (or errors):
	// keep NVENC visible and conservatively report one GPU.
	fs := fakeFS(map[string]error{nvidiaControlNode: nil}, nil, nil)
	got := probeLinuxHWBackends(fs)
	if !got.nvenc || got.nvencGPUs != 1 {
		t.Errorf("nvenc=%v gpus=%d, want true/1 (fail open)", got.nvenc, got.nvencGPUs)
	}
}

func TestProbeLinuxNVENCPermDeniedFailsOpen(t *testing.T) {
	fs := fakeFS(map[string]error{nvidiaControlNode: permDenied(nvidiaControlNode)}, nil, nil)
	got := probeLinuxHWBackends(fs)
	if !got.nvenc {
		t.Errorf("nvenc = false, want true (permission denied must fail open)")
	}
}

func TestProbeLinuxNVENViaNonzeroGPUNode(t *testing.T) {
	// Containers may expose only nonzero GPU nodes (no /dev/nvidia0);
	// enumeration alone must keep NVENC available.
	fs := fakeFS(nil, map[string][]string{nvidiaPattern: {"/dev/nvidia1"}}, nil)
	got := probeLinuxHWBackends(fs)
	if !got.nvenc || got.nvencGPUs != 1 {
		t.Errorf("nvenc=%v gpus=%d, want true/1", got.nvenc, got.nvencGPUs)
	}
}

// TestRenderNodes pins the DRM render-node list alongside the
// availability booleans: multi-node hosts get a sorted list, the
// canonical node is offered alone when only it stats, and the
// fail-open branches (glob error, unreadable /dev) still offer the
// canonical node so device dropdowns stay populated.
func TestRenderNodes(t *testing.T) {
	cases := []struct {
		name  string
		fs    fsProbe
		want  []string
		avail bool
	}{
		{
			name:  "multiple nodes sorted",
			fs:    fakeFS(nil, map[string][]string{driRenderPattern: {"/dev/dri/renderD129", "/dev/dri/renderD128"}}, nil),
			want:  []string{"/dev/dri/renderD128", "/dev/dri/renderD129"},
			avail: true,
		},
		{
			name:  "only canonical node stats",
			fs:    fakeFS(map[string]error{driRenderNode: nil}, nil, nil),
			want:  []string{driRenderNode},
			avail: true,
		},
		{
			name:  "glob error fails open with canonical node",
			fs:    fakeFS(nil, nil, map[string]error{driRenderPattern: os.ErrPermission}),
			want:  []string{driRenderNode},
			avail: true,
		},
		{
			name:  "stat permission fails open with canonical node",
			fs:    fakeFS(map[string]error{driRenderNode: permDenied(driRenderNode)}, nil, nil),
			want:  []string{driRenderNode},
			avail: true,
		},
		{
			name:  "nothing found",
			fs:    fakeFS(nil, nil, nil),
			want:  nil,
			avail: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			avail, nodes := renderNodes(tc.fs)
			if avail != tc.avail {
				t.Errorf("available = %v, want %v", avail, tc.avail)
			}
			if len(nodes) != len(tc.want) {
				t.Fatalf("renderNodes = %v, want %v", nodes, tc.want)
			}
			for i := range nodes {
				if nodes[i] != tc.want[i] {
					t.Fatalf("renderNodes = %v, want %v", nodes, tc.want)
				}
			}
		})
	}
}

// hwFilterFixture builds a ParseEncoders result covering every backend
// family plus software and audio encoders.
func hwFilterFixture(t *testing.T) map[string]model.Encoder {
	t.Helper()
	output := `Encoders:
 V..... h264_vaapi           H.264 (VAAPI)
 V..... hevc_vaapi           HEVC (VAAPI)
 V..... h264_nvenc           NVIDIA NVENC H.264
 V..... hevc_nvenc           NVIDIA NVENC hevc
 V..... h264_qsv             H.264 QSV
 V..... hevc_qsv             HEVC QSV
 V..... libx264              libx264 H.264
 V..... libx265              libx265 HEVC
 A..... aac                  AAC
`
	return ParseEncoders(output)
}

func TestFilterUnavailableHWEncoders(t *testing.T) {
	full := hwFilterFixture(t)

	cases := []struct {
		name string
		hw   hwAvailability
		keep []string
		miss []string
	}{
		{"all available", hwAvailability{vaapi: true, qsv: true, nvenc: true, nvencGPUs: 1},
			[]string{"h264_vaapi", "hevc_vaapi", "h264_nvenc", "hevc_nvenc", "h264_qsv", "hevc_qsv", "libx264", "libx265", "aac", "copy"},
			nil},
		{"none available", hwAvailability{},
			[]string{"libx264", "libx265", "aac", "copy"},
			[]string{"h264_vaapi", "hevc_vaapi", "h264_nvenc", "hevc_nvenc", "h264_qsv", "hevc_qsv"}},
		{"vaapi only", hwAvailability{vaapi: true},
			[]string{"h264_vaapi", "hevc_vaapi", "libx264", "libx265", "aac", "copy"},
			[]string{"h264_nvenc", "hevc_nvenc", "h264_qsv", "hevc_qsv"}},
		{"nvenc only", hwAvailability{nvenc: true, nvencGPUs: 2},
			[]string{"h264_nvenc", "hevc_nvenc", "libx264", "libx265", "aac", "copy"},
			[]string{"h264_vaapi", "hevc_vaapi", "h264_qsv", "hevc_qsv"}},
		{"qsv only", hwAvailability{qsv: true},
			[]string{"h264_qsv", "hevc_qsv", "libx264", "libx265", "aac", "copy"},
			[]string{"h264_vaapi", "hevc_vaapi", "h264_nvenc", "hevc_nvenc"}},
		{"vaapi+nvenc", hwAvailability{vaapi: true, nvenc: true, nvencGPUs: 1},
			[]string{"h264_vaapi", "h264_nvenc", "libx264", "aac", "copy"},
			[]string{"h264_qsv", "hevc_qsv"}},
		{"vaapi+qsv", hwAvailability{vaapi: true, qsv: true},
			[]string{"h264_vaapi", "h264_qsv", "libx265", "aac", "copy"},
			[]string{"h264_nvenc", "hevc_nvenc"}},
		{"qsv+nvenc", hwAvailability{qsv: true, nvenc: true, nvencGPUs: 1},
			[]string{"h264_qsv", "h264_nvenc", "libx264", "aac", "copy"},
			[]string{"h264_vaapi", "hevc_vaapi"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Fresh copy per case: the filter mutates in place.
			encoders := make(map[string]model.Encoder, len(full))
			for name, enc := range full {
				encoders[name] = enc
			}

			got := filterUnavailableHWEncoders(encoders, tc.hw)

			for _, name := range tc.keep {
				if _, ok := got[name]; !ok {
					t.Errorf("expected %q to survive filtering", name)
				}
			}
			for _, name := range tc.miss {
				if _, ok := got[name]; ok {
					t.Errorf("expected %q to be filtered out", name)
				}
			}
		})
	}
}

// stubDeviceProbe stands in for the VA-API device prober: no devices,
// no calls recorded.
func stubDeviceProbe(context.Context, []string) []vaapi.DeviceInfo { return nil }

func TestDiscoverFiltersHWWithoutDevices(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("device probe is Linux-specific and the test helper is a POSIX shell script")
	}
	dir := t.TempDir()
	ffmpegPath := writeExecutable(t, dir, "ffmpeg", `#!/bin/sh
case "$2" in
  -version) printf 'ffmpeg version test-1\n' ;;
  -encoders) printf 'Encoders:\n V..... hevc_nvenc test\n V..... h264_vaapi test\n V..... libx265 test\n A..... aac test\n' ;;
  -hwaccels) printf 'Hardware acceleration methods:\nvaapi\ncuda\n' ;;
  *) exit 2 ;;
esac
`)
	ffprobePath := writeExecutable(t, dir, "ffprobe", `#!/bin/sh
printf 'ffprobe version test-2\n'
`)

	got, err := discover(context.Background(), model.FFmpegConfig{
		FFmpegPath:  ffmpegPath,
		FFprobePath: ffprobePath,
	}, fakeFS(nil, nil, nil), stubDeviceProbe)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"hevc_nvenc", "h264_vaapi"} {
		if _, ok := got.Encoders[name]; ok {
			t.Errorf("expected %q to be filtered out without device nodes", name)
		}
	}
	for _, name := range []string{"libx265", "aac", "copy"} {
		if _, ok := got.Encoders[name]; !ok {
			t.Errorf("expected %q to survive filtering", name)
		}
	}
	if got.HWVAAPIAvailable || got.HWQSVAvailable || got.HWNVENCAvailable || got.NVENCGPUCount != 0 {
		t.Errorf("probe flags = %+v gpus=%d, want all false/0", model.Capabilities{
			HWVAAPIAvailable: got.HWVAAPIAvailable,
			HWQSVAvailable:   got.HWQSVAvailable,
			HWNVENCAvailable: got.HWNVENCAvailable,
		}, got.NVENCGPUCount)
	}
	if got.RenderNodes != nil {
		t.Errorf("RenderNodes = %v, want nil without device nodes", got.RenderNodes)
	}
}

func TestDiscoverKeepsHWWithDevices(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("device probe is Linux-specific and the test helper is a POSIX shell script")
	}
	dir := t.TempDir()
	ffmpegPath := writeExecutable(t, dir, "ffmpeg", `#!/bin/sh
case "$2" in
  -version) printf 'ffmpeg version test-1\n' ;;
  -encoders) printf 'Encoders:\n V..... hevc_nvenc test\n V..... h264_vaapi test\n V..... libx265 test\n A..... aac test\n' ;;
  -hwaccels) printf 'Hardware acceleration methods:\nvaapi\n' ;;
  *) exit 2 ;;
esac
`)
	ffprobePath := writeExecutable(t, dir, "ffprobe", `#!/bin/sh
printf 'ffprobe version test-2\n'
`)

	fs := fakeFS(
		map[string]error{driRenderNode: nil, nvidiaControlNode: nil},
		map[string][]string{nvidiaPattern: {"/dev/nvidia0", "/dev/nvidia1"}},
		nil,
	)
	got, err := discover(context.Background(), model.FFmpegConfig{
		FFmpegPath:  ffmpegPath,
		FFprobePath: ffprobePath,
	}, fs, stubDeviceProbe)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"hevc_nvenc", "h264_vaapi", "libx265", "aac", "copy"} {
		if _, ok := got.Encoders[name]; !ok {
			t.Errorf("expected %q to survive filtering", name)
		}
	}
	if !got.HWVAAPIAvailable || !got.HWQSVAvailable || !got.HWNVENCAvailable {
		t.Errorf("probe flags = vaapi:%v qsv:%v nvenc:%v, want all true", got.HWVAAPIAvailable, got.HWQSVAvailable, got.HWNVENCAvailable)
	}
	if len(got.RenderNodes) != 1 || got.RenderNodes[0] != driRenderNode {
		t.Errorf("RenderNodes = %v, want [%s]", got.RenderNodes, driRenderNode)
	}
	if got.NVENCGPUCount != 2 {
		t.Errorf("NVENCGPUCount = %d, want 2", got.NVENCGPUCount)
	}
}

func TestDiscoverRecordsVAAPIDevices(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("device probe is Linux-specific and the test helper is a POSIX shell script")
	}
	dir := t.TempDir()
	ffmpegPath := writeExecutable(t, dir, "ffmpeg", `#!/bin/sh
case "$2" in
  -version) printf 'ffmpeg version test-1\n' ;;
  -encoders) printf 'Encoders:\n V..... h264_vaapi test\n' ;;
  -hwaccels) printf 'Hardware acceleration methods:\nvaapi\n' ;;
  *) exit 2 ;;
esac
`)
	ffprobePath := writeExecutable(t, dir, "ffprobe", `#!/bin/sh
printf 'ffprobe version test-2\n'
`)
	cfg := model.FFmpegConfig{FFmpegPath: ffmpegPath, FFprobePath: ffprobePath}
	nodes := []string{"/dev/dri/renderD128", "/dev/dri/renderD129"}
	fs := fakeFS(nil, map[string][]string{driRenderPattern: nodes}, nil)

	t.Run("probe results are carried verbatim in node order", func(t *testing.T) {
		want := []vaapi.DeviceInfo{
			{
				Path:   "/dev/dri/renderD128",
				Driver: "VA-API NVDEC driver [direct backend]",
				EntryPoints: map[vaapi.Profile][]vaapi.Entrypoint{
					vaapi.ProfileHEVCMain: {vaapi.EntrypointVLD},
				},
			},
			{
				Path:   "/dev/dri/renderD129",
				Driver: "Mesa Gallium driver (radeonsi)",
				EntryPoints: map[vaapi.Profile][]vaapi.Entrypoint{
					vaapi.ProfileHEVCMain: {vaapi.EntrypointVLD, vaapi.EntrypointEncSlice},
				},
			},
		}
		got, err := discover(context.Background(), cfg, fs, func(_ context.Context, got []string) []vaapi.DeviceInfo {
			if !slices.Equal(got, nodes) {
				t.Errorf("prober received nodes %v, want %v", got, nodes)
			}
			return want
		})
		if err != nil {
			t.Fatal(err)
		}
		if !slices.EqualFunc(got.VAAPIDevices, want, func(a, b vaapi.DeviceInfo) bool {
			return a.Path == b.Path && a.Driver == b.Driver && a.Err == b.Err
		}) {
			t.Errorf("VAAPIDevices = %+v, want %+v", got.VAAPIDevices, want)
		}
	})

	t.Run("a prober without results keeps discovery healthy", func(t *testing.T) {
		got, err := discover(context.Background(), cfg, fs, stubDeviceProbe)
		if err != nil {
			t.Fatal(err)
		}
		if got.VAAPIDevices != nil {
			t.Errorf("VAAPIDevices = %+v, want nil", got.VAAPIDevices)
		}
		if !got.HWVAAPIAvailable || len(got.RenderNodes) != 2 {
			t.Errorf("nodes=%v vaapi=%v, want both nodes and vaapi available", got.RenderNodes, got.HWVAAPIAvailable)
		}
	})

	t.Run("prober is not called without render nodes", func(t *testing.T) {
		called := false
		_, err := discover(context.Background(), cfg, fakeFS(nil, nil, nil),
			func(context.Context, []string) []vaapi.DeviceInfo {
				called = true
				return nil
			})
		if err != nil {
			t.Fatal(err)
		}
		if called {
			t.Error("prober must not run when no render nodes were detected")
		}
	})
}
