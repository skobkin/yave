package ui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"git.skobk.in/skobkin/yave/internal/ffmpeg"
	"git.skobk.in/skobkin/yave/internal/ffprobe"
	"git.skobk.in/skobkin/yave/internal/model"
	"git.skobk.in/skobkin/yave/internal/templates"
	"git.skobk.in/skobkin/yave/internal/transcode"
)

// Staging naming: real staging directories are created with
// stagingPattern; previews substitute previewStagingName so rendering a
// command preview never creates a directory.
const (
	stagingPattern     = "yave-export-*"
	previewStagingName = "yave-export-preview"
)

// publishFile is injectable in tests (hardlink publication is
// filesystem-dependent); production always uses the os functions.
var (
	linkFile   = os.Link
	removeFile = os.Remove
)

// trimExportJob is the fully realized export plan: every stage's
// command, publication closure, and the staging directory. It is built
// once on the UI thread — including the ffmpeg/ffprobe config snapshot
// — so the run goroutine and every stage closure read only the job and
// never the mutable controller state.
type trimExportJob struct {
	stages      []transcode.Stage
	outputs     []string        // user-visible targets, in publication order
	approved    map[string]bool // per-path overwrite approval from the dialog
	tempDir     string          // staging dir next to the output; "" for direct runs
	ffprobePath string          // config captured at build time
	ffmpegPath  string          // ditto — stage closures never read c.config
	warnings    exportWarnings  // publication cleanup warnings, drained at completion
}

// cleanup removes the staging directory. Safe to call unconditionally:
// published outputs have already left the staging dir (rename or
// link+remove), so removal can only destroy unfinished intermediates.
func (j *trimExportJob) cleanup() {
	if j.tempDir == "" {
		return
	}
	_ = os.RemoveAll(j.tempDir)
}

// exportWarnings collects non-fatal problems (publication cleanup
// failures) from stage closures. The run goroutine drains it after the
// sequence ends and surfaces the entries as run-log warnings — never as
// a failed status: an output whose commit point succeeded is published
// regardless of what happened to the staging file afterwards.
type exportWarnings struct {
	mu    sync.Mutex
	items []string
}

func (w *exportWarnings) add(msg string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.items = append(w.items, msg)
}

func (w *exportWarnings) drain() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	items := w.items
	w.items = nil
	return items
}

// publishFile moves a staged output to its final user-visible path.
//
// Overwrite-approved targets use os.Rename — an atomic replacement on
// POSIX same-filesystem setups; on Windows Go uses MoveFileEx with
// REPLACE_EXISTING, which Microsoft does not document as atomic.
// Unapproved targets are published no-clobber via os.Link + os.Remove:
// the link is the commit point, an existing target (EEXIST — including
// one that appeared after the dialog) fails with the file untouched,
// and a filesystem that cannot hardlink fails with an actionable
// error. There is deliberately NO weakened fallback (no
// existence-check + rename): the no-clobber guarantee is never
// silently traded away.
//
// A failure removing the staged file AFTER a successful commit is not
// a publication failure — the output exists and must count as
// published — so it comes back as (published=true, warning, nil); the
// caller surfaces the warning in the run log.
func publishFile(staged, final string, overwrite bool) (published bool, warning error, err error) {
	if overwrite {
		if err := os.Rename(staged, final); err != nil {
			return false, nil, fmt.Errorf("publish %s: %w", final, err)
		}
		// Rename moved the staged file; nothing left to clean up.
		return true, nil, nil
	}
	if err := linkFile(staged, final); err != nil {
		if errors.Is(err, os.ErrExist) {
			return false, nil, fmt.Errorf(
				"publish %s: the file already exists (it may have appeared after overwrite was declined); the existing file was left untouched: %w",
				final, err)
		}
		return false, nil, fmt.Errorf(
			"publish %s: cannot safely publish without overwriting on this filesystem: %w",
			final, err)
	}
	// Link success is the publication commit point; the staged file
	// remains and is best-effort removed.
	if err := removeFile(staged); err != nil {
		return true, fmt.Errorf("could not remove staging file %s: %w", staged, err), nil
	}
	return true, nil, nil
}

