package ui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/storage"
	"fyne.io/fyne/v2/widget"
	"fyne.io/systray"

	"git.skobk.in/skobkin/yave/internal/encoder"
	"git.skobk.in/skobkin/yave/internal/ffmpeg"
	"git.skobk.in/skobkin/yave/internal/ffprobe"
	"git.skobk.in/skobkin/yave/internal/model"
	"git.skobk.in/skobkin/yave/internal/profiles"
	"git.skobk.in/skobkin/yave/internal/resources"
	"git.skobk.in/skobkin/yave/internal/templates"
	"git.skobk.in/skobkin/yave/internal/transcode"
	"git.skobk.in/skobkin/yave/internal/vaapi"
	"git.skobk.in/skobkin/yave/internal/validation"
)

const (
	progressInfoWidthSample = "999h59m59s | 999.9x"
	cancelGracePeriod       = 5 * time.Second
	runLogFlushInterval     = 100 * time.Millisecond
)

type cancellationStage uint8

type conversionControl interface {
	RequestCancel()
	Terminate()
}

const (
	cancellationRunning cancellationStage = iota
	cancellationRequested
	cancellationTerminateAvailable
	cancellationTerminating
)

type Controller struct {
	app          fyne.App
	window       fyne.Window
	configPath   string
	config       model.Config
	caps         model.Capabilities
	media        model.MediaInfo
	inputPath    string
	outputPath   string
	runControl   conversionControl
	cancelTimer  *time.Timer
	cancelDelay  time.Duration
	cancelStage  cancellationStage
	cancelSince  time.Time
	activeRunLog *runLog
	quitPrompt   fyne.Window
	uiDispatch   func(func())
	loading      bool
	// windowVisible mirrors the Fyne window's visibility so the tray click
	// handler can decide between Show() and Hide() without polling the
	// driver. fyne.Window has no Visible() accessor, so we track it
	// ourselves. It is flipped to true in ShowAndRun and toggled in
	// configureTray's click handler.
	windowVisible bool
	trayProgress  *trayProgressIndicator

	status        *widget.Label
	detection     *widget.Label
	inputEntry    *widget.Entry
	mediaInfo     *widget.Label
	profile       *widget.Select
	container     *widget.Select
	output        *widget.Entry
	outputPreview *widget.Label
	videoEncoder  *widget.Select
	rateControl   *widget.Select
	// rateControlHint explains the selected rate-control mode (Auto
	// and VBR semantics are not obvious). It is a plain label so mode
	// switches update it by SetText without rebuilding the form.
	rateControlHint *widget.Label
	qp              *integerSlider
	crf             *integerSlider
	// qpRangeEncoder and crfRangeEncoder record which encoder's range is
	// currently set on the QP and CRF sliders. An empty string means the
	// slider's range is stale (the prior encoder did not expose the control),
	// so updateIntegerRanges should not remap the stored value — it would
	// silently rewrite defaults that the user never touched.
	qpRangeEncoder  string
	crfRangeEncoder string
	// presetRangeEncoder tracks which encoder's PresetRange is currently
	// set on the preset slider. An empty value means the slider has no
	// range yet (initial state or the prior encoder used a categorical
	// preset). Tracking the prior encoder allows a proportional remap
	// when switching between two integer-preset encoders with different
	// ranges (e.g. libsvtav1 {0,13} -> libaom-av1 {0,8}) while
	// preserving the categorical value when switching to a slider for
	// the first time (e.g. libx264 "slow" -> libsvtav1).
	presetRangeEncoder string
	bitrate            *widget.Entry
	maxrate            *widget.Entry
	bufsize            *widget.Entry
	preset             *widget.Entry
	presetSlider       *integerSlider
	presetControl      *fyne.Container
	tune               *widget.Entry
	videoProfile       *widget.Select
	frameMode          *widget.Select
	frameRate          *widget.Entry
	maxWidth           *widget.Entry
	maxHeight          *widget.Entry
	keepAspect         *widget.Check
	noUpscale          *widget.Check
	vaapiDevice        *widget.Select
	qsvDevice          *widget.Select
	// vaapiDeviceSeed / qsvDeviceSeed record the device paths the
	// loaded profile specified. Startup order is loadProfile → detect,
	// so the dropdowns have no options yet at load time; reconciliation
	// against the probed node list happens when detection delivers
	// them. reconcileDevices owns every programmatic selection.
	vaapiDeviceSeed string
	qsvDeviceSeed   string
	// vaapiDeviceHint explains what the "(Auto)" device selection
	// currently resolves to. It is a plain label like rateControlHint so
	// detection updates change text without rebuilding the form.
	vaapiDeviceHint *widget.Label
	// qsvDeviceHint explains which GPU a manual QSV device pick is and
	// warns when it cannot drive Quick Sync at all.
	qsvDeviceHint *widget.Label
	gpuIndex      *widget.Entry
	audioMode     *widget.Select
	audioEncoder  *widget.Select
	audioBitrate  *widget.Select
	// audioBitrateSeed keeps the loaded profile's bitrate while the
	// bitrate dropdown is not shown (copy mode): the hidden widget's
	// selection is empty, and profileFromWidgets falls back to the
	// seed so a Save round-trips the value instead of wiping it.
	audioBitrateSeed   string
	audioFormBox       *fyne.Container
	audioFormSignature string
	compression        *integerSlider
	allAudio           *widget.Check
	subtitles          *widget.Select
	videoFormBox       *fyne.Container
	videoFormSignature string
	// aspectFlags groups the keep-aspect / no-upscale checks into one
	// container created once: the video form signature hashes widget
	// pointers, so the row's HBox must be stable across
	// videoFormItems calls or every state change would rebuild the
	// form (see formItemsSignature).
	aspectFlags  *fyne.Container
	command      *widget.Entry
	progress     *widget.ProgressBar
	progressInfo *widget.Label
	log          *widget.Entry
	start        *widget.Button
	cancel       *widget.Button
	tabs         *container.AppTabs
	runTab       *container.TabItem
}

func New(a fyne.App, initialInput string) (*Controller, error) {
	configPath, err := profiles.DefaultPath()
	if err != nil {
		return nil, err
	}
	cfg, err := profiles.Load(configPath)
	if err != nil {
		return nil, err
	}
	c := &Controller{
		app:        a,
		window:     a.NewWindow(windowTitle()),
		configPath: configPath,
		config:     cfg,
		inputPath:  initialInput,
	}
	c.createWidgets()
	c.window.SetContent(c.buildLayout())
	c.window.Resize(fyne.NewSize(1050, 760))
	c.window.CenterOnScreen()
	c.window.SetIcon(resources.AppIcon())
	c.window.SetOnDropped(func(_ fyne.Position, uris []fyne.URI) {
		if len(uris) > 0 {
			c.setInput(uris[0].Path())
		}
	})
	c.loadProfile(c.startupProfile())
	c.detect()
	if initialInput != "" {
		c.setInput(initialInput)
	}
	if desk, ok := a.(desktop.App); ok {
		c.configureTray(desk)
	}
	return c, nil
}

// configureTray wires up the system tray icon, visibility toggle, and an
// explicit Quit action that can coordinate with an active FFmpeg process.
//
// The systray callback runs in the systray's own goroutine, so the
// window Show/Hide calls must be dispatched onto Fyne's UI thread via
// fyne.Do. Wrapping the whole toggle inside fyne.Do also keeps the
// read-modify-write of windowVisible on a single goroutine, avoiding a
// data race with ShowAndRun.
//
// With a tray available, closing the main window hides it instead of exiting.
func (c *Controller) configureTray(desk desktop.App) {
	desk.SetSystemTrayIcon(resources.TrayIcon())
	c.trayProgress = newTrayProgressIndicator(desk.SetSystemTrayIcon)
	quit := fyne.NewMenuItem("Quit", c.requestQuit)
	quit.IsQuit = true
	desk.SetSystemTrayMenu(fyne.NewMenu("YAVE", quit))
	c.window.SetCloseIntercept(c.hideWindow)
	systray.SetOnTapped(func() {
		fyne.Do(func() {
			if c.windowVisible {
				c.window.Hide()
				c.windowVisible = false
				return
			}
			c.window.Show()
			c.windowVisible = true
		})
	})
}

func (c *Controller) hideWindow() {
	c.window.Hide()
	c.windowVisible = false
}

func (c *Controller) ShowAndRun() {
	c.window.Show()
	c.windowVisible = true
	c.window.ShowAndRun()
}

