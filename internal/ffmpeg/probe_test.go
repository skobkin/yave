package ffmpeg

import (
	"context"
	"os"
	"runtime"
	"testing"

	"git.skobk.in/skobkin/yave/internal/model"
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

func TestDiscoverFiltersHWWithoutDevices(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test helper is a POSIX shell script")
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
	}, fakeFS(nil, nil, nil))
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
}

func TestDiscoverKeepsHWWithDevices(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test helper is a POSIX shell script")
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
	}, fs)
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
	if got.NVENCGPUCount != 2 {
		t.Errorf("NVENCGPUCount = %d, want 2", got.NVENCGPUCount)
	}
}
