package app

import "testing"

// The TestBuildVersion* tests mutate the package-level Version and
// BuildDate vars (set via goreleaser ldflags) and restore them via
// t.Cleanup. Do not mark these tests t.Parallel() — TestWindowTitle in
// internal/ui mutates the same globals and a parallel run would race.

// TestBuildVersion covers the BuildVersion helper.
func TestBuildVersion(t *testing.T) {
	original := Version
	t.Cleanup(func() {
		Version = original
	})

	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "defaults to dev", in: "", want: DevBuildVersion},
		{name: "trims value", in: " 1.2.3 ", want: "1.2.3"},
		{name: "preserves version", in: "v0.5.1", want: "v0.5.1"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			Version = tt.in
			if got := BuildVersion(); got != tt.want {
				t.Fatalf("BuildVersion() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestBuildDateYMD(t *testing.T) {
	original := BuildDate
	t.Cleanup(func() {
		BuildDate = original
	})

	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "empty stays empty", in: "", want: ""},
		{name: "rfc3339 timestamp", in: "2026-06-16T15:30:45Z", want: "2026-06-16"},
		{name: "ymd passes through", in: "2026-06-16", want: "2026-06-16"},
		{name: "ymd with trailing data", in: "2026-06-16T00:00:00Z", want: "2026-06-16"},
		{name: "garbage is returned raw", in: "not-a-date", want: "not-a-date"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			BuildDate = tt.in
			if got := BuildDateYMD(); got != tt.want {
				t.Fatalf("BuildDateYMD() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestBuildVersionWithDate(t *testing.T) {
	originalVersion := Version
	originalBuildDate := BuildDate
	t.Cleanup(func() {
		Version = originalVersion
		BuildDate = originalBuildDate
	})

	tests := []struct {
		name      string
		version   string
		buildDate string
		want      string
	}{
		{name: "version only", version: "1.2.3", buildDate: "", want: "1.2.3"},
		{
			name:      "version with rfc3339 date",
			version:   "1.2.3",
			buildDate: "2026-06-16T15:30:45Z",
			want:      "1.2.3 (2026-06-16)",
		},
		{
			name:      "version with pre-formatted date",
			version:   "1.2.3",
			buildDate: "2026-06-16",
			want:      "1.2.3 (2026-06-16)",
		},
		{name: "dev with date", version: "", buildDate: "2026-06-16", want: "dev (2026-06-16)"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			Version = tt.version
			BuildDate = tt.buildDate
			if got := BuildVersionWithDate(); got != tt.want {
				t.Fatalf("BuildVersionWithDate() = %q, want %q", got, tt.want)
			}
		})
	}
}