// plannedStage is one planned stage plus the staged path awaiting
// publication. stagedPath is set only for stages that PUBLISH a
// user-visible output (separate parts, single/join finals); join
// intermediates write their staged paths via Plan.OutputPath alone and
// are never published, and the direct no-trim run's command writes the
// user-visible output itself.
type plannedStage struct {
	stage      transcode.Stage
	stagedPath string
}

// exportPlan is the abstract export shape: the realized stages, the
// user-visible outputs, and the join bookkeeping (intermediates, list
// file, staged final). planExport computes it once and both the
// preview and the executed job consume it, so they cannot drift.
type exportPlan struct {
	mode        model.TrimExportMode
	stages      []plannedStage
	outputs     []string // user-visible outputs in publication order
	concatList  []string // join intermediates (staging paths), order preserved
	listPath    string   // join: staging path of the concat list file
	stagedFinal string   // join: staging path the concat pass writes
}

// exportRequest carries everything planExport may read: the realized
// profile, the resolved output, the staging directory, the ffprobe
// executable (captured from the config at job-build time — the join
// verification closure captures this value, never the live config),
// and the per-path overwrite approval (nil for previews).
type exportRequest struct {
	profile     model.Profile
	baseOutput  string
	stagingDir  string
	ffprobePath string
	approved    map[string]bool
}

// planExport builds the export plan for the request. Previews pass
// previewStagingName (a fixed representative name — no directory is
// created) and a nil approval map; the executed job passes a real
// os.MkdirTemp directory next to the output.
func (c *Controller) planExport(req exportRequest) (exportPlan, error) {
	trim := c.trimRequest()
	if !trim.Active() {
		plan, err := ffmpeg.BuildCommand(ffmpeg.BuildRequest{
			FFmpegPath: c.config.FFmpeg.FFmpegPath,
			InputPath:  c.inputPath, OutputPath: req.baseOutput,
			Profile: req.profile, Media: c.media,
			Overwrite: req.approved[req.baseOutput],
		})
		if err != nil {
			return exportPlan{}, err
		}
		return exportPlan{
			mode: model.TrimExportSeparate,
			stages: []plannedStage{{
				stage: transcode.Stage{
					Plan: plan, Duration: c.media.Duration,
					Label: "conversion", OutputPath: req.baseOutput,
				},
			}},
			outputs: []string{req.baseOutput},
		}, nil
	}

	staged := func(name string) string { return filepath.Join(req.stagingDir, name) }
	segments := trim.Segments

	// A single segment (either mode) is one staged encode published to
	// the resolved output: no concat pass, no intermediate verification.
	if len(segments) == 1 {
		stagedPath := staged("final" + containerExt(req.profile.Container))
		plan, err := ffmpeg.BuildCommand(ffmpeg.BuildRequest{
			FFmpegPath: c.config.FFmpeg.FFmpegPath,
			InputPath:  c.inputPath, OutputPath: stagedPath,
			Profile: req.profile, Media: c.media,
			Trim: model.TrimSelection{Segments: segments[:1]},
		})
		if err != nil {
			return exportPlan{}, err
		}
		return exportPlan{
			mode: c.trimExportMode,
			stages: []plannedStage{{
				stage: transcode.Stage{
					Plan: plan, Duration: segments[0].Length(),
					Label: "conversion", OutputPath: req.baseOutput,
				},
				stagedPath: stagedPath,
			}},
			outputs: []string{req.baseOutput},
		}, nil
	}

	if c.trimExportMode == model.TrimExportJoin {
		return c.planJoinExport(req, staged, segments)
	}
	return c.planSeparateExport(req, staged, segments)
}

