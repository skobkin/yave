package encoder

import (
	"testing"

	"git.skobk.in/skobkin/yave/internal/model"
)

// TestSettingsSummaryVideo pins the {video_settings} token per encoder
// family: rate-control modes distinguish (including cbr vs vbr), the
// preset appears only for encoders whose builders emit one (nvenc/qsv
// defaults included, vaapi never), tune rides when set, fps only on
// conversion, and one-sided dimensions stay representable.
func TestSettingsSummaryVideo(t *testing.T) {
	tests := []struct {
		name    string
		encoder string
		video   model.VideoSettings
		want    string
	}{
		{"copy short-circuits", "copy", model.VideoSettings{}, "copy"},
		{"x265 crf preset dims", "libx265", model.VideoSettings{
			CRF: 40, Preset: "medium", MaxWidth: 1280, MaxHeight: 720,
		}, "libx265_crf40_medium_1280x720"},
		{"x265 main10 disambiguates within one saved profile", "libx265", model.VideoSettings{
			Profile: "main10", CRF: 40, Preset: "medium",
		}, "libx265_main10_crf40_medium"},
		{"x265 tune rides", "libx265", model.VideoSettings{
			CRF: 28, Preset: "slow", Tune: "grain",
		}, "libx265_crf28_slow_grain"},
		{"x264 empty preset stays omitted", "libx264", model.VideoSettings{CRF: 23}, "libx264_crf23"},
		{"case and space variants normalize to the curated spelling", "hevc_vaapi", model.VideoSettings{
			RateControl: "CQP", QP: 30, Profile: " MAIN10 ",
		}, "hevc_vaapi_main10_qp30"},
		{"vaapi cqp no preset", "hevc_vaapi", model.VideoSettings{
			RateControl: "CQP", QP: 30, MaxWidth: 1280, MaxHeight: 720,
		}, "hevc_vaapi_qp30_1280x720"},
		{"vaapi auto without bitrate renders no rc token", "hevc_vaapi", model.VideoSettings{}, "hevc_vaapi"},
		{"vaapi auto with bitrate carries b", "hevc_vaapi", model.VideoSettings{Bitrate: "6M"}, "hevc_vaapi_b6m"},
		{"vaapi vbr", "hevc_vaapi", model.VideoSettings{RateControl: "VBR", Bitrate: "6M"}, "hevc_vaapi_vbr6m"},
		{"vaapi cbr distinct from vbr", "hevc_vaapi", model.VideoSettings{RateControl: "CBR", Bitrate: "6M"}, "hevc_vaapi_cbr6m"},
		{"nvenc vbr with default preset materializes", "hevc_nvenc", model.VideoSettings{
			Profile: "main10", RateControl: "VBR", Bitrate: "6M",
		}, "hevc_nvenc_main10_vbr6m_p4"},
		{"nvenc empty mode defaults to cqp", "hevc_nvenc", model.VideoSettings{Profile: "main10"}, "hevc_nvenc_main10_qp0_p4"},
		{"nvenc vbr", "hevc_nvenc", model.VideoSettings{
			RateControl: "VBR", Bitrate: "6M", Preset: "p5",
		}, "hevc_nvenc_vbr6m_p5"},
		{"nvenc cbr", "hevc_nvenc", model.VideoSettings{RateControl: "CBR", Bitrate: "4M"}, "hevc_nvenc_cbr4m_p4"},
		{"qsv default preset is medium", "hevc_qsv", model.VideoSettings{QP: 26}, "hevc_qsv_qp26_medium"},
		{"svtav1 integer preset default", "libsvtav1", model.VideoSettings{CRF: 30}, "libsvtav1_crf30_6"},
		{"fps only on conversion", "libx265", model.VideoSettings{
			CRF: 28, FrameRateMode: "keep", FrameRate: "60",
		}, "libx265_crf28"},
		{"fps renders on convert", "libx265", model.VideoSettings{
			CRF: 28, FrameRateMode: "convert", FrameRate: "60",
		}, "libx265_crf28_60fps"},
		{"one-sided width", "libx265", model.VideoSettings{CRF: 28, MaxWidth: 1280}, "libx265_crf28_1280w"},
		{"one-sided height", "libx265", model.VideoSettings{CRF: 28, MaxHeight: 720}, "libx265_crf28_720h"},
		{"no dims no component", "libx265", model.VideoSettings{CRF: 28}, "libx265_crf28"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			video, _ := SettingsSummary(tt.encoder, tt.video, model.AudioSettings{})
			if video != tt.want {
				t.Errorf("SettingsSummary video = %q, want %q", video, tt.want)
			}
		})
	}
}

