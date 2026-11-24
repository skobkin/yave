package ui

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/widget"

	"git.skobk.in/skobkin/yave/internal/encoder"
	"git.skobk.in/skobkin/yave/internal/model"
)

// newStreamIncludeChecks builds the "Include video" / "Include audio"
// checkboxes. They show the EFFECTIVE state: an input without that
// stream forces the box off and disabled (a job-level override), while
// the user's own choice — the one saved with the profile — lives in
// userIncludeVideo / userIncludeAudio and is restored when a later input
// has the stream again.
func (c *Controller) newStreamIncludeChecks() {
	c.userIncludeVideo, c.userIncludeAudio = true, true
	c.includeVideo = widget.NewCheck("Include video", func(on bool) {
		if c.loading || c.applyingStreamOverride {
			return
		}
		c.userIncludeVideo = on
		c.changed("")
	})
	c.includeVideo.Checked = true
	c.includeAudio = widget.NewCheck("Include audio", func(on bool) {
		if c.loading || c.applyingStreamOverride {
			return
		}
		c.userIncludeAudio = on
		c.changed("")
	})
	c.includeAudio.Checked = true
	c.includeVideoHint = widget.NewLabel("")
	c.includeVideoHint.TextStyle = fyne.TextStyle{Italic: true}
	c.includeAudioHint = widget.NewLabel("")
	c.includeAudioHint.TextStyle = fyne.TextStyle{Italic: true}
}

// streamIncludeRow lays out a checkbox with its hint.
func streamIncludeRow(check *widget.Check, hint *widget.Label) fyne.CanvasObject {
	return container.NewHBox(check, hint, layout.NewSpacer())
}

// missingStreams reports which stream types the loaded input lacks. It is
// false/false until an input has been inspected successfully, so a file
// that is still being probed (or failed to probe) is never treated as
// stream-less.
func missingStreams(inputPath string, inspecting bool, media model.MediaInfo) (noVideo, noAudio bool) {
	if inputPath == "" || inspecting {
		return false, false
	}
	if len(media.VideoStreams)+len(media.AudioStreams)+len(media.CoverArtStreams) == 0 {
		return false, false
	}
	return len(media.VideoStreams) == 0, len(media.AudioStreams) == 0
}

// withStreamOverrides applies the job-level overrides to a profile built
// from the widgets: a missing stream is excluded regardless of the saved
// choice. The saved profile itself is never modified.
func (c *Controller) withStreamOverrides(p model.Profile) model.Profile {
	noVideo, noAudio := missingStreams(c.inputPath, c.inspecting, c.media)
	p.ExcludeVideo = p.ExcludeVideo || noVideo
	p.ExcludeAudio = p.ExcludeAudio || noAudio
	return p
}

// updateStreamIncludeControls syncs the checkboxes, hints and the tab
// forms with the current overrides. The callbacks are muted while the
// controls are driven programmatically.
func (c *Controller) updateStreamIncludeControls() {
	if c.includeVideo == nil {
		return
	}
	noVideo, noAudio := missingStreams(c.inputPath, c.inspecting, c.media)
	c.applyingStreamOverride = true
	defer func() { c.applyingStreamOverride = false }()
	syncInclude(c.includeVideo, c.includeVideoHint, c.videoFormBox, c.userIncludeVideo, noVideo,
		"Input has no video stream", "Video is not written to the output")
	// The audio form box is rebuilt below instead of hidden: it also holds
	// the subtitle selector, which stays usable without audio.
	syncInclude(c.includeAudio, c.includeAudioHint, nil, c.userIncludeAudio, noAudio,
		"Input has no audio stream", "Audio is not written to the output")
	if c.audioMode != nil && c.audioFormBox != nil {
		c.rebuildAudioForm()
	}
}

func syncInclude(check *widget.Check, hint *widget.Label, form *fyne.Container, user, missing bool, missingText, excludedText string) {
	include := user && !missing
	check.SetChecked(include)
	if missing {
		check.Disable()
		hint.SetText(missingText)
	} else {
		check.Enable()
		if user {
			hint.SetText("")
		} else {
			hint.SetText(excludedText)
		}
	}
	if form == nil {
		return
	}
	if include {
		form.Show()
	} else {
		form.Hide()
	}
}

// trimValidationInputs returns the encoder name and audio settings the
// trim validators should see: an excluded stream contributes nothing, so
// its copy/keyframe/priming warnings must not fire.
func trimValidationInputs(p model.Profile) (string, model.AudioSettings) {
	videoEncoder, audio := p.Video.Encoder, p.Audio
	if p.ExcludeVideo {
		videoEncoder = ""
	}
	if p.ExcludeAudio {
		audio = model.AudioSettings{}
	}
	return videoEncoder, audio
}

// templateFields derives the output-template inputs from a profile:
// the extension follows the container (mka/m4a for audio-only), {codec}
// names the encoder that is actually used, and an excluded stream has no
// settings token.
func templateFields(p model.Profile) (ext, codec, videoSettings, audioSettings string) {
	codec = p.Video.Encoder
	if p.ExcludeVideo && !p.ExcludeAudio {
		codec = p.Audio.Encoder
	}
	videoSettings, audioSettings = encoder.SettingsSummary(p.Video.Encoder, p.Video, p.Audio)
	if p.ExcludeVideo {
		videoSettings = ""
	}
	if p.ExcludeAudio {
		audioSettings = ""
	}
	return p.OutputExtension(), codec, videoSettings, audioSettings
}
