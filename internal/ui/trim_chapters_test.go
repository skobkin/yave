package ui

import (
	"reflect"
	"testing"
	"time"

	"git.skobk.in/skobkin/yave/internal/model"
)

func TestChaptersToSegments(t *testing.T) {
	s := time.Second
	chapters := []model.Chapter{
		{Title: "Intro", Start: 0, End: 10 * s},
		{Title: "Main", Start: 10 * s, End: 60 * s},
		{Start: 60 * s, End: 60 * s}, // zero length
		{Title: "Outro", Start: 60 * s, End: 90 * s},
	}
	tests := []struct {
		name        string
		chapters    []model.Chapter
		picked      []int
		duration    time.Duration
		want        []model.TimeRange
		wantSkipped []string
	}{
		{
			name: "all usable chapters", chapters: chapters, picked: []int{0, 1, 3}, duration: 100 * s,
			want: []model.TimeRange{{Start: 0, End: 10 * s}, {Start: 10 * s, End: 60 * s}, {Start: 60 * s, End: 90 * s}},
		},
		{
			name: "picked order and duplicates do not matter", chapters: chapters, picked: []int{3, 0, 3, 0}, duration: 100 * s,
			want: []model.TimeRange{{Start: 0, End: 10 * s}, {Start: 60 * s, End: 90 * s}},
		},
		{
			name: "a zero-length chapter is skipped and reported", chapters: chapters, picked: []int{1, 2}, duration: 100 * s,
			want:        []model.TimeRange{{Start: 10 * s, End: 60 * s}},
			wantSkipped: []string{"3. Chapter 3: zero length"},
		},
		{
			name: "out-of-range indexes are ignored", chapters: chapters, picked: []int{-1, 0, 9}, duration: 100 * s,
			want: []model.TimeRange{{Start: 0, End: 10 * s}},
		},
		{
			name: "nothing picked", chapters: chapters, picked: nil, duration: 100 * s,
			want: nil,
		},
		{
			name: "ends clamp to the media duration", chapters: chapters, picked: []int{3}, duration: 75 * s,
			want: []model.TimeRange{{Start: 60 * s, End: 75 * s}},
		},
		{
			name:     "a chapter starting past the end is skipped",
			chapters: []model.Chapter{{Title: "Late", Start: 80 * s, End: 90 * s}}, picked: []int{0}, duration: 70 * s,
			want:        nil,
			wantSkipped: []string{"1. Late: zero length"},
		},
		{
			name: "an overlapping chapter starts where the previous segment ends",
			chapters: []model.Chapter{
				{Title: "A", Start: 0, End: 30 * s}, {Title: "B", Start: 20 * s, End: 50 * s},
			},
			picked: []int{0, 1}, duration: 100 * s,
			want: []model.TimeRange{{Start: 0, End: 30 * s}, {Start: 30 * s, End: 50 * s}},
		},
		{
			name: "a chapter swallowed by the previous one is skipped",
			chapters: []model.Chapter{
				{Title: "Big", Start: 0, End: 50 * s}, {Title: "Inside", Start: 10 * s, End: 20 * s}, {Title: "After", Start: 50 * s, End: 60 * s},
			},
			picked: []int{0, 1, 2}, duration: 100 * s,
			want:        []model.TimeRange{{Start: 0, End: 50 * s}, {Start: 50 * s, End: 60 * s}},
			wantSkipped: []string{"2. Inside: overlaps the previous chapter"},
		},
		{
			name: "unknown duration does not clamp", chapters: chapters, picked: []int{3}, duration: 0,
			want: []model.TimeRange{{Start: 60 * s, End: 90 * s}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, skipped := chaptersToSegments(tt.chapters, tt.picked, tt.duration)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("segments = %v, want %v", got, tt.want)
			}
			if !reflect.DeepEqual(skipped, tt.wantSkipped) {
				t.Errorf("skipped = %q, want %q", skipped, tt.wantSkipped)
			}
		})
	}
}

