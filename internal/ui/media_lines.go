package ui

import (
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"git.skobk.in/skobkin/yave/internal/model"
)

// rowKind is the stream type of an information table row; it picks the
// row's icon.
type rowKind int

const (
	rowPlain rowKind = iota
	rowVideo
	rowCoverArt
	rowAudio
	rowSubtitles
	rowChapters
)

// infoRow is one row of an information table: a muted label, an optional
// primary value (the codec), and the details. Muted marks details that
// state an absence ("None").
type infoRow struct {
	Kind    rowKind
	Label   string
	Primary string
	Details string
	Muted   bool
}

// String renders the row as plain text.
func (r infoRow) String() string {
	return joinNonEmpty("  ", r.Label, r.Primary, r.Details)
}

// mediaView is the Media panel's content: the file name as a title, a
// container summary, notable-property badges, and one table row per
// stream group. Values are humanized for reading; every lookup falls back
// to the raw ffprobe name, and Copy keeps the raw formatMediaInfo text.
type mediaView struct {
	Title   string
	Summary string
	Badges  []string
	Rows    []infoRow
}

// String renders the view as plain text.
func (v mediaView) String() string {
	lines := []string{v.Title, v.Summary}
	if len(v.Badges) > 0 {
		lines = append(lines, strings.Join(v.Badges, " "))
	}
	for _, r := range v.Rows {
		lines = append(lines, r.String())
	}
	return strings.Join(lines, "\n")
}

func mediaInfoView(info model.MediaInfo) mediaView {
	view := mediaView{
		Title: filepath.Base(info.Path),
		Summary: strings.Join([]string{
			containerDisplayName(info.Format, info.Path),
			knownOr(info.Duration > 0, func() string { return formatClock(info.Duration) }, "unknown duration"),
			knownOr(info.Size > 0, func() string { return formatByteSize(info.Size) }, "unknown size"),
			knownOr(info.Bitrate > 0, func() string { return formatBitrate(info.Bitrate) }, "unknown bitrate"),
		}, infoSeparator),
	}
	if info.Path == "" {
		view.Title = unknownText + " file"
	}
	for i, v := range info.VideoStreams {
		label := "Video"
		if len(info.VideoStreams) > 1 {
			label = fmt.Sprintf("Video %d", i+1)
		}
		view.Rows = append(view.Rows, infoRow{Kind: rowVideo, Label: label, Primary: codecDisplayName(v.Codec), Details: videoDetails(v)})
		if hdr := hdrDisplay(v); hdr != "" && !slices.Contains(view.Badges, hdr) {
			view.Badges = append(view.Badges, hdr)
		}
		if v.Rotation != 0 {
			view.Badges = append(view.Badges, fmt.Sprintf("Rotated %d°", v.Rotation))
		}
	}
	for _, v := range info.CoverArtStreams {
		view.Rows = append(view.Rows, infoRow{Kind: rowCoverArt, Label: "Cover art", Primary: codecDisplayName(v.Codec), Details: dimensionsDisplay(v.Width, v.Height)})
	}
	for i, a := range info.AudioStreams {
		view.Rows = append(view.Rows, infoRow{Kind: rowAudio, Label: fmt.Sprintf("Audio %d", i+1), Primary: codecDisplayName(a.Codec), Details: audioDetails(a)})
	}
	subtitles := infoRow{Kind: rowSubtitles, Label: "Subtitles", Details: "None", Muted: true}
	if len(info.SubtitleStreams) > 0 {
		// All tracks share one wrapping row: files with dozens of
		// subtitle tracks must not push everything else out of view.
		parts := make([]string, 0, len(info.SubtitleStreams))
		for _, s := range info.SubtitleStreams {
			parts = append(parts, joinNonEmpty(" ", codecDisplayName(s.Codec), streamName(s.Language, s.Title)))
		}
		subtitles.Details, subtitles.Muted = strings.Join(parts, infoSeparator), false
	}
	view.Rows = append(view.Rows, subtitles)
	chapters := infoRow{Kind: rowChapters, Label: "Chapters", Details: "None", Muted: true}
	if info.ChapterCount > 0 {
		chapters.Details, chapters.Muted = strconv.Itoa(info.ChapterCount), false
	}
	view.Rows = append(view.Rows, chapters)
	return view
}

// infoSeparator joins facts within one line.
const infoSeparator = " · "

func videoDetails(v model.VideoStream) string {
	parts := []string{
		dimensionsDisplay(v.Width, v.Height),
		frameRateDisplay(v.FrameRate),
		pixelFormatDisplay(v.PixelFormat),
	}
	if v.ColorRange == "pc" {
		parts = append(parts, "full range")
	}
	if standard := colorStandardDisplay(v); standard != "" {
		parts = append(parts, standard)
	}
	if v.Bitrate > 0 {
		parts = append(parts, formatBitrate(v.Bitrate))
	}
	if name := streamName(v.Language, v.Title); name != "" {
		parts = append(parts, name)
	}
	return strings.Join(parts, infoSeparator)
}

