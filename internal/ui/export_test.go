package ui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"fyne.io/fyne/v2/container"
	fynetest "fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"

	"git.skobk.in/skobkin/yave/internal/model"
	"git.skobk.in/skobkin/yave/internal/templates"
	"git.skobk.in/skobkin/yave/internal/transcode"
)

// --- helpers ---

// exportTestController builds a controller for export tests: real
// widgets and tabs (runJob's completion path refreshes the UI), an
// isolated cache dir so run logs never touch the developer's machine,
// a fixed executable snapshot in the config, and a channel-based
// uiDispatch the tests pump so every widget write lands on the test
// goroutine.
// exportTestController returns the controller, the output workspace
// directory, and the UI dispatch queue the tests pump.
func exportTestController(t *testing.T) (*Controller, string, <-chan func()) {
	t.Helper()
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	app := fynetest.NewApp()
	t.Cleanup(app.Quit)
	dir := t.TempDir() // workspace for the input, outputs, and staging
	c := &Controller{
		config: model.Config{
			FFmpeg: model.FFmpegConfig{
				FFmpegPath:  "/usr/bin/ffmpeg",
				FFprobePath: "/usr/bin/ffprobe",
			},
			Profiles: []model.Profile{{
				Name: "test", Container: model.ContainerMKV,
				OutputTemplate: "{dir}/{name}_test.{ext}",
				Video:          model.VideoSettings{Encoder: "libx264"},
				Audio:          model.AudioSettings{Mode: model.AudioCopy, Encoder: "copy"},
			}},
		},
		media:     model.MediaInfo{Duration: 30 * time.Second},
		inputPath: filepath.Join(dir, "export-input.mkv"),
	}
	c.previews = newTrimPreviews()
	c.runStages = (transcode.Runner{}).RunSequence
	updates := make(chan func(), 64)
	c.uiDispatch = func(f func()) { updates <- f }
	c.createWidgets()
	c.profile.Options = c.profileNames()
	c.profile.Selected = "test"
	c.container.Selected = model.ContainerMKV
	c.output.Text = "{dir}/{name}_test.{ext}"
	c.runTab = container.NewTabItem("Run", widget.NewLabel("run"))
	c.tabs = container.NewAppTabs(container.NewTabItem("Input", widget.NewLabel("input")), c.runTab)
	return c, dir, updates
}

// exportTestProfile is the materialized profile the tests run with:
// the same template the controller's widget state carries, so
// resolveOutput lands next to the input.
func exportTestProfile() model.Profile {
	return model.Profile{
		Name: "test", Container: model.ContainerMKV,
		OutputTemplate: "{dir}/{name}_test.{ext}",
		Video:          model.VideoSettings{Encoder: "libx264"},
		Audio:          model.AudioSettings{Mode: model.AudioCopy, Encoder: "copy"},
	}
}

// setTrimSegments activates trimming with the given segments and makes
// both boundary entries valid, mirroring a committed selection.
func setTrimSegments(c *Controller, segments ...model.TimeRange) {
	c.trimEnabled = true
	c.trimSelection = model.TrimSelection{Segments: segments}
	c.trimSelected = 0
	c.trimInValid, c.trimOutValid = true, true
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path) //nolint:gosec // G304: test-owned temp paths
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func overrideLink(t *testing.T, f func(string, string) error) {
	t.Helper()
	orig := linkFile
	linkFile = f
	t.Cleanup(func() { linkFile = orig })
}

func overrideRemove(t *testing.T, f func(string) error) {
	t.Helper()
	orig := removeFile
	removeFile = f
	t.Cleanup(func() { removeFile = orig })
}

func writeScript(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	// 0755 is required for direct execution of the fake script.
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755); err != nil { //nolint:gosec // G306: executable script
		t.Fatal(err)
	}
	return path
}

// errScriptCancel tells fakeRunStages to emulate a cancellation
// arriving before the stage's publication: bare sentinel, nothing
// published.
var errScriptCancel = errors.New("stage script: cancel")

// fakeRunStages installs a runStages seam that emulates RunSequence's
// contract with a per-stage script: returning errScriptCancel fails the
// stage with the bare cancellation sentinel after the preceding stages
// published; any other error wraps with the stage label like
// RunSequence does; nil runs the stage's publication and reports done.
// The returned channel closes when the seam returns.
func fakeRunStages(c *Controller, script func(i int, stage transcode.Stage) error) <-chan struct{} {
	done := make(chan struct{})
	c.runStages = func(_ *transcode.RunControl, stages []transcode.Stage, cb transcode.Callbacks) error {
		defer close(done)
		for i, stage := range stages {
			scriptErr := script(i, stage)
			switch {
			case errors.Is(scriptErr, errScriptCancel):
				cb.OnStageDone(i, len(stages), stage, context.Canceled)
				return context.Canceled
			case scriptErr != nil:
				cb.OnStageDone(i, len(stages), stage, scriptErr)
				return fmt.Errorf("%s: %w", stage.Label, scriptErr)
			}
			if stage.Publish != nil {
				if err := stage.Publish(); err != nil {
					cb.OnStageDone(i, len(stages), stage, err)
					return fmt.Errorf("%s: %w", stage.Label, err)
				}
			}
			cb.OnStageDone(i, len(stages), stage, nil)
		}
		return nil
	}
	return done
}

// pumpUntil drains the UI dispatch queue, executing every closure on
// the test goroutine, until cond holds or the deadline passes. Every
// widget write of a run reaches the test through this, so assertions
// have a happens-before edge and -race stays quiet.
func pumpUntil(t *testing.T, updates <-chan func(), cond func() bool, what string) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case f := <-updates:
			f()
			if cond() {
				return
			}
		case <-deadline:
			t.Fatalf("%s: timed out", what)
		}
	}
}

// --- publishFile ---

