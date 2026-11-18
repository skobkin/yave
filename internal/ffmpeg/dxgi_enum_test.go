package ffmpeg

import (
	"context"
	"errors"
	"os"
	"runtime"
	"testing"

	"git.skobk.in/skobkin/yave/internal/model"
	"git.skobk.in/skobkin/yave/internal/vaapi"
)

// TestCollectAdaptersCompleteness pins the enumeration contract: the
// NOT_FOUND sentinel is the normal end (complete, list published —
// including the empty list), while any other error makes the probe
// incomplete so no partial inventory is ever published.
func TestCollectAdaptersCompleteness(t *testing.T) {
	t.Run("two adapters then NOT_FOUND", func(t *testing.T) {
		calls := 0
		next := func() (model.GPUAdapter, error) {
			calls++
			switch calls {
			case 1:
				return model.GPUAdapter{Index: 0, Name: "NVIDIA", VendorID: VendorNVIDIA}, nil
			case 2:
				return model.GPUAdapter{Index: 1, Name: "AMD", VendorID: 0x1002}, nil
			default:
				return model.GPUAdapter{}, errDXGINotFound
			}
		}
		got, complete := collectAdapters(next)
		if !complete {
			t.Fatalf("exhausted enumeration must be complete")
		}
		if len(got) != 2 || got[0].Name != "NVIDIA" || got[1].VendorID != 0x1002 {
			t.Errorf("adapters = %+v, want the two accumulated entries", got)
		}
	})

	t.Run("empty enumeration is complete", func(t *testing.T) {
		got, complete := collectAdapters(func() (model.GPUAdapter, error) {
			return model.GPUAdapter{}, errDXGINotFound
		})
		if !complete {
			t.Fatalf("zero-adapter enumeration must be complete")
		}
		if got != nil {
			t.Errorf("adapters = %+v, want empty", got)
		}
	})

	t.Run("mid-enumeration error is incomplete", func(t *testing.T) {
		calls := 0
		next := func() (model.GPUAdapter, error) {
			calls++
			if calls == 1 {
				return model.GPUAdapter{Index: 0}, nil
			}
			return model.GPUAdapter{}, errors.New("HRESULT 0x80070005")
		}
		got, complete := collectAdapters(next)
		if complete {
			t.Fatalf("aborted enumeration must not be reported complete")
		}
		if got != nil {
			t.Errorf("adapters = %+v, want nil (no partial inventory)", got)
		}
	})
}

// TestCountVendorAdapters is trivial but pins the NVIDIA counting the
// Windows NVENCGPUCount fallback relies on.
func TestCountVendorAdapters(t *testing.T) {
	list := []model.GPUAdapter{
		{Index: 0, VendorID: VendorNVIDIA},
		{Index: 1, VendorID: VendorIntel},
		{Index: 2, VendorID: VendorNVIDIA},
	}
	if got := countVendorAdapters(list, VendorNVIDIA); got != 2 {
		t.Errorf("NVIDIA count = %d, want 2", got)
	}
	if got := countVendorAdapters(list, VendorIntel); got != 1 {
		t.Errorf("Intel count = %d, want 1", got)
	}
	if got := countVendorAdapters(nil, VendorNVIDIA); got != 0 {
		t.Errorf("nil count = %d, want 0", got)
	}
}

// TestDiscoverGPUAdaptersSeam pins the discover() integration: a
// complete probe populates GPUAdapters verbatim and can backfill
// NVENCGPUCount when the node probe found nothing; an incomplete
// probe publishes nothing and never overrides the node-derived count.
func TestDiscoverGPUAdaptersSeam(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test helpers are POSIX shell scripts")
	}
	dir := t.TempDir()
	ffmpegPath := writeExecutable(t, dir, "ffmpeg", `#!/bin/sh
case "$2" in
  -version) printf 'ffmpeg version test-1\n' ;;
  -encoders) printf 'Encoders:\n' ;;
  -hwaccels) printf 'Hardware acceleration methods:\n' ;;
  *) exit 2 ;;
esac
`)
	ffprobePath := writeExecutable(t, dir, "ffprobe", `#!/bin/sh
printf 'ffprobe version test-2\n'
`)
	config := model.FFmpegConfig{FFmpegPath: ffmpegPath, FFprobePath: ffprobePath}
	emptyFS := fsProbe{
		stat: func(string) error { return os.ErrNotExist },
		glob: func(string) ([]string, error) { return nil, nil },
	}
	noProbe := func(context.Context, []string) []vaapi.DeviceInfo { return nil }

	fake := []model.GPUAdapter{
		{Index: 0, Name: "NVIDIA GeForce GTX 1660 Ti", VendorID: VendorNVIDIA},
		{Index: 1, Name: "AMD Radeon", VendorID: 0x1002},
	}
	original := osGPUAdapterProbe
	defer func() { osGPUAdapterProbe = original }()

	osGPUAdapterProbe = func() ([]model.GPUAdapter, bool) { return fake, true }
	caps, err := discover(context.Background(), config, emptyFS, noProbe)
	if err != nil {
		t.Fatalf("discover failed: %v", err)
	}
	if len(caps.GPUAdapters) != 2 || caps.GPUAdapters[0].Name != fake[0].Name {
		t.Errorf("GPUAdapters = %+v, want the fake inventory verbatim", caps.GPUAdapters)
	}
	if caps.NVENCGPUCount != 1 {
		t.Errorf("NVENCGPUCount = %d, want 1 (NVIDIA adapter fallback)", caps.NVENCGPUCount)
	}

	osGPUAdapterProbe = func() ([]model.GPUAdapter, bool) { return fake, false }
	caps, err = discover(context.Background(), config, emptyFS, noProbe)
	if err != nil {
		t.Fatalf("discover failed: %v", err)
	}
	if caps.GPUAdapters != nil {
		t.Errorf("incomplete probe published GPUAdapters %+v, want nil", caps.GPUAdapters)
	}
	if caps.NVENCGPUCount != 0 {
		t.Errorf("NVENCGPUCount = %d, want 0 (incomplete probe must not backfill)", caps.NVENCGPUCount)
	}
}
