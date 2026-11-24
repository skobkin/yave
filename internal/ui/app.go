package ui

import (
	"context"
	"sync/atomic"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/widget"
	"fyne.io/systray"

	"git.skobk.in/skobkin/yave/internal/model"
	"git.skobk.in/skobkin/yave/internal/notify"
	"git.skobk.in/skobkin/yave/internal/profiles"
	"git.skobk.in/skobkin/yave/internal/resources"
	"git.skobk.in/skobkin/yave/internal/transcode"
)

type Controller struct {
	// notifyGo runs a notification send; nil means a new goroutine. Tests
	// run it inline.
	notifyGo   func(func())
	app        fyne.App
	window     fyne.Window
	configPath string
	config     model.Config
	caps       model.Capabilities
	media      model.MediaInfo
	inputPath  string
	outputPath string
	runControl conversionControl
	// runDone closes when the run goroutine has fully unwound —
	// including staging-directory cleanup — so quitting mid-export can
	// await it instead of leaking yave-export-* intermediates: the
	// process dies when main returns, and goroutines cannot outlive it.
	runDone chan struct{}
	// runStages executes a staged conversion sequence. A seam so tests
	// can intercept the runner; New defaults it to
	// (transcode.Runner{}).RunSequence.
	runStages    func(*transcode.RunControl, []transcode.Stage, transcode.Callbacks) error
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
	// updateChecker polls the forge for new releases; nil when unavailable.
	updateChecker   updateChecker
	checkingUpdates atomic.Bool
	// trayApp publishes the tray menu; nil when no system tray is available.
	trayApp        desktop.App
	trayToggleItem *fyne.MenuItem
	trayCheckItem  *fyne.MenuItem
	trayQuitItem   *fyne.MenuItem

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
	// Trim state belongs to the current input/conversion job — it is
	// reset on every input change and never persisted into profiles.
	// See trim.go for the lifecycle rules.
	trimEnabled   bool
	trimSelection model.TrimSelection
	trimInValid   bool
	trimOutValid  bool
	// trimSelected is the index of the segment the boundary entries,
	// Set In/Out buttons, and Ctrl+[/] chords edit. UI-thread state.
	trimSelected int
	// trimExportMode is the job-scoped export mode (separate files per
	// segment vs. join into one output). Like the trim selection it is
	// reset on every input change and never persisted into profiles.
	trimExportMode model.TrimExportMode
	// displayingBoundaries guards programmatic writes into the boundary
	// entries: Entry.SetText fires OnChanged whenever the text differs,
	// and the editing callback would re-parse the millisecond-truncated
	// display text back over the exact selection. Set by
	// setBoundaryDisplay around every display write.
	displayingBoundaries bool
	inputGeneration      uint64 // guards late async results per input
	detectGeneration     uint64 // guards late FFmpeg detection results
	// discover runs FFmpeg capability detection; nil means ffmpeg.Discover.
	// Tests inject it to control timing.
	discover func(context.Context, model.FFmpegConfig) (model.Capabilities, error)
	previews trimPreviews

	// Optional Trim-tab playback (see playback.go): its state and the
	// transport buttons that sit next to the enable check.
	play              playbackState
	trimPlay          *widget.Button
	trimPlaySelection *widget.Button

	// tips draws button tooltips over the window content (Fyne has none);
	// the trim zoom buttons drive the timeline's visible window.
	tips        *tooltipLayer
	trimZoomOut *tipButton
	trimZoomIn  *tipButton
	trimZoomFit *tipButton
	trimZoomSel *tipButton

	// Snap toggle, marker navigation and "Segments from chapters…".
	trimSnap       *widget.Check
	trimPrevMarker *tipButton
	trimNextMarker *tipButton
	trimChapters   *widget.Button
	// trimCopyVideo mirrors whether the effective video encoder is stream
	// copy, and trimMsgEncoder/trimMsgAudio the inputs of the last trim
	// message, so asynchronous keyframe results can refresh the message.
	// Scene detection: the button, the live threshold slider, a status
	// line, and the containers that hide them when the build lacks the
	// needed filters or there are no results yet.
	scenes              sceneState
	sceneDetect         *tipButton
	sceneThreshold      *widget.Slider
	sceneThresholdLabel *widget.Label
	sceneStatus         *widget.Label
	trimScenesBox       *fyne.Container
	sceneThresholdBox   *fyne.Container

	// Waveform lane: its analysis state, a status line, and the trim-tab
	// containers to re-lay-out when the lane changes the timeline's
	// minimum height.
	waves           waveState
	waveStatus      *widget.Label
	trimControlsBox *fyne.Container
	trimTabBox      *fyne.Container

	trimCopyVideo  bool
	trimMsgEncoder string
	trimMsgAudio   model.AudioSettings
	// confirmHook and chapterPickerHook replace the real dialogs in tests.
	confirmHook       func(title, message string, onYes func())
	chapterPickerHook func(onPicked func(picked []int))

	trimEnable             *widget.Check
	savedProfile           model.Profile // editor baseline for dirty tracking
	hasSavedProfile        bool
	profileLabel           *widget.Label
	includeVideo           *widget.Check
	includeAudio           *widget.Check
	includeVideoHint       *widget.Label
	includeAudioHint       *widget.Label
	userIncludeVideo       bool // the user's (saved) choice; the checkbox shows the effective state
	userIncludeAudio       bool
	applyingStreamOverride bool
	notifier               notify.Notifier
	segmentPanel           *fyne.Container
	segmentHeader          *widget.Label
	segmentRows            *fyne.Container
	segmentRowViews        []segmentRow
	showAllFiles           *widget.Check
	shortcutHandlers       map[fyne.KeyName]func() // Ctrl+<key> handlers, also driven by tests
	defaultMarker          *widget.Label
	retryDetect            *widget.Button
	detectActivity         *widget.Activity
	detecting              bool
	openFolder             *widget.Button
	playOutput             *widget.Button
	runResult              *widget.Label
	lastOutputs            []string
	runPercent             int  // progress shown in the title while runShowing
	runShowing             bool // a run is going and its progress is in the title
	startHint              *widget.Label
	logFollow              *widget.Check
	logPending             string
	runInput               string
	logPendingSet          bool
	openLog                *widget.Button
	copyCommand            *widget.Button
	trimHint               *widget.Label // why trimming is unavailable
	inspecting             bool          // ffprobe is running for the current input
	trimTimeline           *timeline
	trimIn                 *widget.Entry
	trimOut                *widget.Entry
	trimSetIn              *widget.Button
	trimSetOut             *widget.Button
	trimReset              *widget.Button
	trimExport             *widget.Select
	trimAdd                *widget.Button
	trimDelete             *widget.Button
	trimDuration           *widget.Label
	trimMessage            *widget.Label // trim validation: entry errors + copy warning
	previewStatus          *widget.Label // extraction/indexing status, separate from validation
	previewTimeLabel       *widget.Label // timestamp of the displayed preview frame
	hoverTimeLabel         *widget.Label // hovered timestamp, shown while hovering
	previewImage           *canvas.Image
	previewVeil            *canvas.Rectangle   // foreground tint over the stale frame while extracting
	previewSpinner         *spinner            // extraction-in-flight cue spinning over the preview's center
	trimControls           []fyne.CanvasObject // disabled while trimming is off
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
	// presetSelect shows the curated preset list when the selected
	// encoder curates one (Presets()); otherwise the free-text entry or
	// the integer slider applies. All three live in presetControl.
	presetSelect  *widget.Select
	presetControl *fyne.Container
	tune          *widget.Entry
	// tuneSelect shows the curated tune list when the selected encoder
	// curates one (Tunes()); tuneControl stacks it with the free-text
	// entry.
	tuneSelect   *widget.Select
	tuneControl  *fyne.Container
	videoProfile *widget.Select
	frameMode    *widget.Select
	frameRate    *widget.Entry
	maxWidth     *widget.Entry
	maxHeight    *widget.Entry
	keepAspect   *widget.Check
	noUpscale    *widget.Check
	vaapiDevice  *widget.Select
	qsvDevice    *widget.Select
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
	// gpuIndexEntry is the free numeric fallback; gpuIndexSelect lists
	// the detected NVIDIA GPU indices (0..N-1) once detection reported
	// any. gpuIndexControl stacks the two so the NVENC form row can
	// swap widgets without rebuilding the form. gpuIndexSeed records
	// the loaded profile's index while the entry shows empty text so a
	// Save before detection round-trips the loaded value. gpu.go owns
	// the mode switching and the load-vs-detection rules.
	gpuIndexEntry   *widget.Entry
	gpuIndexSelect  *widget.Select
	gpuIndexControl *fyne.Container
	gpuIndexSeed    int
	audioMode       *widget.Select
	audioEncoder    *widget.Select
	audioBitrate    *widget.Select
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
	c.previews = newTrimPreviews()
	c.runStages = (transcode.Runner{}).RunSequence
	c.createWidgets()
	c.window.SetContent(c.withTooltips(c.buildLayout()))
	c.registerShortcuts()
	c.window.Resize(initialWindowSize(cfg.UI.WindowWidth, cfg.UI.WindowHeight))
	c.window.CenterOnScreen()
	c.window.SetIcon(resources.AppIcon())
	c.window.SetOnDropped(func(_ fyne.Position, uris []fyne.URI) { c.handleDrop(uris) })
	c.loadProfile(c.startupProfile())
	c.detect()
	if initialInput != "" {
		c.setInput(initialInput)
	}
	if desk, ok := a.(desktop.App); ok {
		c.configureTray(desk)
	}
	c.setupNotifier()
	c.setupUpdateChecker()
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
	c.trayApp = desk
	c.trayToggleItem = fyne.NewMenuItem(trayToggleLabel(true), func() {
		fyne.Do(c.toggleWindow)
	})
	c.trayCheckItem = fyne.NewMenuItem("Check for updates…", func() {
		// Fyne's tray menus already dispatch item actions onto the UI
		// thread; the fyne.Do keeps that true even for a backend that
		// delivers them off-thread, as the icon tap handler below needs.
		fyne.Do(c.checkForUpdates)
	})
	c.trayQuitItem = fyne.NewMenuItem("Quit", c.requestQuit)
	c.trayQuitItem.IsQuit = true
	desk.SetSystemTrayMenu(c.trayMenu())
	c.window.SetCloseIntercept(c.onCloseRequested)
	systray.SetOnTapped(func() { fyne.Do(c.toggleWindow) })
}

