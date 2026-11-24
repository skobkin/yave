// Package analysis runs the background media analyses the Trim tab draws
// on its timeline — scene-cut detection and the audio waveform — through
// the configured FFmpeg executable. It has no GUI dependency and is
// independent of the transcoding runner: every function takes a context,
// stops its subprocess promptly when that context ends, and reports
// failures with FFmpeg's own diagnostics.
package analysis