func audioDetails(a model.AudioStream) string {
	parts := []string{sampleRateDisplay(a.SampleRate), channelsDisplay(a.ChannelLayout, a.Channels)}
	if a.BitsPerSample > 0 {
		parts = append(parts, fmt.Sprintf("%d-bit", a.BitsPerSample))
	}
	if a.Bitrate > 0 {
		parts = append(parts, formatBitrate(a.Bitrate))
	}
	if name := streamName(a.Language, a.Title); name != "" {
		parts = append(parts, name)
	}
	return strings.Join(parts, infoSeparator)
}

// knownOr returns value() when ok, else fallback.
func knownOr(ok bool, value func() string, fallback string) string {
	if ok {
		return value()
	}
	return fallback
}

func joinNonEmpty(sep string, parts ...string) string {
	kept := parts[:0:0]
	for _, p := range parts {
		if p != "" {
			kept = append(kept, p)
		}
	}
	return strings.Join(kept, sep)
}

// streamName is the language and quoted title of a stream: `eng "Main"`.
// "und" is ffprobe's undetermined language and is omitted.
func streamName(language, title string) string {
	if language == "und" {
		language = ""
	}
	if title != "" {
		title = strconv.Quote(title)
	}
	return joinNonEmpty(" ", language, title)
}

func dimensionsDisplay(width, height int) string {
	if width <= 0 || height <= 0 {
		return unknownText + " size"
	}
	return fmt.Sprintf("%d×%d", width, height)
}

var codecDisplayNames = map[string]string{
	// video
	"h264": "H.264", "hevc": "HEVC", "av1": "AV1", "vp8": "VP8", "vp9": "VP9",
	"mpeg2video": "MPEG-2", "mpeg4": "MPEG-4", "prores": "ProRes", "dnxhd": "DNxHD",
	"ffv1": "FFV1", "theora": "Theora", "mjpeg": "MJPEG", "png": "PNG",
	// audio
	"aac": "AAC", "opus": "Opus", "mp3": "MP3", "flac": "FLAC", "alac": "ALAC",
	"ac3": "AC-3", "eac3": "E-AC-3", "dts": "DTS", "truehd": "TrueHD", "vorbis": "Vorbis",
	// subtitles
	"subrip": "SubRip", "ass": "ASS", "ssa": "SSA", "webvtt": "WebVTT", "mov_text": "MP4 text",
	"hdmv_pgs_subtitle": "PGS", "dvd_subtitle": "VobSub", "dvb_subtitle": "DVB",
}

// codecDisplayName maps an ffprobe codec name to its common name, e.g.
// "h264" → "H.264" and "pcm_s16le" → "PCM s16le". Unknown codecs are
// shown as reported.
func codecDisplayName(codec string) string {
	codec = strings.TrimSpace(codec)
	if codec == "" {
		return unknownText + " codec"
	}
	if name, ok := codecDisplayNames[codec]; ok {
		return name
	}
	if sample, ok := strings.CutPrefix(codec, "pcm_"); ok && sample != "" {
		return "PCM " + sample
	}
	return codec
}

// containerDisplayName maps ffprobe's format_name to a readable name.
// ffprobe reports a whole demuxer family for MP4 and Matroska inputs
// ("mov,mp4,m4a,3gp,3g2,mj2"), so the file extension picks the member
// when it is one of them. Unknown formats are shown as reported.
func containerDisplayName(format, path string) string {
	format = strings.TrimSpace(format)
	ext := strings.ToLower(filepath.Ext(path))
	switch format {
	case "":
		return unknownText + " container"
	case "mov,mp4,m4a,3gp,3g2,mj2":
		switch ext {
		case ".mp4", ".m4v":
			return "MP4"
		case ".mov":
			return "QuickTime"
		case ".m4a":
			return "M4A"
		case ".3gp", ".3g2":
			return "3GP"
		}
		return "MP4 / QuickTime"
	case "matroska,webm":
		switch ext {
		case ".mkv", ".mka", ".mks":
			return "Matroska"
		case ".webm":
			return "WebM"
		}
		return "Matroska / WebM"
	}
	if name, ok := containerDisplayNames[format]; ok {
		return name
	}
	return format
}

var containerDisplayNames = map[string]string{
	"avi": "AVI", "mpegts": "MPEG-TS", "mpeg": "MPEG-PS", "flv": "FLV", "ogg": "Ogg",
	"wav": "WAV", "mp3": "MP3", "flac": "FLAC", "asf": "ASF", "aac": "AAC",
}

// frameRateDisplay renders ffprobe's rational frame rate as a decimal:
// "24000/1001" → "23.976 fps", "60/1" → "60 fps". Values that do not
// parse are shown as reported.
func frameRateDisplay(rate string) string {
	rate = strings.TrimSpace(rate)
	if rate == "" || rate == "0" || rate == "0/0" {
		return unknownText + " fps"
	}
	num, den, isRatio := strings.Cut(rate, "/")
	if !isRatio {
		den = "1"
	}
	n, errN := strconv.ParseFloat(num, 64)
	d, errD := strconv.ParseFloat(den, 64)
	if errN != nil || errD != nil || d <= 0 || n <= 0 {
		return rate + " fps"
	}
	return strconv.FormatFloat(float64(int64(n/d*1000+0.5))/1000, 'f', -1, 64) + " fps"
}