func (c *Controller) createWidgets() {
	c.status = widget.NewLabel("Idle")
	c.detection = widget.NewLabel("Detecting FFmpeg...")
	c.detection.Wrapping = fyne.TextWrapWord
	c.inputEntry = widget.NewEntry()
	c.inputEntry.SetPlaceHolder("Select or drop a video file")
	c.inputEntry.OnSubmitted = c.setInput
	c.mediaInfo = widget.NewLabel("No input selected")
	c.mediaInfo.Wrapping = fyne.TextWrapWord

	c.profile = widget.NewSelect(c.profileNames(), func(name string) {
		if c.loading {
			return
		}
		if p, ok := profiles.Find(c.config.Profiles, name); ok {
			c.loadProfile(p)
		}
	})
	c.container = widget.NewSelect([]string{model.ContainerMKV, model.ContainerMP4, model.ContainerWebM}, c.changed)
	c.output = widget.NewEntry()
	c.output.OnChanged = c.changed
	c.outputPreview = widget.NewLabel("")
	c.outputPreview.Wrapping = fyne.TextWrapWord

	videoOpts := append([]string{"copy"}, encoder.DefaultRegistry().VideoNames()...)
	c.videoEncoder = widget.NewSelect(videoOpts, c.changed)
	// rateControl options are populated per-encoder by updateControlState.
	c.rateControl = widget.NewSelect([]string{}, c.changed)
	c.rateControlHint = widget.NewLabel("")
	c.rateControlHint.Wrapping = fyne.TextWrapWord
	c.qp = newIntegerSlider(mustQPRange("hevc_vaapi"), 30, c.changed)
	c.crf = newIntegerSlider(mustCRFRange("libx265"), 28, c.changed)
	c.bitrate = entry(c.changed)
	c.maxrate = entry(c.changed)
	c.bufsize = entry(c.changed)
	c.preset = entry(c.changed)
	c.presetSlider = newIntegerSlider(mustPresetRange("libsvtav1"), 6, c.changed)
	c.presetSlider.Hide()
	c.presetControl = container.NewStack(c.preset, c.presetSlider.Object())
	c.tune = entry(c.changed)
	// Profile options are populated per-encoder by populateProfileOptions.
	c.videoProfile = widget.NewSelect([]string{}, c.changed)
	c.frameMode = widget.NewSelect([]string{"keep", "convert"}, c.changed)
	c.frameRate = entry(c.changed)
	c.maxWidth = numericEntry(c.changed)
	c.maxHeight = numericEntry(c.changed)
	c.keepAspect = widget.NewCheck("Keep aspect ratio", func(bool) { c.changed("") })
	c.noUpscale = widget.NewCheck("Prevent upscaling", func(bool) { c.changed("") })
	c.vaapiDevice = widget.NewSelect([]string{}, c.changed)
	c.qsvDevice = widget.NewSelect([]string{}, c.changed)
	c.vaapiDeviceHint = widget.NewLabel("")
	c.vaapiDeviceHint.Wrapping = fyne.TextWrapWord
	c.qsvDeviceHint = widget.NewLabel("")
	c.qsvDeviceHint.Wrapping = fyne.TextWrapWord
	c.gpuIndex = numericEntry(c.changed)

	c.audioMode = widget.NewSelect([]string{model.AudioCopy, model.AudioEncode}, c.changed)
	// "copy" is deliberately absent: copy mode has no encoder settings
	// (see updateAudioControls), so the dropdown only ever lists real
	// encoders.
	c.audioEncoder = widget.NewSelect(encoder.DefaultRegistry().AudioNames(), c.changed)
	c.audioBitrate = widget.NewSelect([]string{}, c.changed)
	c.compression = newIntegerSlider(mustAudioCompressionRange("flac"), 8, c.changed)
	c.allAudio = widget.NewCheck("Process all audio streams", func(bool) { c.changed("") })
	c.subtitles = widget.NewSelect([]string{model.SubtitleNone, model.SubtitleCopy}, c.changed)
	// The audio tab is one dynamic form rebuilt behind a signature; build
	// the initial (copy-mode) row set here so tests and buildLayout see
	// coherent state before the first updateControlState.
	c.audioFormBox = container.NewVBox()
	c.rebuildAudioForm()
	// Same for the video tab (initially just the Encoder row).
	c.videoFormBox = container.NewVBox()
	// The aspect checks live in a stable HBox so the video form's
	// pointer-based signature does not change on every videoFormItems
	// call.
	c.aspectFlags = container.NewHBox(c.keepAspect, c.noUpscale)
	c.rebuildVideoForm()

	c.command = widget.NewMultiLineEntry()
	c.command.Disable()
	c.command.Wrapping = fyne.TextWrapWord
	c.command.SetMinRowsVisible(3)
	c.progress = widget.NewProgressBar()
	c.progressInfo = widget.NewLabel("")
	c.progressInfo.Truncation = fyne.TextTruncateEllipsis
	c.progress.Hide()
	c.progressInfo.Hide()
	c.log = widget.NewMultiLineEntry()
	c.log.Disable()
	// Word wrapping a rapidly changing FFmpeg log makes Entry repeatedly lay
	// out thousands of fragments on the UI thread. Horizontal scrolling is a
	// much better tradeoff for machine-generated output.
	c.log.Wrapping = fyne.TextWrapOff
	c.log.SetMinRowsVisible(12)
	c.start = widget.NewButton("Start", c.startConversion)
	c.cancel = widget.NewButton("Cancel", c.cancelConversion)
	c.cancel.Disable()
}

func (c *Controller) buildLayout() fyne.CanvasObject {
	browse := widget.NewButton("Browse...", c.chooseInput)
	inputTab := container.NewVBox(
		widget.NewCard("FFmpeg", "", c.detection),
		widget.NewCard("Input", "", container.NewBorder(nil, nil, nil, browse, c.inputEntry)),
		widget.NewCard("Media information", "", c.mediaInfo),
	)

	outputTab := container.NewVBox(
		widget.NewForm(
			widget.NewFormItem("Container", c.container),
			widget.NewFormItem("Output template", container.NewBorder(
				nil, nil, nil, c.outputTemplateHelpButton(), c.output,
			)),
		),
		widget.NewCard("Resolved output", "", c.outputPreview),
	)

	videoTab := container.NewVScroll(container.NewVBox(c.videoFormBox))

	audioTab := container.NewVBox(c.audioFormBox)

	runTab := container.NewBorder(
		container.NewVBox(widget.NewLabel("Command"), c.command, widget.NewLabel("Output")),
		nil, nil, nil,
		c.log,
	)

	c.runTab = container.NewTabItem("Run", runTab)
	c.tabs = container.NewAppTabs(
		container.NewTabItem("Input", inputTab),
		container.NewTabItem("Output", outputTab),
		container.NewTabItem("Video", videoTab),
		container.NewTabItem("Audio & Subtitles", audioTab),
		c.runTab,
	)
	return container.NewBorder(c.profileBar(), c.buildStatusBar(), nil, nil, c.tabs)
}

func (c *Controller) buildStatusBar() *fyne.Container {
	progressInfoSize := widget.NewLabel(progressInfoWidthSample).MinSize()
	progressInfo := container.NewGridWrap(progressInfoSize, c.progressInfo)
	status := container.NewHBox(c.status, progressInfo)
	actions := container.NewHBox(c.start, c.cancel)

	return container.NewBorder(nil, nil, status, actions, c.progress)
}

func (c *Controller) outputTemplateHelpButton() *widget.Button {
	button := widget.NewButton("?", c.showOutputTemplateHelp)
	button.Importance = widget.LowImportance
	return button
}

// outputTemplateHelpText renders the template-help dialog body from
// templates.Placeholders — the single source of truth — so the help
// cannot drift from what Resolve actually accepts. Keys are listed
// sorted, space-padded to a common width; the example block below is
// hand-written prose the map cannot express.
func outputTemplateHelpText() string {
	keys := make([]string, 0, len(templates.Placeholders))
	width := 0
	for key := range templates.Placeholders {
		keys = append(keys, key)
		if len(key) > width {
			width = len(key)
		}
	}
	sort.Strings(keys)

	var b strings.Builder
	for _, key := range keys {
		b.WriteString("{" + key + "}")
		b.WriteString(strings.Repeat(" ", width-len(key)+2))
		b.WriteString(templates.Placeholders[key])
		b.WriteString("\n")
	}
	b.WriteString(`
Template:
{dir}/{name}_{profile}.{ext}

Result:
/home/user/Videos/movie_HEVC_720p.mkv`)
	return b.String()
}

func (c *Controller) showOutputTemplateHelp() {
	help := outputTemplateHelpText()
	text := widget.NewMultiLineEntry()
	text.SetText(help)
	text.SetMinRowsVisible(16)
	text.Disable()

	helpDialog := dialog.NewCustom("Output template placeholders", "Close", text, c.window)
	helpDialog.Resize(fyne.NewSize(760, 600))
	helpDialog.Show()
}

func (c *Controller) profileBar() fyne.CanvasObject {
	actions := container.NewHBox(
		widget.NewButton("Save", c.saveCurrentProfile),
		widget.NewButton("Save as", func() { c.askProfileName("Save profile as", "", c.saveAs) }),
		widget.NewButton("Duplicate", func() { c.askProfileName("Duplicate profile", c.profile.Selected+" copy", c.saveAs) }),
		widget.NewButton("Rename", func() { c.askProfileName("Rename profile", c.profile.Selected, c.renameProfile) }),
		widget.NewButton("Delete", c.deleteProfile),
		widget.NewButton("Set default", c.setDefault),
		widget.NewButton("Settings", c.showSettings),
	)
	return container.NewBorder(nil, nil, widget.NewLabel("Profile:"), actions, c.profile)
}

func (c *Controller) chooseInput() {
	d := dialog.NewFileOpen(func(reader fyne.URIReadCloser, err error) {
		if err != nil {
			dialog.ShowError(err, c.window)
			return
		}
		if reader == nil {
			return
		}
		_ = reader.Close()
		c.setInput(reader.URI().Path())
	}, c.window)
	d.SetFilter(storage.NewExtensionFileFilter([]string{".mkv", ".mp4", ".webm", ".mov", ".avi", ".m4v"}))
	d.Show()
}

func (c *Controller) setInput(path string) {
	path = strings.TrimSpace(path)
	if path == "" {
		return
	}
	c.inputPath = path
	c.inputEntry.SetText(path)
	c.mediaInfo.SetText("Inspecting input...")
	go func() {
		info, err := ffprobe.Inspect(context.Background(), c.config.FFmpeg.FFprobePath, path)
		fyne.Do(func() {
			if err != nil {
				c.media = model.MediaInfo{}
				c.mediaInfo.SetText("Inspection failed: " + err.Error())
			} else {
				c.media = info
				c.mediaInfo.SetText(formatMediaInfo(info))
			}
			c.refresh()
		})
	}()
}

func (c *Controller) detect() {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		caps, err := ffmpeg.Discover(ctx, c.config.FFmpeg)
		fyne.Do(func() {
			if err != nil {
				c.caps = model.Capabilities{}
				c.detection.SetText("Detection failed: " + err.Error())
				// Re-derive the derived labels (the automatic-device
				// hint in particular): with caps emptied, a hint that
				// still names the previous detection's resolved device
				// would contradict the "no device detected" validation
				// error the next Start shows.
				c.updateVAAPIDeviceHint()
				c.updateQSVDeviceHint()
			} else {
				c.applyDetection(caps)
			}
			c.refresh()
		})
	}()
}

// applyDetection applies freshly discovered capabilities on the UI
// thread. The ordering is behavior: dropdown options are refreshed
// before anything selects from them (Fyne's Select.SetSelected
// silently drops values not in Options), devices are reconciled
// before a runnable profile is auto-selected because
// selectRunnableProfile may loadProfile a fallback candidate whose
// load-time reconciliation must win, and updateControlState runs
// before that selection so "encode + Encoder=copy" can never exist
// for it to trip over (the A3 silent-revert bug).
func (c *Controller) applyDetection(caps model.Capabilities) {
	c.caps = caps
	c.config.FFmpeg.FFmpegPath = caps.FFmpegPath
	c.config.FFmpeg.FFprobePath = caps.FFprobePath
	summary := fmt.Sprintf("%s\n%s\nHardware acceleration: %s\nHardware devices: %s",
		caps.FFmpegVersion, caps.FFprobeVersion, strings.Join(caps.HWAccels, ", "), hardwareDevicesSummary(caps))
	if devices := vaapiDevicesSummary(caps); devices != "" {
		summary += "\nVAAPI devices: " + devices
	}
	c.detection.SetText(summary)
	c.updateEncoderOptions()
	c.updateDeviceOptions()
	c.reconcileDevices(false)
	c.updateControlState()
	c.selectRunnableProfile()
}

func (c *Controller) changed(string) {
	if c.loading {
		return
	}
	c.updateControlState()
	c.refresh()
}

