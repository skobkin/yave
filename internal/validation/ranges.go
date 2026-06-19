package validation

import (
	"strings"

	"git.skobk.in/skobkin/yave/internal/encoder"
)

// IntegerRange aliases encoder.IntegerRange so existing tests and
// consumers that take validation.IntegerRange keep working unchanged
// after the refactor.
type IntegerRange = encoder.IntegerRange

// ProportionalConvert is re-exported from the encoder package so the
// proportional-remap helpers stay callable as validation.ProportionalConvert.
var ProportionalConvert = encoder.ProportionalConvert

// lookupVideoEncoder returns the VideoEncoder registered under name,
// performing a case-insensitive lookup to match the pre-refactor
// behaviour (the original code lowercased the input before switching).
func lookupVideoEncoder(name string) (encoder.VideoEncoder, bool) {
	want := strings.ToLower(strings.TrimSpace(name))
	for _, e := range encoder.DefaultRegistry().All() {
		if !strings.EqualFold(e.Name(), want) {
			continue
		}
		ve, ok := e.(encoder.VideoEncoder)
		return ve, ok
	}
	return nil, false
}

// lookupAudioEncoder returns the AudioEncoder registered under name,
// case-insensitively.
func lookupAudioEncoder(name string) (encoder.AudioEncoder, bool) {
	want := strings.ToLower(strings.TrimSpace(name))
	for _, e := range encoder.DefaultRegistry().All() {
		if !strings.EqualFold(e.Name(), want) {
			continue
		}
		ae, ok := e.(encoder.AudioEncoder)
		return ae, ok
	}
	return nil, false
}

// QPRange returns the QP integer range for the given video encoder name,
// or (zero, false) if the encoder is unknown or has no QP control.
func QPRange(encoderName string) (IntegerRange, bool) {
	ve, ok := lookupVideoEncoder(encoderName)
	if !ok {
		return IntegerRange{}, false
	}
	return ve.QPRange()
}

// CRFRange returns the CRF integer range for the given video encoder
// name, or (zero, false) if the encoder is unknown or has no CRF control.
func CRFRange(encoderName string) (IntegerRange, bool) {
	ve, ok := lookupVideoEncoder(encoderName)
	if !ok {
		return IntegerRange{}, false
	}
	return ve.CRFRange()
}

// PresetRange returns the preset integer range for the given video
// encoder name, or (zero, false) if the encoder is unknown or has no
// numeric preset.
func PresetRange(encoderName string) (IntegerRange, bool) {
	ve, ok := lookupVideoEncoder(encoderName)
	if !ok {
		return IntegerRange{}, false
	}
	return ve.PresetRange()
}

// AudioCompressionRange returns the audio compression integer range
// (e.g. FLAC compression level) for the given audio encoder name, or
// (zero, false) if the encoder is unknown or has no compression control.
func AudioCompressionRange(encoderName string) (IntegerRange, bool) {
	ae, ok := lookupAudioEncoder(encoderName)
	if !ok {
		return IntegerRange{}, false
	}
	return ae.AudioCompressionRange()
}