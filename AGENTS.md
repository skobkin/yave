# Repository Guidelines

## Project Structure & Module Organization

- `cmd/yave`: desktop application entry point.
- `internal/ui`: Fyne window construction, widgets, event handling, and application orchestration.
- `internal/model`: shared configuration, profile, media, and conversion data types and defaults.
- `internal/ffmpeg`: local FFmpeg discovery and structured command generation.
- `internal/ffprobe`: input media inspection and ffprobe JSON parsing.
- `internal/profiles`: YAML configuration and profile persistence under the user config directory.
- `internal/templates`: output filename template expansion and path-safety checks.
- `internal/validation`: profile, source stream, encoder, and container compatibility checks.
- `internal/transcode`: FFmpeg process execution, progress parsing, logging, and cancellation.
- `internal/updates`: Forgejo release checks via go4updates; periodic and manual update verification.
- `PRD.md`: MVP product requirements and the source of truth for intended behavior.

Keep package boundaries explicit. UI code may coordinate these packages, but command generation, validation, persistence, and parsing logic should remain independently testable outside Fyne.

## Build, Test, and Development Commands

- `go run ./cmd/yave`: start the desktop app.
- `go run ./cmd/yave /path/to/video.mkv`: start with an input file.
- `go build ./cmd/yave`: build the application.
- `go test ./...`: run all unit tests.
- `go vet ./...`: run Go's static checks.

Fyne builds on Linux require the OpenGL and X11 development libraries documented in `README.md`. Runtime media inspection and conversion require `ffmpeg` and `ffprobe` in `PATH`, unless executable overrides are configured in the app.

## Coding Style & Naming Conventions

- Language: Go (`go 1.24` in `go.mod`).
- Run `gofmt` on every changed Go file.
- Use short lowercase package names and follow normal Go identifier conventions.
- Keep subprocess calls structured with `exec.CommandContext`; do not construct commands through a shell.
- Treat the displayed shell command as a preview only. Execution must use the validated executable and argument slice.
- Keep UI updates triggered by goroutines on Fyne's UI thread with `fyne.Do` or `fyne.DoAndWait`.
- Preserve graceful degradation when optional media metadata is absent. Missing metadata should be displayed or validated clearly, not cause a panic.
- Wrap errors with operation-specific context and retain useful FFmpeg/ffprobe diagnostics.
- Avoid exposing arbitrary FFmpeg options unless the product requirements are intentionally expanded. YAVE uses a curated codec and container model.

## FFmpeg and Media Rules

- Detect capabilities from the user's configured FFmpeg build; never assume an encoder or hardware acceleration method exists.
- Keep `copy` compatibility source-aware. Validate the actual input codec against the selected output container.
- Keep container compatibility checks aligned between validation and command generation.
- Preserve stream mapping behavior: the all-audio option applies to both encoded and copied audio, while the default processes only the first audio stream.
- Unsupported MP4 subtitle streams are skipped with a warning rather than silently producing an invalid command.
- VAAPI changes must account for device initialization, upload format, scaling filters, and the absence of a render device.
- Output template changes must continue to reject unknown placeholders and path traversal.
- Profile writes must remain atomic so an interrupted save does not corrupt `config.yaml`.

## Testing Guidelines

- Place tests beside implementation files using `*_test.go`.
- Prefer table-driven tests for parsing, validation, compatibility matrices, templates, and argument generation.
- Use fake executables or controlled commands for FFmpeg, ffprobe, and runner tests; unit tests must not depend on the host's codec set or media files.
- Add regression tests for bug fixes and cover neighboring edge cases where practical.
- Test generated argument slices directly. Do not rely only on the human-readable shell preview.
- Run focused package tests while iterating, then run the full completion checklist.
- Manual GUI checks are appropriate for layout, file picking, drag and drop, progress updates, and cancellation, but do not replace unit coverage for non-UI behavior.

## Completion Checklist

Before stating that work is complete, run:

```bash
gofmt -w <changed-go-files>
go test ./...
go vet ./...
go build ./cmd/yave
```

Also run `git diff --check`. Report any check that could not be run, including missing desktop libraries, FFmpeg capabilities, media fixtures, or VAAPI hardware. For UI changes, summarize the manual workflow exercised.

If `PRD.md` is relevant to the task, verify the implementation against it before finishing. Do not broaden MVP scope without calling out the product decision.

When summarizing completed work, also provide a recommended commit message.

## Commit and Pull Request Guidelines

- Follow the repository's Conventional Commit style, for example `feat(ui): ...`, `fix(ffmpeg): ...`, or `test(validation): ...`.
- Keep commits focused on one coherent behavioral change.
- Explain user-visible behavior and compatibility impact in pull request descriptions.
- List the exact automated checks and manual GUI/media workflows performed.
- Include screenshots for material UI changes.
- Call out changes to profile schema, defaults, generated FFmpeg commands, stream mapping, or output paths because they may affect existing users.
- If asked to do a full work on an issue, do fetch and work in a separate branch based on latest `origin/master`. If the session is already on a feature branch (especially one with an open pull request), `origin/master` is behind in-flight work the issue builds on, or if unsure for other reasons, ask the user — ideally during planning — whether to continue on the existing branch or start a new one instead of choosing unilaterally.

## Configuration and Runtime Data

- Configuration is stored at `os.UserConfigDir()/yave/config.yaml` (normally `~/.config/yave/config.yaml` on Linux).
- Do not hard-code FFmpeg paths, input files, output directories, or VAAPI devices outside documented defaults.
- Tests that exercise configuration persistence must use temporary directories or an isolated `XDG_CONFIG_HOME`; they must not read or modify the developer's real configuration.
- Do not commit generated binaries, local media files, conversion outputs, IDE metadata, or machine-specific configuration.