func TestPublishFile(t *testing.T) {
	t.Run("approved overwrite replaces the existing file", func(t *testing.T) {
		dir := t.TempDir()
		staged, final := filepath.Join(dir, "staged"), filepath.Join(dir, "final.mkv")
		writeFile(t, staged, "new")
		writeFile(t, final, "old")
		published, warning, err := publishFile(staged, final, true)
		if err != nil || warning != nil {
			t.Fatalf("publish = %v / %v, want success", warning, err)
		}
		if !published {
			t.Fatal("approved overwrite must report published")
		}
		if got := readFile(t, final); got != "new" {
			t.Fatalf("final = %q, want the staged content", got)
		}
		if _, statErr := os.Stat(staged); !os.IsNotExist(statErr) {
			t.Fatal("rename must move the staged file away")
		}
	})

	t.Run("unapproved over an existing file fails and leaves it intact", func(t *testing.T) {
		dir := t.TempDir()
		staged, final := filepath.Join(dir, "staged"), filepath.Join(dir, "final.mkv")
		writeFile(t, staged, "new")
		writeFile(t, final, "old")
		published, warning, err := publishFile(staged, final, false)
		if published || warning != nil || err == nil {
			t.Fatalf("publish = %v / %v / %v, want a failure", published, warning, err)
		}
		if got := readFile(t, final); got != "old" {
			t.Fatalf("existing file changed to %q, want old content intact", got)
		}
	})

	t.Run("unapproved to an absent path publishes by hardlink", func(t *testing.T) {
		dir := t.TempDir()
		staged, final := filepath.Join(dir, "staged"), filepath.Join(dir, "final.mkv")
		writeFile(t, staged, "data")
		published, warning, err := publishFile(staged, final, false)
		if err != nil || warning != nil || !published {
			t.Fatalf("publish = %v / %v / %v, want success", published, warning, err)
		}
		if got := readFile(t, final); got != "data" {
			t.Fatalf("final = %q, want the staged content", got)
		}
		if _, statErr := os.Stat(staged); !os.IsNotExist(statErr) {
			t.Fatal("staged file must be removed after a link publication")
		}
	})

	t.Run("late-appearing target is never overwritten", func(t *testing.T) {
		dir := t.TempDir()
		staged, final := filepath.Join(dir, "staged"), filepath.Join(dir, "final.mkv")
		writeFile(t, staged, "new")
		// The dialog saw no file here; one appeared before publication.
		writeFile(t, final, "late")
		published, _, err := publishFile(staged, final, false)
		if published || err == nil {
			t.Fatalf("publish = %v / %v, want a no-clobber failure", published, err)
		}
		if !strings.Contains(err.Error(), "already exists") {
			t.Fatalf("error = %v, want an actionable already-exists message", err)
		}
		if got := readFile(t, final); got != "late" {
			t.Fatalf("late-appearing file changed to %q", got)
		}
	})

	t.Run("hardlink failure is an actionable error with no fallback", func(t *testing.T) {
		dir := t.TempDir()
		staged, final := filepath.Join(dir, "staged"), filepath.Join(dir, "final.mkv")
		writeFile(t, staged, "data")
		overrideLink(t, func(_, _ string) error {
			// e.g. a filesystem without hardlink support
			return errors.New("operation not supported")
		})
		published, warning, err := publishFile(staged, final, false)
		if published || warning != nil || err == nil {
			t.Fatalf("publish = %v / %v / %v, want a failure", published, warning, err)
		}
		if !strings.Contains(err.Error(), "cannot safely publish without overwriting") {
			t.Fatalf("error = %v, want the actionable no-fallback message", err)
		}
		if _, statErr := os.Stat(final); !os.IsNotExist(statErr) {
			t.Fatal("no fallback may create the final path")
		}
	})

	t.Run("failed staged removal after a successful link is a warning, not a failure", func(t *testing.T) {
		dir := t.TempDir()
		staged, final := filepath.Join(dir, "staged"), filepath.Join(dir, "final.mkv")
		writeFile(t, staged, "data")
		overrideRemove(t, func(_ string) error {
			return errors.New("permission denied")
		})
		published, warning, err := publishFile(staged, final, false)
		if err != nil {
			t.Fatalf("err = %v, want nil: the commit point succeeded", err)
		}
		if !published {
			t.Fatal("published must be true: the link succeeded")
		}
		if warning == nil || !strings.Contains(warning.Error(), "could not remove staging file") {
			t.Fatalf("warning = %v, want the cleanup warning", warning)
		}
		if got := readFile(t, final); got != "data" {
			t.Fatalf("final = %q, want the published content", got)
		}
	})
}

// Per-path approval: a part the user approved for replacement is
// published with rename semantics while a target that appeared after
// the dialog (never approved) is protected by the no-clobber path.
func TestPublishFilePerPathApproval(t *testing.T) {
	dir := t.TempDir()
	staged1, part1 := filepath.Join(dir, "s1"), filepath.Join(dir, "movie_part01.mkv")
	staged2, part2 := filepath.Join(dir, "s2"), filepath.Join(dir, "movie_part02.mkv")
	writeFile(t, staged1, "one")
	writeFile(t, staged2, "two")
	writeFile(t, part1, "old1")

	published, _, err := publishFile(staged1, part1, true)
	if err != nil || !published {
		t.Fatalf("approved part1: %v / %v", published, err)
	}
	if got := readFile(t, part1); got != "one" {
		t.Fatalf("approved replacement = %q", got)
	}

	// part02's target did not exist at dialog time; it appeared since.
	writeFile(t, part2, "late")
	published, _, err = publishFile(staged2, part2, false)
	if published || err == nil {
		t.Fatalf("unapproved part2: %v / %v, want a no-clobber failure", published, err)
	}
	if got := readFile(t, part2); got != "late" {
		t.Fatalf("part2 changed to %q, want it untouched", got)
	}
}

// --- target paths, existing files, dialog message ---

