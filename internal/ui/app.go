package ui

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/storage"
	"fyne.io/fyne/v2/widget"

	"git.skobk.in/skobkin/yave/internal/ffmpeg"
	"git.skobk.in/skobkin/yave/internal/ffprobe"
	"git.skobk.in/skobkin/yave/internal/model"
	"git.skobk.in/skobkin/yave/internal/profiles"
	"git.skobk.in/skobkin/yave/internal/templates"
	"git.skobk.in/skobkin/yave/internal/transcode"
	"git.skobk.in/skobkin/yave/internal/validation"
)

const progressInfoWidthSample = "999h59m59s | 999.9x"

type Controller struct {
	app        fyne.App
	window     fyne.Window
	configPath string
	config     model.Config
	caps       model.Capabilities
	media      model.MediaInfo
	inputPath  string
	outputPath string
	runCancel  context.CancelFunc
	loading    bool

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
	qp            *integerSlider
	crf           *integerSlider
	// qpRangeEncoder and crfRangeEncoder record which encoder's range is
	// currently set on the QP and CRF sliders. An empty string means the
	// slider's range is stale (the prior encoder did not expose the control),
	// so updateIntegerRanges should not remap the stored value — it would
	// silently rewrite defaults that the user never touched.
	qpRangeEncoder  string
	crfRangeEncoder string
	bitrate         *widget.Entry
	maxrate         *widget.Entry
	bufsize         *widget.Entry
	preset          *widget.Entry
	presetSlider    *integerSlider
	presetControl   *fyne.Container
	tune            *widget.Entry
	videoProfile    *widget.Entry
	frameMode       *widget.Select
	frameRate       *widget.Entry
	maxWidth        *widget.Entry
	maxHeight       *widget.Entry
	keepAspect      *widget.Check
	noUpscale       *widget.Check
	vaapiDevice     *widget.Entry
	audioMode       *widget.Select
	audioEncoder    *widget.Select
	audioBitrate    *widget.Select
	compression     *integerSlider
	allAudio        *widget.Check
	subtitles       *widget.Select
	command         *widget.Entry
	progress        *widget.ProgressBar
	progressInfo    *widget.Label
	log             *widget.Entry
	start           *widget.Button
	cancel          *widget.Button
	tabs            *container.AppTabs
	runTab          *container.TabItem
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
	return c, nil
}