func (c *Controller) refresh() {
	// Materialize the automatic VAAPI device so the previewed command
	// shows the resolved -vaapi_device, not the empty auto value.
	p := c.commandProfile()
	c.outputPath = ""
	c.command.SetText("")
	if c.inputPath != "" {
		width, height := dimensions(p, c.media)
		resolved, err := templates.Resolve(p.OutputTemplate, templates.Request{
			InputPath: c.inputPath,
			Extension: p.Container,
			Profile:   p.Name,
			Codec:     p.Video.Encoder,
			Width:     width,
			Height:    height,
			Time:      time.Now(),
		})
		if err != nil {
			c.outputPreview.SetText("Invalid template: " + err.Error())
		} else {
			c.outputPath = resolved
			c.outputPreview.SetText(resolved)
			plan, buildErr := ffmpeg.BuildCommand(ffmpeg.BuildRequest{
				FFmpegPath: c.config.FFmpeg.FFmpegPath,
				InputPath:  c.inputPath,
				OutputPath: c.outputPath,
				Profile:    p,
				Media:      c.media,
			})
			// Explain a blank preview instead of silently keeping the
			// stale text: automatic device selection makes unreachable
			// builds (no compatible VAAPI device) a normal preview state.
			if buildErr != nil {
				c.command.SetText("Command error: " + buildErr.Error())
			} else {
				c.command.SetText(plan.Preview)
			}
		}
	}
	if c.runControl == nil {
		c.start.Enable()
	} else {
		c.start.Disable()
	}
}

func (c *Controller) startConversion() {
	p := c.profileFromWidgets()
	resolved, resolveErr := c.resolveOutput(p, time.Now())
	result := validation.Validate(p, &c.media, c.caps, c.inputPath, resolved)
	messages := issueMessages(result)
	if resolveErr != nil {
		messages = append([]string{"Error: " + resolveErr.Error()}, messages...)
	}
	if resolveErr != nil || !result.Valid() {
		c.showRunError("Validation failed", strings.Join(messages, "\n"))
		return
	}
	// The run uses the materialized profile (automatic VAAPI device
	// resolved to a concrete path); validation above deliberately saw
	// the raw profile so its auto semantics could apply.
	cp := c.commandProfile()
	start := func(overwrite bool) {
		plan, buildErr := ffmpeg.BuildCommand(ffmpeg.BuildRequest{
			FFmpegPath: c.config.FFmpeg.FFmpegPath,
			InputPath:  c.inputPath, OutputPath: resolved,
			Profile: cp, Media: c.media, Overwrite: overwrite,
		})
		if buildErr != nil {
			c.showRunError("Command generation failed", buildErr.Error())
			return
		}
		c.run(plan, messages)
	}
	if _, statErr := os.Stat(resolved); statErr == nil {
		dialog.NewConfirm("Overwrite output?",
			"The output file already exists:\n"+resolved+"\n\nOverwrite it?",
			func(ok bool) {
				if ok {
					start(true)
				}
			}, c.window).Show()
		return
	}
	start(false)
}

func (c *Controller) showRunError(status, message string) {
	c.status.SetText(status)
	c.progressInfo.SetText("")
	c.progress.SetValue(0)
	c.log.SetText(message)
	c.tabs.Select(c.runTab)
	c.setProgressVisible(false)
}

func (c *Controller) run(plan model.RunPlan, messages []string) {
	control := transcode.NewRunControl()
	c.runControl = control
	c.cancelStage = cancellationRunning
	c.cancelSince = time.Time{}
	c.start.Disable()
	c.cancel.SetText("Cancel")
	c.cancel.Enable()
	c.status.SetText("Running")
	c.progress.SetValue(0)
	c.setTrayProgress(0)
	c.progressInfo.SetText("")
	c.command.SetText(plan.Preview)
	c.setProgressVisible(true)
	for _, warning := range plan.Warnings {
		messages = append(messages, "Warning: "+warning)
	}
	runLog, logErr := openLastRunLog()
	c.activeRunLog = runLog
	if logErr != nil {
		runLog.AppendLine("Warning: complete conversion log is unavailable: " + logErr.Error())
	}
	runLog.AppendLine("Command: " + plan.Preview)
	for _, message := range messages {
		runLog.AppendLine(message)
	}
	runLog.AppendLine("")
	initialLog, _ := runLog.LiveSnapshot()
	c.log.SetText(initialLog)
	stopLogUpdates := c.startRunLogUpdates(runLog, control)

	go func() {
		err := (transcode.Runner{}).Run(control, plan, c.media.Duration, transcode.Callbacks{
			OnLog: func(line string) {
				runLog.AppendLine(line)
			},
			OnProgress: func(p model.Progress) {
				fyne.Do(func() {
					c.progress.SetValue(p.Percentage)
					c.setTrayProgress(p.Percentage)
					c.progressInfo.SetText(formatProgressInfo(p))
				})
			},
		})
		close(stopLogUpdates)
		_ = runLog.Close()
		finalLog, _ := runLog.LiveSnapshot()
		fyne.Do(func() {
			if c.runControl != control {
				return
			}
			c.stopCancelTimer()
			c.runControl = nil
			c.activeRunLog = nil
			c.cancelStage = cancellationRunning
			c.cancelSince = time.Time{}
			c.cancel.SetText("Cancel")
			c.cancel.Disable()
			c.log.SetText(finalLog)
			c.setRunResult(err)
			c.refresh()
		})
	}()
}

func (c *Controller) startRunLogUpdates(runLog *runLog, control conversionControl) chan struct{} {
	stop := make(chan struct{})
	go func() {
		ticker := time.NewTicker(runLogFlushInterval)
		defer ticker.Stop()
		completed := make(chan struct{}, 1)
		var displayedVersion uint64
		updatePending := false
		for {
			select {
			case <-ticker.C:
				if updatePending {
					continue
				}
				text, version := runLog.LiveSnapshot()
				if version == displayedVersion {
					continue
				}
				displayedVersion = version
				updatePending = true
				c.dispatchUI(func() {
					if c.runControl == control {
						c.log.SetText(text)
					}
					completed <- struct{}{}
				})
			case <-completed:
				updatePending = false
			case <-stop:
				return
			}
		}
	}()
	return stop
}

func formatProgressInfo(progress model.Progress) string {
	return fmt.Sprintf("%s | %s", progress.OutTime.Round(time.Second), progress.Speed)
}

func (c *Controller) setRunResult(err error) {
	c.resetTrayProgress()
	switch err {
	case nil:
		c.status.SetText("Completed")
		c.progress.SetValue(1)
	case context.Canceled: //nolint:errorlint // context.Canceled is a sentinel; == comparison is intentional
		c.status.SetText("Canceled")
	default:
		c.status.SetText("Failed: " + err.Error())
		c.tabs.Select(c.runTab)
	}
	c.setProgressVisible(false)
}

// setProgressVisible shows or hides the progress bar and its paired
// progressInfo label together. They are visually coupled in buildStatusBar,
// so the bar must never appear without its sibling.
func (c *Controller) setProgressVisible(visible bool) {
	if visible {
		c.progress.Show()
		c.progressInfo.Show()
		return
	}
	c.progress.Hide()
	c.progressInfo.Hide()
}

func (c *Controller) cancelConversion() {
	control := c.runControl
	if control == nil {
		return
	}
	switch c.cancelStage {
	case cancellationRunning:
		c.cancelStage = cancellationRequested
		c.cancelSince = time.Now()
		c.status.SetText("Canceling…")
		// Keep the control clickable. Under normal conditions the timer changes
		// its label to Terminate; if the UI callback is delayed, a click after
		// the grace period can still force termination based on wall time.
		c.cancel.Enable()
		control.RequestCancel()
		delay := c.cancellationDelay()
		c.cancelTimer = time.AfterFunc(delay, func() {
			c.dispatchUI(func() {
				if c.runControl != control || c.cancelStage != cancellationRequested {
					return
				}
				c.cancelStage = cancellationTerminateAvailable
				c.status.SetText("FFmpeg is not responding")
				c.cancel.SetText("Terminate")
				c.cancel.Enable()
			})
		})
	case cancellationRequested:
		if time.Since(c.cancelSince) < c.cancellationDelay() {
			return
		}
		c.terminateConversion(control)
	case cancellationTerminateAvailable:
		c.terminateConversion(control)
	}
}

func (c *Controller) cancellationDelay() time.Duration {
	if c.cancelDelay > 0 {
		return c.cancelDelay
	}
	return cancelGracePeriod
}

func (c *Controller) terminateConversion(control conversionControl) {
	c.cancelStage = cancellationTerminating
	c.status.SetText("Terminating…")
	c.cancel.SetText("Terminate")
	c.cancel.Disable()
	control.Terminate()
}

func (c *Controller) dispatchUI(fn func()) {
	if c.uiDispatch != nil {
		c.uiDispatch(fn)
		return
	}
	fyne.Do(fn)
}

func (c *Controller) stopCancelTimer() {
	if c.cancelTimer != nil {
		c.cancelTimer.Stop()
		c.cancelTimer = nil
	}
}

func (c *Controller) requestQuit() {
	if c.runControl == nil {
		c.quitApplication()
		return
	}
	if c.quitPrompt != nil {
		c.quitPrompt.Show()
		return
	}

	prompt := c.app.NewWindow("Quit YAVE?")
	c.quitPrompt = prompt
	closePrompt := func() {
		if c.quitPrompt != prompt {
			return
		}
		c.quitPrompt = nil
		prompt.SetCloseIntercept(nil)
		prompt.Close()
	}
	keepRunning := widget.NewButton("Keep Running", closePrompt)
	terminateAndQuit := widget.NewButton("Terminate and Quit", c.quitApplication)
	terminateAndQuit.Importance = widget.HighImportance
	warning := widget.NewLabel("A conversion is running. Quitting will terminate FFmpeg and may leave an incomplete output file.")
	warning.Wrapping = fyne.TextWrapWord
	prompt.SetContent(container.NewPadded(container.NewVBox(
		warning,
		container.NewHBox(keepRunning, terminateAndQuit),
	)))
	prompt.Resize(fyne.NewSize(520, 150))
	prompt.CenterOnScreen()
	prompt.SetCloseIntercept(closePrompt)
	prompt.Show()
}

func (c *Controller) quitApplication() {
	control := c.runControl
	if control != nil {
		control.Terminate()
	}
	c.runControl = nil
	c.stopCancelTimer()
	if c.activeRunLog != nil {
		_ = c.activeRunLog.Close()
	}
	c.activeRunLog = nil
	if c.quitPrompt != nil {
		prompt := c.quitPrompt
		c.quitPrompt = nil
		prompt.SetCloseIntercept(nil)
		prompt.Close()
	}
	c.window.SetCloseIntercept(nil)
	c.app.Quit()
}

