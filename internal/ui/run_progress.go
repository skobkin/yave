package ui

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/systray"

	"git.skobk.in/skobkin/yave/internal/model"
)

// runProgressText renders the status line of a running export: percent
// and ETA when the total duration is known (else the encoded position),
// elapsed time, speed, bitrate and the current stage of a staged export.
func runProgressText(p model.Progress, elapsed time.Duration, stage, stages int, durationKnown bool) string {
	var parts []string
	if durationKnown {
		parts = append(parts, fmt.Sprintf("%d%%", percentValue(p.Percentage)))
		if eta, ok := estimateRemaining(p.Percentage, elapsed); ok {
			parts = append(parts, "ETA "+formatClock(eta))
		}
	} else {
		parts = append(parts, formatClock(p.OutTime))
	}
	parts = append(parts, formatClock(elapsed)+" elapsed")
	if speed := strings.TrimSpace(p.Speed); speed != "" && speed != "N/A" {
		parts = append(parts, speed)
	}
	if bitrate := strings.TrimSpace(p.Bitrate); bitrate != "" && bitrate != "N/A" {
		parts = append(parts, bitrate)
	}
	if stages > 1 && stage >= 1 {
		parts = append(parts, fmt.Sprintf("stage %d/%d", min(stage, stages), stages))
	}
	return strings.Join(parts, " | ")
}

// progressInfoWidthSample is the widest status line the label must fit.
const progressInfoWidthSample = "100% | ETA 99h59m59s | 99h59m59s elapsed | 999.9x | 99999.9kbits/s | stage 9/9"

// percentValue converts a 0..1 fraction to a whole percent, clamped so an
// out-of-range or NaN value never shows nonsense.
func percentValue(fraction float64) int {
	if fraction != fraction || fraction < 0 { //nolint:gocritic // NaN check
		return 0
	}
	return int(min(fraction, 1) * 100)
}

// estimateRemaining extrapolates the time left from the elapsed time and
// completed fraction. It reports false until enough progress exists for a
// stable estimate (under 1%), and at completion.
func estimateRemaining(fraction float64, elapsed time.Duration) (time.Duration, bool) {
	if fraction != fraction || fraction < 0.01 || fraction >= 1 || elapsed <= 0 {
		return 0, false
	}
	return time.Duration(float64(elapsed) * (1 - fraction) / fraction), true
}

// formatClock renders a duration as 1h02m03s / 2m03s / 5s, rounded to
// whole seconds; negative values clamp to zero.
func formatClock(d time.Duration) string {
	d = max(d, 0).Round(time.Second)
	h, m, s := int(d/time.Hour), int(d/time.Minute)%60, int(d/time.Second)%60
	switch {
	case h > 0:
		return fmt.Sprintf("%dh%02dm%02ds", h, m, s)
	case m > 0:
		return fmt.Sprintf("%dm%02ds", m, s)
	default:
		return fmt.Sprintf("%ds", s)
	}
}

// progressTitle is the window title / tray tooltip while a run is going.
func progressTitle(percent int, inputName, base string) string {
	if inputName == "" {
		return fmt.Sprintf("%d%% - %s", percent, base)
	}
	return fmt.Sprintf("%d%% - %s - %s", percent, inputName, base)
}

// updateWindowTitle composes the window title from the build title, the
// unsaved-profile marker and, during a run, the progress.
func (c *Controller) updateWindowTitle() {
	if c.window == nil {
		return
	}
	title := windowTitle()
	if c.profileDirty() {
		title += " *"
	}
	if c.runShowing {
		title = progressTitle(c.runPercent, filepath.Base(c.inputPath), title)
	}
	c.window.SetTitle(title)
}

// setRunProgressTitle publishes the progress in the window title and the
// tray tooltip; -1 clears it.
func (c *Controller) setRunProgressTitle(percent int) {
	c.runShowing = percent >= 0
	c.runPercent = max(percent, 0)
	c.updateWindowTitle()
	if c.trayApp == nil {
		return
	}
	if percent < 0 {
		systray.SetTooltip(baseTitle)
		return
	}
	systray.SetTooltip(progressTitle(percent, filepath.Base(c.inputPath), baseTitle))
}

// formatRunSummary describes a successful run: elapsed time and either the
// single output (name and size) or the number of files written.
func formatRunSummary(elapsed time.Duration, outputs []string, sizes []int64) string {
	switch len(outputs) {
	case 0:
		return "Completed in " + formatClock(elapsed)
	case 1:
		text := "Completed in " + formatClock(elapsed) + " → " + filepath.Base(outputs[0])
		if len(sizes) == 1 && sizes[0] > 0 {
			text += " (" + formatByteSize(sizes[0]) + ")"
		}
		return text
	default:
		var total int64
		for _, s := range sizes {
			total += s
		}
		text := fmt.Sprintf("Completed in %s → %d files", formatClock(elapsed), len(outputs))
		if total > 0 {
			text += " (" + formatByteSize(total) + ")"
		}
		return text
	}
}

// formatByteSize renders a size with a binary-prefixed unit (KB = 1024).
func formatByteSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	value, exp := float64(n), 0
	for value >= unit && exp < 4 {
		value /= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", value, "KMGT"[exp-1])
}

// outputSizes stats each output, reporting 0 for unreadable ones.
func outputSizes(outputs []string) []int64 {
	sizes := make([]int64, len(outputs))
	for i, path := range outputs {
		if info, err := os.Stat(path); err == nil {
			sizes[i] = info.Size()
		}
	}
	return sizes
}

// showRunActions reveals the Open folder / Play buttons for the outputs of
// a successful run, and a per-file list when there are several.
func (c *Controller) showRunActions(outputs []string) {
	c.lastOutputs = outputs
	if len(outputs) == 0 {
		c.hideRunActions()
		return
	}
	if c.openFolder == nil {
		return
	}
	c.openFolder.Show()
	c.playOutput.Show()
	if len(outputs) > 1 {
		names := make([]string, len(outputs))
		for i, path := range outputs {
			names[i] = filepath.Base(path)
		}
		c.runResult.SetText(strings.Join(names, "\n"))
		c.runResult.Show()
	} else {
		c.runResult.Hide()
	}
}

// hideRunActions hides the post-run buttons and file list.
func (c *Controller) hideRunActions() {
	c.lastOutputs = nil
	if c.openFolder == nil {
		return
	}
	c.openFolder.Hide()
	c.playOutput.Hide()
	c.runResult.Hide()
}

func openFile(path string) error {
	return fyne.CurrentApp().OpenURL(&url.URL{Scheme: "file", Path: filepath.ToSlash(path)})
}

func (c *Controller) openOutputFolder() {
	if len(c.lastOutputs) > 0 {
		if err := openFile(filepath.Dir(c.lastOutputs[0])); err != nil {
			c.status.SetText("Cannot open folder: " + err.Error())
		}
	}
}

func (c *Controller) playOutputFile() {
	if len(c.lastOutputs) > 0 {
		if err := openFile(c.lastOutputs[0]); err != nil {
			c.status.SetText("Cannot play output: " + err.Error())
		}
	}
}
