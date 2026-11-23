package ui

import (
	"strings"
	"testing"
	"time"

	"git.skobk.in/skobkin/yave/internal/model"
)

func TestFormatMediaInfoFull(t *testing.T) {
	t.Parallel()
	got := formatMediaInfo(model.MediaInfo{
		Format: "matroska,webm", Duration: 62 * time.Second, Size: 412 << 20, Bitrate: 8_100_000,
		VideoStreams: []model.VideoStream{{
			Codec: "hevc", Width: 3840, Height: 2160, FrameRate: "24/1", PixelFormat: "yuv420p10le",
			ColorRange: "tv", ColorTransfer: "smpte2084", ColorPrimaries: "bt2020", Bitrate: 7_000_000,
		}},
		AudioStreams: []model.AudioStream{{
			Codec: "opus", SampleRate: 48000, Channels: 6, ChannelLayout: "5.1(side)", Bitrate: 192_000,
			Language: "eng", Title: "Main",
		}},
		SubtitleStreams: []model.SubtitleStream{{Codec: "subrip", Language: "fra"}},
		ChapterCount:    3,
	})
	for _, want := range []string{
		"Duration: 1m02s", "Size: 412.0 MB", "Bitrate: 8.1 Mbps",
		"Video: hevc, 3840x2160, 24/1 fps, yuv420p10le, tv range, HDR (PQ), bt2020, 7.0 Mbps",
		`Audio 1: opus, 48000 Hz, 5.1(side) (6 ch), 192 kbps — eng "Main"`,
		"Subtitle 1: subrip — fra", "Chapters: 3",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

func TestFormatMediaInfoMissingDataIsUnknown(t *testing.T) {
	t.Parallel()
	got := formatMediaInfo(model.MediaInfo{
		VideoStreams: []model.VideoStream{{}},
		AudioStreams: []model.AudioStream{{}},
	})
	for _, want := range []string{
		"Format: unknown | Duration: unknown | Size: unknown | Bitrate: unknown",
		"Video: unknown, unknown size, unknown fps, unknown",
		"Audio 1: unknown, unknown, unknown channels",
		"Subtitles: none",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	for _, bad := range []string{" 0s", "0x0", "range \n", " fps,  "} {
		if strings.Contains(got, bad) {
			t.Errorf("blank/zero artifact %q in:\n%s", bad, got)
		}
	}
}

func TestFormatMediaInfoCoverArtOnly(t *testing.T) {
	t.Parallel()
	got := formatMediaInfo(model.MediaInfo{
		AudioStreams:    []model.AudioStream{{Codec: "mp3", SampleRate: 44100, Channels: 2}},
		CoverArtStreams: []model.VideoStream{{Codec: "mjpeg", Width: 500, Height: 500}},
	})
	if !strings.Contains(got, "Cover art: mjpeg, 500x500") || strings.Contains(got, "Video:") {
		t.Fatalf("cover art must be listed separately from video:\n%s", got)
	}
}

func TestFormatBitrate(t *testing.T) {
	t.Parallel()
	for in, want := range map[int64]string{0: "unknown", 128_000: "128 kbps", 1_500_000: "1.5 Mbps"} {
		if got := formatBitrate(in); got != want {
			t.Errorf("formatBitrate(%d) = %q, want %q", in, got, want)
		}
	}
}
