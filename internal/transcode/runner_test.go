package transcode

import (
	"testing"
	"time"
)

func TestParseProgress(t *testing.T) {
	t.Parallel()
	p := ParseProgress(map[string]string{
		"frame":       "120",
		"out_time_us": "5000000",
		"speed":       "2.0x",
		"bitrate":     "900kbits/s",
		"progress":    "continue",
	}, 10*time.Second)
	if p.Frame != 120 || p.OutTime != 5*time.Second || p.Percentage != 0.5 {
		t.Fatalf("unexpected progress: %+v", p)
	}
}