// planSeparateExport plans one staged encode per segment, each
// publishing its own _partNN output immediately after it succeeds.
func (c *Controller) planSeparateExport(req exportRequest, staged func(string) string, segments []model.TimeRange) (exportPlan, error) {
	ext := containerExt(req.profile.Container)
	width := max(2, len(strconv.Itoa(len(segments))))
	planned := make([]plannedStage, 0, len(segments))
	outputs := make([]string, 0, len(segments))
	for i, seg := range segments {
		final := templates.SegmentPath(req.baseOutput, i+1, len(segments))
		if err := templates.ValidateOutput(c.inputPath, final); err != nil {
			return exportPlan{}, fmt.Errorf("segment %d output: %w", i+1, err)
		}
		stagedPath := staged(fmt.Sprintf("part%0*d%s", width, i+1, ext))
		plan, err := ffmpeg.BuildCommand(ffmpeg.BuildRequest{
			FFmpegPath: c.config.FFmpeg.FFmpegPath,
			InputPath:  c.inputPath, OutputPath: stagedPath,
			Profile: req.profile, Media: c.media,
			// Staged names are unique within the fresh staging dir;
			// -n keeps a stray collision a loud failure instead of a
			// silent overwrite.
			Trim: model.TrimSelection{Segments: []model.TimeRange{seg}},
		})
		if err != nil {
			return exportPlan{}, fmt.Errorf("segment %d command: %w", i+1, err)
		}
		planned = append(planned, plannedStage{stage: transcode.Stage{
			Plan: plan, Duration: seg.Length(),
			Label:      fmt.Sprintf("segment %d of %d", i+1, len(segments)),
			OutputPath: final,
		}, stagedPath: stagedPath})
		outputs = append(outputs, final)
	}
	return exportPlan{mode: model.TrimExportSeparate, stages: planned, outputs: outputs}, nil
}

// planJoinExport plans the join pipeline: per-segment Matroska
// intermediates (processing-only stages — never counted or logged as
// published), runtime verification that every intermediate shares one
// stream layout, and a concat-demuxer pass to a staged final carrying
// the real container extension. The pass copies the copied streams;
// FLAC audio is re-encoded continuously (JoinAudioArgs).
func (c *Controller) planJoinExport(req exportRequest, staged func(string) string, segments []model.TimeRange) (exportPlan, error) {
	if strings.TrimSpace(req.ffprobePath) == "" {
		return exportPlan{}, errors.New("joining requires ffprobe to verify segment compatibility; configure an ffprobe executable in Settings")
	}
	intermediate := req.profile
	intermediate.Container = model.ContainerMKV
	intermediates := make([]string, len(segments))
	names := make([]string, len(segments))
	planned := make([]plannedStage, 0, len(segments)+1)
	for i, seg := range segments {
		names[i] = fmt.Sprintf("segment%03d.mkv", i+1)
		intermediates[i] = staged(names[i])
		plan, err := ffmpeg.BuildCommand(ffmpeg.BuildRequest{
			FFmpegPath: c.config.FFmpeg.FFmpegPath,
			InputPath:  c.inputPath, OutputPath: intermediates[i],
			Profile: intermediate, Media: c.media,
			Trim: model.TrimSelection{Segments: []model.TimeRange{seg}},
		})
		if err != nil {
			return exportPlan{}, fmt.Errorf("segment %d command: %w", i+1, err)
		}
		planned = append(planned, plannedStage{stage: transcode.Stage{
			Plan: plan, Duration: seg.Length(),
			Label: fmt.Sprintf("segment %d of %d", i+1, len(segments)),
			// OutputPath deliberately empty: intermediates are
			// temporary processing, never user-visible outputs.
		}})
	}
	listPath := staged("concat.txt")
	stagedFinal := staged("final" + containerExt(req.profile.Container))
	// FLAC audio must be re-encoded as one continuous stream in the
	// join pass (per-segment STREAMINFO and restarted frame numbering
	// make a copied join non-conforming); every other audio codec stays
	// copied. Decision from the realized profile plus the media
	// snapshot, not the live config.
	joinAudio, err := ffmpeg.JoinAudioArgs(req.profile, c.media)
	if err != nil {
		return exportPlan{}, fmt.Errorf("join command: %w", err)
	}
	concat, err := ffmpeg.BuildConcatCommand(ffmpeg.ConcatRequest{
		FFmpegPath: c.config.FFmpeg.FFmpegPath,
		ListPath:   listPath,
		OutputPath: stagedFinal,
		Container:  req.profile.Container,
		AudioArgs:  joinAudio,
	})
	if err != nil {
		return exportPlan{}, fmt.Errorf("join command: %w", err)
	}
	// Capture the probe path from the request: the closure runs on the
	// run goroutine and must never read the mutable config.
	probePath := req.ffprobePath
	planned = append(planned, plannedStage{
		stagedPath: stagedFinal,
		stage: transcode.Stage{
			Plan: concat,
			// The join pass stream-copies every segment except FLAC
			// audio, which it re-encodes continuously (lossless); its
			// progress denominator is the whole selection.
			Duration:   model.TrimSelection{Segments: segments}.Total(),
			Label:      "joining segments",
			OutputPath: req.baseOutput,
			Verify: func(ctx context.Context) error {
				keys := make([][]ffprobe.StreamKey, len(intermediates))
				for i, path := range intermediates {
					// The context comes from the run's cancellation control,
					// so a canceled run interrupts a hung probe.
					k, err := ffprobe.StreamKeys(ctx, probePath, path)
					if err != nil {
						return fmt.Errorf("verify segment %d of %d (%s): %w", i+1, len(intermediates), names[i], err)
					}
					keys[i] = k
				}
				return ffprobe.CompareStreamLayouts(names, keys)
			},
		}})
	return exportPlan{
		mode:        model.TrimExportJoin,
		stages:      planned,
		outputs:     []string{req.baseOutput},
		concatList:  intermediates,
		listPath:    listPath,
		stagedFinal: stagedFinal,
	}, nil
}

