package ui

import (
	"testing"

	"git.skobk.in/skobkin/yave/internal/app"
)

// TestWindowTitle mutates the package-level app.Version and app.BuildDate
// vars (set via goreleaser ldflags) and restores them via t.Cleanup. Do
// not mark this test t.Parallel() — the global mutation is not safe to
// run concurrently with TestBuildVersion* in internal/app.
func TestWindowTitle(t *testing.T) {
	originalVersion := app.Version
	originalBuildDate := app.BuildDate
	t.Cleanup(func() {
		app.Version = originalVersion
		app.BuildDate = originalBuildDate
	})

	tests := []struct {
		name      string
		version   string
		buildDate string
		want      string
	}{
		{
			name:    "dev build omits version",
			version: app.DevBuildVersion,
			want:    baseTitle,
		},
		{
			name:    "empty version falls back to base title",
			version: "",
			want:    baseTitle,
		},
		{
			name:    "release build appends version",
			version: "1.2.3",
			want:    baseTitle + " 1.2.3",
		},
		{
			name:      "release build with date shows only version in title",
			version:   "1.2.3",
			buildDate: "2026-06-16T15:30:45Z",
			want:      baseTitle + " 1.2.3",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app.Version = tt.version
			app.BuildDate = tt.buildDate
			if got := windowTitle(); got != tt.want {
				t.Fatalf("windowTitle() = %q, want %q", got, tt.want)
			}
		})
	}
}