func (c *Controller) loadProfile(p model.Profile) {
	c.loading = true
	defer func() {
		c.loading = false
		c.updateControlState()
		c.reconcileDevices(true)
		c.refresh()
	}()
	c.profile.SetSelected(p.Name)
	c.container.SetSelected(p.Container)
	c.output.SetText(p.OutputTemplate)
	c.videoEncoder.SetSelected(p.Video.Encoder)
	// Seed rate-control options for the just-selected encoder before
	// SetSelected. Fyne silently drops a SetSelected call whose value
	// isn't in Options yet, which would cause the deferred
	// updateControlState to see an empty selection, treat it as
	// unsupported, and fall back to the encoder default (e.g. VAAPI
	// "Auto"). Built-in VAAPI profiles saved as CQP/VBR would then
	// silently reload as Auto and emit different FFmpeg args.
	// populateRateControlOptions also performs the SetSelected when
	// preferred is non-empty, so the explicit SetSelected below is
	// redundant and would re-fire OnChanged.
	// populateRateControlOptions keeps the current selection when
	// preferred is empty — the right behavior for user-driven encoder
	// switches, but wrong for a fresh load: an empty saved value must
	// land on the encoder default instead of inheriting the previous
	// profile's mode. ClearSelected empties the selection so the
	// populate call falls back (OnChanged is swallowed by c.loading).
	if p.Video.RateControl == "" && c.rateControl.Selected != "" {
		c.rateControl.ClearSelected()
	}
	c.populateRateControlOptions(p.Video.RateControl)
	c.updateIntegerRanges()
	c.qp.SetValue(p.Video.QP)
	c.crf.SetValue(p.Video.CRF)
	c.bitrate.SetText(p.Video.Bitrate)
	c.maxrate.SetText(p.Video.Maxrate)
	c.bufsize.SetText(p.Video.Bufsize)
	c.preset.SetText(p.Video.Preset)
	c.tune.SetText(p.Video.Tune)
	// Seed the profile options for the just-selected encoder before
	// anything reads the selection: same Fyne ordering rule as the
	// rate-control dropdown above (SetSelected silently drops values
	// missing from Options). populateProfileOptions also performs the
	// SetSelected when preferred is non-empty.
	// Same empty-value rule as the rate-control dropdown above: a
	// profile saved without -profile:v must land on "(default)", not
	// keep the previously loaded profile's selection (which would be
	// emitted for this profile and persisted by Save).
	if p.Video.Profile == "" && c.videoProfile.Selected != "" {
		c.videoProfile.ClearSelected()
	}
	c.populateProfileOptions(p.Video.Profile)
	c.frameMode.SetSelected(p.Video.FrameRateMode)
	c.frameRate.SetText(p.Video.FrameRate)
	c.maxWidth.SetText(intText(p.Video.MaxWidth))
	c.maxHeight.SetText(intText(p.Video.MaxHeight))
	c.keepAspect.SetChecked(p.Video.KeepAspectRatio)
	c.noUpscale.SetChecked(p.Video.PreventUpscaling)
	// Record the loaded device paths as seeds, then rebuild the device
	// dropdowns from the detected nodes (empty before the first
	// detection). reconcileDevices in the deferred block performs the
	// actual selections once loading is done — including the
	// saved-device-not-detected replacement + notification.
	c.vaapiDeviceSeed = p.Video.VAAPIDevice
	c.qsvDeviceSeed = p.Video.QSVDevice
	c.updateDeviceOptions()
	c.gpuIndex.SetText(intText(p.Video.GPUIndex))
	c.audioMode.SetSelected(p.Audio.Mode)
	// Copy-mode profiles carry Encoder: "copy" in YAML for validation
	// compatibility; the dropdown only lists real encoders, so the
	// value is not selected (the encoder row is hidden in copy mode
	// and profileFromWidgets re-normalizes to "copy").
	if p.Audio.Encoder != "copy" {
		c.audioEncoder.SetSelected(p.Audio.Encoder)
	}
	// Seed the bitrate options for the just-loaded encoder before
	// anything reads the selection (Fyne drops SetSelected values
	// missing from Options). The seed keeps the loaded bitrate
	// round-tripping while the row is hidden in copy mode. The row is
	// also hidden or meaningless for a copy-mode / bitrate-less load,
	// so clear the dropdown first — populateAudioBitrateOptions keeps
	// the current selection when preferred is empty, which would
	// inherit the previously loaded profile's bitrate.
	c.audioBitrateSeed = p.Audio.Bitrate
	if p.Audio.Bitrate == "" || p.Audio.Encoder == "copy" {
		c.audioBitrate.ClearSelected()
	}
	c.populateAudioBitrateOptions(p.Audio.Encoder, p.Audio.Bitrate)
	c.compression.SetValue(p.Audio.Compression)
	c.allAudio.SetChecked(p.Audio.CopyAllStreams)
	c.subtitles.SetSelected(p.Subtitles.Mode)
}

// videoProfileValue maps the Profile dropdown to the profile value,
// emitting "" for encoders that do not accept -profile:v — their row
// is hidden, so a stale invisible selection must never reach the
// saved profile.
func (c *Controller) videoProfileValue() string {
	if ve := c.currentVideoEncoder(); ve == nil || !ve.SupportsProfile() {
		return ""
	}
	return profileOptionToValue(c.videoProfile.Selected)
}

// resolveAutoVAAPI resolves what the "(Auto)" device option means for
// the given encoder and -profile:v value against the current
// capabilities. ok is false for encoders with no VAAPI device
// requirement, so callers skip the auto logic entirely.
func (c *Controller) resolveAutoVAAPI(encoderName, profile string) (choice vaapi.DeviceChoice, ok bool) {
	required, ok := encoder.RequiredVAProfiles(encoderName, profile)
	if !ok || len(required) == 0 {
		return vaapi.DeviceChoice{}, false
	}
	return vaapi.SelectDevice(c.caps.RenderNodes, c.caps.VAAPIDevices, required), true
}

// commandProfile returns the widget state as the profile shape
// ffmpeg.BuildCommand consumes: an automatic VAAPI device ("") is
// materialized into the concrete path resolveAutoVAAPI picks from the
// current capabilities. profileFromWidgets stays the persisted shape
// where "" means automatic — Save must keep the Auto intent.
// resolveAutoVAAPI is pure and both call sites (the refresh preview
// and the startConversion run) see identical inputs, so preview,
// validation (which resolves the same way internally), and the actual
// run always agree.
func (c *Controller) commandProfile() model.Profile {
	p := c.profileFromWidgets()
	if strings.TrimSpace(p.Video.VAAPIDevice) != "" {
		return p
	}
	choice, ok := c.resolveAutoVAAPI(p.Video.Encoder, p.Video.Profile)
	if ok && choice.Path != "" {
		p.Video.VAAPIDevice = choice.Path
	}
	return p
}

func (c *Controller) profileFromWidgets() model.Profile {
	audioMode := c.audioMode.Selected
	audioEncoder := c.audioEncoder.Selected
	if audioMode != model.AudioEncode {
		// Copy mode has no encoder settings; YAML/validation expect
		// the literal "copy" encoder alongside the copy mode.
		audioEncoder = "copy"
	}
	// In copy mode the bitrate row is hidden, so the invisible
	// dropdown selection must never win: only a visible encode-mode
	// selection takes precedence over the loaded value.
	audioBitrate := c.audioBitrateSeed
	if audioMode == model.AudioEncode {
		audioBitrate = firstNonEmpty(c.audioBitrate.Selected, c.audioBitrateSeed)
	}
	return model.Profile{
		Name: c.profile.Selected, Container: c.container.Selected, OutputTemplate: c.output.Text,
		Video: model.VideoSettings{
			Encoder: c.videoEncoder.Selected, RateControl: c.rateControl.Selected,
			QP: c.qp.Value(), CRF: c.crf.Value(), Bitrate: c.bitrate.Text,
			Maxrate: c.maxrate.Text, Bufsize: c.bufsize.Text, Preset: c.presetValue(),
			Tune: c.tune.Text, Profile: c.videoProfileValue(),
			FrameRateMode: c.frameMode.Selected, FrameRate: c.frameRate.Text,
			MaxWidth: atoi(c.maxWidth.Text), MaxHeight: atoi(c.maxHeight.Text),
			KeepAspectRatio: c.keepAspect.Checked, PreventUpscaling: c.noUpscale.Checked,
			// When no nodes were detected (detection failure,
			// non-Linux, pre-detect window) the dropdown selection is
			// empty; fall back to the loaded profile's saved value so a
			// Save in that state keeps the configured device instead of
			// wiping it from config.yaml. The seed only backfills an
			// empty selection: an explicit "(Auto)" pick maps to the
			// empty (automatic) value and must win over the seed, or
			// the model would silently keep the pre-pick device while
			// the dropdown shows "(Auto)".
			VAAPIDevice: vaapiDeviceFromWidgets(c),
			QSVDevice:   qsvDeviceFromWidgets(c),
			GPUIndex:    atoi(c.gpuIndex.Text),
		},
		Audio: model.AudioSettings{
			Mode: audioMode, Encoder: audioEncoder,
			Bitrate:        audioBitrate,
			Compression:    c.compression.Value(),
			CopyAllStreams: c.allAudio.Checked,
		},
		Subtitles: model.SubtitleSettings{Mode: c.subtitles.Selected},
	}
}

// populateRateControlOptions rebuilds the rate-control dropdown from
// the currently-selected encoder's advertised modes. preferred (when
// non-empty) is the value we want the select to land on after the
// options exist; the current selection is used when preferred is empty.
// If the preferred/current value isn't in the new options, we fall
// back to the encoder's default so a stale mode never lingers across
// encoder switches.
//
// Callers that are about to call SetSelected (loadProfile in
// particular) must invoke this first: Fyne silently drops a
// SetSelected whose value isn't already in Options, so the saved
// profile's rate control would otherwise be discarded and replaced by
// the encoder default during the deferred updateControlState.
func (c *Controller) populateRateControlOptions(preferred string) {
	encoderName := c.videoEncoder.Selected
	var ve encoder.VideoEncoder
	if encoderName != "copy" {
		if e, ok := encoder.DefaultRegistry().Get(encoderName); ok {
			ve, _ = e.(encoder.VideoEncoder)
		}
	}
	if ve == nil {
		c.rateControl.Options = []string{}
		return
	}
	modes := ve.RateControlModes()
	options := make([]string, len(modes))
	supported := false
	target := preferred
	if target == "" {
		target = c.rateControl.Selected
	}
	for i, m := range modes {
		label := rateControlLabel(m)
		options[i] = label
		if label == target {
			supported = true
		}
	}
	c.rateControl.Options = options
	// Only SetSelected when the value actually changes; firing
	// OnChanged from inside updateControlState would re-enter
	// updateControlState via changed() and recurse until the
	// goroutine stack overflows.
	if supported {
		if c.rateControl.Selected != target {
			c.rateControl.SetSelected(target)
		}
	} else {
		fallback := rateControlLabel(ve.DefaultRateControl())
		if c.rateControl.Selected != fallback {
			c.rateControl.SetSelected(fallback)
		}
	}
}

// defaultOption is the dropdown label standing in for "emit no
// -profile:v" (Profile) and "use ffmpeg's default device" (QSV
// device) — both map to the empty string in the profile model.
const defaultOption = "(default)"

func profileValueToOption(value string) string {
	if strings.TrimSpace(value) == "" {
		return defaultOption
	}
	return value
}

func profileOptionToValue(option string) string {
	if option == defaultOption {
		return ""
	}
	return option
}

// profileChoices returns the profile value list for the encoder: the
// detected build's narrowed list when available, else the curated
// list (before detection completes, or when the build enumerates
// nothing — absence of an enumeration is not evidence of non-support).
func (c *Controller) profileChoices(ve encoder.VideoEncoder) []string {
	if m, ok := c.caps.Encoders[ve.Name()]; ok && len(m.Profiles) > 0 {
		return m.Profiles
	}
	return ve.Profiles()
}

