package ffprobe

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestParseCompleteDocument(t *testing.T) {
	data := []byte(`{
	  "streams": [
	    {
	      "index": 0, "codec_name": "hevc", "codec_type": "video",
	      "width": 3840, "height": 2160, "avg_frame_rate": "30000/1001",
	      "r_frame_rate": "30/1", "pix_fmt": "yuv420p10le", "color_range": "tv",
	      "tags": {"language": "eng"}, "disposition": {"default": 1}
	    },
	    {
	      "index": 1, "codec_name": "opus", "codec_type": "audio",
	      "sample_rate": "48000", "channels": 6
	    },
	    {"index": 2, "codec_name": "subrip", "codec_type": "subtitle"},
	    {"index": 3, "codec_name": "mjpeg", "codec_type": "attachment"}
	  ],
	  "chapters": [
	    {"id": 0, "time_base": "1/1000", "start_time": "0.000", "end_time": "10.000", "tags": {"title": "Intro"}}
	  ],
	  "format": {
	    "format_name": "matroska,webm", "duration": "61.250000",
	    "bit_rate": "8123456", "tags": {"title": "Example"}
	  }
	}`)

	got, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	if got.Format != "matroska,webm" || got.Duration != 61*time.Second+250*time.Millisecond || got.Bitrate != 8123456 {
		t.Fatalf("unexpected format metadata: %#v", got)
	}
	if len(got.VideoStreams) != 1 || got.VideoStreams[0].FrameRate != "30000/1001" {
		t.Fatalf("unexpected video streams: %#v", got.VideoStreams)
	}
	if got.VideoStreams[0].Width != 3840 || got.VideoStreams[0].PixelFormat != "yuv420p10le" {
		t.Fatalf("unexpected video metadata: %#v", got.VideoStreams[0])
	}
	if len(got.AudioStreams) != 1 || got.AudioStreams[0].SampleRate != 48000 || got.AudioStreams[0].Channels != 6 {
		t.Fatalf("unexpected audio streams: %#v", got.AudioStreams)
	}
	if len(got.SubtitleStreams) != 1 || got.ChapterCount != 1 {
		t.Fatalf("unexpected subtitle/chapter counts: %#v", got)
	}
}

func TestParseToleratesMissingAndMalformedOptionalFields(t *testing.T) {
	data := []byte(`{
	  "streams": [
	    {"index": 4, "codec_type": "video", "avg_frame_rate": "0/0", "r_frame_rate": "24000/1001"},
	    {"index": 5, "codec_type": "audio", "sample_rate": "N/A"}
	  ],
	  "format": {"duration": "N/A", "bit_rate": ""}
	}`)

	got, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	if got.Duration != 0 || got.Bitrate != 0 || got.AudioStreams[0].SampleRate != 0 {
		t.Fatalf("malformed optional values were not ignored: %#v", got)
	}
	if got.VideoStreams[0].FrameRate != "24000/1001" {
		t.Fatalf("frame rate fallback = %q", got.VideoStreams[0].FrameRate)
	}
}

// Regression: format.duration counts from the container's absolute
// zero only on Matroska, whose Duration element stores the maximum
// block timestamp — with a positive start_time it overshoots the
// playable span (an 8s MKV with start_time 10 reported 18.023s) — so
// trim validation accepted cuts past the video's actual end, which
// ffmpeg cannot seek into and which produce an empty, unreadable
// output. With a negative start_time (-copyts/-itsoffset recordings)
// it undershoots instead (a 4s MKV with start_time -1.028 reported
// 3.009s), and seeding the trim end with that dropped the leading
// second of content. Duration is reported input-relative there.
// Span-based containers (MP4/MOV, MPEG-TS) already report the playable
// span with a nonzero origin — subtracting universally shortened valid
// inputs (an 8s MP4 with start_time 2 reported 6s, an 8s MPEG-TS with
// start_time 3.4 reported 4.6s), making the timeline end inaccessible.
func TestParseNormalizesPositiveOriginDuration(t *testing.T) {
	cases := []struct {
		name            string
		format          string
		duration, start string
		want            time.Duration
	}{
		{"matroska positive origin shifted", "matroska,webm", "18.023000", "10.000000", 8*time.Second + 23*time.Millisecond},
		{"mp4 span preserved", "mov,mp4,m4a,3gp,3g2,mj2", "8.000000", "2.000000", 8 * time.Second},
		{"mov span preserved", "mov,mp4,m4a,3gp,3g2,mj2", "8.000000", "2.000000", 8 * time.Second},
		{"mpegts span preserved", "mpegts", "8.000000", "3.400000", 8 * time.Second},
		{"matroska zero origin unchanged", "matroska,webm", "61.250000", "0.000000", 61*time.Second + 250*time.Millisecond},
		{"matroska missing origin unchanged", "matroska,webm", "12.500000", "", 12*time.Second + 500*time.Millisecond},
		{"matroska negative origin shifted", "matroska,webm", "3.009000", "-1.028000", 4*time.Second + 37*time.Millisecond},
		{"matroska negative origin keeps missing duration", "matroska,webm", "", "-1.500000", 0},
		{"matroska duration not past origin", "matroska,webm", "5.000000", "10.000000", 5 * time.Second},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			data := []byte(`{"format": {"format_name": "` + tc.format + `", "duration": "` + tc.duration + `", "start_time": "` + tc.start + `"}}`)
			got, err := Parse(data)
			if err != nil {
				t.Fatal(err)
			}
			if got.Duration != tc.want {
				t.Errorf("duration = %s, want %s", got.Duration, tc.want)
			}
		})
	}
}

