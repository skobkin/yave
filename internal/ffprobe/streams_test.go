package ffprobe

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
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

// Real FLAC STREAMINFO dumps (see the FLAC subtest of
// TestCompareStreamLayouts).
const flacSeg1Extradata = `
00000000: 03bc 03bc 0000 e100 04a9 0ac4 4170 0000  ......Ap..
00000010: ac44 7a7a 9784 5dab af28 c48c 5056 fef7  .Dzz..]..(..PV..
00000020: ca4b                                     .K
`
const flacSeg2Extradata = `
00000000: 02f0 02f0 0002 7c00 03cd 0ac4 4170 0000  ......|.....Ap..
00000010: ac44 345e a1b0 f4b9 f83b bfef 408e bf9a  .D4^.....;..@...
00000020: e1f9                                     ..
`

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
}

func TestParseHexDump(t *testing.T) {
	b, ok := parseHexDump(flacSeg1Extradata)
	if !ok {
		t.Fatal("a real ffprobe dump must parse")
	}
	if len(b) != 34 {
		t.Fatalf("decoded %d bytes, want 34 (FLAC STREAMINFO)", len(b))
	}
	want := []byte{
		0x03, 0xbc, 0x03, 0xbc, 0x00, 0x00, 0xe1, 0x00, 0x04, 0xa9,
		0x0a, 0xc4, 0x41, 0x70, 0x00, 0x00, 0xac, 0x44, 0x7a, 0x7a,
	}
	if !bytes.Equal(b[:20], want) {
		t.Fatalf("decoded prefix = %x, want %x", b[:20], want)
	}

	// The ASCII column may contain hex-like text; it must be ignored.
	b2, ok := parseHexDump("00000000: 0a00  abc\n")
	if !ok || !bytes.Equal(b2, []byte{0x0a, 0x00}) {
		t.Fatalf("ascii column leaked into the decode: %v ok=%v", b2, ok)
	}
	if _, ok := parseHexDump("no hexdump here"); ok {
		t.Fatal("a non-hexdump string must fail to parse")
	}
}