func (c *Controller) ShowAndRun() {
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

	c.videoEncoder = widget.NewSelect([]string{
		"copy", "hevc_vaapi", "h264_vaapi", "av1_vaapi", "vp9_vaapi",
		"libx265", "libx264", "libsvtav1", "libvpx-vp9",
	}, c.changed)
	c.rateControl = widget.NewSelect([]string{"Auto", "CQP", "VBR", "CBR", "CRF", "bitrate"}, c.changed)
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
	c.videoProfile = entry(c.changed)
	c.frameMode = widget.NewSelect([]string{"keep", "convert"}, c.changed)
	c.frameRate = entry(c.changed)
	c.maxWidth = numericEntry(c.changed)
	c.maxHeight = numericEntry(c.changed)
	c.keepAspect = widget.NewCheck("Keep aspect ratio", func(bool) { c.changed("") })
	c.noUpscale = widget.NewCheck("Prevent upscaling", func(bool) { c.changed("") })
	c.vaapiDevice = entry(c.changed)

	c.audioMode = widget.NewSelect([]string{model.AudioCopy, model.AudioEncode}, c.changed)
	c.audioEncoder = widget.NewSelect([]string{"copy", "libopus", "opus", "aac", "libmp3lame", "mp3", "flac"}, c.changed)
	c.audioBitrate = widget.NewSelect([]string{"96k", "128k", "160k", "192k", "256k", "320k"}, c.changed)
	c.compression = newIntegerSlider(mustAudioCompressionRange("flac"), 8, c.changed)
	c.allAudio = widget.NewCheck("Process all audio streams", func(bool) { c.changed("") })
	c.subtitles = widget.NewSelect([]string{model.SubtitleNone, model.SubtitleCopy}, c.changed)

	c.command = widget.NewMultiLineEntry()
	c.command.Disable()
	c.command.Wrapping = fyne.TextWrapWord
	c.command.SetMinRowsVisible(3)
	c.progress = widget.NewProgressBar()
	c.progressInfo = widget.NewLabel("")
	c.progressInfo.Truncation = fyne.TextTruncateEllipsis
	c.log = widget.NewMultiLineEntry()
	c.log.Disable()
	c.log.Wrapping = fyne.TextWrapWord
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

	videoTab := container.NewVScroll(container.NewVBox(
		widget.NewForm(
			widget.NewFormItem("Encoder", c.videoEncoder),
			widget.NewFormItem("Rate control", c.rateControl),
			widget.NewFormItem("QP", c.qp.Object()),
			widget.NewFormItem("CRF", c.crf.Object()),
			widget.NewFormItem("Target bitrate", c.bitrate),
			widget.NewFormItem("Maximum bitrate", c.maxrate),
			widget.NewFormItem("Buffer size", c.bufsize),
			widget.NewFormItem("Preset", c.presetControl),
			widget.NewFormItem("Tune", c.tune),
			widget.NewFormItem("Profile", c.videoProfile),
			widget.NewFormItem("Frame rate mode", c.frameMode),
			widget.NewFormItem("Frame rate", c.frameRate),
			widget.NewFormItem("Maximum width", c.maxWidth),
			widget.NewFormItem("Maximum height", c.maxHeight),
			widget.NewFormItem("VAAPI device", c.vaapiDevice),
		),
		container.NewHBox(c.keepAspect, c.noUpscale),
	))

	audioTab := container.NewVBox(widget.NewForm(
		widget.NewFormItem("Mode", c.audioMode),
		widget.NewFormItem("Encoder", c.audioEncoder),
		widget.NewFormItem("Bitrate", c.audioBitrate),
		widget.NewFormItem("FLAC compression", c.compression.Object()),
		widget.NewFormItem("", c.allAudio),
		widget.NewFormItem("Subtitles", c.subtitles),
	))

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

func (c *Controller) showOutputTemplateHelp() {
	const help = `{dir}       Input directory, for example /home/user/Videos
{name}      Input filename without extension, for example movie
{ext}       Output extension without dot, for example mkv
{input_ext} Input extension without dot, for example mp4
{datetime}  Local date and time, for example 20260614-153045
{date}      Local date, for example 20260614
{time}      Local time, for example 153045
{profile}   Selected profile name, for example HEVC_720p
{codec}     Selected video encoder, for example libx265
{width}     Output maximum or source width, for example 1280
{height}    Output maximum or source height, for example 720

Template:
{dir}/{name}_{profile}.{ext}

Result:
/home/user/Videos/movie_HEVC_720p.mkv`

	text := widget.NewMultiLineEntry()
	text.SetText(help)
	text.SetMinRowsVisible(16)
	text.Disable()

	copyButton := widget.NewButton("Copy all", func() {
		c.app.Clipboard().SetContent(help)
	})
	content := container.NewBorder(nil, copyButton, nil, nil, text)
	helpDialog := dialog.NewCustom("Output template placeholders", "Close", content, c.window)
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
			} else {
				c.caps = caps
				c.config.FFmpeg.FFmpegPath = caps.FFmpegPath
				c.config.FFmpeg.FFprobePath = caps.FFprobePath
				c.detection.SetText(fmt.Sprintf("%s\n%s\nHardware acceleration: %s",
					caps.FFmpegVersion, caps.FFprobeVersion, strings.Join(caps.HWAccels, ", ")))
				c.selectRunnableProfile()
			}
			c.updateEncoderOptions()
			c.refresh()
		})
	}()
}

func (c *Controller) changed(string) {
	if c.loading {
		return
	}
	c.updateControlState()
	c.refresh()
}

