package analysis

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"
)

const (
	// WaveformSampleRate is the rate the audio is decoded to for the
	// waveform: the strip only needs the envelope, and 8 kHz mono keeps the
	// pipe and the reduction cheap.
	WaveformSampleRate = 8000
	// minWaveformBucket is the finest envelope resolution.
	minWaveformBucket = 10 * time.Millisecond
	// waveformMaxBuckets bounds the result for any duration: the bucket
	// grows with the input so a long file keeps a bounded memory footprint
	// (200k peaks are 800 KB).
	waveformMaxBuckets = 200_000
	// waveformUpdateEvery throttles progressive updates.
	waveformUpdateEvery = 250 * time.Millisecond
)

// Peak is the lowest and highest sample of one bucket.
type Peak struct {
	Min, Max int16
}

// Waveform is an audio envelope: Peaks[i] covers the input-relative range
// [i*Bucket, (i+1)*Bucket).
type Waveform struct {
	Bucket time.Duration
	Peaks  []Peak
}

// Duration is the length of media the peaks cover.
func (w Waveform) Duration() time.Duration {
	return time.Duration(len(w.Peaks)) * w.Bucket
}

// WaveformOptions describe one computation.
type WaveformOptions struct {
	FFmpegPath string
	Input      string
	// Duration is the input's length; it sizes the bucket so the result
	// stays bounded. Zero uses the finest bucket.
	Duration time.Duration
}

// samplesPerBucket sizes the bucket for a duration: the finest bucket,
// coarsened so at most waveformMaxBuckets peaks result, in whole samples.
func samplesPerBucket(duration time.Duration) int {
	bucket := max(minWaveformBucket, duration/waveformMaxBuckets)
	return max(int(bucket*WaveformSampleRate/time.Second), 1)
}

func bucketDuration(perBucket int) time.Duration {
	return time.Duration(perBucket) * time.Second / WaveformSampleRate
}

// WaveformArgs builds the decode command line: the first audio stream as
// 8 kHz mono signed 16-bit PCM on stdout. aresample with async=1 and
// first_pts=0 pads the lead-in of audio that starts after the container's
// origin with silence: raw PCM carries no timestamps, so without it the
// whole envelope would be shifted earlier by that delay (a file whose
// audio starts 1 s late would draw its sound a second too early).
func WaveformArgs(o WaveformOptions) []string {
	return []string{
		"-hide_banner", "-nostdin", "-loglevel", "error",
		"-i", o.Input,
		"-map", "0:a:0", "-vn", "-sn", "-dn",
		"-af", "aresample=async=1:first_pts=0",
		"-ac", "1", "-ar", fmt.Sprint(WaveformSampleRate),
		"-f", "s16le", "pipe:1",
	}
}

// ComputeWaveform decodes the input's first audio stream and reduces it
// to a min/max envelope while it streams, so memory stays bounded however
// long the input is. onUpdate (when non-nil) receives progressively
// longer waveforms, throttled; the returned waveform is final. A
// cancelled context returns its error and no partial result.
//
// The waveforms handed to onUpdate share their peaks with the running
// computation: peaks already written are never modified, so they are safe
// to read concurrently, but the slices must not be appended to.
func ComputeWaveform(ctx context.Context, o WaveformOptions, onUpdate func(Waveform)) (Waveform, error) {
	if strings.TrimSpace(o.Input) == "" {
		return Waveform{}, fmt.Errorf("compute waveform: input path is empty")
	}
	perBucket := samplesPerBucket(o.Duration)
	reducer := peakReducer{perBucket: perBucket}
	reducer.peaks = make([]Peak, 0, 4096)
	var readErr error
	err := run(ctx, o.FFmpegPath, WaveformArgs(o),
		func(stdout io.Reader) {
			buf := make([]byte, 64<<10)
			last := time.Now()
			for {
				n, err := stdout.Read(buf)
				reducer.write(buf[:n])
				if onUpdate != nil && time.Since(last) >= waveformUpdateEvery {
					last = time.Now()
					onUpdate(reducer.snapshot())
				}
				if err != nil {
					if err != io.EOF { //nolint:errorlint // io.Reader returns the bare sentinel
						readErr = err
					}
					return
				}
			}
		},
		nil)
	if err != nil {
		return Waveform{}, fmt.Errorf("compute waveform of %q: %w", o.Input, err)
	}
	if readErr != nil {
		return Waveform{}, fmt.Errorf("compute waveform of %q: read audio: %w", o.Input, readErr)
	}
	reducer.flush()
	return reducer.snapshot(), nil
}

// peakReducer folds a signed 16-bit little-endian PCM stream into buckets
// of perBucket samples, keeping each bucket's lowest and highest sample.
// It copes with writes that split a sample.
type peakReducer struct {
	perBucket int
	cur       Peak
	count     int
	carry     byte
	hasCarry  bool
	peaks     []Peak
}

func (r *peakReducer) write(p []byte) {
	if r.hasCarry && len(p) > 0 {
		r.sample(int16(uint16(r.carry) | uint16(p[0])<<8)) //nolint:gosec // G115: reassembling a little-endian sample
		r.hasCarry = false
		p = p[1:]
	}
	for len(p) >= 2 {
		r.sample(int16(uint16(p[0]) | uint16(p[1])<<8)) //nolint:gosec // G115: reassembling a little-endian sample
		p = p[2:]
	}
	if len(p) == 1 {
		r.carry, r.hasCarry = p[0], true
	}
}

func (r *peakReducer) sample(v int16) {
	if r.count == 0 {
		r.cur = Peak{Min: v, Max: v}
	} else {
		r.cur.Min = min(r.cur.Min, v)
		r.cur.Max = max(r.cur.Max, v)
	}
	r.count++
	if r.count == r.perBucket {
		r.peaks = append(r.peaks, r.cur)
		r.count = 0
	}
}

// flush closes a partially filled last bucket.
func (r *peakReducer) flush() {
	if r.count > 0 {
		r.peaks = append(r.peaks, r.cur)
		r.count = 0
	}
}

// snapshot returns the finished buckets as a waveform that shares the
// reducer's storage (capacity-limited, so it cannot be appended to).
func (r *peakReducer) snapshot() Waveform {
	n := len(r.peaks)
	return Waveform{Bucket: bucketDuration(r.perBucket), Peaks: r.peaks[:n:n]}
}
