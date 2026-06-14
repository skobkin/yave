package ffprobe

import (
	"context"
	"os"
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

func TestParseRejectsInvalidJSON(t *testing.T) {
	_, err := Parse([]byte(`{"streams":`))
	if err == nil || !strings.Contains(err.Error(), "decode ffprobe JSON") {
		t.Fatalf("unexpected error: %v", err)
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
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
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
	args, err := os.ReadFile(argsPath)
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