func trayToggleLabel(visible bool) string {
	if visible {
		return "Hide YAVE"
	}
	return "Show YAVE"
}

// toggleWindow shows the window when hidden and hides it when visible.
// Must run on the UI thread.
func (c *Controller) toggleWindow() {
	if c.windowVisible {
		c.hideWindow()
		return
	}
	c.window.Show()
	c.setWindowVisible(true)
}

// setWindowVisible records the window's visibility and keeps the tray
// toggle item's label in step with it, republishing the menu when the
// label changed (AppIndicator hosts do not poll the item).
func (c *Controller) setWindowVisible(visible bool) {
	c.windowVisible = visible
	if c.trayToggleItem == nil {
		return
	}
	label := trayToggleLabel(visible)
	if c.trayToggleItem.Label == label {
		return
	}
	c.trayToggleItem.Label = label
	if c.trayApp != nil {
		c.trayApp.SetSystemTrayMenu(c.trayMenu())
	}
}

func (c *Controller) hideWindow() {
	c.rememberWindowSize()
	c.window.Hide()
	c.setWindowVisible(false)
}

// onCloseRequested handles the window close button. With "quit on close"
// it goes through the normal quit flow (including the run-in-progress
// prompt); otherwise it hides the window to the tray, explaining once how
// to get it back.
func (c *Controller) onCloseRequested() {
	if c.config.UI.QuitOnClose {
		c.requestQuit()
		return
	}
	if c.config.UI.TrayNoticeShown {
		c.hideWindow()
		return
	}
	c.config.UI.TrayNoticeShown = true
	c.saveConfig()
	notice := dialog.NewInformation("YAVE keeps running",
		"YAVE keeps running in the system tray. Use the tray icon menu (Show YAVE) to bring the window back, or turn off \"Close to tray\" in Settings to quit when the window is closed.",
		c.window)
	// The dialog lives on the window, so hide the window only once the
	// notice is dismissed.
	notice.SetOnClosed(c.hideWindow)
	notice.Show()
}

