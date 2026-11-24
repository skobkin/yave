package ui

import (
	"reflect"
	"testing"
	"time"

	"git.skobk.in/skobkin/yave/internal/model"
)

func TestMediaInfoView(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		info model.MediaInfo
		want mediaView
	}{
		{
			name: "OBS replay: HEVC with three Opus tracks",
			info: model.MediaInfo{
				Path: "/v/2026-11-16 replay.mp4", Format: "mov,mp4,m4a,3gp,3g2,mj2",
				Duration: 179 * time.Second, Size: 555_000_000, Bitrate: 24_800_000,
				VideoStreams: []model.VideoStream{{
					Codec: "hevc", Width: 1920, Height: 1080, FrameRate: "60/1", PixelFormat: "yuv420p",
					ColorRange: "tv", ColorPrimaries: "bt709", Bitrate: 24_500_000,
				}},
				AudioStreams: []model.AudioStream{
					{Codec: "opus", SampleRate: 48000, Channels: 2, ChannelLayout: "stereo", Bitrate: 134_000},
					{Codec: "opus", SampleRate: 48000, Channels: 2, ChannelLayout: "stereo", Bitrate: 1_000},
				},
			},
			want: mediaView{
				Title:   "2026-11-16 replay.mp4",
				Summary: "MP4 · 2m59s · 529.3 MB · 24.8 Mbps",
				Rows: []infoRow{
					{Kind: rowVideo, Label: "Video", Primary: "HEVC", Details: "1920×1080 · 60 fps · 8-bit 4:2:0 · BT.709 · 24.5 Mbps"},
					{Kind: rowAudio, Label: "Audio 1", Primary: "Opus", Details: "48 kHz · stereo · 134 kbps"},
					{Kind: rowAudio, Label: "Audio 2", Primary: "Opus", Details: "48 kHz · stereo · 1 kbps"},
					{Kind: rowSubtitles, Label: "Subtitles", Details: "None", Muted: true},
					{Kind: rowChapters, Label: "Chapters", Details: "None", Muted: true},
				},
			},
		},
		{
			name: "HDR, full range, rotation, names, subtitles share one row, chapters",
			info: model.MediaInfo{
				Path: "/v/film.mkv", Format: "matroska,webm", Duration: 62 * time.Second, Size: 412 << 20, Bitrate: 8_100_000,
				VideoStreams: []model.VideoStream{{
					Codec: "hevc", Width: 3840, Height: 2160, FrameRate: "24000/1001", PixelFormat: "yuv420p10le",
					ColorRange: "pc", ColorTransfer: "smpte2084", ColorPrimaries: "bt2020", Rotation: 90,
					Language: "und", Title: "Main",
				}},
				AudioStreams: []model.AudioStream{{
					Codec: "flac", SampleRate: 44100, Channels: 6, ChannelLayout: "5.1(side)", BitsPerSample: 24,
					Language: "eng", Title: "Commentary",
				}},
				SubtitleStreams: []model.SubtitleStream{
					{Codec: "subrip", Language: "eng"},
					{Codec: "hdmv_pgs_subtitle", Language: "jpn", Title: "Signs *[1]*"},
					{Codec: "weird_sub"},
				},
				ChapterCount: 12,
			},
			want: mediaView{
				Title:   "film.mkv",
				Summary: "Matroska · 1m02s · 412.0 MB · 8.1 Mbps",
				Badges:  []string{"HDR (PQ)", "Rotated 90°"},
				Rows: []infoRow{
					{Kind: rowVideo, Label: "Video", Primary: "HEVC", Details: `3840×2160 · 23.976 fps · 10-bit 4:2:0 · full range · BT.2020 · "Main"`},
					{Kind: rowAudio, Label: "Audio 1", Primary: "FLAC", Details: `44.1 kHz · 5.1(side) · 24-bit · eng "Commentary"`},
					{Kind: rowSubtitles, Label: "Subtitles", Details: `SubRip eng · PGS jpn "Signs *[1]*" · weird_sub`},
					{Kind: rowChapters, Label: "Chapters", Details: "12"},
				},
			},
		},
		{
			name: "missing metadata stays visible as unknown",
			info: model.MediaInfo{VideoStreams: []model.VideoStream{{}}, AudioStreams: []model.AudioStream{{}}},
			want: mediaView{
				Title:   "unknown file",
				Summary: "unknown container · unknown duration · unknown size · unknown bitrate",
				Rows: []infoRow{
					{Kind: rowVideo, Label: "Video", Primary: "unknown codec", Details: "unknown size · unknown fps · unknown pixel format"},
					{Kind: rowAudio, Label: "Audio 1", Primary: "unknown codec", Details: "unknown sample rate · unknown channels"},
					{Kind: rowSubtitles, Label: "Subtitles", Details: "None", Muted: true},
					{Kind: rowChapters, Label: "Chapters", Details: "None", Muted: true},
				},
			},
		},
		{
			name: "cover art is not video; two HDR streams badge once",
			info: model.MediaInfo{
				Path: "/m/song.mp3", Format: "mp3", Duration: time.Second, Size: 1, Bitrate: 1000, ChapterCount: 1,
				AudioStreams:    []model.AudioStream{{Codec: "mp3", SampleRate: 44100, Channels: 2}},
				CoverArtStreams: []model.VideoStream{{Codec: "mjpeg", Width: 500, Height: 500}},
			},
			want: mediaView{
				Title:   "song.mp3",
				Summary: "MP3 · 1s · 1 B · 1 kbps",
				Rows: []infoRow{
					{Kind: rowCoverArt, Label: "Cover art", Primary: "MJPEG", Details: "500×500"},
					{Kind: rowAudio, Label: "Audio 1", Primary: "MP3", Details: "44.1 kHz · 2 ch"},
					{Kind: rowSubtitles, Label: "Subtitles", Details: "None", Muted: true},
					{Kind: rowChapters, Label: "Chapters", Details: "1"},
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := mediaInfoView(tt.info)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("mediaInfoView() =\n%#v\nwant\n%#v", got, tt.want)
			}
		})
	}
}

