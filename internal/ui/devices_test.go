package ui

import (
	"slices"
	"strings"
	"testing"

	fynetest "fyne.io/fyne/v2/test"

	"git.skobk.in/skobkin/yave/internal/ffmpeg"
	"git.skobk.in/skobkin/yave/internal/model"
	"git.skobk.in/skobkin/yave/internal/vaapi"
	"git.skobk.in/skobkin/yave/internal/validation"
)

// adapterFixture is the two-GPU Windows shape: NVIDIA first, AMD
// second — the vendor split the QSV hint warning distinguishes.
func adapterFixture() []model.GPUAdapter {
	return []model.GPUAdapter{
		{Index: 0, Name: "NVIDIA GeForce GTX 1660 Ti", VendorID: ffmpeg.VendorNVIDIA},
		{Index: 1, Name: "AMD Radeon Graphics", VendorID: 0x1002},
	}
}

// TestDeviceOptionsFromAdapterInventory pins the Windows dropdown
// shape: a complete adapter enumeration replaces the node list in both
// device dropdowns, every option maps back to its bare ordinal, and a
// stale label recorded under an older GPU name still resolves to its
// value.
func TestDeviceOptionsFromAdapterInventory(t *testing.T) {
	app := fynetest.NewApp()
	defer app.Quit()

	c := testController()
	c.createWidgets()
	c.caps = model.Capabilities{GPUAdapters: adapterFixture()}
	c.updateDeviceOptions()

	vaapiWant := []string{vaapiAutoOption, "NVIDIA GeForce GTX 1660 Ti (adapter 0)", "AMD Radeon Graphics (adapter 1)"}
	if !slices.Equal(c.vaapiDevice.Options, vaapiWant) {
		t.Errorf("vaapiDevice options = %v, want %v", c.vaapiDevice.Options, vaapiWant)
	}
	qsvWant := []string{defaultOption, "NVIDIA GeForce GTX 1660 Ti (adapter 0)", "AMD Radeon Graphics (adapter 1)"}
	if !slices.Equal(c.qsvDevice.Options, qsvWant) {
		t.Errorf("qsvDevice options = %v, want %v", c.qsvDevice.Options, qsvWant)
	}

	if got := vaapiDeviceValue(c.caps, "AMD Radeon Graphics (adapter 1)"); got != "1" {
		t.Errorf("vaapiDeviceValue(adapter 1 label) = %q, want \"1\"", got)
	}
	if got := qsvDeviceValue(c.caps, "AMD Radeon Graphics (adapter 1)"); got != "1" {
		t.Errorf("qsvDeviceValue(adapter 1 label) = %q, want \"1\"", got)
	}
	// A label recorded when the GPU had a different name (driver
	// update, rename between detections) still resolves by ordinal.
	if got := vaapiDeviceValue(c.caps, "Older GPU Name (adapter 1)"); got != "1" {
		t.Errorf("vaapiDeviceValue(stale label) = %q, want \"1\"", got)
	}
	// "(Auto)" and "(default)" keep their empty-value meanings.
	if got := vaapiDeviceValue(c.caps, vaapiAutoOption); got != "" {
		t.Errorf("vaapiDeviceValue(Auto) = %q, want empty", got)
	}
	if got := qsvDeviceValue(c.caps, defaultOption); got != "" {
		t.Errorf("qsvDeviceValue(default) = %q, want empty", got)
	}
}

// TestReconcileDevicesAdaptersPerKind pins the load-time reconciliation
// against a complete adapter inventory: each seed resolves against its
// own kind's inventory independently, so a vanished QSV seed is
// replaced and reseeded without disturbing the VAAPI seed, and valid
// seeds select their adapter labels.
func TestReconcileDevicesAdaptersPerKind(t *testing.T) {
	app := fynetest.NewApp()
	defer app.Quit()

	c := testController()
	c.createWidgets()
	c.caps = model.Capabilities{GPUAdapters: adapterFixture()}
	c.updateDeviceOptions()
	c.vaapiDeviceSeed = "1"
	c.qsvDeviceSeed = "9" // outside the enumerated range: vanished

	c.reconcileDevices(true)

	if got := c.vaapiDevice.Selected; got != "AMD Radeon Graphics (adapter 1)" {
		t.Errorf("vaapiDevice selection = %q, want the adapter 1 label", got)
	}
	if c.vaapiDeviceSeed != "1" {
		t.Errorf("vaapiDeviceSeed = %q, want untouched", c.vaapiDeviceSeed)
	}
	if got := c.qsvDevice.Selected; got != defaultOption {
		t.Errorf("qsvDevice selection = %q, want the (default) replacement", got)
	}
	if c.qsvDeviceSeed != "" {
		t.Errorf("qsvDeviceSeed = %q, want reseeded to the empty default value", c.qsvDeviceSeed)
	}
	if got := c.profileFromWidgets().Video.VAAPIDevice; got != "1" {
		t.Errorf("profile VAAPI device = %q, want the adapter ordinal", got)
	}
}