func (c *Controller) refresh() {
	p := c.profileFromWidgets()
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
			if buildErr == nil {
				c.command.SetText(plan.Preview)
			}
		}
	}
	if c.runCancel == nil {
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
	start := func(overwrite bool) {
		plan, buildErr := ffmpeg.BuildCommand(ffmpeg.BuildRequest{
			FFmpegPath: c.config.FFmpeg.FFmpegPath,
			InputPath:  c.inputPath, OutputPath: resolved,
			Profile: p, Media: c.media, Overwrite: overwrite,
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
}

func (c *Controller) run(plan model.RunPlan, messages []string) {
	ctx, cancel := context.WithCancel(context.Background())
	c.runCancel = cancel
	c.start.Disable()
	c.cancel.Enable()
	c.status.SetText("Running")
	c.progress.SetValue(0)
	c.progressInfo.SetText("")
	c.command.SetText(plan.Preview)
	for _, warning := range plan.Warnings {
		messages = append(messages, "Warning: "+warning)
	}
	if len(messages) == 0 {
		c.log.SetText("")
	} else {
		c.log.SetText(strings.Join(messages, "\n") + "\n\n")
	}
	go func() {
		err := (transcode.Runner{}).Run(ctx, plan, c.media.Duration, transcode.Callbacks{
			OnLog: func(line string) {
				fyne.Do(func() {
					c.log.SetText(c.log.Text + line + "\n")
				})
			},
			OnProgress: func(p model.Progress) {
				fyne.Do(func() {
					c.progress.SetValue(p.Percentage)
					c.progressInfo.SetText(formatProgressInfo(p))
				})
			},
		})
		fyne.Do(func() {
			c.runCancel = nil
			c.cancel.Disable()
			c.setRunResult(err)
			c.refresh()
		})
	}()
}

func formatProgressInfo(progress model.Progress) string {
	return fmt.Sprintf("%s | %s", progress.OutTime.Round(time.Second), progress.Speed)
}

func (c *Controller) setRunResult(err error) {
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
}

func (c *Controller) cancelConversion() {
	if c.runCancel != nil {
		c.runCancel()
	}
}

func (c *Controller) loadProfile(p model.Profile) {
	c.loading = true
	defer func() {
		c.loading = false
		c.updateControlState()
		c.refresh()
	}()
	c.profile.SetSelected(p.Name)
	c.container.SetSelected(p.Container)
	c.output.SetText(p.OutputTemplate)
	c.videoEncoder.SetSelected(p.Video.Encoder)
	c.rateControl.SetSelected(p.Video.RateControl)
	c.updateIntegerRanges()
	c.qp.SetValue(p.Video.QP)
	c.crf.SetValue(p.Video.CRF)
	c.bitrate.SetText(p.Video.Bitrate)
	c.maxrate.SetText(p.Video.Maxrate)
	c.bufsize.SetText(p.Video.Bufsize)
	c.preset.SetText(p.Video.Preset)
	c.tune.SetText(p.Video.Tune)
	c.videoProfile.SetText(p.Video.Profile)
	c.frameMode.SetSelected(p.Video.FrameRateMode)
	c.frameRate.SetText(p.Video.FrameRate)
	c.maxWidth.SetText(intText(p.Video.MaxWidth))
	c.maxHeight.SetText(intText(p.Video.MaxHeight))
	c.keepAspect.SetChecked(p.Video.KeepAspectRatio)
	c.noUpscale.SetChecked(p.Video.PreventUpscaling)
	c.vaapiDevice.SetText(p.Video.VAAPIDevice)
	c.audioMode.SetSelected(p.Audio.Mode)
	c.audioEncoder.SetSelected(p.Audio.Encoder)
	c.audioBitrate.SetSelected(p.Audio.Bitrate)
	c.compression.SetValue(p.Audio.Compression)
	c.allAudio.SetChecked(p.Audio.CopyAllStreams)
	c.subtitles.SetSelected(p.Subtitles.Mode)
}

func (c *Controller) profileFromWidgets() model.Profile {
	return model.Profile{
		Name: c.profile.Selected, Container: c.container.Selected, OutputTemplate: c.output.Text,
		Video: model.VideoSettings{
			Encoder: c.videoEncoder.Selected, RateControl: c.rateControl.Selected,
			QP: c.qp.Value(), CRF: c.crf.Value(), Bitrate: c.bitrate.Text,
			Maxrate: c.maxrate.Text, Bufsize: c.bufsize.Text, Preset: c.presetValue(),
			Tune: c.tune.Text, Profile: c.videoProfile.Text,
			FrameRateMode: c.frameMode.Selected, FrameRate: c.frameRate.Text,
			MaxWidth: atoi(c.maxWidth.Text), MaxHeight: atoi(c.maxHeight.Text),
			KeepAspectRatio: c.keepAspect.Checked, PreventUpscaling: c.noUpscale.Checked,
			VAAPIDevice: c.vaapiDevice.Text,
		},
		Audio: model.AudioSettings{
			Mode: c.audioMode.Selected, Encoder: c.audioEncoder.Selected,
			Bitrate: c.audioBitrate.Selected, Compression: c.compression.Value(),
			CopyAllStreams: c.allAudio.Checked,
		},
		Subtitles: model.SubtitleSettings{Mode: c.subtitles.Selected},
	}
}

func (c *Controller) updateControlState() {
	encoder := c.videoEncoder.Selected
	c.updateIntegerRanges()
	copyVideo := encoder == "copy"
	vaapi := strings.HasSuffix(encoder, "_vaapi")
	setEnabled(c.rateControl, !copyVideo)
	setEnabled(c.qp, vaapi && strings.EqualFold(c.rateControl.Selected, "CQP"))
	setEnabled(c.crf, !copyVideo && !vaapi && !strings.EqualFold(c.rateControl.Selected, "bitrate"))
	bitrateMode := strings.EqualFold(c.rateControl.Selected, "VBR") ||
		strings.EqualFold(c.rateControl.Selected, "CBR") ||
		strings.EqualFold(c.rateControl.Selected, "bitrate") ||
		strings.EqualFold(c.rateControl.Selected, "Auto")
	setEnabled(c.bitrate, !copyVideo && bitrateMode)
	setEnabled(c.maxrate, vaapi && (strings.EqualFold(c.rateControl.Selected, "VBR") || strings.EqualFold(c.rateControl.Selected, "CBR")))
	setEnabled(c.bufsize, vaapi && (strings.EqualFold(c.rateControl.Selected, "VBR") || strings.EqualFold(c.rateControl.Selected, "CBR")))
	svtPreset := encoder == "libsvtav1"
	if svtPreset {
		if value, err := strconv.Atoi(strings.TrimSpace(c.preset.Text)); err == nil {
			c.presetSlider.SetValue(value)
		}
		c.preset.Hide()
		c.presetSlider.Show()
	} else {
		c.presetSlider.Hide()
		c.preset.Show()
	}
	setEnabled(c.preset, encoder == "libx264" || encoder == "libx265" || encoder == "libvpx-vp9")
	setEnabled(c.presetSlider, svtPreset)
	setEnabled(c.tune, encoder == "libx264" || encoder == "libx265")
	setEnabled(c.videoProfile, vaapi || encoder == "libx264" || encoder == "libx265")
	setEnabled(c.frameMode, !copyVideo)
	setEnabled(c.frameRate, !copyVideo && c.frameMode.Selected == "convert")
	setEnabled(c.maxWidth, !copyVideo)
	setEnabled(c.maxHeight, !copyVideo)
	setEnabled(c.keepAspect, !copyVideo)
	setEnabled(c.noUpscale, !copyVideo)
	setEnabled(c.vaapiDevice, vaapi)

	copyAudio := c.audioMode.Selected == model.AudioCopy
	setEnabled(c.audioEncoder, !copyAudio)
	setEnabled(c.audioBitrate, !copyAudio && c.audioEncoder.Selected != "flac")
	setEnabled(c.compression, !copyAudio && c.audioEncoder.Selected == "flac")
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
}

func (c *Controller) presetValue() string {
	if c.videoEncoder.Selected == "libsvtav1" {
		return strconv.Itoa(c.presetSlider.Value())
	}
	return c.preset.Text
}

func (c *Controller) updateEncoderOptions() {
	video, audio := []string{"copy"}, []string{"copy"}
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
	sort.Strings(audio[1:])
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

type disableable interface {
	Enable()
	Disable()
}

func setEnabled(w disableable, enabled bool) {
	if enabled {
		w.Enable()
	} else {
		w.Disable()
	}
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
