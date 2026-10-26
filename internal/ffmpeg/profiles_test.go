package ffmpeg

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"

	"git.skobk.in/skobkin/yave/internal/model"
)

func TestParseProfileValues(t *testing.T) {
	tests := []struct {
		name   string
		output string
		want   []string
	}{
		{
			name: "int enumeration with following option",
			output: `  -profile           <int>        E..V....... Set profile (profile_idc and constraint_set*_flag) (from -99 to 65535) (default -99)
     constrained_baseline 578          E..V.......
     main            77           E..V.......
     high            100          E..V.......
     high10          110          E..V.......
  -level             <int>        E..V....... Set level (level_idc) (from -99 to 255) (default -99)
     1               10           E..V.......
`,
			want: []string{"constrained_baseline", "main", "high", "high10"},
		},
		{
			name: "int enumeration at end of output",
			output: `Encoder hevc_qsv:
  -profile           <int>        E..V....... (from 0 to INT_MAX) (default unknown)
     unknown         0            E..V.......
     main            1            E..V.......
     main10          2            E..V.......
`,
			want: []string{"unknown", "main", "main10"},
		},
		{
			name:   "string option enumerates nothing",
			output: "  -profile           <string>     E..V....... Set profile restrictions (cf. x264 --fullhelp)\n",
			want:   nil,
		},
		{
			name:   "absent option enumerates nothing",
			output: "Encoder libsvtav1:\n  -preset            <int>        E..V....... (from 0 to 13)\n",
			want:   nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseProfileValues(tt.output)
			if !slices.Equal(got, tt.want) {
				t.Errorf("parseProfileValues() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestIntersectProfiles(t *testing.T) {
	curated := []string{"main", "main10", "rext"}

	got := intersectProfiles(curated, []string{"unknown", "MAIN", "main10", "mainsp", "rext", "scc"})
	want := []string{"main", "main10", "rext"}
	if !slices.Equal(got, want) {
		t.Errorf("intersectProfiles() = %v, want %v (curated order, case-insensitive)", got, want)
	}

	got = intersectProfiles(curated, []string{"main10"})
	if !slices.Equal(got, []string{"main10"}) {
		t.Errorf("intersectProfiles() = %v, want [main10]", got)
	}

	// A build enumerating nothing curated must not narrow to empty.
	if got := intersectProfiles(curated, []string{"bogus"}); got != nil {
		t.Errorf("intersectProfiles() = %v, want nil for disjoint lists", got)
	}
	if got := intersectProfiles(curated, nil); got != nil {
		t.Errorf("intersectProfiles() = %v, want nil for empty enumeration", got)
	}
}

// TestDiscoverNarrowsProfiles checks the discovery path end to end
// with a fake ffmpeg: h264_vaapi's enumeration misses
// constrained_baseline, so the capability list narrows; libx264
// enumerates nothing and keeps its curated list; a probe failure
// (exit 2) also keeps the curated list.
func TestDiscoverNarrowsProfiles(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test helper is a POSIX shell script")
	}
	dir := t.TempDir()
	ffmpegPath := writeExecutable(t, dir, "ffmpeg", `#!/bin/sh
case "$2" in
  -version) printf 'ffmpeg version test-1\n' ;;
  -encoders) printf 'Encoders:\n V..... h264_vaapi test\n V..... libx264 test\n' ;;
  -hwaccels) printf 'Hardware acceleration methods:\n' ;;
  -h)
    case "$3" in
      # h264_vaapi's build enumerates but lacks constrained_baseline.
      encoder=h264_vaapi) printf '  -profile <int> E\n     main 77 E\n     high 100 E\n' ;;
      # libx264's probe fails: fail open to the curated list.
      *) exit 2 ;;
    esac ;;
  *) exit 2 ;;
esac
`)
	ffprobePath := writeExecutable(t, dir, "ffprobe", `#!/bin/sh
printf 'ffprobe version test-2\n'
`)

	got, err := Discover(context.Background(), model.FFmpegConfig{
		FFmpegPath:  ffmpegPath,
		FFprobePath: ffprobePath,
	})
	if err != nil {
		t.Fatal(err)
	}

	vaapi, ok := got.Encoders["h264_vaapi"]
	if !ok {
		t.Fatal("h264_vaapi not discovered")
	}
	if !slices.Equal(vaapi.Profiles, []string{"main", "high"}) {
		t.Errorf("h264_vaapi Profiles = %v, want [main high] (constrained_baseline dropped by build probe)", vaapi.Profiles)
	}

	x264, ok := got.Encoders["libx264"]
	if !ok {
		t.Fatal("libx264 not discovered")
	}
	// libx264's probe fails (exit 2), exercising the fail-open path.
	if !slices.Equal(x264.Profiles, []string{"baseline", "main", "high"}) {
		t.Errorf("libx264 Profiles = %v, want curated list unchanged", x264.Profiles)
	}
}

// TestProbeEncoderProfilesExecFailure pins the fail-open contract: a
// failing probe returns an error and no values.
func TestProbeEncoderProfilesExecFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test helper is a POSIX shell script")
	}
	dir := t.TempDir()
	broken := writeExecutable(t, dir, "broken-ffmpeg", "#!/bin/sh\nexit 3\n")

	values, err := probeEncoderProfiles(context.Background(), broken, "h264_vaapi")
	if err == nil {
		t.Fatal("expected probe error")
	}
	if values != nil {
		t.Errorf("values = %v, want nil on error", values)
	}
	_ = os.Remove(filepath.Join(dir, "broken-ffmpeg"))
}