// TestReconcileDevicesAdaptersPendingSeed covers the Windows startup
// ordering: the profile loads before detection (empty selection), and
// when the adapter inventory arrives the detect-time reconcile applies
// the pending seed — not the first adapter.
func TestReconcileDevicesAdaptersPendingSeed(t *testing.T) {
	app := fynetest.NewApp()
	defer app.Quit()

	c := testController()
	c.createWidgets()
	c.loadProfile(model.Profile{
		Name: "test", Container: model.ContainerMKV, OutputTemplate: "{dir}/{name}.{ext}",
		Video: model.VideoSettings{Encoder: "hevc_vaapi", VAAPIDevice: "1"},
	})

	c.caps = model.Capabilities{GPUAdapters: adapterFixture()}
	c.updateDeviceOptions()
	c.reconcileDevices(false)
	if got := c.vaapiDevice.Selected; got != "AMD Radeon Graphics (adapter 1)" {
		t.Errorf("pending seed selection = %q, want the adapter 1 label", got)
	}
}

// TestReconcileDevicesNoInventoryIsNoOp pins the incomplete-probe
// contract: without any authoritative inventory (GPUAdapters nil after
// an incomplete DXGI probe, no render nodes) reconciliation must not
// rewrite seeds or selections — a partial enumeration must never be
// treated as exhaustive.
func TestReconcileDevicesNoInventoryIsNoOp(t *testing.T) {
	app := fynetest.NewApp()
	defer app.Quit()

	c := testController()
	c.createWidgets()
	c.caps = model.Capabilities{} // probe failed or incomplete
	c.updateDeviceOptions()
	c.vaapiDeviceSeed = "9"
	c.qsvDeviceSeed = "7"

	c.reconcileDevices(true)

	if c.vaapiDeviceSeed != "9" || c.qsvDeviceSeed != "7" {
		t.Errorf("seeds = %q/%q, want both preserved with no inventory", c.vaapiDeviceSeed, c.qsvDeviceSeed)
	}
	if c.vaapiDevice.Selected != "" || c.qsvDevice.Selected != "" {
		t.Errorf("selections = %q/%q, want empty so the seeds survive a Save",
			c.vaapiDevice.Selected, c.qsvDevice.Selected)
	}
	if got := c.profileFromWidgets().Video.VAAPIDevice; got != "9" {
		t.Errorf("profile VAAPI device = %q, want the preserved seed", got)
	}
}

// TestQSVDeviceHintAdapterWarning pins the Windows QSV hint: the
// adapter pick is named from the enumeration, and a definitively
// non-Intel vendor ID gains the warning — an NVIDIA adapter cannot
// serve QSV, an Intel one stays quiet.
func TestQSVDeviceHintAdapterWarning(t *testing.T) {
	app := fynetest.NewApp()
	defer app.Quit()

	c := testController()
	c.createWidgets()
	c.caps = model.Capabilities{GPUAdapters: adapterFixture()}
	c.updateDeviceOptions()
	c.videoEncoder.SetSelected("hevc_qsv")

	c.qsvDevice.SetSelected("NVIDIA GeForce GTX 1660 Ti (adapter 0)")
	c.updateQSVDeviceHint()
	want := warningPrefix + "Adapter 0 — NVIDIA GeForce GTX 1660 Ti — not an Intel GPU (QSV requires Intel graphics)"
	if c.qsvDeviceHint.Text != want {
		t.Errorf("qsv hint = %q, want %q", c.qsvDeviceHint.Text, want)
	}

	c.qsvDevice.SetSelected("AMD Radeon Graphics (adapter 1)")
	c.updateQSVDeviceHint()
	want = warningPrefix + "Adapter 1 — AMD Radeon Graphics — not an Intel GPU (QSV requires Intel graphics)"
	if c.qsvDeviceHint.Text != want {
		t.Errorf("qsv hint = %q, want %q", c.qsvDeviceHint.Text, want)
	}

	intelCaps := model.Capabilities{GPUAdapters: []model.GPUAdapter{
		{Index: 0, Name: "Intel UHD Graphics", VendorID: ffmpeg.VendorIntel},
	}}
	c.caps = intelCaps
	c.updateDeviceOptions()
	c.qsvDevice.SetSelected("Intel UHD Graphics (adapter 0)")
	c.updateQSVDeviceHint()
	if want := "Adapter 0 — Intel UHD Graphics"; c.qsvDeviceHint.Text != want {
		t.Errorf("qsv hint = %q, want %q without the warning", c.qsvDeviceHint.Text, want)
	}
	if strings.Contains(c.qsvDeviceHint.Text, warningPrefix) {
		t.Errorf("qsv hint %q must not warn about an Intel GPU", c.qsvDeviceHint.Text)
	}
}

