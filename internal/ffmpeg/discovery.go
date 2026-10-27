package ffmpeg

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"sort"
	"strings"

	"git.skobk.in/skobkin/yave/internal/encoder"
	"git.skobk.in/skobkin/yave/internal/model"
	"git.skobk.in/skobkin/yave/internal/vaapi"
)

const (
	MediaVideo = "video"
	MediaAudio = "audio"

	KindHardware    = "hardware"
	KindSoftware    = "software"
	KindEncode      = "encode"
	KindPassthrough = "passthrough"
)

// Discover resolves both programs and queries their versions and FFmpeg's
// encoder and hardware-acceleration capabilities, and probes the host for
// the device nodes the hardware encoder families need.
func Discover(ctx context.Context, config model.FFmpegConfig) (model.Capabilities, error) {
	return discover(ctx, config, osFSProbe, osVAAPIDeviceProbe)
}

// vaapiDeviceProber is the seam for the per-device VA-API capability
// probe; tests inject fakes. The contract is fail-open by construction:
// it never returns an error, per-device failures travel inside each
// DeviceInfo as Err.
type vaapiDeviceProber func(ctx context.Context, nodes []string) []vaapi.DeviceInfo

var osVAAPIDeviceProbe vaapiDeviceProber = vaapi.Probe

func discover(ctx context.Context, config model.FFmpegConfig, fs fsProbe, probeDevices vaapiDeviceProber) (model.Capabilities, error) {
	ffmpegPath, err := ResolveExecutable(config.FFmpegPath, "ffmpeg")
	if err != nil {
		return model.Capabilities{}, err
	}
	ffprobePath, err := ResolveExecutable(config.FFprobePath, "ffprobe")
	if err != nil {
		return model.Capabilities{}, err
	}

	ffmpegVersion, err := commandOutput(ctx, ffmpegPath, "-hide_banner", "-version")
	if err != nil {
		return model.Capabilities{}, fmt.Errorf("detect FFmpeg version: %w", err)
	}
	ffprobeVersion, err := commandOutput(ctx, ffprobePath, "-hide_banner", "-version")
	if err != nil {
		return model.Capabilities{}, fmt.Errorf("detect ffprobe version: %w", err)
	}
	encodersOutput, err := commandOutput(ctx, ffmpegPath, "-hide_banner", "-encoders")
	if err != nil {
		return model.Capabilities{}, fmt.Errorf("detect FFmpeg encoders: %w", err)
	}
	hwaccelsOutput, err := commandOutput(ctx, ffmpegPath, "-hide_banner", "-hwaccels")
	if err != nil {
		return model.Capabilities{}, fmt.Errorf("detect FFmpeg hardware accelerators: %w", err)
	}

	hw := probeHWBackends(fs)
	// Per-device VA-API probing runs inside the caller's detection
	// budget; every failure mode degrades to Err-carrying entries, so
	// discovery itself cannot fail on it.
	var vaapiDevices []vaapi.DeviceInfo
	if runtime.GOOS == "linux" && len(hw.renderNodes) > 0 {
		vaapiDevices = probeDevices(ctx, hw.renderNodes)
	}
	encoders := filterUnavailableHWEncoders(ParseEncoders(encodersOutput), hw)
	narrowProfilesToBuild(ctx, ffmpegPath, encoders)
	return model.Capabilities{
		FFmpegPath:     ffmpegPath,
		FFprobePath:    ffprobePath,
		FFmpegVersion:  ParseVersion(ffmpegVersion),
		FFprobeVersion: ParseVersion(ffprobeVersion),
		Encoders:       encoders,
		HWAccels:       ParseHWAccels(hwaccelsOutput),

		HWVAAPIAvailable: hw.vaapi,
		HWQSVAvailable:   hw.qsv,
		HWNVENCAvailable: hw.nvenc,
		NVENCGPUCount:    hw.nvencGPUs,
		RenderNodes:      hw.renderNodes,
		VAAPIDevices:     vaapiDevices,
	}, nil
}

