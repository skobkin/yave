package templates

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestResolveAllPlaceholders(t *testing.T) {
	now := time.Date(2026, 6, 14, 12, 34, 56, 0, time.FixedZone("test", 3*60*60))
	values := Values{
		InputPath: "/videos/My Film.mkv",
		Extension: ".mp4",
		Profile:   "H.264 / Web",
		Codec:     "libx264",
		Width:     1920,
		Height:    1080,
		Time:      now,
	}
	template := "{dir}/{name}_{profile}_{codec}_{width}x{height}_{input_ext}_{datetime}_{date}_{time}.{ext}"

	got, err := Resolve(template, values)
	if err != nil {
		t.Fatal(err)
	}
	want := "/videos/My_Film_H.264_Web_libx264_1920x1080_mkv_20260614-123456_20260614_123456.mp4"
	if got != want {
		t.Fatalf("Resolve() = %q, want %q", got, want)
	}
}

func TestResolveRequiresSuppliedTime(t *testing.T) {
	_, err := Resolve("{dir}/{name}_{date}.{ext}", Values{
		InputPath: "/videos/input.mkv",
		Extension: "mp4",
	})
	if err == nil || !strings.Contains(err.Error(), "time is required") {
		t.Fatalf("Resolve() error = %v", err)
	}
}

func TestResolveRejectsUnknownAndMalformedPlaceholders(t *testing.T) {
	values := Values{InputPath: "/videos/input.mkv", Extension: "mp4", Time: time.Now()}
	for _, template := range []string{
		"{dir}/{unknown}.{ext}",
		"{dir}/{name}_{broken.{ext}",
	} {
		if _, err := Resolve(template, values); err == nil {
			t.Errorf("Resolve(%q) unexpectedly succeeded", template)
		}
	}
}

func TestResolvePreventsTraversal(t *testing.T) {
	values := Values{
		InputPath: "/videos/input.mkv",
		Extension: "mp4",
		Profile:   "../../outside",
		Time:      time.Now(),
	}

	got, err := Resolve("{dir}/{name}_{profile}.{ext}", values)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(got) != "/videos" || strings.Contains(got, "..") {
		t.Fatalf("sanitized placeholder escaped directory: %q", got)
	}

	for _, template := range []string{
		"{dir}/../outside.{ext}",
		"/tmp/{name}.{ext}",
	} {
		if _, err := Resolve(template, values); err == nil {
			t.Errorf("Resolve(%q) unexpectedly succeeded", template)
		}
	}
}

func TestValidateOutput(t *testing.T) {
	if err := ValidateOutput("/videos/input.mkv", "/videos/output.mkv"); err != nil {
		t.Fatal(err)
	}
	if err := ValidateOutput("/videos/input.mkv", "/videos/./input.mkv"); err == nil {
		t.Fatal("ValidateOutput() accepted source as output")
	}
}

func TestResolveRejectsSourceAsOutput(t *testing.T) {
	_, err := Resolve("{dir}/{name}.{input_ext}", Request{
		InputPath: "/videos/input.mkv",
		Time:      time.Now(),
	})
	if err == nil || !strings.Contains(err.Error(), "same as the input") {
		t.Fatalf("Resolve() error = %v", err)
	}
}

func TestValidateOutputRejectsSameExistingFile(t *testing.T) {
	dir := t.TempDir()
	input := filepath.Join(dir, "input.mkv")
	alias := filepath.Join(dir, "alias.mkv")
	if err := os.WriteFile(input, []byte("video"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(input, alias); err != nil {
		t.Fatal(err)
	}
	if err := ValidateOutput(input, alias); err == nil {
		t.Fatal("ValidateOutput() accepted a hard link to the source")
	}
}

func TestSegmentPath(t *testing.T) {
	tests := []struct {
		name  string
		path  string
		index int
		count int
		want  string
	}{
		{name: "single segment unchanged", path: "/out/movie.mkv", index: 1, count: 1, want: "/out/movie.mkv"},
		{name: "zero count unchanged", path: "/out/movie.mkv", index: 1, count: 0, want: "/out/movie.mkv"},
		{name: "two digits for small counts", path: "/out/movie.mkv", index: 1, count: 3, want: "/out/movie_part01.mkv"},
		{name: "second part", path: "/out/movie.mkv", index: 2, count: 3, want: "/out/movie_part02.mkv"},
		{name: "width grows at ten parts", path: "/out/movie.mkv", index: 10, count: 12, want: "/out/movie_part10.mkv"},
		{name: "width grows at hundred parts", path: "/out/movie.mkv", index: 1, count: 100, want: "/out/movie_part001.mkv"},
		{name: "extension without dot prefix", path: "clip.mp4", index: 2, count: 2, want: "clip_part02.mp4"},
		{name: "no extension appends", path: "/out/movie", index: 1, count: 2, want: "/out/movie_part01"},
		{name: "dotfile keeps suffix at end", path: "/out/.hidden", index: 1, count: 2, want: "/out/.hidden_part01"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := SegmentPath(tt.path, tt.index, tt.count); got != tt.want {
				t.Fatalf("SegmentPath(%q, %d, %d) = %q, want %q", tt.path, tt.index, tt.count, got, tt.want)
			}
		})
	}
}
