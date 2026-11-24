package ffmpeg

import (
	"context"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"git.skobk.in/skobkin/yave/internal/model"
)

// TestStreamCopyCutStartsAtThePrecedingKeyframe pins the behavior the
// Trim tab's keyframe UI describes to the user: a stream-copy cut begins
// at the keyframe at or before In (the output carries the whole GOP up to
// In) and ends at Out to within a few frames — only the start snaps. The
// UI shows "the cut starts at keyframe X, N s before In" from keyframe
// positions alone, so this test builds the command with the app's own
// BuildCommand and measures the real output.
//
// The packet span (first to last video packet, including MP4's hidden
// edit-list preroll) is container-independent: it equals End minus the
// keyframe the cut started at.
func TestStreamCopyCutStartsAtThePrecedingKeyframe(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test (real FFmpeg)")
	}
	ffmpegPath, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not installed")
	}
	ffprobePath, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("ffprobe not installed")
	}
	requireEncoders(t, ffmpegPath, "mpeg4")

	dir := t.TempDir()
	input := dir + "/gop.mkv"                                         // keyframes every 3s: 0, 3, 6, 9, 12
	generate := exec.CommandContext(context.Background(), ffmpegPath, //nolint:gosec // G204: locally discovered FFmpeg on a t.TempDir file
		"-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc2=duration=15:size=160x120:rate=30",
		"-c:v", "mpeg4", "-g", "90", "-bf", "0", "-q:v", "3", input)
	if out, err := generate.CombinedOutput(); err != nil {
		t.Fatalf("generating test media: %v: %s", err, out)
	}

	s := time.Second
	cases := []struct {
		name       string
		in, out    time.Duration
		wantSource time.Duration // keyframe the cut must start at
	}{
		{"In mid-GOP", 7500 * time.Millisecond, 12 * s, 6 * s},
		{"In just after a keyframe", 9100 * time.Millisecond, 12 * s, 9 * s},
		{"In exactly on a keyframe", 9 * s, 12 * s, 9 * s},
	}
	for _, container := range []struct{ name, id, ext string }{
		{"mkv", model.ContainerMKV, ".mkv"},
		{"mp4", model.ContainerMP4, ".mp4"},
	} {
		for _, tc := range cases {
			t.Run(container.name+" "+tc.name, func(t *testing.T) {
				profile := model.BuiltinProfiles()[3]
				profile.Video.Encoder = "copy"
				profile.ExcludeAudio = true
				profile.Container = container.id
				output := dir + "/out-" + container.name + strconv.Itoa(int(tc.in/time.Millisecond)) + container.ext
				plan, err := BuildCommand(BuildRequest{
					FFmpegPath: ffmpegPath,
					InputPath:  input,
					OutputPath: output,
					Profile:    profile,
					Media:      model.MediaInfo{VideoStreams: []model.VideoStream{{Width: 160, Height: 120}}},
					Trim:       model.TrimSelection{Segments: []model.TimeRange{{Start: tc.in, End: tc.out}}},
					Overwrite:  true,
				})
				if err != nil {
					t.Fatal(err)
				}
				run := exec.CommandContext(context.Background(), plan.Executable, plan.Args...) //nolint:gosec // G204: the command under test
				if out, err := run.CombinedOutput(); err != nil {
					t.Fatalf("running the copy cut: %v: %s", err, out)
				}

				probe := exec.CommandContext(context.Background(), ffprobePath, //nolint:gosec // G204: locally discovered ffprobe
					"-v", "error", "-select_streams", "v:0", "-show_entries", "packet=pts_time", "-of", "csv=p=0", output)
				out, err := probe.Output()
				if err != nil {
					t.Fatal(err)
				}
				var pts []float64
				for _, line := range strings.Fields(string(out)) {
					if v, err := strconv.ParseFloat(strings.Trim(line, ","), 64); err == nil {
						pts = append(pts, v)
					}
				}
				if len(pts) < 10 {
					t.Fatalf("only %d packets in the output", len(pts))
				}
				sort.Float64s(pts)
				span := time.Duration((pts[len(pts)-1] - pts[0] + 1.0/30) * float64(time.Second))

				// The cut carries the media from the start keyframe to Out.
				want := tc.out - tc.wantSource
				const slack = 150 * time.Millisecond // a few reordered frames at the end
				if span < want-slack || span > want+slack {
					t.Errorf("output spans %s, want %s (Out %s minus the keyframe at %s); an exact-In cut would span %s",
						span, want, tc.out, tc.wantSource, tc.out-tc.in)
				}
			})
		}
	}
}
