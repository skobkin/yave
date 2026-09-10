package model

const (
	ContainerMKV  = "mkv"
	ContainerMP4  = "mp4"
	ContainerWebM = "webm"

	AudioCopy   = "copy"
	AudioEncode = "encode"

	SubtitleNone = "none"
	SubtitleCopy = "copy_if_compatible"
)

func DefaultConfig() Config {
	profiles := BuiltinProfiles()
	return Config{
		Version:        ConfigVersion,
		Profiles:       profiles,
		DefaultProfile: profiles[0].Name,
		FFmpeg: FFmpegConfig{
			FFmpegPath:  "ffmpeg",
			FFprobePath: "ffprobe",
		},
		Updates: UpdatesConfig{
			CheckAutomatically: true,
		},
	}
}

func BuiltinProfiles() []Profile {
	base := Profile{
		Container:      ContainerMKV,
		OutputTemplate: "{dir}/{name}_comp.{ext}",
		Video: VideoSettings{
			RateControl:      "CQP",
			QP:               30,
			CRF:              28,
			Preset:           "medium",
			Profile:          "main",
			FrameRateMode:    "keep",
			KeepAspectRatio:  true,
			PreventUpscaling: true,
			// VAAPIDevice is left empty on purpose: empty means automatic
			// device selection (see VideoSettings.VAAPIDevice), which lets
			// multi-GPU hosts use the encode-capable render node instead
			// of a hardcoded path.
		},
		Audio: AudioSettings{
			Mode:        AudioCopy,
			Encoder:     "copy",
			Bitrate:     "160k",
			Compression: 8,
		},
		Subtitles: SubtitleSettings{Mode: SubtitleCopy},
	}

	hevc720 := base
	hevc720.Name = "HEVC VAAPI 720p CQ"
	hevc720.Video.Encoder = "hevc_vaapi"
	hevc720.Video.MaxWidth = 1280
	hevc720.Video.MaxHeight = 720

	hevcOriginal := base
	hevcOriginal.Name = "HEVC VAAPI Original Size CQ"
	hevcOriginal.OutputTemplate = "{dir}/{name}_hevc.{ext}"
	hevcOriginal.Video.Encoder = "hevc_vaapi"

	h264 := base
	h264.Name = "H.264 VAAPI 1080p"
	h264.Container = ContainerMP4
	h264.OutputTemplate = "{dir}/{name}_h264.{ext}"
	h264.Video.Encoder = "h264_vaapi"
	h264.Video.RateControl = "VBR"
	h264.Video.Bitrate = "6000k"
	h264.Video.Maxrate = "9000k"
	h264.Video.Bufsize = "12000k"
	h264.Video.MaxWidth = 1920
	h264.Video.MaxHeight = 1080
	h264.Audio.Mode = AudioEncode
	h264.Audio.Encoder = "aac"

	x265 := base
	x265.Name = "x265 Software Archive"
	x265.OutputTemplate = "{dir}/{name}_x265.{ext}"
	x265.Video.Encoder = "libx265"
	x265.Video.RateControl = "CRF"
	x265.Video.CRF = 28
	x265.Video.Preset = "slow"

	return []Profile{hevc720, hevcOriginal, h264, x265}
}