// TestWindowsAutoVAAPIResolvesAndBuilds pins the whole Windows Auto
// route: with adapters and no render nodes, "(Auto)" materializes to
// the first adapter ordinal in the command profile, the built command
// carries -vaapi_device 0, and the hint names the adapter.
func TestWindowsAutoVAAPIResolvesAndBuilds(t *testing.T) {
	app := fynetest.NewApp()
	defer app.Quit()

	c := testController()
	c.createWidgets()
	c.caps = model.Capabilities{GPUAdapters: adapterFixture()} // Windows shape: no RenderNodes
	c.updateDeviceOptions()
	c.videoEncoder.SetSelected("hevc_vaapi")
	c.reconcileDevices(true) // empty seed → "(Auto)"

	if got := c.vaapiDevice.Selected; got != vaapiAutoOption {
		t.Fatalf("selection = %q, want %q", got, vaapiAutoOption)
	}
	cp := c.commandProfile()
	if got := cp.Video.VAAPIDevice; got != "0" {
		t.Fatalf("command profile VAAPI device = %q, want the first adapter ordinal \"0\"", got)
	}

	plan, err := ffmpeg.BuildCommand(ffmpeg.BuildRequest{
		FFmpegPath: "ffmpeg", InputPath: "/tmp/in.mkv", OutputPath: "/tmp/out.mkv", Profile: cp,
	})
	if err != nil {
		t.Fatalf("BuildCommand failed: %v", err)
	}
	found := false
	for i, arg := range plan.Args {
		if arg == "-vaapi_device" && i+1 < len(plan.Args) && plan.Args[i+1] == "0" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("plan args %v miss -vaapi_device 0", plan.Args)
	}

	c.updateVAAPIDeviceHint()
	if want := "Auto: NVIDIA GeForce GTX 1660 Ti (adapter 0)"; c.vaapiDeviceHint.Text != want {
		t.Errorf("vaapi hint = %q, want %q", c.vaapiDeviceHint.Text, want)
	}
}

// TestWindowsAutoVAAPIValidationPasses pins the raw-profile Auto form:
// a persisted profile whose VAAPIDevice is the empty string (the Auto
// intent) must validate against an adapter-only capability set instead
// of falling into the no-device error. Without adapters the error
// stays, pinning the fail-open's scope.
func TestWindowsAutoVAAPIValidationPasses(t *testing.T) {
	profile := model.Profile{
		Name: "test", Container: model.ContainerMKV, OutputTemplate: "{dir}/{name}.{ext}",
		Video: model.VideoSettings{
			Encoder: "hevc_vaapi", RateControl: "CQP", QP: 26,
			FrameRateMode: "keep", KeepAspectRatio: true, PreventUpscaling: true,
			VAAPIDevice: "", // the raw Auto form
		},
		Audio:     model.AudioSettings{Mode: model.AudioCopy, Encoder: "copy"},
		Subtitles: model.SubtitleSettings{Mode: model.SubtitleCopy},
	}
	caps := model.Capabilities{
		GPUAdapters: adapterFixture(),
		Encoders:    map[string]model.Encoder{"hevc_vaapi": {Name: "hevc_vaapi"}},
	}

	result := validation.ValidateProfile(profile, caps, nil)
	for _, issue := range result.Errors {
		if issue.Field == "profile.video.vaapi_device" {
			t.Errorf("adapter-only Auto profile errored on vaapi_device: %s", issue.Message)
		}
	}

	// The same profile with no adapter inventory keeps the hard error.
	result = validation.ValidateProfile(profile, model.Capabilities{
		Encoders: map[string]model.Encoder{"hevc_vaapi": {Name: "hevc_vaapi"}},
	}, nil)
	found := false
	for _, issue := range result.Errors {
		if issue.Field == "profile.video.vaapi_device" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("Auto profile without any device inventory must error on profile.video.vaapi_device, got %#v", result.Errors)
	}
}

