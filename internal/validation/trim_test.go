package validation

import (
	"strings"
	"testing"
	"time"

	"git.skobk.in/skobkin/yave/internal/model"
)

func TestValidateTrim(t *testing.T) {
	t.Parallel()
	duration := 10 * time.Minute
	media := &model.MediaInfo{Duration: duration}
	valid := model.TrimSelection{Segments: []model.TimeRange{{Start: time.Second, End: duration}}}

	tests := []struct {
		name          string
		trim          model.TrimSelection
		media         *model.MediaInfo
		encoder       string
		wantErrors    int
		wantWarnings  int
		wantSubstring string
	}{
		{name: "inactive is always valid", trim: model.TrimSelection{}, media: media, encoder: "copy"},
		{
			name: "valid range at exact end",
			trim: valid, media: media, encoder: "libx264",
		},
		{
			name: "copy warns but stays valid",
			trim: valid, media: media, encoder: "copy",
			wantWarnings: 1, wantSubstring: "keyframes",
		},
		{
			name: "copy is case-insensitive",
			trim: valid, media: media, encoder: "Copy",
			wantWarnings: 1,
		},
		{
			name: "real encoder does not warn",
			trim: valid, media: media, encoder: "hevc_vaapi",
		},
		{
			name: "nil media", trim: valid, media: nil, encoder: "libx264",
			wantErrors: 1, wantSubstring: "known duration",
		},
		{
			name: "zero duration", trim: valid, media: &model.MediaInfo{}, encoder: "libx264",
			wantErrors: 1, wantSubstring: "known duration",
		},
		{
			name:  "negative start",
			trim:  model.TrimSelection{Segments: []model.TimeRange{{Start: -time.Second, End: time.Second}}},
			media: media, encoder: "libx264",
			wantErrors: 1, wantSubstring: "negative",
		},
		{
			name:  "end equals start",
			trim:  model.TrimSelection{Segments: []model.TimeRange{{Start: time.Second, End: time.Second}}},
			media: media, encoder: "libx264",
			wantErrors: 1, wantSubstring: "after start",
		},
		{
			name:  "end before start",
			trim:  model.TrimSelection{Segments: []model.TimeRange{{Start: 5 * time.Second, End: time.Second}}},
			media: media, encoder: "libx264",
			wantErrors: 1, wantSubstring: "after start",
		},
		{
			name:  "start at duration",
			trim:  model.TrimSelection{Segments: []model.TimeRange{{Start: duration, End: duration + time.Second}}},
			media: media, encoder: "libx264",
			wantErrors: 1, wantSubstring: "beyond",
		},
		{
			name:  "end past duration",
			trim:  model.TrimSelection{Segments: []model.TimeRange{{Start: time.Second, End: duration + time.Second}}},
			media: media, encoder: "libx264",
			wantErrors: 1, wantSubstring: "exceeds",
		},
		{
			// Temporary Phase-1 restriction; lifted once multi-segment
			// trimming lands (see model.TrimSelection).
			name: "two segments rejected",
			trim: model.TrimSelection{Segments: []model.TimeRange{
				{Start: time.Second, End: 2 * time.Second},
				{Start: 3 * time.Second, End: 4 * time.Second},
			}},
			media: media, encoder: "libx264",
			wantErrors: 1, wantSubstring: "single segment",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := ValidateTrim(tt.trim, tt.media, tt.encoder)
			if len(result.Errors) != tt.wantErrors {
				t.Errorf("errors = %d (%v), want %d", len(result.Errors), result.Errors, tt.wantErrors)
			}
			if len(result.Warnings) != tt.wantWarnings {
				t.Errorf("warnings = %d (%v), want %d", len(result.Warnings), result.Warnings, tt.wantWarnings)
			}
			if tt.wantSubstring != "" {
				found := false
				for _, issue := range append(append([]Issue{}, result.Errors...), result.Warnings...) {
					if strings.Contains(issue.Message, tt.wantSubstring) {
						found = true
					}
				}
				if !found {
					t.Errorf("no issue mentions %q: %v", tt.wantSubstring, result)
				}
			}
			if tt.wantErrors == 0 && !result.Valid() {
				t.Errorf("expected valid result, got %v", result.Errors)
			}
		})
	}
}

func TestResultMerge(t *testing.T) {
	t.Parallel()
	base := Result{Errors: []Issue{{Severity: SeverityError, Field: "a", Message: "a"}}}
	other := Result{
		Errors:   []Issue{{Severity: SeverityError, Field: "b", Message: "b"}},
		Warnings: []Issue{{Severity: SeverityWarning, Field: "w", Message: "w"}},
	}
	base.Merge(other)
	if len(base.Errors) != 2 || base.Errors[0].Field != "a" || base.Errors[1].Field != "b" {
		t.Errorf("merged errors wrong: %v", base.Errors)
	}
	if len(base.Warnings) != 1 || base.Warnings[0].Field != "w" {
		t.Errorf("merged warnings wrong: %v", base.Warnings)
	}
}