func TestMediaInfoViewHDRBadgeOnce(t *testing.T) {
	t.Parallel()
	hdr := model.VideoStream{Codec: "hevc", ColorTransfer: "arib-std-b67"}
	got := mediaInfoView(model.MediaInfo{VideoStreams: []model.VideoStream{hdr, hdr}})
	if !reflect.DeepEqual(got.Badges, []string{"HDR (HLG)"}) {
		t.Fatalf("badges = %v, want a single HDR (HLG)", got.Badges)
	}
	if got.Rows[0].Label != "Video 1" || got.Rows[1].Label != "Video 2" {
		t.Fatalf("multiple video streams must be numbered: %+v", got.Rows[:2])
	}
}

func TestContainerDisplayName(t *testing.T) {
	t.Parallel()
	tests := []struct{ format, path, want string }{
		{"mov,mp4,m4a,3gp,3g2,mj2", "/a/b.MP4", "MP4"},
		{"mov,mp4,m4a,3gp,3g2,mj2", "/a/b.mov", "QuickTime"},
		{"mov,mp4,m4a,3gp,3g2,mj2", "/a/b.m4a", "M4A"},
		{"mov,mp4,m4a,3gp,3g2,mj2", "/a/b.bin", "MP4 / QuickTime"},
		{"matroska,webm", "/a/b.webm", "WebM"},
		{"matroska,webm", "/a/b.mkv", "Matroska"},
		{"matroska,webm", "", "Matroska / WebM"},
		{"mpegts", "/a/b.ts", "MPEG-TS"},
		{"some_new_demuxer", "/a/b.x", "some_new_demuxer"},
		{"", "/a/b.mkv", "unknown container"},
	}
	for _, tt := range tests {
		if got := containerDisplayName(tt.format, tt.path); got != tt.want {
			t.Errorf("containerDisplayName(%q, %q) = %q, want %q", tt.format, tt.path, got, tt.want)
		}
	}
}

func TestCodecDisplayName(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{
		"h264": "H.264", "hevc": "HEVC", "eac3": "E-AC-3", "hdmv_pgs_subtitle": "PGS",
		"pcm_s24le": "PCM s24le", "pcm_": "pcm_", "brand_new": "brand_new", " ": "unknown codec",
	} {
		if got := codecDisplayName(in); got != want {
			t.Errorf("codecDisplayName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFrameRateDisplay(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{
		"60/1": "60 fps", "24000/1001": "23.976 fps", "30000/1001": "29.97 fps", "25": "25 fps",
		"12.5/1": "12.5 fps", "": "unknown fps", "0/0": "unknown fps", "abc": "abc fps", "1/0": "1/0 fps",
	} {
		if got := frameRateDisplay(in); got != want {
			t.Errorf("frameRateDisplay(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPixelFormatDisplay(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{
		"yuv420p": "8-bit 4:2:0", "yuvj420p": "8-bit 4:2:0", "yuv420p10le": "10-bit 4:2:0",
		"yuv422p12be": "12-bit 4:2:2", "yuva444p": "8-bit 4:4:4 + alpha", "gbrp10le": "10-bit RGB",
		"gray": "8-bit grayscale", "nv12": "8-bit 4:2:0", "p010le": "10-bit 4:2:0", "rgb24": "8-bit RGB",
		"yuv420p10": "yuv420p10", "yuv420pfoo": "yuv420pfoo", "xyz12le": "xyz12le", "": "unknown pixel format",
	} {
		if got := pixelFormatDisplay(in); got != want {
			t.Errorf("pixelFormatDisplay(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestColorDisplay(t *testing.T) {
	t.Parallel()
	tests := []struct {
		v             model.VideoStream
		hdr, standard string
	}{
		{model.VideoStream{}, "", ""},
		{model.VideoStream{ColorPrimaries: "bt709"}, "", "BT.709"},
		{model.VideoStream{ColorPrimaries: "unknown", ColorSpace: "bt2020nc"}, "", "BT.2020"},
		{model.VideoStream{ColorTransfer: "arib-std-b67", ColorPrimaries: "bt2020"}, "HDR (HLG)", "BT.2020"},
		{model.VideoStream{ColorTransfer: "smpte2084"}, "HDR (PQ)", ""},
		{model.VideoStream{ColorPrimaries: "jedec-p22"}, "", "jedec-p22"},
	}
	for _, tt := range tests {
		if got := hdrDisplay(tt.v); got != tt.hdr {
			t.Errorf("hdrDisplay(%+v) = %q, want %q", tt.v, got, tt.hdr)
		}
		if got := colorStandardDisplay(tt.v); got != tt.standard {
			t.Errorf("colorStandardDisplay(%+v) = %q, want %q", tt.v, got, tt.standard)
		}
	}
}

func TestChannelsAndSampleRateDisplay(t *testing.T) {
	t.Parallel()
	if got := channelsDisplay("unknown", 3); got != "3 ch" {
		t.Errorf("unknown layout = %q, want the count", got)
	}
	if got := channelsDisplay("", 0); got != "unknown channels" {
		t.Errorf("no channel data = %q", got)
	}
	if got := sampleRateDisplay(22050); got != "22.05 kHz" {
		t.Errorf("sampleRateDisplay(22050) = %q", got)
	}
}
