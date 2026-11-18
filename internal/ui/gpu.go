package ui

import (
	"strconv"
	"strings"
)

// The GPU index control is a stack of a dropdown and a numeric entry.
// The dropdown lists the detected NVIDIA GPU indices (0..N-1) when
// detection reported at least one; the entry is the fallback for a
// failed or device-less probe and for loaded profiles whose saved
// index falls outside the detected range — the reported count is
// approximate (CUDA_VISIBLE_DEVICES can hide or reorder GPUs), so an
// out-of-range saved index is kept verbatim instead of being clamped.
//
// Profile loading is authoritative for the control: loading always
// shows the loaded value. The detection-time refresh instead preserves
// whatever the control currently shows — including an edit typed into
// the entry before detection lands. Detection can run repeatedly
// (settings changes re-probe), so the refresh is idempotent.

// applyGPUIndexSeed loads a profile's GPU index into the control,
// replacing whatever was shown before: consecutive loads of gpu 2 and
// gpu 0 must leave the control at 0.
func (c *Controller) applyGPUIndexSeed(v int) {
	c.gpuIndexSeed = v
	c.refreshGPUIndexControl(v)
}

// updateGPUIndexOptions refreshes the control after detection from its
// currently shown value (a dropdown selection or an entry edit,
// whichever widget is active).
func (c *Controller) updateGPUIndexOptions() {
	c.refreshGPUIndexControl(c.gpuIndexValue())
}

// refreshGPUIndexControl rebuilds the stack around value: dropdown
// mode when at least one GPU was detected and value fits the reported
// range, entry mode otherwise.
func (c *Controller) refreshGPUIndexControl(value int) {
	count := c.caps.NVENCGPUCount
	if count >= 1 && value >= 0 && value < count {
		options := make([]string, 0, count)
		for i := 0; i < count; i++ {
			options = append(options, strconv.Itoa(i))
		}
		c.gpuIndexSelect.Options = options
		c.gpuIndexSelect.Refresh()
		if selected := strconv.Itoa(value); c.gpuIndexSelect.Selected != selected {
			// Guarded: Fyne drops a SetSelected whose value is not in
			// Options, and OnChanged should only fire for a real change.
			c.gpuIndexSelect.SetSelected(selected)
		}
		c.gpuIndexEntry.Hide()
		c.gpuIndexSelect.Show()
		return
	}
	// Entry mode: show the value verbatim (including an out-of-range
	// loaded index and a still-undetected count of zero; intText renders
	// 0 as empty text and gpuIndexValue falls back to the seed then).
	c.gpuIndexSelect.Options = []string{}
	c.gpuIndexSelect.Selected = ""
	c.gpuIndexSelect.Refresh()
	c.gpuIndexSelect.Hide()
	if text := intText(value); c.gpuIndexEntry.Text != text {
		c.gpuIndexEntry.SetText(text)
	}
	c.gpuIndexEntry.Show()
}

// gpuIndexValue reads the control's current value: the dropdown
// selection in dropdown mode, the entry text in entry mode with the
// loaded seed as the empty-text fallback.
func (c *Controller) gpuIndexValue() int {
	if c.gpuIndexSelect.Visible() {
		return atoi(c.gpuIndexSelect.Selected)
	}
	if strings.TrimSpace(c.gpuIndexEntry.Text) == "" {
		return c.gpuIndexSeed
	}
	return atoi(c.gpuIndexEntry.Text)
}