func TestChaptersToSegmentsSatisfyValidationRules(t *testing.T) {
	// Whatever the chapters look like, the result must be sorted,
	// non-overlapping and inside the media.
	s := time.Second
	chapters := []model.Chapter{
		{Start: 50 * s, End: 80 * s}, {Start: 0, End: 70 * s}, {Start: 60 * s, End: 65 * s}, {Start: 90 * s, End: 400 * s},
	}
	got, _ := chaptersToSegments(chapters, []int{0, 1, 2, 3}, 100*s)
	for i, seg := range got {
		if seg.Start < 0 || seg.End > 100*s || seg.End <= seg.Start {
			t.Errorf("segment %d %v is outside the media or empty", i, seg)
		}
		if i > 0 && seg.Start < got[i-1].End {
			t.Errorf("segment %d %v overlaps %v", i, seg, got[i-1])
		}
	}
}

func TestChapterLabelAndMarkers(t *testing.T) {
	titled := model.Chapter{Title: "Intro", Start: time.Second, End: 2 * time.Second}
	if got := chapterLabel(0, titled); got != "Intro" {
		t.Errorf("label = %q", got)
	}
	if got := chapterLabel(2, model.Chapter{}); got != "Chapter 3" {
		t.Errorf("untitled label = %q, want Chapter 3", got)
	}
	markers := chapterMarkers([]model.Chapter{titled, {Start: 2 * time.Second, End: 3 * time.Second}})
	want := []timelineMarker{
		{At: time.Second, End: 2 * time.Second, Label: "Intro"},
		{At: 2 * time.Second, End: 3 * time.Second, Label: "Chapter 2"},
	}
	if !reflect.DeepEqual(markers, want) {
		t.Errorf("markers = %v, want %v", markers, want)
	}
}

func TestIsFullRangeSelection(t *testing.T) {
	d := 10 * time.Second
	full := model.TrimSelection{Segments: []model.TimeRange{{Start: 0, End: d}}}
	tests := []struct {
		name string
		sel  model.TrimSelection
		want bool
	}{
		{"untouched default", full, true},
		{"trimmed start", model.TrimSelection{Segments: []model.TimeRange{{Start: time.Second, End: d}}}, false},
		{"trimmed end", model.TrimSelection{Segments: []model.TimeRange{{Start: 0, End: d - time.Second}}}, false},
		{"several segments", model.TrimSelection{Segments: []model.TimeRange{{Start: 0, End: d / 2}, {Start: d / 2, End: d}}}, false},
		{"empty", model.TrimSelection{}, false},
	}
	for _, tt := range tests {
		if got := isFullRangeSelection(tt.sel, d); got != tt.want {
			t.Errorf("%s: got %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestAdjacentMarker(t *testing.T) {
	s := time.Second
	times := []time.Duration{10 * s, 20 * s, 30 * s}
	tests := []struct {
		name   string
		at     time.Duration
		dir    int
		want   time.Duration
		wantOK bool
	}{
		{"next from the start", 0, 1, 10 * s, true},
		{"next skips the marker under the playhead", 20 * s, 1, 30 * s, true},
		{"previous skips the marker under the playhead", 20 * s, -1, 10 * s, true},
		{"playhead a hair off a marker still skips it", 20*s + 400*time.Microsecond, 1, 30 * s, true},
		{"between markers, next", 25 * s, 1, 30 * s, true},
		{"between markers, previous", 25 * s, -1, 20 * s, true},
		{"no later marker", 30 * s, 1, 0, false},
		{"no earlier marker", 10 * s, -1, 0, false},
		{"before everything, previous", 5 * s, -1, 0, false},
	}
	for _, tt := range tests {
		got, ok := adjacentMarker(times, tt.at, tt.dir)
		if ok != tt.wantOK || got != tt.want {
			t.Errorf("%s: got (%s, %v), want (%s, %v)", tt.name, got, ok, tt.want, tt.wantOK)
		}
	}
	if _, ok := adjacentMarker(nil, 0, 1); ok {
		t.Error("no markers: nothing to jump to")
	}
}