// TestVAAPIDeviceAutoPicksMain10CapableDevice pins the media-aware
// auto selection: with two probed devices and only the second listing
// HEVC Main10 encode, a 10-bit source resolves "(Auto)" to the second
// device (the chain RequiredVAProfiles derives from the source depth
// needs VAProfileHEVCMain10), an 8-bit source still picks the first —
// and an explicit main10 on a Main-only host reports no compatible
// device instead of silently picking a device that cannot serve it.
func TestVAAPIDeviceAutoPicksMain10CapableDevice(t *testing.T) {
	app := fynetest.NewApp()
	defer app.Quit()

	mainOnly := vaapi.DeviceInfo{
		Path:   "/dev/dri/renderD128",
		Driver: "fake",
		EntryPoints: map[vaapi.Profile][]vaapi.Entrypoint{
			vaapi.ProfileHEVCMain: {vaapi.EntrypointEncSlice},
		},
	}
	main10Too := vaapi.DeviceInfo{
		Path:   "/dev/dri/renderD129",
		Driver: "fake",
		EntryPoints: map[vaapi.Profile][]vaapi.Entrypoint{
			vaapi.ProfileHEVCMain:   {vaapi.EntrypointEncSlice},
			vaapi.ProfileHEVCMain10: {vaapi.EntrypointEncSlice},
		},
	}
	tenBit := model.MediaInfo{VideoStreams: []model.VideoStream{{PixelFormat: "yuv420p10le"}}}
	eightBit := model.MediaInfo{VideoStreams: []model.VideoStream{{PixelFormat: "yuv420p"}}}

	c := testController()
	c.createWidgets()
	c.caps = model.Capabilities{
		RenderNodes:  []string{"/dev/dri/renderD128", "/dev/dri/renderD129"},
		VAAPIDevices: []vaapi.DeviceInfo{mainOnly, main10Too},
	}

	c.media = tenBit
	choice, ok := c.resolveAutoVAAPI("hevc_vaapi", "")
	if !ok || choice.Status != vaapi.DeviceSelected || choice.Path != "/dev/dri/renderD129" {
		t.Errorf("auto with a 10-bit source = %+v (ok %v), want renderD129", choice, ok)
	}

	c.media = eightBit
	choice, ok = c.resolveAutoVAAPI("hevc_vaapi", "")
	if !ok || choice.Status != vaapi.DeviceSelected || choice.Path != "/dev/dri/renderD128" {
		t.Errorf("auto with an 8-bit source = %+v (ok %v), want renderD128", choice, ok)
	}

	c.media = model.MediaInfo{}
	choice, ok = c.resolveAutoVAAPI("hevc_vaapi", "")
	if !ok || choice.Status != vaapi.DeviceSelected || choice.Path != "/dev/dri/renderD128" {
		t.Errorf("auto without media = %+v (ok %v), want the 8-bit fallback renderD128", choice, ok)
	}

	// Explicit main10 needs the Main10-capable device; a Main-only
	// host must not be auto-served.
	c.media = eightBit
	choice, ok = c.resolveAutoVAAPI("hevc_vaapi", "main10")
	if !ok || choice.Status != vaapi.DeviceSelected || choice.Path != "/dev/dri/renderD129" {
		t.Errorf("explicit main10 = %+v (ok %v), want renderD129", choice, ok)
	}
	c.caps.VAAPIDevices = []vaapi.DeviceInfo{mainOnly, mainOnly}
	c.caps.RenderNodes = []string{"/dev/dri/renderD128", "/dev/dri/renderD129"}
	choice, ok = c.resolveAutoVAAPI("hevc_vaapi", "main10")
	if !ok || choice.Status != vaapi.DeviceNoneCompatible {
		t.Errorf("explicit main10 on a Main-only host = %+v (ok %v), want DeviceNoneCompatible", choice, ok)
	}
}