func TestParseStreamKeysMalformedJSON(t *testing.T) {
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

	t.Run("FLAC STREAMINFO: per-segment data ignored, block-size constraint enforced", func(t *testing.T) {
		// Real FLAC STREAMINFO dumps ffprobe reported for two
		// one-second libx264/FLAC cuts of one input (offsets 1s and
		// 4s): identical decoder configuration, per-segment block and
		// frame sizes, per-segment decoded-audio MD5. The dumps go
		// through parseStreamKeys — the production path — because the
		// extradata normalization happens there: flattening the hexdump
		// before parsing it destroyed its structure and silently
		// reverted FLAC to the whole-blob comparison.
		flacDoc := func(extradata string) []byte {
			doc := map[string]any{"streams": []map[string]any{{
				"codec_name": "flac", "codec_type": "audio",
				"channel_layout": "mono", "sample_rate": "44100", "channels": 1,
				"extradata": extradata,
			}}}
			b, err := json.Marshal(doc)
			if err != nil {
				t.Fatal(err)
			}
			return b
		}
		parse := func(extradata string) []StreamKey {
			keys, err := parseStreamKeys(flacDoc(extradata))
			if err != nil {
				t.Fatal(err)
			}
			return keys
		}

		seg1 := parse(flacSeg1Extradata)
		seg2 := parse(flacSeg2Extradata)
		if seg1[0].Extradata == strings.Join(strings.Fields(flacSeg1Extradata), " ") {
			t.Fatal("FLAC extradata must be reduced to the config signature, not the flattened dump")
		}
		if seg1[0].FLACMaxBlockSize == 0 || seg2[0].FLACMaxBlockSize == 0 {
			t.Fatalf("FLAC streams must parse their maximum block size, got %d and %d",
				seg1[0].FLACMaxBlockSize, seg2[0].FLACMaxBlockSize)
		}
		if err := CompareStreamLayouts(names[:2], [][]StreamKey{seg1, seg2}); err != nil {
			t.Fatalf("valid FLAC segments must pass verification: %v", err)
		}

		// The join decodes every segment against segment 1's STREAMINFO,
		// and the decoder silently drops frames larger than its declared
		// maximum block size (measured with ffmpeg 9.0.2: maxima 955 then
		// 969 published 44595 of 88200 samples, exit status 0). A segment
		// whose maximum exceeds segment 1's must therefore fail
		// verification with the dedicated message — equality and smaller
		// maxima stay allowed, since only larger frames are dropped.
		bigger := strings.Replace(flacSeg2Extradata, "02f0 02f0", "0400 0400", 1)
		if bigger == flacSeg2Extradata {
			t.Fatal("test setup: block-size replacement did not apply")
		}
		err := CompareStreamLayouts(names[:2], [][]StreamKey{seg1, parse(bigger)})
		if err == nil || !strings.Contains(err.Error(), "block size") || !strings.Contains(err.Error(), "separate files") {
			t.Fatalf("larger FLAC maximum block size must fail with the block-size report, got %v", err)
		}
		equal := strings.Replace(flacSeg2Extradata, "02f0 02f0", "03bc 03bc", 1)
		if equal == flacSeg2Extradata {
			t.Fatal("test setup: block-size replacement did not apply")
		}
		if err := CompareStreamLayouts(names[:2], [][]StreamKey{seg1, parse(equal)}); err != nil {
			t.Fatalf("equal FLAC maximum block sizes must pass: %v", err)
		}

		// An unparsable dump parses no block size, and the constraint
		// cannot be checked — graceful degradation, like the rest of
		// the optional-metadata handling.
		if got := parse("0xAAAA")[0].FLACMaxBlockSize; got != 0 {
			t.Fatalf("unparsable FLAC dump must parse no block size, got %d", got)
		}

		// A genuinely different configuration (the segment re-encoded
		// at 48 kHz: the packed sample-rate field differs) still fails.
		other := strings.Replace(flacSeg1Extradata, "0ac4 4170", "0bc4 8070", 1)
		if other == flacSeg1Extradata {
			t.Fatal("test setup: sample-rate replacement did not apply")
		}
		err = CompareStreamLayouts(names[:2], [][]StreamKey{seg1, parse(other)})
		if err == nil || !strings.Contains(err.Error(), "extradata") {
			t.Fatalf("FLAC config difference must fail with an extradata report, got %v", err)
		}

		// Bit depth lives in the byte-13 nibble the signature keeps:
		// the captured dumps are 24-bit (bps field 10111), so an
		// otherwise identical 16-bit segment (11111) must be rejected.
		sixteenBit := strings.Replace(flacSeg1Extradata, "0ac4 4170", "0ac4 40f0", 1)
		err = CompareStreamLayouts(names[:2], [][]StreamKey{seg1, parse(sixteenBit)})
		if err == nil || !strings.Contains(err.Error(), "extradata") {
			t.Fatalf("FLAC bit-depth difference must fail, got %v", err)
		}

		// Unparsable dumps cannot extract the config region and fall
		// back to the raw comparison — strictly, never looser.
		if err := CompareStreamLayouts(names[:2], [][]StreamKey{
			parse("0xAAAA"), parse("0xBBBB"),
		}); err == nil || !strings.Contains(err.Error(), "extradata") {
			t.Fatalf("unparsable dumps must compare raw, got %v", err)
		}
		if err := CompareStreamLayouts(names[:2], [][]StreamKey{
			parse("0xAAAA"), parse("0xAAAA"),
		}); err != nil {
			t.Fatalf("identical unparsable dumps must match: %v", err)
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

// spawnHeldPipeProbe writes a wrapper probe that prints the fixture
// JSON and exits, leaving a background child holding the inherited
// stdout for 30 seconds — the shape that would block CombinedOutput
// until the descendant exits without a WaitDelay bound.
func spawnHeldPipeProbe(t *testing.T, dir string) (script, target string) {
	t.Helper()
	script = filepath.Join(dir, "wrapper-ffprobe")
	if err := os.WriteFile(script, []byte( //nolint:gosec // G306: executable bit required for the fake probe
		`#!/bin/sh`+"\n"+`sleep 30 &`+"\n"+`cat <<'JSON'`+"\n"+streamKeysFixture+"\n"+`JSON`+"\n",
	), 0o755); err != nil {
		t.Fatal(err)
	}
	target = filepath.Join(dir, "segment001.mkv")
	if err := os.WriteFile(target, []byte("media"), 0o600); err != nil {
		t.Fatal(err)
	}
	return script, target
}

func shortenProbeWaitDelay(t *testing.T) {
	t.Helper()
	original := probeWaitDelay
	probeWaitDelay = 200 * time.Millisecond
	t.Cleanup(func() { probeWaitDelay = original })
}

// TestStreamKeysToleratesDescendantHeldPipes pins the bounded drain: a
// probe that exits successfully while a descendant still holds its
// output pipes must yield its complete JSON once probeWaitDelay bounds
// the drain — not block until the descendant exits, and not fail.
func TestStreamKeysToleratesDescendantHeldPipes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell fake executable")
	}
	script, target := spawnHeldPipeProbe(t, t.TempDir())
	shortenProbeWaitDelay(t)

	started := time.Now()
	keys, err := StreamKeys(context.Background(), script, target)
	if elapsed := time.Since(started); err != nil || elapsed > 5*time.Second {
		t.Fatalf("StreamKeys = %#v, %v after %s; the pipe drain must be bounded and the complete output parsed", keys, err, elapsed)
	}
	if len(keys) != 2 || keys[0].CodecName != "hevc" {
		t.Fatalf("keys from wrapper probe: %#v", keys)
	}
}

// TestStreamKeysCancelBoundsHeldPipes pins the cancellation side of the
// bound: canceling a probe whose descendant holds the pipes must return
// the context error promptly instead of waiting for the descendant.
func TestStreamKeysCancelBoundsHeldPipes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell fake executable")
	}
	script, target := spawnHeldPipeProbe(t, t.TempDir())
	shortenProbeWaitDelay(t)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	started := time.Now()
	_, err := StreamKeys(ctx, script, target)
	if elapsed := time.Since(started); !errors.Is(err, context.Canceled) || elapsed > 5*time.Second {
		t.Fatalf("err = %v after %s; a canceled probe must return context.Canceled within the WaitDelay bound", err, elapsed)
	}
}