func TestExportTargetPaths(t *testing.T) {
	base := "/out/movie.mkv"
	three := []model.TimeRange{
		{Start: time.Second, End: 2 * time.Second},
		{Start: 4 * time.Second, End: 5 * time.Second},
		{Start: 7 * time.Second, End: 8 * time.Second},
	}
	tests := []struct {
		name string
		mode model.TrimExportMode
		trim model.TrimSelection
		want []string
	}{
		{"no trim", model.TrimExportSeparate, model.TrimSelection{}, []string{base}},
		{"join", model.TrimExportJoin, model.TrimSelection{Segments: three}, []string{base}},
		{"single separate", model.TrimExportSeparate,
			model.TrimSelection{Segments: three[:1]}, []string{base}},
		{"separate three", model.TrimExportSeparate, model.TrimSelection{Segments: three}, []string{
			"/out/movie_part01.mkv", "/out/movie_part02.mkv", "/out/movie_part03.mkv",
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := exportTargetPaths(tt.mode, tt.trim, base)
			if strings.Join(got, "|") != strings.Join(tt.want, "|") {
				t.Fatalf("targets = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestExistingPathsAndPromptMessage(t *testing.T) {
	dir := t.TempDir()
	existing := filepath.Join(dir, "a.mkv")
	missing := filepath.Join(dir, "b.mkv")
	writeFile(t, existing, "x")

	got := existingPaths([]string{existing, missing})
	if len(got) != 1 || got[0] != existing {
		t.Fatalf("existingPaths = %v, want [%s]", got, existing)
	}

	message := overwritePromptMessage(got)
	if !strings.Contains(message, existing) {
		t.Fatalf("prompt %q must list the existing file", message)
	}
	if strings.Contains(message, missing) {
		t.Fatalf("prompt %q must not list missing targets", message)
	}
}

// Regression (Codex review round 2): a per-part target that exists as a
// directory (output_part02.mkv/) passed planning and the overwrite
// prompt, so the export encoded every part and failed only when
// publishing part 2 — rename and link cannot replace a directory. The
// run must be rejected before the dialog, before any encode starts.
func TestUnpublishableTargetRejectsNonRegularFiles(t *testing.T) {
	dir := t.TempDir()
	regular := filepath.Join(dir, "movie_part01.mkv")
	writeFile(t, regular, "x")
	missing := filepath.Join(dir, "movie_part03.mkv")
	directory := filepath.Join(dir, "movie_part02.mkv")
	if err := os.Mkdir(directory, 0o755); err != nil {
		t.Fatal(err)
	}

	if got := unpublishableTarget([]string{missing, regular}); got != "" {
		t.Fatalf("regular and missing targets rejected: %q", got)
	}
	got := unpublishableTarget([]string{regular, directory, missing})
	if !strings.Contains(got, directory) {
		t.Fatalf("directory target not named: %q", got)
	}
}

// Regression (Codex review round 6): a dangling symlink target read as
// absent through os.Stat — no overwrite prompt, the whole segment
// encoded, publication failed with EEXIST. The link exists as a link:
// it belongs in the overwrite prompt and the publication rename can
// replace it, so the preflight must Lstat and treat symlinks as
// publishable.
func TestDanglingSymlinkTargetIsListedAndPublishable(t *testing.T) {
	dir := t.TempDir()
	link := filepath.Join(dir, "movie_part01.mkv")
	if err := os.Symlink(filepath.Join(dir, "gone-target"), link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	missing := filepath.Join(dir, "movie_part02.mkv")

	if got := unpublishableTarget([]string{link, missing}); got != "" {
		t.Fatalf("symlink target rejected: %q", got)
	}
	got := existingPaths([]string{link, missing})
	if len(got) != 1 || got[0] != link {
		t.Fatalf("existingPaths = %v, want the dangling link [%s]", got, link)
	}
}

// --- plan/job shapes ---

func TestPlanExportShapes(t *testing.T) {
	segments := []model.TimeRange{
		{Start: time.Second, End: 5 * time.Second},
		{Start: 8 * time.Second, End: 12 * time.Second},
		{Start: 15 * time.Second, End: 20 * time.Second},
	}

	t.Run("separate stages one encode per part", func(t *testing.T) {
		c, _, _ := exportTestController(t)
		setTrimSegments(c, segments...)
		planned, err := c.planExport(exportRequest{
			profile: exportTestProfile(), baseOutput: "/out/movie.mkv",
			stagingDir: previewStagingName, ffprobePath: "/usr/bin/ffprobe",
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(planned.stages) != 3 || len(planned.outputs) != 3 {
			t.Fatalf("plan = %d stages / %d outputs, want 3/3", len(planned.stages), len(planned.outputs))
		}
		for i, ps := range planned.stages {
			want := templates.SegmentPath("/out/movie.mkv", i+1, 3)
			if ps.stage.OutputPath != want {
				t.Errorf("stage %d output = %s, want %s", i, ps.stage.OutputPath, want)
			}
			if !strings.Contains(ps.stagedPath, previewStagingName+"/") {
				t.Errorf("stage %d staged path %s must live in the staging dir", i, ps.stagedPath)
			}
			if !strings.HasSuffix(ps.stagedPath, ".mkv") {
				t.Errorf("stage %d staged name %s must carry the container extension", i, ps.stagedPath)
			}
			if ps.stage.Duration != segments[i].Length() {
				t.Errorf("stage %d duration = %s, want the segment length", i, ps.stage.Duration)
			}
			if ps.stage.Publish != nil {
				t.Errorf("stage %d must not carry a Publish closure at plan time", i)
			}
		}
	})

	t.Run("join stages intermediates plus a verified concat", func(t *testing.T) {
		c, _, _ := exportTestController(t)
		setTrimSegments(c, segments[:2]...)
		c.trimExportMode = model.TrimExportJoin
		dir := t.TempDir()
		planned, err := c.planExport(exportRequest{
			profile: exportTestProfile(), baseOutput: filepath.Join(dir, "movie.mkv"),
			stagingDir: dir, ffprobePath: "/usr/bin/ffprobe",
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(planned.stages) != 3 {
			t.Fatalf("plan = %d stages, want 2 intermediates + concat", len(planned.stages))
		}
		for i := 0; i < 2; i++ {
			ps := planned.stages[i]
			if ps.stage.OutputPath != "" {
				t.Errorf("intermediate %d exposes output %q, want empty (processing-only)", i, ps.stage.OutputPath)
			}
			// The command itself writes into the staging dir as Matroska.
			if !strings.HasSuffix(ps.stage.Plan.OutputPath, fmt.Sprintf("segment%03d.mkv", i+1)) {
				t.Errorf("intermediate %d command output = %s", i, ps.stage.Plan.OutputPath)
			}
			if ps.stage.Verify != nil {
				t.Errorf("intermediate %d must not verify", i)
			}
		}
		concat := planned.stages[2]
		if concat.stage.OutputPath != filepath.Join(dir, "movie.mkv") {
			t.Fatalf("concat output = %s, want the base output", concat.stage.OutputPath)
		}
		if concat.stage.Verify == nil {
			t.Fatal("concat stage must verify intermediate layouts")
		}
		if concat.stagedPath != planned.stagedFinal {
			t.Fatalf("concat staged path = %s, want the staged final %s", concat.stagedPath, planned.stagedFinal)
		}
		if !strings.HasSuffix(planned.stagedFinal, ".mkv") {
			t.Fatalf("staged final %s must carry the real container extension", planned.stagedFinal)
		}
		// The concat plan is the concat-demuxer invocation over the list.
		args := concat.stage.Plan.Args
		joined := strings.Join(args, " ")
		if !strings.Contains(joined, "-f concat") || !strings.Contains(joined, "-c copy") {
			t.Fatalf("concat args = %v", args)
		}
		if len(planned.concatList) != 2 {
			t.Fatalf("concat list = %v, want both intermediates", planned.concatList)
		}
	})

	t.Run("join re-encodes FLAC audio continuously", func(t *testing.T) {
		// FLAC cannot be stream-copy joined: independently produced
		// segments each finalize their own STREAMINFO and restart frame
		// numbering, and a copied join preserves both — the FLAC
		// reference decoder rejects the result. The concat pass must
		// re-encode FLAC audio as one continuous (lossless) stream,
		// for re-encoded segments and copied source audio alike, and
		// must leave every other audio codec copied.
		c, _, _ := exportTestController(t)
		setTrimSegments(c, segments[:2]...)
		c.trimExportMode = model.TrimExportJoin
		dir := t.TempDir()

		concatArgs := func(profile model.Profile, media model.MediaInfo) string {
			t.Helper()
			c.media = media
			planned, err := c.planExport(exportRequest{
				profile: profile, baseOutput: filepath.Join(dir, "movie.mkv"),
				stagingDir: dir, ffprobePath: "/usr/bin/ffprobe",
			})
			if err != nil {
				t.Fatal(err)
			}
			return strings.Join(planned.stages[len(planned.stages)-1].stage.Plan.Args, " ")
		}

		flacEncode := exportTestProfile()
		flacEncode.Audio = model.AudioSettings{Mode: model.AudioEncode, Encoder: "flac", Compression: 7}
		if joined := concatArgs(flacEncode, model.MediaInfo{Duration: 30 * time.Second}); !strings.Contains(joined, "-c:a flac -compression_level 7") {
			t.Errorf("flac-encode join must re-encode with the profile's settings: %s", joined)
		}

		copied := exportTestProfile()
		flacMedia := model.MediaInfo{
			Duration:     30 * time.Second,
			AudioStreams: []model.AudioStream{{Codec: "flac"}},
		}
		if joined := concatArgs(copied, flacMedia); !strings.Contains(joined, "-c copy -c:a flac") || strings.Contains(joined, "-compression_level") {
			t.Errorf("copied FLAC source must re-encode with encoder defaults: %s", joined)
		}

		aacMedia := model.MediaInfo{
			Duration:     30 * time.Second,
			AudioStreams: []model.AudioStream{{Codec: "aac"}},
		}
		if joined := concatArgs(exportTestProfile(), aacMedia); strings.Contains(joined, "-c:a") {
			t.Errorf("non-FLAC audio must stay copied: %s", joined)
		}
	})

	t.Run("relative output gets absolute concat-list entries", func(t *testing.T) {
		// A template without {dir} resolves to a path relative to the
		// process working directory, and the staging directory would be
		// relative with it. The concat demuxer interprets list entries
		// against the list file's directory, so a relative entry like
		// "yave-export-X/segment001.mkv" would be re-resolved under the
		// staging directory again and the join could not open the
		// intermediates. The job must absolutize the staging directory.
		c, _, _ := exportTestController(t)
		t.Chdir(t.TempDir()) // scratch working directory for the relative output
		setTrimSegments(c, segments[:2]...)
		c.trimExportMode = model.TrimExportJoin
		job, err := c.buildTrimExportJob(exportTestProfile(), "movie_joined.mkv", nil)
		if err != nil {
			t.Fatal(err)
		}
		defer job.cleanup()
		if !filepath.IsAbs(job.tempDir) {
			t.Fatalf("staging dir = %s, want absolute", job.tempDir)
		}
		list := readFile(t, filepath.Join(job.tempDir, "concat.txt"))
		for _, part := range job.stages[:2] {
			if !filepath.IsAbs(part.Plan.OutputPath) {
				t.Fatalf("intermediate %s must be absolute for the concat list", part.Plan.OutputPath)
			}
			if !strings.Contains(list, part.Plan.OutputPath) {
				t.Fatalf("concat list %q misses %s", list, part.Plan.OutputPath)
			}
		}
	})

	t.Run("job attaches publication only to user-visible stages", func(t *testing.T) {
		c, dir, _ := exportTestController(t)
		setTrimSegments(c, segments[:2]...)
		c.trimExportMode = model.TrimExportJoin
		resolved, err := c.resolveOutput(exportTestProfile(), time.Unix(0, 0))
		if err != nil {
			t.Fatal(err)
		}
		job, err := c.buildTrimExportJob(exportTestProfile(), resolved, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer job.cleanup()
		if job.stages[0].Publish != nil || job.stages[1].Publish != nil {
			t.Fatal("intermediates must not publish")
		}
		if job.stages[2].Publish == nil {
			t.Fatal("the concat stage must publish the joined output")
		}
		list := readFile(t, job.tempDir+"/concat.txt")
		for _, part := range job.stages[:2] {
			if !strings.Contains(list, filepath.Base(part.Plan.OutputPath)) {
				t.Fatalf("concat list %q misses %s", list, part.Plan.OutputPath)
			}
		}
		if len(job.outputs) != 1 || job.outputs[0] != resolved {
			t.Fatalf("job outputs = %v, want exactly [%s]", job.outputs, resolved)
		}
		_ = dir
	})

	t.Run("single-segment join is one staged encode", func(t *testing.T) {
		c, dir, _ := exportTestController(t)
		setTrimSegments(c, segments[0])
		c.trimExportMode = model.TrimExportJoin
		job, err := c.buildTrimExportJob(exportTestProfile(), filepath.Join(dir, "movie.mkv"), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer job.cleanup()
		if len(job.stages) != 1 {
			t.Fatalf("stages = %d, want a single staged encode", len(job.stages))
		}
		if job.stages[0].Verify != nil {
			t.Fatal("a single-segment join must not verify")
		}
		if job.stages[0].Publish == nil {
			t.Fatal("the staged encode must publish to the output")
		}
	})

	t.Run("no trim stays a direct single run", func(t *testing.T) {
		c, _, _ := exportTestController(t) // trimming disabled
		resolved, err := c.resolveOutput(exportTestProfile(), time.Unix(0, 0))
		if err != nil {
			t.Fatal(err)
		}
		job, err := c.buildTrimExportJob(exportTestProfile(), resolved, map[string]bool{resolved: true})
		if err != nil {
			t.Fatal(err)
		}
		defer job.cleanup()
		if job.tempDir != "" {
			t.Fatalf("tempDir = %s, want no staging for a direct run", job.tempDir)
		}
		if len(job.stages) != 1 || job.stages[0].Publish != nil {
			t.Fatalf("stages = %d, want one unpublished direct stage", len(job.stages))
		}
		if job.stages[0].OutputPath != resolved {
			t.Fatalf("direct stage output = %s, want %s", job.stages[0].OutputPath, resolved)
		}
		// Approval flows into the command the same way the old dialog did.
		if !containsArg(job.stages[0].Plan.Args, "-y") {
			t.Fatalf("approved direct run args = %v, want -y", job.stages[0].Plan.Args)
		}
	})
}

func containsArg(args []string, want string) bool {
	for _, arg := range args {
		if arg == want {
			return true
		}
	}
	return false
}

// The join verification closure must run the ffprobe captured at job
// build time, never the mutable config.
func TestBuildJobCapturesConfig(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell fake executable")
	}
	c, dir, _ := exportTestController(t)
	setTrimSegments(c, []model.TimeRange{
		{Start: time.Second, End: 5 * time.Second},
		{Start: 8 * time.Second, End: 12 * time.Second},
	}...)
	c.trimExportMode = model.TrimExportJoin

	log := filepath.Join(dir, "probe-log.txt")
	t.Setenv("EXPORT_TEST_LOG", log)
	probe := writeScript(t, dir, "fake-ffprobe", `printf '%s\n' "$0" >> "$EXPORT_TEST_LOG"
cat <<'JSON'
{"streams":[{"codec_type":"video","codec_name":"hevc"},{"codec_type":"audio","codec_name":"aac"}]}
JSON
`)
	c.config.FFmpeg.FFmpegPath = filepath.Join(dir, "fake-ffmpeg")
	c.config.FFmpeg.FFprobePath = probe

	resolved, err := c.resolveOutput(exportTestProfile(), time.Unix(0, 0))
	if err != nil {
		t.Fatal(err)
	}
	job, err := c.buildTrimExportJob(exportTestProfile(), resolved, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer job.cleanup()

	// The caller mutates the config after the job exists — a classic
	// Settings-changed-mid-run race the snapshot must absorb.
	c.config.FFmpeg.FFmpegPath = "/changed/ffmpeg"
	c.config.FFmpeg.FFprobePath = "/changed/ffprobe"

	for i, stage := range job.stages {
		if stage.Plan.Executable != filepath.Join(dir, "fake-ffmpeg") {
			t.Errorf("stage %d executable = %s, want the captured path", i, stage.Plan.Executable)
		}
	}
	if job.ffprobePath != probe {
		t.Fatalf("job ffprobe = %s, want the captured path", job.ffprobePath)
	}

	// The verification closure probes with the captured executable.
	if err := job.stages[2].Verify(context.Background()); err != nil {
		t.Fatalf("verify with identical fake streams: %v", err)
	}
	recorded := readFile(t, log)
	if !strings.Contains(recorded, probe) {
		t.Fatalf("probe log %q must show the captured executable %s", recorded, probe)
	}
	if strings.Contains(recorded, "/changed/ffprobe") {
		t.Fatal("verification must never run the mutable config's ffprobe")
	}
}

// A canceled context must interrupt the verification probes: the
// closure surfaces the bare cancellation sentinel, which is what
// RunSequence maps to its canceled-run semantics.
func TestJoinVerificationHonorsCancellation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell fake executable")
	}
	c, dir, _ := exportTestController(t)
	setTrimSegments(c, []model.TimeRange{
		{Start: time.Second, End: 5 * time.Second},
		{Start: 8 * time.Second, End: 12 * time.Second},
	}...)
	c.trimExportMode = model.TrimExportJoin
	c.config.FFmpeg.FFprobePath = writeScript(t, dir, "fake-ffprobe", "exit 1\n")

	resolved, err := c.resolveOutput(exportTestProfile(), time.Unix(0, 0))
	if err != nil {
		t.Fatal(err)
	}
	job, err := c.buildTrimExportJob(exportTestProfile(), resolved, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer job.cleanup()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	verifyErr := job.stages[2].Verify(ctx)
	if !errors.Is(verifyErr, context.Canceled) {
		t.Fatalf("verify error = %v, want the context.Canceled chain", verifyErr)
	}
}

// --- preview/execution agreement ---

func TestRefreshPreviewMatchesJob(t *testing.T) {
	segments := []model.TimeRange{
		{Start: time.Second, End: 5 * time.Second},
		{Start: 8 * time.Second, End: 12 * time.Second},
	}

	t.Run("separate", func(t *testing.T) {
		c, _, _ := exportTestController(t)
		setTrimSegments(c, segments...)
		resolved, err := c.resolveOutput(exportTestProfile(), time.Unix(0, 0))
		if err != nil {
			t.Fatal(err)
		}
		c.renderExportPreview(exportTestProfile(), resolved)
		job, err := c.buildTrimExportJob(exportTestProfile(), resolved, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer job.cleanup()
		if len(job.stages) != 2 {
			t.Fatalf("job stages = %d, want 2", len(job.stages))
		}
		// The command box shows stage 1's command with the representative
		// staging name where the job embeds its real temp dir.
		firstLine, _, _ := strings.Cut(c.command.Text, "\n\n")
		want := strings.ReplaceAll(job.stages[0].Plan.Preview, job.tempDir, previewStagingName)
		if firstLine != want {
			t.Fatalf("preview stage 1 =\n%s\nwant\n%s", firstLine, want)
		}
		if !strings.Contains(c.command.Text, stagedExportNote(len(job.stages))) {
			t.Fatalf("preview %q must carry the multi-stage note", c.command.Text)
		}
		// The resolved-output label lists every part path.
		for _, out := range job.outputs {
			if !strings.Contains(c.outputPreview.Text, out) {
				t.Fatalf("output preview %q misses part %s", c.outputPreview.Text, out)
			}
		}
	})

	t.Run("join", func(t *testing.T) {
		c, _, _ := exportTestController(t)
		setTrimSegments(c, segments...)
		c.trimExportMode = model.TrimExportJoin
		resolved, err := c.resolveOutput(exportTestProfile(), time.Unix(0, 0))
		if err != nil {
			t.Fatal(err)
		}
		c.renderExportPreview(exportTestProfile(), resolved)
		job, err := c.buildTrimExportJob(exportTestProfile(), resolved, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer job.cleanup()
		if len(job.stages) != 3 {
			t.Fatalf("job stages = %d, want 3", len(job.stages))
		}
		firstLine, _, _ := strings.Cut(c.command.Text, "\n\n")
		want := strings.ReplaceAll(job.stages[0].Plan.Preview, job.tempDir, previewStagingName)
		if firstLine != want {
			t.Fatalf("preview stage 1 =\n%s\nwant\n%s", firstLine, want)
		}
		// The label shows the single joined output, not the intermediates.
		if got := c.outputPreview.Text; got != resolved {
			t.Fatalf("output preview = %q, want the joined output %s", got, resolved)
		}
	})

	t.Run("single stage shows the bare command", func(t *testing.T) {
		c, _, _ := exportTestController(t)
		resolved, err := c.resolveOutput(exportTestProfile(), time.Unix(0, 0))
		if err != nil {
			t.Fatal(err)
		}
		c.renderExportPreview(exportTestProfile(), resolved)
		if strings.Contains(c.command.Text, stagedExportNote(1)) {
			t.Fatalf("single-stage preview %q must not carry a stage note", c.command.Text)
		}
	})
}

// --- runJob: publication, counting, and reporting ---

// runTestJob starts runJob with the scripted seam and pumps the UI
// queue until the status settles on a non-running state, returning the
// final status text.
func runTestJob(t *testing.T, c *Controller, updates <-chan func(), job *trimExportJob, script func(i int, stage transcode.Stage) error) string {
	t.Helper()
	done := fakeRunStages(c, script)
	c.runJob(job, nil)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("run did not finish")
	}
	pumpUntil(t, updates, func() bool {
		return c.status.Text != "Running"
	}, "run completion")
	return c.status.Text
}

func publishedCount(log string) int { return strings.Count(log, "published → ") }
func encodedCount(log string) int   { return strings.Count(log, "encoded → ") }

func TestRunJobSeparatePartialFailure(t *testing.T) {
	c, _, updates := exportTestController(t)
	setTrimSegments(c, []model.TimeRange{
		{Start: time.Second, End: 5 * time.Second},
		{Start: 8 * time.Second, End: 12 * time.Second},
	}...)
	resolved, err := c.resolveOutput(exportTestProfile(), time.Unix(0, 0))
	if err != nil {
		t.Fatal(err)
	}
	job, err := c.buildTrimExportJob(exportTestProfile(), resolved, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Stage 1's staged file exists, so its publication succeeds and
	// publishes part01; stage 2's encode fails before publishing.
	writeFile(t, job.stages[0].Plan.OutputPath, "one")

	status := runTestJob(t, c, updates, job, func(i int, _ transcode.Stage) error {
		if i == 1 {
			return errors.New("boom")
		}
		return nil
	})

	want := "Failed: segment 2 of 2: boom (1 of 2 parts written)"
	if status != want {
		t.Fatalf("status = %q, want %q", status, want)
	}
	part1 := templates.SegmentPath(resolved, 1, 2)
	if got := readFile(t, part1); got != "one" {
		t.Fatalf("part01 = %q, want the published content", got)
	}
	part2 := templates.SegmentPath(resolved, 2, 2)
	if _, statErr := os.Stat(part2); !os.IsNotExist(statErr) {
		t.Fatal("part02 must not exist after its stage failed")
	}
	if _, statErr := os.Stat(job.tempDir); !os.IsNotExist(statErr) {
		t.Fatal("staging directory must be cleaned after a failure")
	}
}

func TestRunJobCancelBetweenParts(t *testing.T) {
	c, _, updates := exportTestController(t)
	setTrimSegments(c, []model.TimeRange{
		{Start: time.Second, End: 5 * time.Second},
		{Start: 8 * time.Second, End: 12 * time.Second},
	}...)
	resolved, err := c.resolveOutput(exportTestProfile(), time.Unix(0, 0))
	if err != nil {
		t.Fatal(err)
	}
	job, err := c.buildTrimExportJob(exportTestProfile(), resolved, nil)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, job.stages[0].Plan.OutputPath, "one")

	status := runTestJob(t, c, updates, job, func(i int, _ transcode.Stage) error {
		if i == 1 {
			return errScriptCancel
		}
		return nil
	})

	want := "Canceled (1 of 2 parts written)"
	if status != want {
		t.Fatalf("status = %q, want %q", status, want)
	}
	if got := readFile(t, templates.SegmentPath(resolved, 1, 2)); got != "one" {
		t.Fatalf("part01 = %q, want it preserved across cancellation", got)
	}
}

func TestRunJobJoinOutputCounting(t *testing.T) {
	segments := []model.TimeRange{
		{Start: time.Second, End: 5 * time.Second},
		{Start: 8 * time.Second, End: 12 * time.Second},
	}
	buildJob := func(t *testing.T, c *Controller, probe string) (*trimExportJob, string) {
		t.Helper()
		setTrimSegments(c, segments...)
		c.trimExportMode = model.TrimExportJoin
		if probe != "" {
			c.config.FFmpeg.FFprobePath = probe
		}
		resolved, err := c.resolveOutput(exportTestProfile(), time.Unix(0, 0))
		if err != nil {
			t.Fatal(err)
		}
		job, err := c.buildTrimExportJob(exportTestProfile(), resolved, nil)
		if err != nil {
			t.Fatal(err)
		}
		return job, resolved
	}

	t.Run("cancel during concat claims zero outputs", func(t *testing.T) {
		c, _, updates := exportTestController(t)
		job, resolved := buildJob(t, c, "")
		status := runTestJob(t, c, updates, job, func(i int, _ transcode.Stage) error {
			if i == 2 {
				return errScriptCancel
			}
			return nil
		})
		if status != "Canceled" {
			t.Fatalf("status = %q, want the plain Canceled text", status)
		}
		if n := publishedCount(c.log.Text); n != 0 {
			t.Fatalf("log claims %d published outputs, want zero", n)
		}
		if n := encodedCount(c.log.Text); n != 2 {
			t.Fatalf("log reports %d encoded intermediates, want 2", n)
		}
		if _, statErr := os.Stat(resolved); !os.IsNotExist(statErr) {
			t.Fatal("no output may exist after a canceled join")
		}
	})

	t.Run("failed verification publishes nothing", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("POSIX shell fake executable")
		}
		c, dir, updates := exportTestController(t)
		probe := writeScript(t, dir, "fake-ffprobe", "exit 1\n")
		job, resolved := buildJob(t, c, probe)
		status := runTestJob(t, c, updates, job, func(i int, stage transcode.Stage) error {
			if i != 2 {
				return nil
			}
			return stage.Verify(context.Background())
		})
		if !strings.HasPrefix(status, "Failed: joining segments: verify segment 1 of 2") {
			t.Fatalf("status = %q, want the verification failure", status)
		}
		if n := publishedCount(c.log.Text); n != 0 {
			t.Fatalf("log claims %d published outputs, want zero", n)
		}
		if _, statErr := os.Stat(resolved); !os.IsNotExist(statErr) {
			t.Fatal("verification failure must not publish")
		}
	})

	t.Run("failed publication leaves the existing target untouched", func(t *testing.T) {
		c, _, updates := exportTestController(t)
		job, resolved := buildJob(t, c, "")
		// The dialog approved nothing, and the target exists (it may
		// have appeared after the declined overwrite).
		writeFile(t, resolved, "keep")
		writeFile(t, job.stages[2].Plan.OutputPath, "joined")
		status := runTestJob(t, c, updates, job, func(i int, _ transcode.Stage) error {
			return nil
		})
		if !strings.HasPrefix(status, "Failed: joining segments: publish ") {
			t.Fatalf("status = %q, want the publication failure", status)
		}
		if got := readFile(t, resolved); got != "keep" {
			t.Fatalf("target = %q, want it untouched", got)
		}
		if n := publishedCount(c.log.Text); n != 0 {
			t.Fatalf("log claims %d published outputs, want zero", n)
		}
	})

	t.Run("successful join publishes exactly one output", func(t *testing.T) {
		c, _, updates := exportTestController(t)
		job, resolved := buildJob(t, c, "")
		job.approved = map[string]bool{resolved: true}
		writeFile(t, job.stages[2].Plan.OutputPath, "joined")
		status := runTestJob(t, c, updates, job, func(i int, _ transcode.Stage) error {
			return nil
		})
		if status != "Completed" {
			t.Fatalf("status = %q, want Completed", status)
		}
		if got := readFile(t, resolved); got != "joined" {
			t.Fatalf("joined output = %q", got)
		}
		if n := publishedCount(c.log.Text); n != 1 {
			t.Fatalf("log claims %d published outputs, want exactly 1", n)
		}
		if _, statErr := os.Stat(job.tempDir); !os.IsNotExist(statErr) {
			t.Fatal("staging must be cleaned after a successful join")
		}
	})
}

// Regression: a publication whose commit point succeeded but whose
// staged-file cleanup failed must complete the run and surface the
// cleanup warning in the log — never as a failed status.
func TestRunJobPublicationCleanupWarning(t *testing.T) {
	c, _, updates := exportTestController(t)
	setTrimSegments(c, model.TimeRange{Start: time.Second, End: 5 * time.Second})
	resolved, err := c.resolveOutput(exportTestProfile(), time.Unix(0, 0))
	if err != nil {
		t.Fatal(err)
	}
	job, err := c.buildTrimExportJob(exportTestProfile(), resolved, nil)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, job.stages[0].Plan.OutputPath, "data")
	overrideRemove(t, func(_ string) error { return errors.New("permission denied") })

	status := runTestJob(t, c, updates, job, func(i int, _ transcode.Stage) error {
		return nil
	})
	if status != "Completed" {
		t.Fatalf("status = %q, want Completed: the output published", status)
	}
	if got := readFile(t, resolved); got != "data" {
		t.Fatalf("output = %q", got)
	}
	if !strings.Contains(c.log.Text, "Warning: could not remove staging file") {
		t.Fatalf("log %q must surface the cleanup warning", c.log.Text)
	}
}

// Per-plan warnings deduplicate: every identical segment command
// repeats the subtitle-skip warning, and the log must carry it once.
func TestRunJobWarningsDeduped(t *testing.T) {
	c, _, updates := exportTestController(t)
	c.media = model.MediaInfo{
		Duration:        30 * time.Second,
		SubtitleStreams: []model.SubtitleStream{{Index: 3, Codec: "subrip"}},
	}
	setTrimSegments(c, []model.TimeRange{
		{Start: time.Second, End: 5 * time.Second},
		{Start: 8 * time.Second, End: 12 * time.Second},
	}...)
	p := exportTestProfile()
	p.Subtitles = model.SubtitleSettings{Mode: model.SubtitleCopy}
	resolved, err := c.resolveOutput(p, time.Unix(0, 0))
	if err != nil {
		t.Fatal(err)
	}
	job, err := c.buildTrimExportJob(p, resolved, nil)
	if err != nil {
		t.Fatal(err)
	}

	status := runTestJob(t, c, updates, job, func(i int, _ transcode.Stage) error {
		// Publication fails immediately (staged files do not exist);
		// the warnings were already written before the run started.
		return errScriptCancel
	})
	if status != "Canceled" {
		t.Fatalf("status = %q, want Canceled", status)
	}
	if n := strings.Count(c.log.Text, "trimming is active: copied subtitle streams are skipped"); n != 1 {
		t.Fatalf("subtitle-skip warning appears %d times, want once:\n%s", n, c.log.Text)
	}
}

// The default runStages seam is the real RunSequence: a join run with a
// hanging fake ffprobe is canceled through cancelConversion, returns
// promptly, publishes nothing, and cleans the staging directory.
func TestRunJobCancellableVerification(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell fake executables")
	}
	c, dir, updates := exportTestController(t)
	c.config.FFmpeg.FFmpegPath = writeScript(t, dir, "fake-ffmpeg", "exit 0\n")
	// exec replaces the shell with sleep: the context kill then
	// terminates the process holding the output pipes, so the probe
	// returns promptly instead of waiting out the grandchild's sleep.
	c.config.FFmpeg.FFprobePath = writeScript(t, dir, "fake-ffprobe", "exec sleep 30\n")
	c.cancelDelay = time.Hour // keep the Terminate escalation out of the test

	setTrimSegments(c, []model.TimeRange{
		{Start: time.Second, End: 5 * time.Second},
		{Start: 8 * time.Second, End: 12 * time.Second},
	}...)
	c.trimExportMode = model.TrimExportJoin
	resolved, err := c.resolveOutput(exportTestProfile(), time.Unix(0, 0))
	if err != nil {
		t.Fatal(err)
	}
	job, err := c.buildTrimExportJob(exportTestProfile(), resolved, nil)
	if err != nil {
		t.Fatal(err)
	}

	// runJob's prologue (runControl wiring, log setup) runs
	// synchronously on this goroutine; the run itself proceeds on the
	// goroutine runJob launches, reaching the test through the queue.
	c.runJob(job, nil)

	// The intermediates encode instantly (fake ffmpeg); the concat
	// stage's verification hangs on the fake ffprobe. Cancel there.
	pumpUntil(t, updates, func() bool {
		return c.runControl != nil && encodedCount(c.log.Text) == 2
	}, "run to reach verification")
	start := time.Now()
	c.cancelConversion()
	pumpUntil(t, updates, func() bool {
		return c.status.Text == "Canceled"
	}, "cancellation to complete")
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("cancellation took %s, want a prompt return", elapsed)
	}
	if n := publishedCount(c.log.Text); n != 0 {
		t.Fatalf("log claims %d published outputs, want zero", n)
	}
	if _, statErr := os.Stat(resolved); !os.IsNotExist(statErr) {
		t.Fatal("nothing may be published by a canceled join")
	}
	if _, statErr := os.Stat(job.tempDir); !os.IsNotExist(statErr) {
		t.Fatal("staging must be cleaned after cancellation")
	}
}

// Regression (Codex review round 4): a multi-part separate export with
// input movie.mkv and template {dir}/{name}.{ext} was rejected before
// planning because the resolved base equaled the input — although the
// export never writes the base, only movie_partNN.mkv targets, which
// are validated individually. The multi-part run must resolve the base
// without the input-collision check.
func TestMultiPartExportResolvesBaseEqualInput(t *testing.T) {
	c, dir, _ := exportTestController(t)
	input := filepath.Join(dir, "movie.mkv")
	writeFile(t, input, "x")
	c.inputPath = input

	p := exportTestProfile()
	p.OutputTemplate = "{dir}/{name}.{ext}"

	// The strict resolution still blocks the colliding base (it is a
	// real target for single-target modes); the part-seeded resolution
	// accepts it.
	if _, err := c.resolveOutput(p, time.Unix(0, 0)); err == nil {
		t.Fatal("strict resolution must reject a base equal to the input")
	}
	base, err := c.resolveOutputParts(p, time.Unix(0, 0))
	if err != nil {
		t.Fatalf("multi-part resolution rejected the colliding base: %v", err)
	}
	if base != input {
		t.Fatalf("base = %s, want the input path %s", base, input)
	}

	setTrimSegments(c,
		model.TimeRange{Start: time.Second, End: 2 * time.Second},
		model.TimeRange{Start: 3 * time.Second, End: 4 * time.Second},
	)
	c.trimExportMode = model.TrimExportSeparate
	trim := c.trimRequest()

	// The derived targets never collide with the input and never hit a
	// non-regular file: the run can proceed to planning.
	targets := exportTargetPaths(model.TrimExportSeparate, trim, base)
	if len(targets) != 2 {
		t.Fatalf("targets = %v, want both part paths", targets)
	}
	for _, target := range targets {
		if target == input {
			t.Fatalf("target %s collides with the input", target)
		}
	}
	if problem := unpublishableTarget(targets); problem != "" {
		t.Fatalf("targets rejected: %s", problem)
	}

	// refresh renders the preview with the part-aware resolver: the
	// colliding base must not mark the whole template invalid.
	c.refresh()
	if strings.HasPrefix(c.outputPreview.Text, "Invalid template") {
		t.Fatalf("refresh marked the multi-part template invalid: %q", c.outputPreview.Text)
	}

	job, err := c.buildTrimExportJob(p, base, nil)
	if err != nil {
		t.Fatalf("multi-part planning failed with the colliding base: %v", err)
	}
	defer job.cleanup()
	if len(job.stages) != 2 {
		t.Fatalf("stages = %d, want one encode per part", len(job.stages))
	}
}