// TestSettingsSummaryAudio pins the {audio_settings} token: bitrate for
// bitrate encoders, the compression level ALWAYS for flac (a stale
// retained bitrate must not leak into a lossless encode's name), and
// copy for stream copy in both spellings.
func TestSettingsSummaryAudio(t *testing.T) {
	tests := []struct {
		name  string
		audio model.AudioSettings
		want  string
	}{
		{"copy mode", model.AudioSettings{Mode: model.AudioCopy, Encoder: "libopus", Bitrate: "160k"}, "copy"},
		{"copy encoder name", model.AudioSettings{Mode: model.AudioEncode, Encoder: "copy"}, "copy"},
		{"opus bitrate", model.AudioSettings{Mode: model.AudioEncode, Encoder: "libopus", Bitrate: "96k"}, "libopus_96k"},
		{"flac renders compression, not the stale bitrate", model.AudioSettings{
			Mode: model.AudioEncode, Encoder: "flac", Compression: 8, Bitrate: "160k",
		}, "flac_c8"},
		{"flac level zero is honest", model.AudioSettings{
			Mode: model.AudioEncode, Encoder: "flac",
		}, "flac_c0"},
		{"aac bitrate", model.AudioSettings{Mode: model.AudioEncode, Encoder: "aac", Bitrate: "192k"}, "aac_192k"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, audio := SettingsSummary("libx265", model.VideoSettings{}, tt.audio)
			if audio != tt.want {
				t.Errorf("SettingsSummary audio = %q, want %q", audio, tt.want)
			}
		})
	}
}

// TestSettingsSummaryFilenamesDontCollide pins the reason the
// placeholders exist: within one saved profile, every setting that
// changes the encode must change the filename. Each step mutates one
// field and the combined token must differ from the previous one.
func TestSettingsSummaryFilenamesDontCollide(t *testing.T) {
	base := model.VideoSettings{
		Encoder: "libx265", Profile: "main", CRF: 40, Preset: "medium", MaxWidth: 1280, MaxHeight: 720,
	}
	video, _ := SettingsSummary(base.Encoder, base, model.AudioSettings{})
	seen := map[string]string{"base": video}

	steps := []struct {
		name   string
		mutate func(*model.VideoSettings)
	}{
		{"codec profile main to main10", func(v *model.VideoSettings) { v.Profile = "main10" }},
		{"crf 40 to 28", func(v *model.VideoSettings) { v.CRF = 28 }},
		{"preset medium to slow", func(v *model.VideoSettings) { v.Preset = "slow" }},
		{"tune set", func(v *model.VideoSettings) { v.Tune = "grain" }},
		{"fps conversion on", func(v *model.VideoSettings) { v.FrameRateMode = "convert"; v.FrameRate = "60" }},
		{"dims narrowed", func(v *model.VideoSettings) { v.MaxWidth, v.MaxHeight = 1280, 544 }},
	}
	for _, step := range steps {
		v := base
		step.mutate(&v)
		video, _ := SettingsSummary(v.Encoder, v, model.AudioSettings{})
		if prev, collision := seen[video]; collision {
			t.Errorf("%s: token %q collides with %s", step.name, video, prev)
		}
		seen[video] = step.name
	}

	audioSteps := []model.AudioSettings{
		{Mode: model.AudioEncode, Encoder: "libopus", Bitrate: "96k"},
		{Mode: model.AudioEncode, Encoder: "libopus", Bitrate: "160k"},
		{Mode: model.AudioEncode, Encoder: "libopus", Bitrate: "96k", CopyAllStreams: true}, // copy flag is not a token part; still distinct from the bitrate change above
		{Mode: model.AudioEncode, Encoder: "flac", Compression: 8},
	}
	audioSeen := map[string]bool{}
	for i, a := range audioSteps {
		_, audio := SettingsSummary("libx265", base, a)
		if i < 2 && audioSeen[audio] {
			t.Errorf("audio step %d: token %q unexpectedly repeats", i, audio)
		}
		audioSeen[audio] = true
	}
}
