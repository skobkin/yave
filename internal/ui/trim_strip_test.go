package ui

import (
	"context"
	"testing"
	"time"
)

func TestNiceStripStep(t *testing.T) {
	tests := []struct {
		ideal, want time.Duration
	}{
		{time.Millisecond, 10 * time.Millisecond},
		{6 * time.Second, 5 * time.Second},
		{8 * time.Second, 10 * time.Second},
		{12 * time.Minute, 10 * time.Minute},
		{10 * time.Hour, 2 * time.Hour},
	}
	for _, tt := range tests {
		if got := niceStripStep(tt.ideal); got != tt.want {
			t.Errorf("niceStripStep(%s) = %s, want %s", tt.ideal, got, tt.want)
		}
	}
}

func TestStripGridFor(t *testing.T) {
	tests := []struct {
		name                  string
		winStart, winEnd, dur time.Duration
		want                  stripGrid
	}{
		{"whole short media", 0, 60 * time.Second, 60 * time.Second, stripGrid{start: 0, cell: 5 * time.Second, n: 12}},
		{"zoomed mid-media gets a margin cell each side", 30 * time.Second, 40 * time.Second, 120 * time.Second,
			stripGrid{start: 29 * time.Second, cell: time.Second, n: 12}},
		{"never past the media end", 55 * time.Second, 60 * time.Second, 60 * time.Second,
			stripGrid{start: 54*time.Second + 500*time.Millisecond, cell: 500 * time.Millisecond, n: 11}},
		{"empty window", 5 * time.Second, 5 * time.Second, 60 * time.Second, stripGrid{}},
		{"unknown duration", 0, time.Second, 0, stripGrid{}},
	}
	for _, tt := range tests {
		got := stripGridFor(tt.winStart, tt.winEnd, tt.dur)
		if got != tt.want {
			t.Errorf("%s: got %+v, want %+v", tt.name, got, tt.want)
		}
	}
}

func TestStripGridSameStepSharesSampleTimes(t *testing.T) {
	// Scrolling at one zoom level must resample at the same instants so
	// cached thumbnails are reused.
	const duration = 600 * time.Second
	a := stripGridFor(100*time.Second, 120*time.Second, duration)
	b := stripGridFor(103*time.Second, 123*time.Second, duration)
	if a.cell != b.cell {
		t.Fatalf("scroll changed the step: %s vs %s", a.cell, b.cell)
	}
	seen := map[time.Duration]bool{}
	for i := range a.n {
		seen[a.sampleAt(i, duration)] = true
	}
	shared := 0
	for i := range b.n {
		if seen[b.sampleAt(i, duration)] {
			shared++
		}
	}
	if shared < 8 {
		t.Errorf("only %d sample times shared between nearby scroll positions", shared)
	}
}

func TestStripGridSampleAtClampsToMediaEnd(t *testing.T) {
	g := stripGrid{start: 0, cell: 10 * time.Second, n: 2}
	if got := g.sampleAt(1, 14*time.Second); got != 12*time.Second {
		t.Errorf("last partial cell sampled at %s, want 12s (midpoint of the valid part)", got)
	}
	if got := g.sampleAt(0, 14*time.Second); got != 5*time.Second {
		t.Errorf("first cell sampled at %s, want 5s", got)
	}
}

func TestBgJobRetiresPreviousRun(t *testing.T) {
	var j bgJob
	ctx1, gen1 := j.start(context.Background())
	ctx2, gen2 := j.start(context.Background())
	if ctx1.Err() == nil {
		t.Error("starting a job must cancel the previous one")
	}
	if j.current(gen1) || !j.current(gen2) {
		t.Error("only the newest generation is current")
	}
	j.stop()
	if ctx2.Err() == nil || j.current(gen2) {
		t.Error("stop must cancel the job and invalidate its generation")
	}
	j.stop() // idempotent
}
