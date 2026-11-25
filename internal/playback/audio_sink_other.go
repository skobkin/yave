//go:build !linux

package playback

// NewAudioSink returns the platform audio output: oto (WASAPI on
// Windows, Core Audio on macOS).
func NewAudioSink() AudioSink { return otoSink{} }
