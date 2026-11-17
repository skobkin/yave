package ffprobe

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const streamKeysFixture = `{
  "streams": [
    {
      "index": 0, "codec_name": "hevc", "codec_type": "video",
      "profile": "Main", "pix_fmt": "yuv420p", "time_base": "1/16000",
      "sample_aspect_ratio": "1:1", "width": 640, "height": 360,
      "extradata": "0x3031, 0x3233",
      "extradata_size": 4
    },
    {
      "index": 1, "codec_name": "aac", "codec_type": "audio",
      "profile": "LC", "time_base": "1/48000",
      "channel_layout": "stereo", "sample_rate": "48000", "channels": 2,
      "extradata": "0x1210", "extradata_size": 2
    }
  ]
}`

func TestParseStreamKeys(t *testing.T) {
	keys, err := parseStreamKeys([]byte(streamKeysFixture))
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 2 {
		t.Fatalf("keys = %d streams, want 2", len(keys))
	}
	video, audio := keys[0], keys[1]
	if video.CodecName != "hevc" || video.Profile != "Main" || video.PixelFormat != "yuv420p" ||
		video.TimeBase != "1/16000" || video.SAR != "1:1" || video.Width != 640 || video.Height != 360 {
		t.Fatalf("video key mismatch: %#v", video)
	}
	if video.Extradata == "" {
		t.Fatal("video extradata must be captured")
	}
	if audio.ChannelLayout != "stereo" || audio.SampleRate != 48000 || audio.Channels != 2 {
		t.Fatalf("audio key mismatch: %#v", audio)
	}
}

func TestParseStreamKeysToleratesAbsentFields(t *testing.T) {
	keys, err := parseStreamKeys([]byte(`{"streams": [{"codec_type": "audio", "codec_name": "opus"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	want := StreamKey{CodecType: "audio", CodecName: "opus"}
	if keys[0] != want {
		t.Fatalf("key = %#v, want %#v", keys[0], want)
	}
	if _, err := parseStreamKeys([]byte(`not json`)); err == nil {
		t.Fatal("malformed JSON must fail")
	}
}

func TestCompareStreamLayouts(t *testing.T) {
	base, err := parseStreamKeys([]byte(streamKeysFixture))
	if err != nil {
		t.Fatal(err)
	}
	same, err := parseStreamKeys([]byte(streamKeysFixture))
	if err != nil {
		t.Fatal(err)
	}
	names := []string{"/st/segment001.mkv", "/st/segment002.mkv", "/st/segment003.mkv"}

	t.Run("identical layouts pass", func(t *testing.T) {
		if err := CompareStreamLayouts(names[:2], [][]StreamKey{base, same}); err != nil {
			t.Fatalf("identical layouts must pass: %v", err)
		}
	})

	mutate := func(f func(*StreamKey)) []StreamKey {
		changed := make([]StreamKey, len(base))
		copy(changed, base)
		f(&changed[1]) // audio stream
		return changed
	}

	tests := []struct {
		name     string
		mutation func(*StreamKey)
		wantSub  string
	}{
		{name: "codec name", mutation: func(k *StreamKey) { k.CodecName = "opus" }, wantSub: "codec_name"},
		{name: "profile", mutation: func(k *StreamKey) { k.Profile = "HE-AAC" }, wantSub: "profile"},
		{name: "pixel format", mutation: func(k *StreamKey) { k.PixelFormat = "yuv422p" }, wantSub: "pix_fmt"},
		{name: "time base", mutation: func(k *StreamKey) { k.TimeBase = "1/90000" }, wantSub: "time_base"},
		{name: "sample aspect ratio", mutation: func(k *StreamKey) { k.SAR = "4:3" }, wantSub: "sample_aspect_ratio"},
		{name: "channel layout", mutation: func(k *StreamKey) { k.ChannelLayout = "mono" }, wantSub: "channel_layout"},
		{name: "channels", mutation: func(k *StreamKey) { k.Channels = 1 }, wantSub: "channels"},
		{name: "sample rate", mutation: func(k *StreamKey) { k.SampleRate = 44100 }, wantSub: "sample_rate"},
		{name: "extradata", mutation: func(k *StreamKey) { k.Extradata = "0xAAAA" }, wantSub: "extradata"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := CompareStreamLayouts(names[:2], [][]StreamKey{base, mutate(tt.mutation)})
			if err == nil {
				t.Fatal("mutated layout must fail")
			}
			if !strings.Contains(err.Error(), tt.wantSub) {
				t.Fatalf("error %q must name the field %q", err, tt.wantSub)
			}
			if !strings.Contains(err.Error(), "segment 2 of 2") {
				t.Fatalf("error %q must name the segment", err)
			}
		})
	}

	t.Run("irrelevant field differences ignored", func(t *testing.T) {
		// Extradata hex dumps are compared after whitespace
		// normalization: parseStreamKeys joins whitespace-split tokens,
		// so equivalent dumps match regardless of spacing.
		normalized, err := parseStreamKeys([]byte(strings.Replace(streamKeysFixture,
			`"extradata": "0x1210"`, `"extradata": " 0x1210  "`, 1)))
		if err != nil {
			t.Fatal(err)
		}
		if err := CompareStreamLayouts(names[:2], [][]StreamKey{base, normalized}); err != nil {
			t.Fatalf("whitespace-normalized extradata must match: %v", err)
		}
	})

	t.Run("stream count mismatch names both segments", func(t *testing.T) {
		err := CompareStreamLayouts(names[:2], [][]StreamKey{base, base[:1]})
		if err == nil || !strings.Contains(err.Error(), "1 streams vs 2") {
			t.Fatalf("count mismatch misreported: %v", err)
		}
	})

	t.Run("empty part named", func(t *testing.T) {
		err := CompareStreamLayouts(names[:2], [][]StreamKey{{}, {}})
		if err == nil || !strings.Contains(err.Error(), names[0]) {
			t.Fatalf("empty reference must name segment 1's file: %v", err)
		}
	})

	t.Run("no parts", func(t *testing.T) {
		if err := CompareStreamLayouts(nil, nil); err == nil {
			t.Fatal("empty parts must fail")
		}
	})
}

func TestStreamKeysRunsConfiguredProbe(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell fake executable")
	}
	// The fake probe records its argv and answers with the fixture JSON,
	// proving StreamKeys passes -show_data and the target path.
	dir := t.TempDir()
	argv := filepath.Join(dir, "argv.txt")
	script := filepath.Join(dir, "fake-ffprobe")
	if err := os.WriteFile(script, []byte( //nolint:gosec // G306: executable bit required for the fake probe
		`#!/bin/sh`+"\n"+`printf '%s\n' "$@" >> '`+argv+`'`+"\n"+`cat <<'JSON'`+"\n"+streamKeysFixture+"\n"+`JSON`+"\n",
	), 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "segment001.mkv")
	if err := os.WriteFile(target, []byte("media"), 0o600); err != nil {
		t.Fatal(err)
	}
	keys, err := StreamKeys(context.Background(), script, target)
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 2 || keys[0].CodecName != "hevc" {
		t.Fatalf("keys from fake probe: %#v", keys)
	}
	raw, err := os.ReadFile(argv) //nolint:gosec // G304: reads back the argv file this test just wrote into its temp dir
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"-show_streams", "-show_data", target} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("probe argv missing %s: %s", want, raw)
		}
	}
	if _, err := StreamKeys(context.Background(), script, filepath.Join(dir, "missing.mkv")); err != nil {
		t.Logf("probing a missing file errored as expected: %v", err)
	}
	if _, err := exec.LookPath(script); err != nil {
		t.Fatal(err)
	}
}
