package ui

import (
	"context"
	"errors"
	"testing"
	"time"

	"git.skobk.in/skobkin/yave/internal/model"
)

type fakeNotifier struct{ sent [][2]string }

func (f *fakeNotifier) Notify(_ context.Context, summary, body string) error {
	f.sent = append(f.sent, [2]string{summary, body})
	return nil
}

func TestNotificationEnabled(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		cfg  model.NotificationsConfig
		kind notificationKind
		want bool
	}{
		{"default sends everything", model.NotificationsConfig{}, notifyFailed, true},
		{"disable all", model.NotificationsConfig{DisableAll: true}, notifyCompleted, false},
		{"completed off", model.NotificationsConfig{DisableCompleted: true}, notifyCompleted, false},
		{"completed off keeps failed", model.NotificationsConfig{DisableCompleted: true}, notifyFailed, true},
		{"failed off", model.NotificationsConfig{DisableFailed: true}, notifyFailed, false},
		{"canceled off", model.NotificationsConfig{DisableCanceled: true}, notifyCanceled, false},
		{"canceled off keeps completed", model.NotificationsConfig{DisableCanceled: true}, notifyCompleted, true},
	}
	for _, tt := range tests {
		if got := notificationEnabled(tt.cfg, tt.kind); got != tt.want {
			t.Errorf("%s: %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestRunNotificationText(t *testing.T) {
	t.Parallel()
	kind, summary, body := runNotification(nil, "/in/movie.mkv", 192*time.Second, []string{"/out/movie_comp.mkv"}, 1, 1)
	if kind != notifyCompleted || summary != "Conversion finished" || body != "movie_comp.mkv — 3m12s" {
		t.Fatalf("completed: %v %q %q", kind, summary, body)
	}
	kind, _, body = runNotification(nil, "/in/movie.mkv", time.Minute, []string{"/o/a1.mkv", "/o/a2.mkv"}, 2, 2)
	if kind != notifyCompleted || body != "movie.mkv → 2 files — 1m00s" {
		t.Fatalf("multi: %q", body)
	}
	kind, summary, _ = runNotification(context.Canceled, "/in/movie.mkv", time.Second, nil, 0, 1)
	if kind != notifyCanceled || summary != "Conversion canceled" {
		t.Fatalf("canceled: %v %q", kind, summary)
	}
	kind, summary, body = runNotification(errors.New("ffmpeg exploded"), "/in/movie.mkv", time.Second, nil, 0, 1)
	if kind != notifyFailed || summary != "Conversion failed" || body != "movie.mkv: ffmpeg exploded" {
		t.Fatalf("failed: %v %q %q", kind, summary, body)
	}
}

func TestNotifyRunHonorsSettings(t *testing.T) {
	fake := &fakeNotifier{}
	c := &Controller{notifier: fake, inputPath: "/in/a.mkv", notifyGo: func(f func()) { f() }}
	c.notifyRun(nil, time.Second, nil, 1, 1)
	if len(fake.sent) != 1 {
		t.Fatalf("sent = %v, want one completed notification", fake.sent)
	}
	c.config.Notifications.DisableCompleted = true
	c.notifyRun(nil, time.Second, nil, 1, 1)
	c.notifyRun(errors.New("boom"), time.Second, nil, 0, 1)
	if len(fake.sent) != 2 || fake.sent[1][0] != "Conversion failed" {
		t.Fatalf("sent = %v: completed must be muted, failed must still go out", fake.sent)
	}
	c.config.Notifications.DisableAll = true
	c.notifyRun(errors.New("boom"), time.Second, nil, 0, 1)
	if len(fake.sent) != 2 {
		t.Fatal("disable-all must send nothing")
	}
	(&Controller{}).notifyRun(nil, 0, nil, 1, 1) // no notifier: must not panic
}