// narrowProfilesToBuild filters each discovered video encoder's
// curated profile list down to the values the local ffmpeg build
// enumerates via `ffmpeg -h encoder=<name>`. Builds that enumerate
// nothing (free-string or absent -profile option) and probes that
// fail keep the full curated list: absence of an enumeration is not
// evidence of non-support. A probe that matches nothing curated also
// keeps the curated list rather than presenting an empty dropdown.
func narrowProfilesToBuild(ctx context.Context, ffmpegPath string, encoders map[string]model.Encoder) {
	for name, m := range encoders {
		if len(m.Profiles) == 0 {
			continue
		}
		enumerated, err := probeEncoderProfiles(ctx, ffmpegPath, name)
		if err != nil || len(enumerated) == 0 {
			continue
		}
		if narrowed := intersectProfiles(m.Profiles, enumerated); len(narrowed) > 0 {
			m.Profiles = narrowed
			encoders[name] = m
		}
	}
}

// ResolveExecutable resolves configured through PATH when it is a bare
// command, while also accepting explicit relative and absolute paths.
func ResolveExecutable(configured, fallback string) (string, error) {
	name := strings.TrimSpace(configured)
	if name == "" {
		name = fallback
	}
	path, err := exec.LookPath(name)
	if err != nil {
		return "", fmt.Errorf("%s executable %q not found: %w", fallback, name, err)
	}
	return path, nil
}

// ParseVersion returns the first non-empty output line. FFmpeg and ffprobe
// print their complete version identification there.
func ParseVersion(output string) string {
	for line := range strings.SplitSeq(output, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			return line
		}
	}
	return ""
}

// ParseEncoders extracts available encoder names and applies YAVE's curated
// whitelist and native/external encoder preferences.
//
// The curated whitelist is the encoder.DefaultRegistry() — every
// encoder name returned by the registry is matched against the
// ffmpeg -encoders output. This keeps the discovery code in sync
// with the Encoder interface: adding a new curated encoder only
// requires registering it in defaults.go.
func ParseEncoders(output string) map[string]model.Encoder {
	available := make(map[string]struct{})
	for line := range strings.SplitSeq(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || len(fields[0]) < 6 {
			continue
		}
		mediaFlag := fields[0][0]
		if mediaFlag != 'V' && mediaFlag != 'A' {
			continue
		}
		available[fields[1]] = struct{}{}
	}

	reg := encoder.DefaultRegistry()
	result := make(map[string]model.Encoder)
	for _, e := range reg.All() {
		if _, ok := available[e.Name()]; !ok {
			continue
		}
		if m, ok := reg.Model(e.Name()); ok {
			result[e.Name()] = m
		}
	}
	preferEncoder(result, "libopus", "opus")
	preferEncoder(result, "libmp3lame", "mp3")

	// Copy is an FFmpeg pseudo-encoder valid for both media types and therefore
	// does not appear in `ffmpeg -encoders`.
	result["copy"] = model.Encoder{
		Name:     "copy",
		Label:    "Copy",
		Media:    "video,audio",
		Kind:     KindPassthrough,
		Priority: 100,
	}
	return result
}

func preferEncoder(encoders map[string]model.Encoder, preferred, fallback string) {
	if _, ok := encoders[preferred]; ok {
		delete(encoders, fallback)
	}
}

// ParseHWAccels parses the one-name-per-line section emitted by -hwaccels.
func ParseHWAccels(output string) []string {
	var result []string
	seen := make(map[string]struct{})
	inList := false
	for line := range strings.SplitSeq(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.EqualFold(strings.TrimSuffix(line, ":"), "Hardware acceleration methods") {
			inList = true
			continue
		}
		if !inList || strings.ContainsAny(line, " \t") {
			continue
		}
		if _, ok := seen[line]; ok {
			continue
		}
		seen[line] = struct{}{}
		result = append(result, line)
	}
	sort.Strings(result)
	return result
}

func commandOutput(ctx context.Context, executable string, args ...string) (string, error) {
	output, err := exec.CommandContext(ctx, executable, args...).CombinedOutput() //nolint:gosec // G204: executable resolved via exec.LookPath
	if err != nil {
		detail := strings.TrimSpace(string(output))
		if detail == "" {
			detail = err.Error()
		}
		if errors.Is(ctx.Err(), context.Canceled) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return "", ctx.Err()
		}
		return "", fmt.Errorf("%s %s failed: %s", executable, strings.Join(args, " "), detail)
	}
	return string(output), nil
}