// populateProfileOptions rebuilds the Profile dropdown from the
// currently-selected encoder's curated (or build-narrowed) list,
// prefixed with the "(default)" option. preferred (when non-empty) is
// the value the dropdown should land on after the options exist; the
// current selection is used when preferred is empty. The preferred and
// current values are unioned into the options even when outside the
// curated list, so a hand-edited profile value stays visible and
// validation — not a silently emptied dropdown — reports it.
//
// Callers about to read the selection (loadProfile in particular)
// must invoke this first: Fyne silently drops a SetSelected whose
// value isn't already in Options.
func (c *Controller) populateProfileOptions(preferred string) {
	encoderName := c.videoEncoder.Selected
	var ve encoder.VideoEncoder
	if encoderName != "copy" {
		if e, ok := encoder.DefaultRegistry().Get(encoderName); ok {
			ve, _ = e.(encoder.VideoEncoder)
		}
	}
	if ve == nil || !ve.SupportsProfile() {
		c.videoProfile.Options = []string{}
		return
	}
	options := append([]string{defaultOption}, c.profileChoices(ve)...)
	// Union the preferred and current values so out-of-list values
	// stay visible instead of being silently dropped by SetSelected.
	for _, extra := range []string{profileValueToOption(preferred), c.videoProfile.Selected} {
		if extra != "" && extra != defaultOption && !slices.Contains(options, extra) {
			options = append(options, extra)
		}
	}
	c.videoProfile.Options = options

	target := profileValueToOption(preferred)
	if preferred == "" {
		target = c.videoProfile.Selected
	}
	// An empty selection is represented visibly by "(default)" so the
	// dropdown never shows the generic placeholder where a real state
	// belongs.
	if target == "" {
		target = defaultOption
	}
	// Only SetSelected when the value actually changes; firing
	// OnChanged from inside updateControlState would re-enter
	// updateControlState via changed() (see populateRateControlOptions).
	if c.videoProfile.Selected != target {
		c.videoProfile.SetSelected(target)
	}
}

// vaapiAutoOption is the dropdown label standing in for "automatic
// VAAPI device selection": yave picks the first probed render node
// that can encode the selected codec and profile. Distinct from QSV's
// "(default)", where ffmpeg picks the device.
const vaapiAutoOption = "(Auto)"

// warningPrefix marks hint lines whose selection cannot serve the
// current encoder — a visible warning, never a blocker.
const warningPrefix = "⚠️ "

// vaapiDeviceFromWidgets resolves the VAAPI device for the profile
// model. An empty dropdown selection (no nodes detected yet, or never
// selected) falls back to the loaded profile's saved value so a Save
// cannot wipe it; any visible selection — "(Auto)" included — is
// authoritative and maps through vaapiDeviceValue.
func vaapiDeviceFromWidgets(c *Controller) string {
	if c.vaapiDevice.Selected == "" {
		return c.vaapiDeviceSeed
	}
	return vaapiDeviceValue(c.caps, c.vaapiDevice.Selected)
}

// deviceByPath returns the probed VA-API device for a render-node
// path, ok false when the node has no probe record.
func deviceByPath(caps model.Capabilities, node string) (vaapi.DeviceInfo, bool) {
	for _, d := range caps.VAAPIDevices {
		if d.Path == node {
			return d, true
		}
	}
	return vaapi.DeviceInfo{}, false
}

// vaapiDeviceOption renders the VAAPI dropdown label for a render
// node: the GPU identity first, the node basename in parentheses —
// "GeForce GTX 1660 Ti Mobile (renderD128)". A path means nothing to
// the person choosing, so the full path never appears; nodes without
// resolved identity fall back to the bare basename.
func vaapiDeviceOption(caps model.Capabilities, node string) string {
	base := filepath.Base(node)
	if d, ok := deviceByPath(caps, node); ok {
		if name := d.Identity.DisplayName(); name != "" {
			return name + " (" + base + ")"
		}
	}
	return base
}

// vaapiDeviceValue maps a VAAPI device dropdown option back to the
// render-node path stored in the profile. "(Auto)" maps to ""; the
// current option label, a bare node path, a bare basename, or a stale
// label carrying the node's basename all resolve to the node;
// anything else passes through unchanged, keeping the historical
// fail-open behavior for values outside the detected set.
func vaapiDeviceValue(caps model.Capabilities, option string) string {
	if option == vaapiAutoOption {
		return ""
	}
	for _, node := range caps.RenderNodes {
		if option == node || option == filepath.Base(node) || option == vaapiDeviceOption(caps, node) {
			return node
		}
	}
	// Stale label from an older capabilities snapshot: the basename in
	// parentheses is still authoritative.
	for _, node := range caps.RenderNodes {
		if strings.HasSuffix(option, " ("+filepath.Base(node)+")") {
			return node
		}
	}
	return option
}

// qsvDeviceValue maps a QSV device dropdown option to the profile
// value: "(default)" stands for the empty path, which tells ffmpeg to
// pick its default device.
func qsvDeviceValue(option string) string {
	if option == defaultOption {
		return ""
	}
	return option
}

// qsvDeviceFromWidgets resolves the QSV device for the profile model.
// An empty dropdown selection (no nodes detected yet, or never
// selected) falls back to the loaded profile's saved path so a Save
// cannot wipe it; any visible selection — "(default)" included — is
// authoritative and maps through qsvDeviceValue.
func qsvDeviceFromWidgets(c *Controller) string {
	if c.qsvDevice.Selected == "" {
		return c.qsvDeviceSeed
	}
	return qsvDeviceValue(c.qsvDevice.Selected)
}

// firstNonEmpty returns the first value with non-whitespace content,
// or the empty string when there is none.
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// updateDeviceOptions rebuilds the VAAPI and QSV device dropdowns from
// the detected render nodes. It never changes a selection that is
// still valid — reconcileDevices owns programmatic selection so
// manual picks survive re-detections.
func (c *Controller) updateDeviceOptions() {
	nodes := c.caps.RenderNodes
	options := make([]string, 0, len(nodes)+1)
	options = append(options, vaapiAutoOption)
	for _, node := range nodes {
		options = append(options, vaapiDeviceOption(c.caps, node))
	}
	c.vaapiDevice.Options = options
	c.qsvDevice.Options = append([]string{defaultOption}, nodes...)

	// Re-pin a visible selection whose option label changed with the
	// capabilities: a bare value picked before detection, or a label
	// from an older snapshot. Guarded so SetSelected fires at most one
	// OnChanged per capabilities change, and idempotent — a selection
	// already on its current label is left alone, which is what
	// reconcileDevices' guard then sees. updateControlState never
	// rebuilds device options, so no re-entry follows.
	if sel := c.vaapiDevice.Selected; sel != "" && sel != vaapiAutoOption {
		if node := vaapiDeviceValue(c.caps, sel); slices.Contains(nodes, node) {
			if label := vaapiDeviceOption(c.caps, node); label != sel {
				c.vaapiDevice.SetSelected(label)
			}
		}
	}
	c.vaapiDevice.Refresh()
	c.qsvDevice.Refresh()
}

// reconcileDevices aligns the device dropdowns with the probed node
// list and the loaded profile's seeds. atLoad marks the
// loadProfile-deferred call, where the freshly loaded profile is
// authoritative: its device (or the replacement for a seed no longer
// among the nodes) is selected even when a previous profile left a
// valid selection behind — keeping that selection would preview,
// encode, and Save the wrong device into the newly loaded profile.
// The detect-time call (atLoad=false) never overrides an existing
// selection so manual device picks survive re-detections; it only
// fills an empty selection (a load that ran before nodes were known)
// and applies replacements.
//
// An empty VAAPI seed means automatic selection and is a valid target
// (the "(Auto)" option), like an empty QSV seed meaning ffmpeg's
// default. A non-empty seed that no longer exists among the detected
// nodes is replaced (automatic selection for VAAPI — the picker can
// then choose a compatible node once capabilities are known; the
// default device for QSV) and reported once via popup dialog; the
// seed is updated to the replacement so later detections stay quiet.
//
// With no detected nodes (detection failure, non-Linux, pre-detect
// window) reconciliation is a no-op: the dropdowns list only "(Auto)"
// / "(default)" and profileFromWidgets falls back to the seeds, so
// nothing is lost.
func (c *Controller) reconcileDevices(atLoad bool) {
	nodes := c.caps.RenderNodes
	if len(nodes) == 0 {
		return
	}
	var notes []string

	// Resolve each dropdown's target: the loaded seed while it is one
	// of the detected nodes, otherwise the replacement. Only a
	// non-empty vanished seed counts as replaced (reported + reseeded);
	// an empty seed keeps its meaning — automatic selection for VAAPI
	// (the "(Auto)" option) and "ffmpeg's default" for QSV.
	// Seeds stay bare paths; only the dropdown target is rendered as
	// the node's current option label.
	vaapiTarget := c.vaapiDeviceSeed
	if slices.Contains(nodes, vaapiTarget) {
		vaapiTarget = vaapiDeviceOption(c.caps, vaapiTarget)
	}
	vaapiReplaced := false
	if !slices.Contains(nodes, c.vaapiDeviceSeed) {
		// An empty seed means automatic selection — a valid target
		// shown as "(Auto)", kept rather than force-picked to a node.
		vaapiTarget = vaapiAutoOption
		if c.vaapiDeviceSeed != "" {
			vaapiReplaced = true
			notes = append(notes, fmt.Sprintf("The VAAPI device %s was not detected; using automatic selection instead.", c.vaapiDeviceSeed))
			c.vaapiDeviceSeed = ""
		}
	}
	qsvTarget := c.qsvDeviceSeed
	qsvReplaced := false
	if !slices.Contains(nodes, qsvTarget) {
		// An empty QSV device means ffmpeg's default — a valid state,
		// kept visible as "(default)" rather than force-picked.
		qsvTarget = defaultOption
		if c.qsvDeviceSeed != "" {
			qsvReplaced = true
			notes = append(notes, fmt.Sprintf("The QSV device %s was not detected; using the default device instead.", c.qsvDeviceSeed))
			c.qsvDeviceSeed = ""
		}
	}

	// Only SetSelected when the value actually changes; Select fires
	// OnChanged on every assignment, which would re-enter
	// updateControlState via changed().
	selectTarget := func(sel *widget.Select, target string) {
		if sel.Selected != target {
			sel.SetSelected(target)
		}
	}
	if atLoad {
		selectTarget(c.vaapiDevice, vaapiTarget)
		selectTarget(c.qsvDevice, qsvTarget)
	} else {
		if vaapiReplaced || c.vaapiDevice.Selected == "" {
			selectTarget(c.vaapiDevice, vaapiTarget)
		}
		if qsvReplaced || c.qsvDevice.Selected == "" {
			selectTarget(c.qsvDevice, qsvTarget)
		}
	}

	if len(notes) > 0 && c.window != nil {
		dialog.ShowInformation("Profile devices", strings.Join(notes, "\n\n"), c.window)
	}
}

