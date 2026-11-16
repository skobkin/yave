package ui

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"

	"git.skobk.in/skobkin/yave/internal/encoder"
	"git.skobk.in/skobkin/yave/internal/model"
	"git.skobk.in/skobkin/yave/internal/vaapi"
	"git.skobk.in/skobkin/yave/internal/validation"
)

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
