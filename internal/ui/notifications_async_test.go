package ui

import (
	"context"
	"errors"
	"testing"
	"time"
)

type slowNotifier struct{ release chan struct{} }

func (s slowNotifier) Notify(ctx context.Context, _, _ string) error {
	select {
	case <-s.release:
	case <-ctx.Done():
	}
	return nil
}

func TestNotifyRunDoesNotBlockCaller(t *testing.T) {
	n := slowNotifier{release: make(chan struct{})}
	defer close(n.release)
	c := &Controller{notifier: n, inputPath: "/in/a.mkv"}
	done := make(chan struct{})
	go func() { c.notifyRun(nil, time.Second, nil, 1, 1); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("notifyRun blocked on a slow notification service")
	}
}

func TestNotifyRunNamesTheRunningInput(t *testing.T) {
	fake := &fakeNotifier{}
	c := &Controller{notifier: fake, inputPath: "/in/B.mkv", runInput: "/in/A.mkv", notifyGo: func(f func()) { f() }}
	c.notifyRun(errors.New("boom"), 0, nil, 0, 1)
	if len(fake.sent) != 1 || fake.sent[0][1] != "A.mkv: boom" {
		t.Fatalf("sent = %v, want the running input A.mkv", fake.sent)
	}
}