// updateAudioControls settles the audio mode/encoder state before the
// form rebuild: encode mode always has a real encoder selected (the
// dropdown never contains "copy"), with per-encoder bitrate options in
// place. A build with no audio encoders at all reverts the mode to
// copy visibly rather than leaving an encode selection that cannot be
// satisfied.
func (c *Controller) updateAudioControls() {
	if c.audioMode.Selected != model.AudioEncode {
		return
	}
	if len(c.audioEncoder.Options) == 0 {
		// Guarded revert: a visible state change beats a silent
		// profile swap (and validation failing at Start).
		if c.audioMode.Selected != model.AudioCopy {
			c.audioMode.SetSelected(model.AudioCopy)
		}
		return
	}
	if !slices.Contains(c.audioEncoder.Options, c.audioEncoder.Selected) {
		c.audioEncoder.SetSelected(c.audioEncoder.Options[0])
	}
	c.populateAudioBitrateOptions(c.audioEncoder.Selected, "")
}

// audioEncoderForName resolves the curated AudioEncoder for a dropdown
// name, or nil for unknown names (e.g. "copy").
func audioEncoderForName(name string) encoder.AudioEncoder {
	if e, ok := encoder.DefaultRegistry().Get(name); ok {
		ae, _ := e.(encoder.AudioEncoder)
		return ae
	}
	return nil
}

// rebuildAudioForm rebuilds the audio tab's dynamic form when the
// applicable row set changed (copy mode shows only Mode/all-audio/
// Subtitles; encode mode adds Encoder plus Bitrate or Compression).
func (c *Controller) rebuildAudioForm() {
	items := c.audioFormItems()
	rebuildFormOnSignature(c.audioFormBox, &c.audioFormSignature, items)
	// Visible rows are never disabled: hiding replaces greying out.
	c.audioMode.Enable()
	c.allAudio.Enable()
	c.subtitles.Enable()
	if c.audioMode.Selected != model.AudioEncode {
		return
	}
	c.audioEncoder.Enable()
	for _, item := range items {
		switch item.Text {
		case "Bitrate":
			c.audioBitrate.Enable()
		case "Compression":
			c.compression.Enable()
		}
	}
}

// audioFormItems computes the audio tab's row set from the current
// mode and encoder capabilities. The same widget pointers are reused
// across rebuilds, so no widget state is copied — only the row set
// moves between Form instances.
func (c *Controller) audioFormItems() []*widget.FormItem {
	items := []*widget.FormItem{
		widget.NewFormItem("Mode", c.audioMode),
	}
	if c.audioMode.Selected != model.AudioEncode {
		return append(items,
			widget.NewFormItem("", c.allAudio),
			widget.NewFormItem("Subtitles", c.subtitles),
		)
	}
	items = append(items, widget.NewFormItem("Encoder", c.audioEncoder))
	ae := audioEncoderForName(c.audioEncoder.Selected)
	switch {
	case ae != nil && len(ae.BitrateChoices()) > 0:
		items = append(items, widget.NewFormItem("Bitrate", c.audioBitrate))
	case ae != nil:
		if _, ok := ae.AudioCompressionRange(); ok {
			items = append(items, widget.NewFormItem("Compression", c.compression.Object()))
		}
	}
	return append(items,
		widget.NewFormItem("", c.allAudio),
		widget.NewFormItem("Subtitles", c.subtitles),
	)
}

// populateAudioBitrateOptions rebuilds the bitrate dropdown from the
// named encoder's choices. preferred (when non-empty) is the value the
// dropdown should land on; the current selection is used when
// preferred is empty, falling back to 160k (the historical default)
// or the median choice. Out-of-list preferred/current values are
// unioned into the options so hand-edited profiles stay visible and
// validation reports them. Unknown encoders ("copy", pre-selection)
// leave the dropdown untouched — it is hidden in those states and the
// audioBitrateSeed keeps the loaded value round-tripping.
func (c *Controller) populateAudioBitrateOptions(encoderName, preferred string) {
	ae := audioEncoderForName(encoderName)
	if ae == nil {
		return
	}
	options := slices.Clone(ae.BitrateChoices())
	target := preferred
	if target == "" {
		target = c.audioBitrate.Selected
	}
	for _, extra := range []string{target, c.audioBitrate.Selected} {
		if extra != "" && !slices.Contains(options, extra) {
			options = append(options, extra)
		}
	}
	c.audioBitrate.Options = options
	if target == "" {
		switch {
		case slices.Contains(options, "160k"):
			target = "160k"
		case len(options) > 0:
			target = options[len(options)/2]
		}
	}
	if target != "" && c.audioBitrate.Selected != target {
		c.audioBitrate.SetSelected(target)
	}
}

func (c *Controller) updateControlState() {
	encoderName := c.videoEncoder.Selected
	copyVideo := encoderName == "copy" || encoderName == ""

	// Look up the encoder from the registry. Unknown names (e.g. before
	// detection completes, or a name stripped by ParseEncoders) leave ve
	// nil and the video tab collapses to the Encoder row — the dropdown
	// still updates so the user can pick another.
	var ve encoder.VideoEncoder
	if !copyVideo {
		if e, ok := encoder.DefaultRegistry().Get(encoderName); ok {
			ve, _ = e.(encoder.VideoEncoder)
		}
	}

	c.updateIntegerRanges()

	c.populateRateControlOptions("")

	mode := c.rateControl.Selected
	var presetOK, supportsPreset, supportsProfile bool
	var presetR encoder.IntegerRange
	if ve != nil {
		presetR, presetOK = ve.PresetRange()
		supportsProfile = ve.SupportsProfile()
		supportsPreset = ve.SupportsPreset()
	}
	integerPreset := presetOK && presetR.Max >= presetR.Min

	// Preset: toggle between text entry and integer slider. When
	// switching to an integer-preset encoder, sync the slider from the
	// text field if the user had a numeric value there.
	//
	// Both the text field and the slider are gated on SupportsPreset
	// (i.e. the encoder actually emits a preset flag). Encoders like
	// VAAPI that have no preset flag must not expose a writable field
	// here, because the saved value would never reach ffmpeg.
	switch {
	case !supportsPreset:
		c.presetSlider.Hide()
		c.preset.Hide()
	case integerPreset:
		if value, err := strconv.Atoi(strings.TrimSpace(c.preset.Text)); err == nil {
			c.presetSlider.SetValue(value)
		}
		c.preset.Hide()
		c.presetSlider.Show()
	default:
		c.presetSlider.Hide()
		c.preset.Show()
	}

	if supportsProfile {
		// The options must exist before any SetSelected; keep them in
		// sync with the encoder here so encoder switches relist values.
		c.populateProfileOptions("")
	} else {
		// The Profile row is hidden for these encoders (see
		// videoFormItems); clear the invisible selection so switching
		// back to a supporting encoder never resurrects a stale value.
		// The Selected != "" guard prevents ClearSelected from
		// re-firing OnChanged and re-entering updateControlState.
		c.videoProfile.Options = []string{}
		if c.videoProfile.Selected != "" {
			c.videoProfile.ClearSelected()
		}
	}

	// The rate-control hint explains the selected mode — this is what
	// makes Auto and VBR legible. The hint lives in its own label so
	// mode switches that keep the same row set update text without a
	// form rebuild.
	if hint := rateControlHintText(mode); c.rateControlHint.Text != hint {
		c.rateControlHint.SetText(hint)
	}

	c.rebuildVideoForm()
	c.updateVAAPIDeviceHint()
	c.updateQSVDeviceHint()

	// Everything visible is enabled; the form rebuild is what expresses
	// applicability. The preset control keeps its internal entry/slider
	// toggle (both live in one form row).
	c.rateControl.Enable()
	c.qp.Enable()
	c.crf.Enable()
	c.bitrate.Enable()
	c.maxrate.Enable()
	c.bufsize.Enable()
	c.preset.Enable()
	c.presetSlider.Enable()
	c.tune.Enable()
	c.videoProfile.Enable()
	c.frameMode.Enable()
	c.frameRate.Enable()
	c.maxWidth.Enable()
	c.maxHeight.Enable()
	c.keepAspect.Enable()
	c.noUpscale.Enable()
	// The device dropdowns list "(Auto)" / "(default)" even without
	// detected nodes; the seed fallback in profileFromWidgets still
	// round-trips the saved value through an empty selection.
	if len(c.vaapiDevice.Options) > 0 {
		c.vaapiDevice.Enable()
	}
	if len(c.qsvDevice.Options) > 0 {
		c.qsvDevice.Enable()
	}
	c.gpuIndex.Enable()

	// Audio rows: visibility instead of grey-out. updateAudioControls
	// first settles the encoder selection and bitrate options, then the
	// form rebuild expresses the applicable row set; visible rows are
	// always enabled.
	c.updateAudioControls()
	c.rebuildAudioForm()
}

// rateControlHintText explains the selected rate-control mode: what
// the user is expected to set, and what the encoder does with it. An
// unknown/empty mode returns the empty string.
func rateControlHintText(mode string) string {
	switch {
	case strings.EqualFold(mode, "Auto"):
		return "Encoder default rate control. A target bitrate is optional."
	case strings.EqualFold(mode, "CQP"):
		return "Constant quality: every frame gets the same quantization parameter. Set QP (lower is better quality)."
	case strings.EqualFold(mode, "CRF"):
		return "Constant quality: set CRF (lower is better quality)."
	case strings.EqualFold(mode, "VBR"):
		return "Set a target bitrate; maximum bitrate and buffer size cap the peaks."
	case strings.EqualFold(mode, "CBR"):
		return "Constant bitrate: set the target bitrate (the maximum is forced to match)."
	case strings.EqualFold(mode, "bitrate"):
		return "Set the target bitrate."
	}
	return ""
}

// currentVideoEncoder resolves the selected video encoder, or nil for
// "copy", an empty selection, or an unknown name.
func (c *Controller) currentVideoEncoder() encoder.VideoEncoder {
	name := c.videoEncoder.Selected
	if name == "" || name == "copy" {
		return nil
	}
	if e, ok := encoder.DefaultRegistry().Get(name); ok {
		ve, _ := e.(encoder.VideoEncoder)
		return ve
	}
	return nil
}

// rebuildVideoForm rebuilds the Video tab's dynamic form when the
// applicable row set changed; see videoFormItems for the matrix.
func (c *Controller) rebuildVideoForm() {
	rebuildFormOnSignature(c.videoFormBox, &c.videoFormSignature, c.videoFormItems())
}

