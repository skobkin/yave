# YAVE [![status-badge](https://ci.skobk.in/api/badges/13/status.svg?events=push%2Ctag)](https://ci.skobk.in/repos/13)
[![latest release](https://git.skobk.in/skobkin/yave/badges/release.svg)](https://git.skobk.in/skobkin/yave/releases)

YAVE (Yet Another Video Encoder) is a focused desktop FFmpeg transcoding
frontend written in Go and Fyne. It exposes a curated set of modern software
and VAAPI encoders, reusable YAML profiles, safe output templates, transparent
FFmpeg commands, progress reporting, logs, and cancellation.

## Requirements

- Go 1.26 or newer
- FFmpeg and ffprobe available in `PATH`, or configured in the application
- Linux desktop development libraries required by Fyne:
  - OpenGL development files
  - X11 development files
  - `libgl1-mesa-dev` and `xorg-dev` on Debian/Ubuntu
- A usable VAAPI render device, normally `/dev/dri/renderD128`, for VAAPI
  profiles

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

## Verification

```bash
go test ./...
go vet ./...
go build ./cmd/yave
```