// buildTrimExportJob realizes the export plan with a real staging
// directory next to the output, per-path publication closures, and the
// concat list. Every mutable read happens here on the UI thread: stage
// closures use only the job and their captured paths.
func (c *Controller) buildTrimExportJob(cp model.Profile, baseOutput string, approved map[string]bool) (*trimExportJob, error) {
	job := &trimExportJob{
		approved:    approved,
		ffmpegPath:  c.config.FFmpeg.FFmpegPath,
		ffprobePath: c.config.FFmpeg.FFprobePath,
	}
	if c.trimRequest().Active() {
		dir, err := os.MkdirTemp(filepath.Dir(baseOutput), stagingPattern)
		if err != nil {
			return nil, fmt.Errorf("create staging directory: %w", err)
		}
		// The concat demuxer resolves list entries against the list
		// file's own directory, so every staged path must be absolute:
		// a relative output (a template without {dir}) yields a relative
		// staging directory, and an entry like "yave-export-X/part.mkv"
		// would be re-resolved as "yave-export-X/yave-export-X/part.mkv"
		// and the join pass could not open the intermediates. All staged
		// paths derive from this one value.
		if dir, err = filepath.Abs(dir); err != nil {
			_ = os.RemoveAll(dir)
			return nil, fmt.Errorf("resolve staging directory: %w", err)
		}
		job.tempDir = dir
	}
	planned, err := c.planExport(exportRequest{
		profile:     cp,
		baseOutput:  baseOutput,
		stagingDir:  job.tempDir,
		ffprobePath: job.ffprobePath,
		approved:    approved,
	})
	if err != nil {
		job.cleanup()
		return nil, err
	}
	if planned.listPath != "" {
		if err := ffmpeg.WriteConcatList(planned.listPath, planned.concatList); err != nil {
			job.cleanup()
			return nil, fmt.Errorf("write concat list: %w", err)
		}
	}
	for _, ps := range planned.stages {
		stage := ps.stage
		// Staged user-visible outputs get publication closures; staged
		// intermediates (OutputPath empty) are temporary and cleaned up
		// with the staging directory instead.
		if ps.stagedPath != "" && ps.stage.OutputPath != "" {
			stagedPath, final := ps.stagedPath, ps.stage.OutputPath
			stage.Publish = func() error {
				// publishFile's contract: either the commit point
				// succeeded (published, optional cleanup warning) or an
				// error with nothing published — so returning err fails
				// the stage exactly when nothing was published.
				_, warning, err := publishFile(stagedPath, final, job.approved[final])
				if warning != nil {
					job.warnings.add(warning.Error())
				}
				return err
			}
		}
		job.stages = append(job.stages, stage)
	}
	job.outputs = planned.outputs
	return job, nil
}

// exportTargetPaths lists the user-visible outputs an export will
// create, without building any commands: the resolved base for direct,
// single-segment, and join runs; per-part names for separate exports.
// Pure, so startConversion can stat every target before the overwrite
// dialog.
func exportTargetPaths(mode model.TrimExportMode, trim model.TrimSelection, base string) []string {
	switch {
	case !trim.Active(), mode == model.TrimExportJoin, len(trim.Segments) <= 1:
		return []string{base}
	}
	paths := make([]string, len(trim.Segments))
	for i := range trim.Segments {
		paths[i] = templates.SegmentPath(base, i+1, len(trim.Segments))
	}
	return paths
}