func TestParseRejectsInvalidJSON(t *testing.T) {
	_, err := Parse([]byte(`{"streams":`))
	if err == nil || !strings.Contains(err.Error(), "decode ffprobe JSON") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// Regression, real toolchain: a Matroska recording whose first frames
// sit before timestamp zero (-copyts -itsoffset remux with
// -avoid_negative_ts disabled; the muxer clamps the offset away without
// them) reports a NEGATIVE start_time and a duration that undershoots
// the playable span, because the Matroska Duration element stores the
// maximum block timestamp on the shifted line. The origin must be
// subtracted there too — seeding the trim end with the raw duration
// silently dropped the leading second of content.
func TestParseNegativeOriginMatroskaRealDocument(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test (real ffmpeg + ffprobe)")
	}
	ffmpegPath, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not installed")
	}
	ffprobePath, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("ffprobe not installed")
	}

	dir := t.TempDir()
	input := dir + "/input.mkv"
	generate := exec.CommandContext(context.Background(), ffmpegPath, //nolint:gosec // G204: locally discovered FFmpeg on a t.TempDir file
		"-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc2=duration=4:size=160x120:rate=30",
		"-f", "lavfi", "-i", "sine=duration=4",
		"-c:v", "mpeg4", "-q:v", "3", "-c:a", "aac", input)
	if out, err := generate.CombinedOutput(); err != nil {
		t.Fatalf("generating test media: %v: %s", err, out)
	}
	shifted := dir + "/shifted.mkv"
	shift := exec.CommandContext(context.Background(), ffmpegPath, //nolint:gosec // G204: as above
		"-hide_banner", "-loglevel", "error",
		"-copyts", "-itsoffset", "-1.014", "-i", input,
		"-c", "copy", "-avoid_negative_ts", "disabled", shifted)
	if out, err := shift.CombinedOutput(); err != nil {
		t.Fatalf("shifting test media: %v: %s", err, out)
	}

	info, err := Inspect(context.Background(), ffprobePath, shifted)
	if err != nil {
		t.Fatalf("inspecting shifted media: %v", err)
	}
	// The shifted file's raw duration is ~2.986 (max block timestamp at
	// origin -1.014); the playable span is the full 4s plus muxing slack.
	if info.Duration < 4*time.Second || info.Duration > 4*time.Second+100*time.Millisecond {
		t.Errorf("duration = %s, want ~4.03s (the raw value must be origin-shifted)", info.Duration)
	}
}

func TestInspectRunsExpectedCommand(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test helper is a POSIX shell script")
	}
	dir := t.TempDir()
	argsPath := filepath.Join(dir, "args")
	scriptPath := filepath.Join(dir, "ffprobe")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + shellQuote(argsPath) + "\nprintf '%s\\n' '{\"format\":{\"format_name\":\"matroska\"}}'\n"
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil { //nolint:gosec // G306: test helper script must be executable
		t.Fatal(err)
	}

	input := filepath.Join(dir, "input video.mkv")
	got, err := Inspect(context.Background(), scriptPath, input)
	if err != nil {
		t.Fatal(err)
	}
	if got.Path != input || got.Format != "matroska" {
		t.Fatalf("unexpected media info: %#v", got)
	}
	args, err := os.ReadFile(argsPath) //nolint:gosec // G304: path comes from t.TempDir, safe by construction
	if err != nil {
		t.Fatal(err)
	}
	want := "-v\nerror\n-print_format\njson\n-show_format\n-show_streams\n-show_chapters\n" + input + "\n"
	if string(args) != want {
		t.Fatalf("arguments:\n%s\nwant:\n%s", args, want)
	}
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}
