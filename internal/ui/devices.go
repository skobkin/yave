package ui

import (
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"

	"git.skobk.in/skobkin/yave/internal/encoder"
	"git.skobk.in/skobkin/yave/internal/ffmpeg"
	"git.skobk.in/skobkin/yave/internal/model"
	"git.skobk.in/skobkin/yave/internal/vaapi"
	"git.skobk.in/skobkin/yave/internal/validation"
)

// resolveAutoVAAPI resolves what the "(Auto)" device option means for
// the given encoder and -profile:v value against the current
// capabilities. ok is false for encoders with no VAAPI device
// requirement, so callers skip the auto logic entirely.
//
// When no probed render nodes exist but a complete DXGI adapter
// inventory does (Windows), auto resolves to the first adapter
// ordinal — ffmpeg's VAAPI win32 display takes atoi(device) against
// EnumAdapters order. There is no libva probe on Windows, so the
// capability-aware selection cannot run; the pick is best-effort, and
// validation fails open the same way for an empty device there.
func (c *Controller) resolveAutoVAAPI(encoderName, profile string) (choice vaapi.DeviceChoice, ok bool) {
	required, ok := encoder.RequiredVAProfiles(encoderName, profile, c.media)
	if !ok || len(required) == 0 {
		return vaapi.DeviceChoice{}, false
	}
	choice = vaapi.SelectDevice(c.caps.RenderNodes, c.caps.VAAPIDevices, required)
	if choice.Status == vaapi.DeviceNoNodes && len(c.caps.GPUAdapters) > 0 {
		choice = vaapi.DeviceChoice{
			Status: vaapi.DeviceSelected,
			Path:   strconv.Itoa(c.caps.GPUAdapters[0].Index),
		}
	}
	return choice, true
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
// value stored in the profile: a render-node path on Linux, a DXGI
// adapter ordinal on Windows. "(Auto)" maps to ""; the current option
// label, a bare node path, a bare basename, or a stale label carrying
// the node's basename all resolve to the node, and adapter labels
// resolve to their ordinals; anything else passes through unchanged,
// keeping the historical fail-open behavior for values outside the
// detected set.
func vaapiDeviceValue(caps model.Capabilities, option string) string {
	if option == vaapiAutoOption {
		return ""
	}
	for _, a := range caps.GPUAdapters {
		if option == adapterOptionLabel(a) {
			return strconv.Itoa(a.Index)
		}
	}
	if n, ok := parseAdapterOrdinalLabel(option); ok {
		return strconv.Itoa(n)
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

// adapterOptionLabel renders the device-dropdown label for one
// enumerated GPU adapter: "AMD Radeon (adapter 1)". The parenthesized
// ordinal is the profile value; the name alone means nothing to the
// person choosing.
func adapterOptionLabel(a model.GPUAdapter) string {
	return fmt.Sprintf("%s (adapter %d)", a.Name, a.Index)
}

// adapterByIndex returns the enumerated adapter with the given
// ordinal, ok false when none matches.
func adapterByIndex(caps model.Capabilities, index int) (model.GPUAdapter, bool) {
	for _, a := range caps.GPUAdapters {
		if a.Index == index {
			return a, true
		}
	}
	return model.GPUAdapter{}, false
}

// parseAdapterOrdinalLabel recovers the ordinal from an adapter
// option label of the form "<name> (adapter N)" — a stale label from
// an older capabilities snapshot still resolves to its value.
func parseAdapterOrdinalLabel(option string) (int, bool) {
	const sep = " (adapter "
	i := strings.LastIndex(option, sep)
	if i < 0 || !strings.HasSuffix(option, ")") {
		return 0, false
	}
	n, err := strconv.Atoi(strings.TrimSuffix(option[i+len(sep):], ")"))
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}

// qsvDeviceValue maps a QSV device dropdown option to the profile
// value: "(default)" stands for the empty path, which tells ffmpeg to
// pick its default device; adapter labels map to their ordinals. A
// bare ordinal is already the profile value and passes through.
func qsvDeviceValue(caps model.Capabilities, option string) string {
	if option == defaultOption {
		return ""
	}
	for _, a := range caps.GPUAdapters {
		if option == adapterOptionLabel(a) {
			return strconv.Itoa(a.Index)
		}
	}
	if n, ok := parseAdapterOrdinalLabel(option); ok {
		return strconv.Itoa(n)
	}
	return option
}

// qsvDeviceFromWidgets resolves the QSV device for the profile model.
// An empty dropdown selection (no options detected yet, or never
// selected) falls back to the loaded profile's saved path so a Save
// cannot wipe it; any visible selection — "(default)" included — is
// authoritative and maps through qsvDeviceValue.
func qsvDeviceFromWidgets(c *Controller) string {
	if c.qsvDevice.Selected == "" {
		return c.qsvDeviceSeed
	}
	return qsvDeviceValue(c.caps, c.qsvDevice.Selected)
}

// updateDeviceOptions rebuilds the VAAPI and QSV device dropdowns from
// the detected render nodes. It never changes a selection that is
// still valid — reconcileDevices owns programmatic selection so
// manual picks survive re-detections.
func (c *Controller) updateDeviceOptions() {
	nodes := c.caps.RenderNodes
	adapters := c.caps.GPUAdapters

	// A complete adapter inventory (Windows) replaces the node list in
	// both dropdowns: VAAPI win32 and the QSV d3d11va child both take
	// the adapter ordinal as their device string.
	vaapiOptions := make([]string, 0, len(adapters)+len(nodes)+1)
	vaapiOptions = append(vaapiOptions, vaapiAutoOption)
	if len(adapters) > 0 {
		for _, a := range adapters {
			vaapiOptions = append(vaapiOptions, adapterOptionLabel(a))
		}
	} else {
		for _, node := range nodes {
			vaapiOptions = append(vaapiOptions, vaapiDeviceOption(c.caps, node))
		}
	}
	c.vaapiDevice.Options = vaapiOptions

	qsvOptions := make([]string, 0, len(adapters)+len(nodes)+1)
	qsvOptions = append(qsvOptions, defaultOption)
	if len(adapters) > 0 {
		for _, a := range adapters {
			qsvOptions = append(qsvOptions, adapterOptionLabel(a))
		}
	} else {
		qsvOptions = append(qsvOptions, nodes...)
	}
	c.qsvDevice.Options = qsvOptions

	// Re-pin a visible selection whose option label changed with the
	// capabilities: a bare value picked before detection, or a label
	// from an older snapshot. Guarded so SetSelected fires at most one
	// OnChanged per capabilities change, and idempotent — a selection
	// already on its current label is left alone, which is what
	// reconcileDevices' guard then sees. updateControlState never
	// rebuilds device options, so no re-entry follows.
	if sel := c.vaapiDevice.Selected; sel != "" && sel != vaapiAutoOption {
		value := vaapiDeviceValue(c.caps, sel)
		if slices.Contains(nodes, value) {
			if label := vaapiDeviceOption(c.caps, value); label != sel {
				c.vaapiDevice.SetSelected(label)
			}
		}
		if index, err := strconv.Atoi(value); err == nil && index >= 0 {
			if a, ok := adapterByIndex(c.caps, index); ok {
				if label := adapterOptionLabel(a); label != sel {
					c.vaapiDevice.SetSelected(label)
				}
			}
		}
	}
	if sel := c.qsvDevice.Selected; sel != "" && sel != defaultOption {
		value := qsvDeviceValue(c.caps, sel)
		if index, err := strconv.Atoi(value); err == nil && index >= 0 {
			if a, ok := adapterByIndex(c.caps, index); ok {
				if label := adapterOptionLabel(a); label != sel {
					c.qsvDevice.SetSelected(label)
				}
			}
		}
	}
	c.vaapiDevice.Refresh()
	c.qsvDevice.Refresh()
}

// vaapiInventory maps each authoritative VAAPI device value — a
// render-node path on Linux, a DXGI adapter ordinal on Windows — to
// its dropdown label. A complete adapter enumeration takes precedence
// over nodes; a nil map means neither inventory exists, so the kind is
// not reconciled at all.
func (c *Controller) vaapiInventory() map[string]string {
	if len(c.caps.GPUAdapters) > 0 {
		values := make(map[string]string, len(c.caps.GPUAdapters))
		for _, a := range c.caps.GPUAdapters {
			values[strconv.Itoa(a.Index)] = adapterOptionLabel(a)
		}
		return values
	}
	if len(c.caps.RenderNodes) == 0 {
		return nil
	}
	values := make(map[string]string, len(c.caps.RenderNodes))
	for _, node := range c.caps.RenderNodes {
		values[node] = vaapiDeviceOption(c.caps, node)
	}
	return values
}

// qsvInventory maps each authoritative QSV device value to its
// dropdown label: adapter ordinals (Windows) or bare render-node paths
// (Linux). nil when neither inventory exists.
func (c *Controller) qsvInventory() map[string]string {
	if len(c.caps.GPUAdapters) > 0 {
		values := make(map[string]string, len(c.caps.GPUAdapters))
		for _, a := range c.caps.GPUAdapters {
			values[strconv.Itoa(a.Index)] = adapterOptionLabel(a)
		}
		return values
	}
	if len(c.caps.RenderNodes) == 0 {
		return nil
	}
	values := make(map[string]string, len(c.caps.RenderNodes))
	for _, node := range c.caps.RenderNodes {
		values[node] = node
	}
	return values
}

// reconcileDevices aligns the device dropdowns with the authoritative
// inventories and the loaded profile's seeds. atLoad marks the
// loadProfile-deferred call, where the freshly loaded profile is
// authoritative: its device (or the replacement for a seed no longer
// in the inventory) is selected even when a previous profile left a
// valid selection behind — keeping that selection would preview,
// encode, and Save the wrong device into the newly loaded profile.
// The detect-time call (atLoad=false) never overrides an existing
// selection so manual device picks survive re-detections; it only
// fills an empty selection (a load that ran before devices were known)
// and applies replacements.
//
// Each kind reconciles only against its own authoritative inventory —
// VAAPI against the probed render nodes (Linux) or the complete DXGI
// adapter enumeration (Windows), QSV likewise — so discovering
// Windows adapters never rewrites a VAAPI seed, and vice versa. A
// populated GPUAdapters slice is by construction a complete
// enumeration (see model.Capabilities.GPUAdapters), so a replacement
// decided against it is trustworthy.
//
// An empty VAAPI seed means automatic selection and is a valid target
// (the "(Auto)" option), like an empty QSV seed meaning ffmpeg's
// default. A non-empty seed that no longer exists in the inventory is
// replaced (automatic selection for VAAPI — the picker can then choose
// a compatible device once capabilities are known; the default device
// for QSV) and reported once via popup dialog; the seed is updated to
// the replacement so later detections stay quiet.
//
// With no inventory at all (detection failure, platform without a
// probe, pre-detect window) reconciliation is a no-op: the dropdowns
// list only "(Auto)" / "(default)" and profileFromWidgets falls back
// to the seeds, so nothing is lost.
func (c *Controller) reconcileDevices(atLoad bool) {
	var notes []string

	// Only SetSelected when the value actually changes; Select fires
	// OnChanged on every assignment, which would re-enter
	// updateControlState via changed().
	selectTarget := func(sel *widget.Select, target string) {
		if sel.Selected != target {
			sel.SetSelected(target)
		}
	}

	// Resolve one kind's dropdown target from its seed: the seed's
	// inventory label while it is still detected, the auto/default
	// label otherwise. Only a non-empty vanished seed counts as
	// replaced (reported + reseeded to ""); an empty seed keeps its
	// meaning. Seeds stay bare values; only the dropdown target is
	// rendered as a label.
	reconcile := func(sel *widget.Select, seed *string, inventory map[string]string, autoLabel string) (replaced bool, vanished string) {
		if inventory == nil {
			return false, ""
		}
		target := autoLabel
		if label, ok := inventory[*seed]; ok {
			target = label
		} else if *seed != "" {
			replaced, vanished = true, *seed
			target = autoLabel
			*seed = ""
		}
		if atLoad {
			selectTarget(sel, target)
		} else if replaced || sel.Selected == "" {
			selectTarget(sel, target)
		}
		return replaced, vanished
	}

	if replaced, vanished := reconcile(c.vaapiDevice, &c.vaapiDeviceSeed, c.vaapiInventory(), vaapiAutoOption); replaced {
		notes = append(notes, fmt.Sprintf("The VAAPI device %s was not detected; using automatic selection instead.", vanished))
	}
	if replaced, vanished := reconcile(c.qsvDevice, &c.qsvDeviceSeed, c.qsvInventory(), defaultOption); replaced {
		notes = append(notes, fmt.Sprintf("The QSV device %s was not detected; using the default device instead.", vanished))
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
		value := vaapiDeviceValue(c.caps, c.vaapiDevice.Selected)
		// Adapter pick (Windows): described straight from the
		// enumeration — there is no libva probe record to consult.
		if index, err := strconv.Atoi(value); err == nil {
			if a, ok := adapterByIndex(c.caps, index); ok {
				c.vaapiDeviceHint.SetText(adapterOptionLabel(a))
				return
			}
		}
		c.vaapiDeviceHint.SetText(c.describeVAAPIDevice(value, ve, profile))
		return
	}
	choice, ok := c.resolveAutoVAAPI(ve.Name(), profile)
	if !ok {
		c.vaapiDeviceHint.SetText("")
		return
	}
	switch choice.Status {
	case vaapi.DeviceSelected:
		// Windows auto resolves to an adapter ordinal; name the GPU
		// behind it instead of showing the bare number.
		if index, err := strconv.Atoi(choice.Path); err == nil {
			if a, ok2 := adapterByIndex(c.caps, index); ok2 {
				c.vaapiDeviceHint.SetText(fmt.Sprintf("Auto: %s (adapter %d)", a.Name, a.Index))
				return
			}
		}
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
	if required, ok := encoder.RequiredVAProfiles(ve.Name(), profile, c.media); ok && len(required) > 0 {
		if d.FailsRequirement(required) {
			text = warningPrefix + fmt.Sprintf("%s — does not encode %s profile %s", text, ve.Name(), validation.DisplayProfile(profile))
		} else {
			text += " — " + vaapi.DeviceCapability(d, required)
		}
	}
	return text
}

// updateQSVDeviceHint explains the QSV device selection: which GPU
// sits behind a manual pick, and — because QSV only runs on Intel
// graphics — a warning when the picked device is definitively not an
// Intel GPU. The check is deliberately display-only and conservative:
// a PCI vendor ID is definitive, the VA driver string is a heuristic,
// and anything else stays quiet. ffmpeg's "(default)" device stays
// undescribed, since there is nothing reliable to say about a device
// ffmpeg has not picked yet.
//
// On Windows the pick is a DXGI adapter ordinal described straight
// from the enumeration; on Linux it is a probed render node whose
// identity travels inside the probe record — without one there is
// nothing reliable to say.
func (c *Controller) updateQSVDeviceHint() {
	ve := c.currentVideoEncoder()
	if ve == nil || ve.HWBackend() != encoder.HWQSV || c.qsvDevice.Selected == "" || c.qsvDevice.Selected == defaultOption {
		c.qsvDeviceHint.SetText("")
		return
	}
	value := qsvDeviceValue(c.caps, c.qsvDevice.Selected)

	if index, err := strconv.Atoi(value); err == nil {
		if a, ok := adapterByIndex(c.caps, index); ok {
			text := fmt.Sprintf("Adapter %d — %s", a.Index, a.Name)
			if a.VendorID != 0 && a.VendorID != ffmpeg.VendorIntel {
				text = warningPrefix + text + " — not an Intel GPU (QSV requires Intel graphics)"
			}
			c.qsvDeviceHint.SetText(text)
			return
		}
	}

	d, known := deviceByPath(c.caps, value)
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