// existingPaths filters paths to those that currently exist, preserving
// order — the set the overwrite dialog names and the only paths that
// may later be published with replace semantics. Lstat, not Stat: a
// dangling symlink exists as a link, belongs in the prompt, and is
// replaceable by the publication rename; Stat would follow the missing
// referent, hide the collision, and only fail at publication — after
// the whole segment encoded.
func existingPaths(paths []string) []string {
	var existing []string
	for _, p := range paths {
		if _, err := os.Lstat(p); err == nil {
			existing = append(existing, p)
		}
	}
	return existing
}

// unpublishableTarget names the first target that exists but cannot be
// replaced by a publication (a directory, a device node, a FIFO):
// os.Rename and os.Link both fail on such a path, so a separate-file
// export would encode every part and then fail halfway through
// publishing. Checked before the overwrite dialog and before any encode
// starts. "" means every target is publishable. Symlinks count as
// publishable — the publication rename replaces the link itself — so
// Lstat is used: Stat would treat a dangling symlink as absent.
func unpublishableTarget(paths []string) string {
	for _, p := range paths {
		info, err := os.Lstat(p)
		if err != nil {
			continue // missing is fine: overwrite checks own the collision case
		}
		if !info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0 {
			return fmt.Sprintf(
				"output path %s exists and is not a regular file; remove it or change the output template",
				p)
		}
	}
	return ""
}

// overwritePromptMessage renders the collective overwrite confirmation
// body. Approval is scoped to exactly the listed paths; a target that
// appears after the dialog is published no-clobber and fails loudly on
// collision instead of being overwritten.
func overwritePromptMessage(existing []string) string {
	var b strings.Builder
	b.WriteString("The following output file(s) already exist and would be replaced:\n")
	for _, p := range existing {
		b.WriteString("\n" + p)
	}
	b.WriteString("\n\nOverwrite them?")
	return b.String()
}

func containerExt(container string) string {
	return "." + container
}

// stagedExportNote is appended to the command preview when the plan has
// more than one stage: the box shows the first command, the run log
// carries every stage's command verbatim.
func stagedExportNote(total int) string {
	return fmt.Sprintf("(stage 1 of %d — every stage's command is in the run log)", total)
}

// renderExportPreview renders the export plan into the command box and
// the resolved-output label. It calls planExport with the representative
// staging name — the same planner the executed job uses — so the
// preview cannot silently flatten a multi-segment selection into a
// single-segment command.
func (c *Controller) renderExportPreview(p model.Profile, resolved string) {
	planned, err := c.planExport(exportRequest{
		profile:     p,
		baseOutput:  resolved,
		stagingDir:  previewStagingName,
		ffprobePath: c.config.FFmpeg.FFprobePath,
	})
	if err != nil {
		// The resolved output is still correct even when no command can
		// be built (no compatible VAAPI device, missing ffprobe for
		// join); explain the blank preview instead of keeping stale
		// text.
		c.outputPreview.SetText(c.exportPreviewOutputs(resolved))
		c.command.SetText("Command error: " + err.Error())
		return
	}
	c.outputPreview.SetText(c.exportPreviewOutputs(resolved))
	preview := planned.stages[0].stage.Plan.Preview
	if len(planned.stages) > 1 {
		preview += "\n\n" + stagedExportNote(len(planned.stages))
	}
	c.command.SetText(preview)
}

// exportPreviewOutputs renders the resolved-output label: every part
// path for separate exports (capped with a "+k more" line), the single
// resolved output otherwise.
func (c *Controller) exportPreviewOutputs(resolved string) string {
	targets := exportTargetPaths(c.trimExportMode, c.trimRequest(), resolved)
	const maxLines = 4
	var b strings.Builder
	for i, p := range targets {
		if i == maxLines {
			fmt.Fprintf(&b, "(+%d more)", len(targets)-maxLines)
			break
		}
		if i > 0 {
			b.WriteString("\n")
		}
		b.WriteString(p)
	}
	return b.String()
}
