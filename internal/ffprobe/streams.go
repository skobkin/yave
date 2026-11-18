package ffprobe

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// StreamKey is the comparable per-stream signature that concat-demuxer
// joining requires to be identical across intermediates. The compared
// fields are the ones that materially affect decoder compatibility and
// concatenation: codec identity and profile, pixel format, time base,
// dimensions and sample aspect ratio, audio channel layout, and codec
// initialization data. Bitrate, tags, durations, and timestamps are
// deliberately NOT compared — they legitimately differ between segments.
// FLAC streams additionally carry a relational constraint (not equality):
// the join decodes every segment with segment 1's STREAMINFO, so no
// segment may declare frames larger than segment 1's maximum block size.
type StreamKey struct {
	CodecType     string
	CodecName     string
	Profile       string
	PixelFormat   string
	TimeBase      string
	SAR           string // sample_aspect_ratio (video; "" for audio)
	ChannelLayout string // audio, e.g. "stereo" ("" when absent)
	Width         int
	Height        int
	SampleRate    int
	Channels      int
	Extradata     string // hex dump from -show_data; "" when absent
	// FLACMaxBlockSize is the STREAMINFO maximum block size of a FLAC
	// stream, parsed at probe time; 0 when the stream is not FLAC or
	// the dump is unparsable. The join requires it to not exceed the
	// first segment's (see CompareStreamLayouts).
	FLACMaxBlockSize int
}

// keyStream is the ffprobe stream projection StreamKeys parses. It
// carries the signature fields only; unrelated fields are ignored.
type keyStream struct {
	Index             int    `json:"index"`
	CodecName         string `json:"codec_name"`
	CodecType         string `json:"codec_type"`
	Profile           string `json:"profile"`
	PixelFormat       string `json:"pix_fmt"`
	TimeBase          string `json:"time_base"`
	SampleAspectRatio string `json:"sample_aspect_ratio"`
	ChannelLayout     string `json:"channel_layout"`
	Width             int    `json:"width"`
	Height            int    `json:"height"`
	SampleRate        string `json:"sample_rate"`
	Channels          int    `json:"channels"`
	Extradata         string `json:"extradata"`
}

type keyDocument struct {
	Streams []keyStream `json:"streams"`
}

// StreamKeys probes one media file and returns a StreamKey per stream,
// in stream order, via -show_streams -show_data. Cancellation aborts a
// hung probe; ctx errors are returned unwrapped so callers can detect
// context.Canceled.
func StreamKeys(ctx context.Context, ffprobePath, path string) ([]StreamKey, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	executable := strings.TrimSpace(ffprobePath)
	if executable == "" {
		executable = "ffprobe"
	}
	resolved, err := exec.LookPath(executable)
	if err != nil {
		return nil, fmt.Errorf("ffprobe executable %q not found: %w", executable, err)
	}
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("probe streams: path is empty")
	}
	args := []string{
		"-v", "error",
		"-print_format", "json",
		"-show_streams",
		"-show_data",
		path,
	}
	output, err := exec.CommandContext(ctx, resolved, args...).CombinedOutput() //nolint:gosec // G204: resolved via exec.LookPath
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		detail := strings.TrimSpace(string(output))
		if detail == "" {
			detail = err.Error()
		}
		return nil, fmt.Errorf("probe streams %q: %s", path, detail)
	}
	keys, err := parseStreamKeys(output)
	if err != nil {
		return nil, fmt.Errorf("probe streams %q: %w", path, err)
	}
	return keys, nil
}

// parseStreamKeys converts ffprobe's JSON stream document into
// comparable signatures. Numeric strings that are absent or malformed
// become zero values, matching the tolerance of the media parser.
func parseStreamKeys(data []byte) ([]StreamKey, error) {
	var document keyDocument
	if err := json.Unmarshal(data, &document); err != nil {
		return nil, fmt.Errorf("decode ffprobe JSON: %w", err)
	}
	keys := make([]StreamKey, 0, len(document.Streams))
	for _, stream := range document.Streams {
		key := StreamKey{
			CodecType:     stream.CodecType,
			CodecName:     stream.CodecName,
			Profile:       stream.Profile,
			PixelFormat:   stream.PixelFormat,
			TimeBase:      stream.TimeBase,
			SAR:           stream.SampleAspectRatio,
			ChannelLayout: stream.ChannelLayout,
			Width:         stream.Width,
			Height:        stream.Height,
			SampleRate:    parseInt(stream.SampleRate),
			Channels:      stream.Channels,
			Extradata:     streamExtradataSignature(stream.CodecName, stream.Extradata),
		}
		if strings.EqualFold(stream.CodecName, "flac") {
			key.FLACMaxBlockSize = flacMaxBlockSize(stream.Extradata)
		}
		keys = append(keys, key)
	}
	return keys, nil
}

