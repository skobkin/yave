# YAVE [![status-badge](https://ci.skobk.in/api/badges/13/status.svg?events=push%2Ctag)](https://ci.skobk.in/repos/13)[![latest release](https://git.skobk.in/skobkin/yave/badges/release.svg)](https://git.skobk.in/skobkin/yave/releases)

YAVE (Yet Another Video Encoder) is a focused desktop FFmpeg transcoding
frontend written in Go and Fyne. It exposes a curated set of modern software
and VAAPI encoders, reusable YAML profiles, safe output templates, transparent
FFmpeg commands, progress reporting, logs, and cancellation. On multi-GPU
systems YAVE probes each render node through libva and automatically picks a
VAAPI device that can actually encode the selected codec.

## Requirements

- Go 1.26 or newer
- FFmpeg and ffprobe available in `PATH`, or configured in the application
- Linux desktop development libraries required by Fyne:
  - OpenGL development files
  - X11 development files
  - Wayland client and xkbcommon development files (Fyne 2.8 builds
    the Wayland backend by default on Linux)
  - `libgl1-mesa-dev xorg-dev libwayland-dev libxkbcommon-dev` on
    Debian/Ubuntu
- A VAAPI render device (`/dev/dri/renderD*`) for VAAPI profiles. When
  `libva.so.2` and `libva-drm.so.2` are installed, YAVE probes every render
  node and, with the device set to `(Auto)`, uses the first one that can
  encode the chosen codec and profile — decode-only nodes (for example
  NVIDIA's VA-API wrapper) are skipped. Without libva, automatic mode falls
  back to the first render node. An explicit `vaapi_device` in a profile
  always overrides the automatic choice.

## Build and run

```bash
go build -o yave ./cmd/yave
./yave
```

An input file can be supplied as the first argument or dropped onto the
application window:

```bash
./yave /path/to/video.mkv
```

## Profiles and settings

Configuration is stored in the platform user configuration directory. On
Linux the default path is:

```text
~/.config/yave/config.yaml
```

The file is human-readable YAML and contains profiles, the startup default,
and FFmpeg executable paths. Built-in profiles are installed when no profile
configuration exists.

YAVE validates saved profiles against the encoders reported by the current
FFmpeg build. Unavailable or incompatible settings remain editable, but
conversion cannot start until validation errors are resolved.

## Output templates

The default template is:

```text
{dir}/{name}_{profile}.{ext}
```

Supported placeholders are `{dir}`, `{name}`, `{ext}`, `{input_ext}`,
`{datetime}`, `{date}`, `{time}`, `{profile}`, `{codec}`, `{width}`, and
`{height}`. Placeholder values are sanitized and templates containing unknown
placeholders or path traversal are rejected.

## Thanks

Special thanks to the people who helped shape YAVE.

### App icon

- [@lonelyowl](https://github.com/lonelyowl13)
- [@mychaelo](https://github.com/mychaelo)

## Verification

```bash
go test ./...
go vet ./...
go build ./cmd/yave
```
