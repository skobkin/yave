package ui

import (
	"context"
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
