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

	pumpUntil(t, updates, func() bool { return strings.Contains(c.detection.Text, "fresh") }, "fresh detection result")
	close(releaseFirst)
	// The stale result must be dispatched but ignored.
	f := <-updates
	f()
	if !strings.Contains(c.detection.Text, "fresh") {
		t.Fatalf("detection label = %q: a stale result overwrote the newer one", c.detection.Text)
	}
}
