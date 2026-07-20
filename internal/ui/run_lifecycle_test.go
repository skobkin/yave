package ui

import (
	"testing"
	"time"

	"fyne.io/fyne/v2"
	fynetest "fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"
)

type fakeConversionControl struct {
	cancelRequests int
	terminations   int
}

func (c *fakeConversionControl) RequestCancel() {
	c.cancelRequests++
}

func (c *fakeConversionControl) Terminate() {
	c.terminations++
}

func TestCancelEscalatesToTerminateAfterGracePeriod(t *testing.T) {
	app := fynetest.NewApp()
	defer app.Quit()

	control := &fakeConversionControl{}
	updates := make(chan func(), 1)
	c := &Controller{
		runControl:  control,
		cancelDelay: 10 * time.Millisecond,
		status:      widget.NewLabel("Running"),
		cancel:      widget.NewButton("Cancel", nil),
		uiDispatch:  func(update func()) { updates <- update },
	}

	c.cancelConversion()
	if control.cancelRequests != 1 {
		t.Fatalf("cancel requests = %d, want 1", control.cancelRequests)
	}
	if c.status.Text != "Canceling…" || !c.cancel.Disabled() {
		t.Fatalf("initial cancel state = status %q, disabled %v", c.status.Text, c.cancel.Disabled())
	}

	waitForUIUpdate(t, updates)()
	if c.cancel.Text != "Terminate" || c.cancel.Disabled() {
		t.Fatalf("escalated button = %q, disabled %v", c.cancel.Text, c.cancel.Disabled())
	}
	if c.status.Text != "FFmpeg is not responding" {
		t.Fatalf("escalated status = %q", c.status.Text)
	}

	c.cancelConversion()
	if control.terminations != 1 {
		t.Fatalf("terminations = %d, want 1", control.terminations)
	}
	if c.status.Text != "Terminating…" || !c.cancel.Disabled() {
		t.Fatalf("termination state = status %q, disabled %v", c.status.Text, c.cancel.Disabled())
	}
}

func TestCancelTimerDoesNotAffectNewRun(t *testing.T) {
	app := fynetest.NewApp()
	defer app.Quit()

	oldControl := &fakeConversionControl{}
	updates := make(chan func(), 1)
	c := &Controller{
		runControl:  oldControl,
		cancelDelay: 10 * time.Millisecond,
		status:      widget.NewLabel("Running"),
		cancel:      widget.NewButton("Cancel", nil),
		uiDispatch:  func(update func()) { updates <- update },
	}
	c.cancelConversion()

	update := waitForUIUpdate(t, updates)
	c.runControl = &fakeConversionControl{}
	c.cancelStage = cancellationRunning
	c.status.SetText("Running")
	c.cancel.SetText("Cancel")
	c.cancel.Enable()
	update()

	if c.cancelStage != cancellationRunning || c.cancel.Text != "Cancel" || c.cancel.Disabled() {
		t.Fatalf("new run mutated by stale timer: stage %v, text %q, disabled %v", c.cancelStage, c.cancel.Text, c.cancel.Disabled())
	}
}

func TestWindowCloseHandlerHidesMainWindow(t *testing.T) {
	app := fynetest.NewApp()
	defer app.Quit()

	c := &Controller{window: app.NewWindow("YAVE"), windowVisible: true}
	c.window.Show()
	c.hideWindow()
	if c.windowVisible {
		t.Fatal("windowVisible = true after hide")
	}
}

func TestActiveQuitPromptLeavesMainWindowStateUnchangedWhenClosed(t *testing.T) {
	app := fynetest.NewApp()
	defer app.Quit()

	control := &fakeConversionControl{}
	c := &Controller{
		app:           app,
		window:        app.NewWindow("YAVE"),
		windowVisible: false,
		runControl:    control,
	}
	c.requestQuit()
	if c.quitPrompt == nil {
		t.Fatal("quit prompt was not created")
	}
	firstPrompt := c.quitPrompt
	c.requestQuit()
	if c.quitPrompt != firstPrompt {
		t.Fatal("a duplicate quit prompt was created")
	}
	button := findButton(c.quitPrompt.Content(), "Keep Running")
	if button == nil {
		t.Fatal("Keep Running button not found")
	}
	fynetest.Tap(button)

	if c.quitPrompt != nil {
		t.Fatal("quit prompt was not cleared after close")
	}
	if c.windowVisible {
		t.Fatal("main window visibility changed")
	}
	if control.terminations != 0 {
		t.Fatalf("terminations = %d, want 0", control.terminations)
	}
}

func TestConfirmQuitTerminatesActiveConversion(t *testing.T) {
	app := fynetest.NewApp()

	control := &fakeConversionControl{}
	c := &Controller{
		app:        app,
		window:     app.NewWindow("YAVE"),
		runControl: control,
	}
	c.requestQuit()
	button := findButton(c.quitPrompt.Content(), "Terminate and Quit")
	if button == nil {
		t.Fatal("Terminate and Quit button not found")
	}
	fynetest.Tap(button)

	if control.terminations != 1 {
		t.Fatalf("terminations = %d, want 1", control.terminations)
	}
	if c.quitPrompt != nil {
		t.Fatal("quit prompt was not cleared")
	}
}

func findButton(object fyne.CanvasObject, label string) *widget.Button {
	switch object := object.(type) {
	case *widget.Button:
		if object.Text == label {
			return object
		}
	case *fyne.Container:
		for _, child := range object.Objects {
			if button := findButton(child, label); button != nil {
				return button
			}
		}
	}
	return nil
}

func waitForUIUpdate(t *testing.T, updates <-chan func()) func() {
	t.Helper()
	select {
	case update := <-updates:
		return update
	case <-time.After(2 * time.Second):
		t.Fatal("UI update was not dispatched")
		return nil
	}
}
