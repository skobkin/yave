package ui

import (
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"strconv"
	"time"

	"git.skobk.in/skobkin/yave/internal/model"
	"git.skobk.in/skobkin/yave/internal/notify"
)

// notificationKind is one type of run notification; each can be disabled
// separately in the settings.
type notificationKind uint8

const (
	notifyCompleted notificationKind = iota
	notifyFailed
	notifyCanceled
)

// enabled reports whether a notification of the kind may be sent.
func notificationEnabled(cfg model.NotificationsConfig, kind notificationKind) bool {
	if cfg.DisableAll {
		return false
	}
	switch kind {
	case notifyCompleted:
		return !cfg.DisableCompleted
	case notifyFailed:
		return !cfg.DisableFailed
	case notifyCanceled:
		return !cfg.DisableCanceled
	}
	return false
}

// runNotification builds the notification for a finished run: its kind,
// summary and body.
func runNotification(err error, input string, elapsed time.Duration, outputs []string, published, total int) (notificationKind, string, string) {
	name := filepath.Base(input)
	switch {
	case err == nil:
		body := name + " — " + formatClock(elapsed)
		if len(outputs) == 1 {
			body = filepath.Base(outputs[0]) + " — " + formatClock(elapsed)
		} else if len(outputs) > 1 {
			body = name + " → " + strconv.Itoa(len(outputs)) + " files — " + formatClock(elapsed)
		}
		return notifyCompleted, "Conversion finished", body
	case errors.Is(err, context.Canceled):
		return notifyCanceled, "Conversion canceled", partialStatus(name, published, total)
	default:
		return notifyFailed, "Conversion failed", name + ": " + truncateRunes(err.Error(), 200)
	}
}

// truncateRunes shortens s to at most limit runes, adding an ellipsis.
func truncateRunes(s string, limit int) string {
	runes := []rune(s)
	if len(runes) <= limit {
		return s
	}
	return string(runes[:limit]) + "…"
}

// notifyRun sends the notification for a finished run when its type is
// enabled. A backend failure is logged and never shown to the user.
func (c *Controller) notifyRun(err error, elapsed time.Duration, outputs []string, published, total int) {
	if c.notifier == nil {
		return
	}
	kind, summary, body := runNotification(err, c.inputPath, elapsed, outputs, published, total)
	if !notificationEnabled(c.config.Notifications, kind) {
		return
	}
	notifier := c.notifier
	send := func() {
		ctx, cancel := context.WithTimeout(context.Background(), notifyTimeout)
		defer cancel()
		if nerr := notifier.Notify(ctx, summary, body); nerr != nil {
			slog.Warn("send desktop notification", "error", nerr)
		}
	}
	// The notification service can be slow or hung; the UI callback that
	// reports completion must never wait for it.
	if c.notifyGo != nil {
		c.notifyGo(send)
		return
	}
	go send()
}

// notifyTimeout bounds one desktop notification call.
const notifyTimeout = 5 * time.Second

// setupNotifier picks the platform notification backend, falling back to
// a no-op sender when none is available.
func (c *Controller) setupNotifier() {
	n, err := notify.New("YAVE")
	if err != nil {
		slog.Warn("desktop notifications unavailable", "error", err)
		n = notify.Nop{}
	}
	c.notifier = n
}