// streamExtradataSignature normalizes an extradata field into the
// string CompareStreamLayouts compares.
//
// For most codecs the field is pure configuration (AAC's
// AudioSpecificConfig, H.264/HEVC SPS/PPS), so whitespace-normalizing
// the probe text is enough: equivalent dumps match regardless of
// spacing.
//
// FLAC is parsed instead of flattened. ffprobe reports STREAMINFO as a
// hexdump whose structure — the line breaks and the double-space
// separator before the ASCII column — is exactly what tells parseHexDump
// where the bytes end and the decoration begins, so flattening first
// destroys the parse (and splices the ASCII column into the byte
// stream). The decoder-configuration signature is therefore extracted
// while the dump is still intact; everything else in STREAMINFO —
// block and frame sizes, total sample count, the MD5 of the decoded
// audio — is per-segment data the encoder finalizes per file and must
// not reject joinable segments. The join pass re-encodes FLAC audio as
// one continuous stream (ffmpeg.JoinAudioArgs), so per-segment
// STREAMINFO never reaches the output. The one block-size exception is
// the decoder constraint flacMaxBlockSize feeds CompareStreamLayouts:
// frames larger than segment 1's declared maximum would be dropped
// silently. An unparsable dump falls back to the whitespace-normalized
// raw text, keeping the comparison strict.
func streamExtradataSignature(codecName, extradata string) string {
	if strings.EqualFold(codecName, "flac") {
		if b, ok := parseHexDump(extradata); ok && len(b) >= 14 {
			config := append([]byte(nil), b[10:14]...)
			// STREAMINFO bits 80-107 are the packed sample rate (20),
			// channels (3), and bits-per-sample (5) fields: bytes 10-12
			// plus byte 13's top nibble. Everything from bit 108 on is
			// the per-segment total sample count and MD5.
			config[3] &= 0xf0
			return fmt.Sprintf("flac-streaminfo-config:%x", config)
		}
	}
	return strings.Join(strings.Fields(extradata), " ")
}

// flacMaxBlockSize extracts a FLAC STREAMINFO's maximum block size —
// bytes 2-3, big-endian — from a parsable hex dump. The join decodes
// every segment against segment 1's STREAMINFO, and the decoder drops
// frames whose block size exceeds that declared maximum (measured with
// ffmpeg 9.0.2: segments with maxima 955 then 969 published 44595 of
// 88200 samples, exit status 0), so the comparator must enforce the
// bound even though the value is otherwise per-segment data. 0 when
// the dump is unparsable: the constraint cannot be checked, and the
// comparison degrades gracefully.
func flacMaxBlockSize(extradata string) int {
	b, ok := parseHexDump(extradata)
	if !ok || len(b) < 4 {
		return 0
	}
	return int(b[2])<<8 | int(b[3])
}

// CompareStreamLayouts verifies that every intermediate carries the same
// streams, in the same order, with the same signature fields as the
// first segment. The first mismatch is reported with the segment, its
// file name, the stream, the field, and both values, plus the remediation.
func CompareStreamLayouts(names []string, parts [][]StreamKey) error {
	if len(parts) == 0 {
		return errors.New("joining requires at least one encoded segment to verify")
	}
	if len(names) != len(parts) {
		return fmt.Errorf("segment names (%d) do not match probed segments (%d)", len(names), len(parts))
	}
	reference := parts[0]
	if len(reference) == 0 {
		return fmt.Errorf("segment 1 of %d (%s): ffprobe reported no streams", len(parts), names[0])
	}
	for i := 1; i < len(parts); i++ {
		current := parts[i]
		if len(current) != len(reference) {
			return fmt.Errorf(
				"segment %d of %d (%s): %d streams vs %d in segment 1 (%s) — joinable segments must share one stream layout; re-encode all segments with the same profile",
				i+1, len(parts), names[i], len(current), len(reference), names[0])
		}
		for s := range reference {
			if field, refVal, curVal := streamKeyMismatch(reference[s], current[s]); field != "" {
				if field == flacMaxBlocksizeField {
					return fmt.Errorf(
						"segment %d of %d (%s): stream %d (%s): FLAC frames declare a maximum block size of %s samples vs %s in segment 1 (%s) — the join decodes every segment with segment 1's STREAMINFO and silently drops frames larger than it, losing that segment's audio; switch the export mode to separate files",
						i+1, len(parts), names[i], s, current[s].CodecType, curVal, refVal, names[0])
				}
				return fmt.Errorf(
					"segment %d of %d (%s): stream %d (%s): %s %q vs %q in segment 1 — joinable segments must share one stream layout; re-encode all segments with the same profile",
					i+1, len(parts), names[i], s, current[s].CodecType, field, curVal, refVal)
			}
		}
	}
	return nil
}