// trayMenu assembles the system tray menu; the single source of the item
// list so republishing stays in sync with configureTray.
func (c *Controller) trayMenu() *fyne.Menu {
	items := make([]*fyne.MenuItem, 0, 3)
	if c.trayToggleItem != nil {
		items = append(items, c.trayToggleItem)
	}
	items = append(items, c.trayCheckItem, c.trayQuitItem)
	return fyne.NewMenu("YAVE", items...)
}

func (c *Controller) ShowAndRun() {
	c.window.Show()
	c.setWindowVisible(true)
	c.startUpdateChecks()
	c.window.ShowAndRun()
	if c.updateChecker != nil {
		// Not in quitApplication: Stop waits out an in-flight check, which
		// must not delay the UI-thread quit path. After ShowAndRun returns
		// the app is exiting anyway.
		c.updateChecker.Stop()
	}
}

func (c *Controller) requestQuit() {
	if c.runControl == nil {
		// The unsaved-changes prompt lives on the window; a hidden window
		// (tray Quit) would leave it unanswerable.
		if c.profileDirty() {
			c.revealMainWindowIfHidden()
		}
		c.guardUnsavedProfile(c.quitApplication)
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

// quitCleanupTimeout bounds the quit-time wait for the run goroutine
// (see quitApplication). A var so tests can shorten it.
var quitCleanupTimeout = 10 * time.Second

func (c *Controller) quitApplication() {
	control := c.runControl
	if control != nil {
		control.Terminate()
	}
	c.runControl = nil
	c.stopCancelTimer()
	c.cancelTrimWork()
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
	c.rememberWindowSize()
	c.window.SetCloseIntercept(nil)
	// Terminate killed FFmpeg, so the run goroutine unwinds quickly; the
	// staging directory is removed at the end of that unwind, after
	// RunSequence returns. The app process dies the moment ShowAndRun
	// returns to main, so quitting mid-export must wait for the unwind
	// or completed intermediates leak in yave-export-* directories
	// indefinitely. The UI thread blocks only for the kill window, and
	// the goroutine's final dispatch is a non-blocking fyne.Do, so there
	// is no deadlock. Bounded: a wedged stage delays exit by
	// quitCleanupTimeout at most, and then leaks exactly as before the
	// wait existed.
	if c.runDone != nil {
		select {
		case <-c.runDone:
		case <-time.After(quitCleanupTimeout):
		}
	}
	c.app.Quit()
}
