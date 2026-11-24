package ui

import (
	"context"
	"errors"
	"strings"
	"testing"

	"git.skobk.in/skobkin/yave/internal/model"
)

// TestDetectDropsStaleResults pins that a slow earlier detection finishing
// after a newer one cannot overwrite the newer result.
func TestDetectDropsStaleResults(t *testing.T) {
	c, _, updates := exportTestController(t)
	releaseFirst := make(chan struct{})
	firstStarted := make(chan struct{})
	// Per-call behavior is keyed on the snapshot path so the stub never
	// shares mutable state between the two goroutines.
	c.discover = func(ctx context.Context, cfg model.FFmpegConfig) (model.Capabilities, error) {
		if cfg.FFmpegPath == "slow" {
			close(firstStarted)
			<-releaseFirst
			return model.Capabilities{}, errors.New("stale")
		}
		return model.Capabilities{}, errors.New("fresh")
	}

	c.config.FFmpeg.FFmpegPath = "slow"
	c.detect()
	<-firstStarted
	c.config.FFmpeg.FFmpegPath = "fast"
	c.detect()

	pumpUntil(t, updates, func() bool { return strings.Contains(c.detection.String(), "fresh") }, "fresh detection result")
	close(releaseFirst)
	// The stale result must be dispatched but ignored.
	f := <-updates
	f()
	if !strings.Contains(c.detection.String(), "fresh") {
		t.Fatalf("detection label = %q: a stale result overwrote the newer one", c.detection.String())
	}
}

func TestRetryDetectionRerunsDetectionAndTracksBusyState(t *testing.T) {
	c, _, updates := exportTestController(t)
	release := make(chan struct{})
	calls := make(chan struct{}, 4)
	c.discover = func(ctx context.Context, cfg model.FFmpegConfig) (model.Capabilities, error) {
		calls <- struct{}{}
		<-release
		return model.Capabilities{}, errors.New("not installed")
	}

	c.retryDetect.OnTapped()
	<-calls
	if !c.detecting || !c.retryDetect.Disabled() || !c.detectActivity.Visible() {
		t.Fatal("while detecting, Retry must be disabled and the spinner shown")
	}
	close(release)
	pumpUntil(t, updates, func() bool { return !c.detecting }, "detection result")
	if c.retryDetect.Disabled() || c.detectActivity.Visible() {
		t.Fatal("after detection, Retry must be enabled and the spinner hidden")
	}
	if !strings.Contains(c.detection.String(), "not installed") {
		t.Fatalf("diagnostics = %q, want the failure shown", c.detection.String())
	}
}

func TestRetryDetectionIsOffDuringARun(t *testing.T) {
	c, _, _ := exportTestController(t)
	c.runControl = &fakeConversionControl{}
	c.updateRetryDetect()
	if !c.retryDetect.Disabled() {
		t.Fatal("Retry detection must be disabled while a conversion runs")
	}
	c.runControl = nil
	c.updateRetryDetect()
	if c.retryDetect.Disabled() {
		t.Fatal("Retry detection must be available again after the run")
	}
}