// videoFormItems computes the Video tab's row set from the selected
// encoder and rate-control mode. Rows that cannot affect the command
// are left out entirely (hide over grey-out): copy mode collapses the
// tab to the Encoder row, and each rate-control mode shows only the
// controls its BuildVideoArgs consumes.
func (c *Controller) videoFormItems() []*widget.FormItem {
	items := []*widget.FormItem{
		widget.NewFormItem("Encoder", c.videoEncoder),
	}
	ve := c.currentVideoEncoder()
	if ve == nil {
		return items
	}

	if len(ve.RateControlModes()) > 0 {
		items = append(items,
			widget.NewFormItem("Rate control", c.rateControl),
			widget.NewFormItem("", c.rateControlHint),
		)
	}
	mode := c.rateControl.Selected
	rateCapped := strings.EqualFold(mode, "VBR") || strings.EqualFold(mode, "CBR")
	bitrateShown := rateCapped || strings.EqualFold(mode, "bitrate") ||
		(strings.EqualFold(mode, "Auto") && ve.HWBackend() == encoder.HWVAAPI)
	switch {
	case strings.EqualFold(mode, "CQP"):
		items = append(items, widget.NewFormItem("QP", c.qp.Object()))
	case strings.EqualFold(mode, "CRF"):
		items = append(items, widget.NewFormItem("CRF", c.crf.Object()))
	case bitrateShown:
		items = append(items, widget.NewFormItem("Target bitrate", c.bitrate))
	}
	if rateCapped {
		items = append(items,
			widget.NewFormItem("Maximum bitrate", c.maxrate),
			widget.NewFormItem("Buffer size", c.bufsize),
		)
	}
	if ve.SupportsPreset() {
		items = append(items, widget.NewFormItem("Preset", c.presetControl))
	}
	if ve.SupportsTune() {
		items = append(items, widget.NewFormItem("Tune", c.tune))
	}
	if ve.SupportsProfile() {
		items = append(items, widget.NewFormItem("Profile", c.videoProfile))
	}
	items = append(items, widget.NewFormItem("Frame rate mode", c.frameMode))
	if c.frameMode.Selected == "convert" {
		items = append(items, widget.NewFormItem("Frame rate", c.frameRate))
	}
	items = append(items,
		widget.NewFormItem("Maximum width", c.maxWidth),
		widget.NewFormItem("Maximum height", c.maxHeight),
		// c.aspectFlags is a Controller field: a fresh container here
		// would change the signature on every call and rebuild the
		// form on each keystroke.
		widget.NewFormItem("", c.aspectFlags),
	)
	switch ve.HWBackend() {
	case encoder.HWVAAPI:
		items = append(items,
			widget.NewFormItem("VAAPI device", c.vaapiDevice),
			// The hint is a stable-pointer caption row (like the
			// rate-control hint): it is added unconditionally within
			// this branch, so the form signature does not churn.
			widget.NewFormItem("", c.vaapiDeviceHint),
		)
	case encoder.HWQSV:
		items = append(items,
			widget.NewFormItem("QSV device", c.qsvDevice),
			// Same stable-pointer caption pattern as the VAAPI
			// hint: added unconditionally in this branch, so the
			// form signature does not churn.
			widget.NewFormItem("", c.qsvDeviceHint),
		)
	case encoder.HWNVENC:
		gpuItem := widget.NewFormItem("GPU index", c.gpuIndex)
		gpuItem.HintText = "NVIDIA GPU to use when several are present"
		items = append(items, gpuItem)
	}
	return items
}

// rebuildFormOnSignature swaps the form inside box when signature
// changed. Fyne's widget.Form cannot hide rows (FormLayout skips a row
// only when both cells are invisible) and mutating Form.Items is
// unsafe (the render pass pairs items and labels positionally), so a
// changed row set is expressed by building a fresh Form from the same
// widget pointers. The signature guard keeps identical states from
// rebuilding on every changed() tick. None of the widgets placed in
// these forms implement fyne.Validatable, so a rebuild cannot leak
// stale validation state.
func rebuildFormOnSignature(box *fyne.Container, signature *string, items []*widget.FormItem) {
	newSig := formItemsSignature(items)
	if *signature == newSig {
		return
	}
	*signature = newSig
	box.RemoveAll()
	box.Add(widget.NewForm(items...))
	box.Refresh()
}

// formItemsSignature builds a comparable key from a form item list so
// a stale signature is detected even when the caller's cached key and
// the actual row set drift apart.
func formItemsSignature(items []*widget.FormItem) string {
	var b strings.Builder
	for _, item := range items {
		b.WriteString(item.Text)
		b.WriteString("\x00")
		fmt.Fprintf(&b, "%p", item.Widget)
		b.WriteString("\x00")
	}
	return b.String()
}

func (c *Controller) updateIntegerRanges() {
	if r, ok := validation.QPRange(c.videoEncoder.Selected); ok {
		// Only remap when the slider's range was actually set by a previous
		// encoder that supported QP. If qpRangeEncoder is empty (initial state
		// or the prior encoder did not expose QP) or matches the current
		// encoder, the range is either unset or already in sync — just set
		// it and let SetRange's Clamp keep the stored value sensible.
		if c.qpRangeEncoder != "" && c.qpRangeEncoder != c.videoEncoder.Selected {
			// Capture the value before mutating the slider. SetRange
			// internally clamps the value, so reading c.qp.Value()
			// after SetRange would remap the already-clamped value
			// rather than the original.
			old := c.qp.Range()
			oldValue := c.qp.Value()
			c.qp.SetRange(r)
			c.qp.SetValue(validation.ProportionalConvert(oldValue, old, r))
		} else {
			c.qp.SetRange(r)
		}
		c.qpRangeEncoder = c.videoEncoder.Selected
	} else {
		c.qpRangeEncoder = ""
	}
	if r, ok := validation.CRFRange(c.videoEncoder.Selected); ok {
		if c.crfRangeEncoder != "" && c.crfRangeEncoder != c.videoEncoder.Selected {
			old := c.crf.Range()
			oldValue := c.crf.Value()
			c.crf.SetRange(r)
			c.crf.SetValue(validation.ProportionalConvert(oldValue, old, r))
		} else {
			c.crf.SetRange(r)
		}
		c.crfRangeEncoder = c.videoEncoder.Selected
	} else {
		c.crfRangeEncoder = ""
	}
	// Preset slider: only integer-preset encoders own this range.
	// libsvtav1 {0,13} -> libaom-av1 {0,8} should remap 6 -> 4, while
	// categorical->integer (e.g. libx264 "slow" -> libsvtav1) must NOT
	// remap a stale "6" left over from a default; an empty
	// presetRangeEncoder signals the slider has never been owned by an
	// integer encoder and we should just clamp without remap.
	if r, ok := validation.PresetRange(c.videoEncoder.Selected); ok && r.Max >= r.Min {
		if c.presetRangeEncoder != "" && c.presetRangeEncoder != c.videoEncoder.Selected {
			old := c.presetSlider.Range()
			oldValue := c.presetSlider.Value()
			c.presetSlider.SetRange(r)
			c.presetSlider.SetValue(validation.ProportionalConvert(oldValue, old, r))
		} else {
			c.presetSlider.SetRange(r)
		}
		c.presetRangeEncoder = c.videoEncoder.Selected
	} else {
		c.presetRangeEncoder = ""
	}
}

func (c *Controller) presetValue() string {
	if r, ok := validation.PresetRange(c.videoEncoder.Selected); ok && r.Max >= r.Min {
		return strconv.Itoa(c.presetSlider.Value())
	}
	return c.preset.Text
}

func (c *Controller) updateEncoderOptions() {
	// "copy" stays a video option (video passthrough is a real mode)
	// but is never an audio encoder option: audio copy mode is
	// expressed by Mode alone.
	video, audio := []string{"copy"}, []string{}
	for name, enc := range c.caps.Encoders {
		if name == "copy" {
			continue
		}
		if enc.Media == ffmpeg.MediaVideo {
			video = append(video, name)
		}
		if enc.Media == ffmpeg.MediaAudio {
			audio = append(audio, name)
		}
	}
	sort.Strings(video[1:])
	sort.Strings(audio)
	c.videoEncoder.Options = video
	c.videoEncoder.Refresh()
	c.audioEncoder.Options = audio
	c.audioEncoder.Refresh()
}

func (c *Controller) saveCurrentProfile() {
	p := c.profileFromWidgets()
	for i := range c.config.Profiles {
		if c.config.Profiles[i].Name == p.Name {
			c.config.Profiles[i] = p
			c.saveConfig()
			return
		}
	}
}

func (c *Controller) saveAs(name string) {
	p := c.profileFromWidgets()
	p.Name = name
	if err := profiles.Add(&c.config, p); err != nil {
		dialog.ShowError(err, c.window)
		return
	}
	c.profile.Options = c.profileNames()
	c.profile.Refresh()
	c.loadProfile(p)
	c.saveConfig()
}

func (c *Controller) renameProfile(name string) {
	old := c.profile.Selected
	if err := profiles.Rename(&c.config, old, name); err != nil {
		dialog.ShowError(err, c.window)
		return
	}
	c.profile.Options = c.profileNames()
	c.profile.Refresh()
	c.profile.SetSelected(name)
	c.saveConfig()
}

func (c *Controller) deleteProfile() {
	name := c.profile.Selected
	dialog.NewConfirm("Delete profile", "Delete profile "+name+"?", func(ok bool) {
		if !ok {
			return
		}
		if err := profiles.Delete(&c.config, name); err != nil {
			dialog.ShowError(err, c.window)
			return
		}
		c.profile.Options = c.profileNames()
		c.profile.Refresh()
		c.loadProfile(c.startupProfile())
		c.saveConfig()
	}, c.window).Show()
}

func (c *Controller) setDefault() {
	c.config.DefaultProfile = c.profile.Selected
	c.saveConfig()
}

func (c *Controller) askProfileName(title, initial string, callback func(string)) {
	entry := widget.NewEntry()
	entry.SetText(initial)
	items := []*widget.FormItem{
		widget.NewFormItem("Profile name", entry),
	}
	d := dialog.NewForm(title, "OK", "Cancel", items, func(ok bool) {
		if !ok {
			return
		}
		name := strings.TrimSpace(entry.Text)
		if name != "" {
			callback(name)
		}
	}, c.window)
	d.Resize(fyne.NewSize(560, 180))
	d.Show()
}

func (c *Controller) showSettings() {
	ffmpegPath := widget.NewEntry()
	ffmpegPath.SetText(c.config.FFmpeg.FFmpegPath)
	ffprobePath := widget.NewEntry()
	ffprobePath.SetText(c.config.FFmpeg.FFprobePath)
	form := dialog.NewForm("FFmpeg settings", "Save and detect", "Cancel", []*widget.FormItem{
		widget.NewFormItem("FFmpeg path", ffmpegPath),
		widget.NewFormItem("ffprobe path", ffprobePath),
	}, func(ok bool) {
		if !ok {
			return
		}
		c.config.FFmpeg.FFmpegPath = strings.TrimSpace(ffmpegPath.Text)
		c.config.FFmpeg.FFprobePath = strings.TrimSpace(ffprobePath.Text)
		c.saveConfig()
		c.detect()
	}, c.window)
	form.Resize(fyne.NewSize(600, 240))
	form.Show()
}

func (c *Controller) saveConfig() {
	if err := profiles.Save(c.configPath, c.config); err != nil {
		dialog.ShowError(err, c.window)
	}
}