// pixelFormatFamilies maps planar pixel-format prefixes to their chroma
// layout; a bit-depth suffix ("10le") may follow. Longer prefixes come
// before the ones they extend.
var pixelFormatFamilies = []struct{ prefix, layout string }{
	{"yuva420p", "4:2:0 + alpha"}, {"yuva422p", "4:2:2 + alpha"}, {"yuva444p", "4:4:4 + alpha"},
	{"yuvj420p", "4:2:0"}, {"yuvj422p", "4:2:2"}, {"yuvj444p", "4:4:4"},
	{"yuv420p", "4:2:0"}, {"yuv422p", "4:2:2"}, {"yuv444p", "4:4:4"}, {"yuv440p", "4:4:0"},
	{"yuv411p", "4:1:1"}, {"yuv410p", "4:1:0"},
	{"gbrap", "RGB + alpha"}, {"gbrp", "RGB"}, {"gray", "grayscale"},
}

// packedPixelFormats are formats whose name does not follow the
// family+depth pattern.
var packedPixelFormats = map[string]string{
	"nv12": "8-bit 4:2:0", "nv21": "8-bit 4:2:0", "nv16": "8-bit 4:2:2",
	"p010le": "10-bit 4:2:0", "p010be": "10-bit 4:2:0", "p016le": "16-bit 4:2:0",
	"p210le": "10-bit 4:2:2", "p410le": "10-bit 4:4:4",
	"yuyv422": "8-bit 4:2:2", "uyvy422": "8-bit 4:2:2",
	"rgb24": "8-bit RGB", "bgr24": "8-bit RGB", "rgba": "8-bit RGB + alpha", "bgra": "8-bit RGB + alpha",
	"argb": "8-bit RGB + alpha", "abgr": "8-bit RGB + alpha",
	"rgb0": "8-bit RGB", "bgr0": "8-bit RGB", "0rgb": "8-bit RGB", "0bgr": "8-bit RGB",
	"rgb48le": "16-bit RGB", "rgb48be": "16-bit RGB",
}

// pixelFormatDisplay renders a pixel format as bit depth and chroma
// layout: "yuv420p10le" → "10-bit 4:2:0". Formats it does not recognize
// are shown as reported.
func pixelFormatDisplay(pixfmt string) string {
	pixfmt = strings.TrimSpace(pixfmt)
	if pixfmt == "" {
		return unknownText + " pixel format"
	}
	if text, ok := packedPixelFormats[pixfmt]; ok {
		return text
	}
	for _, f := range pixelFormatFamilies {
		rest, ok := strings.CutPrefix(pixfmt, f.prefix)
		if !ok {
			continue
		}
		depth := 8
		if rest != "" {
			digits := strings.TrimSuffix(strings.TrimSuffix(rest, "le"), "be")
			n, err := strconv.Atoi(digits)
			if err != nil || n <= 8 || n > 32 || digits == rest {
				return pixfmt
			}
			depth = n
		}
		return fmt.Sprintf("%d-bit %s", depth, f.layout)
	}
	return pixfmt
}

var colorStandardNames = map[string]string{
	"bt709": "BT.709", "bt2020": "BT.2020", "bt2020nc": "BT.2020", "bt2020c": "BT.2020",
	"bt470bg": "BT.601", "smpte170m": "BT.601", "smpte240m": "SMPTE 240M",
	"smpte431": "DCI-P3", "smpte432": "Display P3",
}

// hdrDisplay names the HDR transfer: "HDR (PQ)", "HDR (HLG)", or "".
func hdrDisplay(v model.VideoStream) string {
	switch v.ColorTransfer {
	case "smpte2084":
		return "HDR (PQ)"
	case "arib-std-b67":
		return "HDR (HLG)"
	}
	return ""
}

// colorStandardDisplay names the color standard ("BT.709"), preferring
// the primaries over the matrix; unknown names are shown as reported and
// absent ones yield "".
func colorStandardDisplay(v model.VideoStream) string {
	standard := v.ColorPrimaries
	if standard == "" || standard == "unknown" {
		standard = v.ColorSpace
	}
	if standard == "" || standard == "unknown" {
		return ""
	}
	if name, ok := colorStandardNames[standard]; ok {
		return name
	}
	return standard
}

// sampleRateDisplay renders Hz as kHz: 48000 → "48 kHz", 44100 →
// "44.1 kHz".
func sampleRateDisplay(rate int) string {
	if rate <= 0 {
		return unknownText + " sample rate"
	}
	return strconv.FormatFloat(float64(rate)/1000, 'f', -1, 64) + " kHz"
}

// channelsDisplay prefers the named layout ("stereo", "5.1(side)"),
// which already implies the channel count.
func channelsDisplay(layout string, channels int) string {
	switch {
	case layout != "" && layout != "unknown":
		return layout
	case channels > 0:
		return fmt.Sprintf("%d ch", channels)
	default:
		return unknownText + " channels"
	}
}