// flacMaxBlocksizeField marks streamKeyMismatch's relational FLAC
// block-size constraint; CompareStreamLayouts reports it with its own
// message because the standard "re-encode with the same profile"
// remedy does not apply — the maximum block size depends on how the
// source's audio chunks fall inside each cut, so the same profile can
// produce both directions.
const flacMaxBlocksizeField = "flac max block size"

// streamKeyMismatch returns the first differing field and both values,
// compared in a stable order so reports are deterministic.
func streamKeyMismatch(reference, current StreamKey) (field, refVal, curVal string) {
	switch {
	case current.CodecType != reference.CodecType:
		return "codec_type", reference.CodecType, current.CodecType
	case current.CodecName != reference.CodecName:
		return "codec_name", reference.CodecName, current.CodecName
	case current.Profile != reference.Profile:
		return "profile", reference.Profile, current.Profile
	case current.PixelFormat != reference.PixelFormat:
		return "pix_fmt", reference.PixelFormat, current.PixelFormat
	case current.TimeBase != reference.TimeBase:
		return "time_base", reference.TimeBase, current.TimeBase
	case current.SAR != reference.SAR:
		return "sample_aspect_ratio", reference.SAR, current.SAR
	case current.Width != reference.Width:
		return "width", fmt.Sprint(reference.Width), fmt.Sprint(current.Width)
	case current.Height != reference.Height:
		return "height", fmt.Sprint(reference.Height), fmt.Sprint(current.Height)
	case current.SampleRate != reference.SampleRate:
		return "sample_rate", fmt.Sprint(reference.SampleRate), fmt.Sprint(current.SampleRate)
	case current.Channels != reference.Channels:
		return "channels", fmt.Sprint(reference.Channels), fmt.Sprint(current.Channels)
	case current.ChannelLayout != reference.ChannelLayout:
		return "channel_layout", reference.ChannelLayout, current.ChannelLayout
	case current.Extradata != reference.Extradata:
		return "extradata", reference.Extradata, current.Extradata
	case current.FLACMaxBlockSize != 0 && reference.FLACMaxBlockSize != 0 &&
		current.FLACMaxBlockSize > reference.FLACMaxBlockSize:
		// Not an equality: frames at or below segment 1's declared
		// maximum decode fine (verified — maxima 955 then 327 decode
		// completely), only frames above it are dropped.
		return flacMaxBlocksizeField,
			strconv.Itoa(reference.FLACMaxBlockSize), strconv.Itoa(current.FLACMaxBlockSize)
	}
	return "", "", ""
}

// parseHexDump decodes an ffprobe hexdump — "00000000: 03bc 03bc …
// ascii" lines — into bytes. The ASCII column is separated from the
// hex groups by two spaces and everything after it is ignored; any
// line that does not parse reports failure.
func parseHexDump(dump string) ([]byte, bool) {
	var out []byte
	for _, line := range strings.Split(dump, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		colon := strings.Index(line, ":")
		if colon < 0 {
			return nil, false
		}
		hexPart := line[colon+1:]
		if sep := strings.Index(hexPart, "  "); sep >= 0 {
			hexPart = hexPart[:sep]
		}
		for _, group := range strings.Fields(hexPart) {
			b, err := hex.DecodeString(group)
			if err != nil {
				return nil, false
			}
			out = append(out, b...)
		}
	}
	return out, true
}