func (c *Controller) resolveOutput(p model.Profile, at time.Time) (string, error) {
	width, height := dimensions(p, c.media)
	return templates.Resolve(p.OutputTemplate, templates.Request{
		InputPath: c.inputPath, Extension: p.Container, Profile: p.Name,
		Codec: p.Video.Encoder, Width: width, Height: height, Time: at,
	})
}

func (c *Controller) startupProfile() model.Profile {
	if p, ok := profiles.Find(c.config.Profiles, c.config.DefaultProfile); ok {
		return p
	}
	return c.config.Profiles[0]
}

func (c *Controller) selectRunnableProfile() {
	current := c.profileFromWidgets()
	if validation.ValidateProfile(current, c.caps, nil).Valid() {
		return
	}
	for _, candidate := range c.config.Profiles {
		if validation.ValidateProfile(candidate, c.caps, nil).Valid() {
			c.loadProfile(candidate)
			return
		}
	}
}

func (c *Controller) profileNames() []string {
	names := make([]string, 0, len(c.config.Profiles))
	for _, p := range c.config.Profiles {
		names = append(names, p.Name)
	}
	return names
}

func entry(changed func(string)) *widget.Entry {
	e := widget.NewEntry()
	e.OnChanged = changed
	return e
}

func numericEntry(changed func(string)) *widget.Entry {
	e := entry(changed)
	e.Validator = func(s string) error {
		if s == "" {
			return nil
		}
		_, err := strconv.Atoi(s)
		return err
	}
	return e
}

func mustQPRange(encoder string) validation.IntegerRange {
	r, _ := validation.QPRange(encoder)
	return r
}

// rateControlLabel maps the encoder package's RateControl enum to the
// display strings the dropdown uses. Kept lower-case for "bitrate" to
// preserve profile-file compatibility — existing profile YAML stores
// the value verbatim as the selected dropdown label.
func rateControlLabel(rc encoder.RateControl) string {
	switch rc {
	case encoder.RCAuto:
		return "Auto"
	case encoder.RCCQP:
		return "CQP"
	case encoder.RCVBR:
		return "VBR"
	case encoder.RCCBR:
		return "CBR"
	case encoder.RCCRF:
		return "CRF"
	case encoder.RCBitrate:
		return "bitrate"
	}
	return string(rc)
}

func mustCRFRange(encoder string) validation.IntegerRange {
	r, _ := validation.CRFRange(encoder)
	return r
}

func mustPresetRange(encoder string) validation.IntegerRange {
	r, _ := validation.PresetRange(encoder)
	return r
}

func mustAudioCompressionRange(encoder string) validation.IntegerRange {
	r, _ := validation.AudioCompressionRange(encoder)
	return r
}

func atoi(value string) int {
	n, _ := strconv.Atoi(strings.TrimSpace(value))
	return n
}

func intText(value int) string {
	if value == 0 {
		return ""
	}
	return strconv.Itoa(value)
}

func dimensions(p model.Profile, media model.MediaInfo) (int, int) {
	width, height := p.Video.MaxWidth, p.Video.MaxHeight
	if source, ok := media.PrimaryVideo(); ok {
		if width <= 0 {
			width = source.Width
		}
		if height <= 0 {
			height = source.Height
		}
	}
	return width, height
}

func issueMessages(result validation.Result) []string {
	items := make([]string, 0, len(result.Errors)+len(result.Warnings))
	for _, issue := range result.Errors {
		items = append(items, "Error: "+issue.Message)
	}
	for _, issue := range result.Warnings {
		items = append(items, "Warning: "+issue.Message)
	}
	return items
}

// vaapiDevicesSummary renders the probed VAAPI devices for the
// detection card, e.g. "/dev/dri/renderD129 (Renoir; Mesa Gallium
// driver (radeonsi))". Devices whose probe failed are shown as
// unavailable. Empty when nothing was probed.
func vaapiDevicesSummary(caps model.Capabilities) string {
	if len(caps.VAAPIDevices) == 0 {
		return ""
	}
	parts := make([]string, 0, len(caps.VAAPIDevices))
	for _, d := range caps.VAAPIDevices {
		details := make([]string, 0, 2)
		if name := d.Identity.DisplayName(); name != "" {
			details = append(details, name)
		}
		switch {
		case !d.Known():
			details = append(details, "unavailable: "+d.Err)
		case d.Driver != "":
			details = append(details, d.Driver)
		}
		if len(details) == 0 {
			parts = append(parts, d.Path)
			continue
		}
		parts = append(parts, fmt.Sprintf("%s (%s)", d.Path, strings.Join(details, "; ")))
	}
	return strings.Join(parts, ", ")
}

// updateVAAPIDeviceHint explains the device selection: what "(Auto)"
// resolves to, and for manual picks which GPU sits behind the node
// and whether it can serve the current encoder. Hidden for non-VAAPI
// encoders, and for manual picks whose node has no probe record —
// identity travels inside the probe, so without one there is nothing
// reliable to say.
func (c *Controller) updateVAAPIDeviceHint() {
	ve := c.currentVideoEncoder()
	if ve == nil || ve.HWBackend() != encoder.HWVAAPI || c.vaapiDevice.Selected == "" {
		c.vaapiDeviceHint.SetText("")
		return
	}
	profile := profileOptionToValue(c.videoProfile.Selected)
	if c.vaapiDevice.Selected != vaapiAutoOption {
		c.vaapiDeviceHint.SetText(c.describeVAAPIDevice(vaapiDeviceValue(c.caps, c.vaapiDevice.Selected), ve, profile))
		return
	}
	choice, ok := c.resolveAutoVAAPI(ve.Name(), profile)
	if !ok {
		c.vaapiDeviceHint.SetText("")
		return
	}
	switch choice.Status {
	case vaapi.DeviceSelected:
		if d, known := deviceByPath(c.caps, choice.Path); known {
			c.vaapiDeviceHint.SetText("Auto: " + vaapi.DeviceDetail(d))
			return
		}
		if choice.Driver != "" {
			c.vaapiDeviceHint.SetText(fmt.Sprintf("Auto: %s — %s", choice.Path, choice.Driver))
		} else {
			c.vaapiDeviceHint.SetText("Auto: " + choice.Path)
		}
	case vaapi.DeviceFallback:
		if d, known := deviceByPath(c.caps, choice.Path); known {
			c.vaapiDeviceHint.SetText(fmt.Sprintf("Auto: %s (capabilities unknown)", vaapi.DeviceDetail(d)))
			return
		}
		c.vaapiDeviceHint.SetText(fmt.Sprintf("Auto: %s (capabilities unknown)", choice.Path))
	case vaapi.DeviceNoneCompatible:
		c.vaapiDeviceHint.SetText(fmt.Sprintf(warningPrefix+"Auto: no compatible device for %s profile %s",
			ve.Name(), validation.DisplayProfile(profile)))
	case vaapi.DeviceNoNodes:
		c.vaapiDeviceHint.SetText("Auto: no VAAPI render device detected")
	}
}

// describeVAAPIDevice renders one manual device selection for the
// hint: the full identity detail, plus the capability verdict for the
// current encoder when a profile requirement is known.
func (c *Controller) describeVAAPIDevice(node string, ve encoder.VideoEncoder, profile string) string {
	d, known := deviceByPath(c.caps, node)
	if !known {
		return ""
	}
	if !d.Known() {
		return fmt.Sprintf("%s — capabilities unknown (%s)", d.Path, d.Err)
	}
	text := vaapi.DeviceDetail(d)
	if required, ok := encoder.RequiredVAProfiles(ve.Name(), profile); ok && len(required) > 0 {
		if d.FailsRequirement(required) {
			text = warningPrefix + fmt.Sprintf("%s — does not encode %s profile %s", text, ve.Name(), validation.DisplayProfile(profile))
		} else {
			text += " — " + vaapi.DeviceCapability(d, required)
		}
	}
	return text
}

// updateQSVDeviceHint explains the QSV device selection: which GPU
// sits behind a manual node, and — because QSV only runs on Intel
// graphics — a warning when the picked device is definitively not an
// Intel GPU. The check is deliberately display-only and conservative:
// a PCI vendor ID is definitive, the VA driver string is a heuristic,
// and anything else stays quiet. ffmpeg's "(default)" device stays
// undescribed, since there is nothing reliable to say about a device
// ffmpeg has not picked yet.
func (c *Controller) updateQSVDeviceHint() {
	ve := c.currentVideoEncoder()
	if ve == nil || ve.HWBackend() != encoder.HWQSV || c.qsvDevice.Selected == "" || c.qsvDevice.Selected == defaultOption {
		c.qsvDeviceHint.SetText("")
		return
	}
	node := qsvDeviceValue(c.qsvDevice.Selected)
	d, known := deviceByPath(c.caps, node)
	if !known {
		c.qsvDeviceHint.SetText("")
		return
	}
	text := vaapi.DeviceDetail(d)
	if isIntel, intelKnown := d.IntelGraphicsVerdict(); intelKnown && !isIntel {
		text = warningPrefix + text + " — not an Intel GPU (QSV requires Intel graphics)"
	}
	c.qsvDeviceHint.SetText(text)
}

// hardwareDevicesSummary renders the probed hardware-encoder backends
// for the detection card, e.g. "vaapi, qsv, nvenc (2 GPUs)" or "none".
func hardwareDevicesSummary(caps model.Capabilities) string {
	var backends []string
	if caps.HWVAAPIAvailable {
		backends = append(backends, "vaapi")
	}
	if caps.HWQSVAvailable {
		backends = append(backends, "qsv")
	}
	if caps.HWNVENCAvailable {
		if caps.NVENCGPUCount > 1 {
			backends = append(backends, fmt.Sprintf("nvenc (%d GPUs)", caps.NVENCGPUCount))
		} else {
			backends = append(backends, "nvenc")
		}
	}
	if len(backends) == 0 {
		return "none"
	}
	return strings.Join(backends, ", ")
}

func formatMediaInfo(info model.MediaInfo) string {
	lines := []string{
		fmt.Sprintf("Format: %s | Duration: %s | Bitrate: %s", info.Format, info.Duration.Round(time.Second), formatBitrate(info.Bitrate)),
	}
	if v, ok := info.PrimaryVideo(); ok {
		lines = append(lines, fmt.Sprintf("Video: %s, %dx%d, %s fps, %s, range %s",
			v.Codec, v.Width, v.Height, v.FrameRate, v.PixelFormat, v.ColorRange))
	}
	for i, a := range info.AudioStreams {
		lines = append(lines, fmt.Sprintf("Audio %d: %s, %d Hz, %d channels", i+1, a.Codec, a.SampleRate, a.Channels))
	}
	lines = append(lines, fmt.Sprintf("Subtitles: %d | Chapters: %d", len(info.SubtitleStreams), info.ChapterCount))
	return strings.Join(lines, "\n")
}

func formatBitrate(value int64) string {
	if value <= 0 {
		return "unknown"
	}
	return fmt.Sprintf("%.1f Mbps", float64(value)/1_000_000)
}
