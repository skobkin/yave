package ui

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

// statusBarLayout arranges the bottom bar on one line:
//
//	idle:    [status ……………………………………] [why Start is blocked] [Start] [Cancel]
//	running: [phase] [██████ 12% ░░░░░░] [0:01 / ~0:05 · 2.9× · 31 MB] [Cancel]
//
// The status and hint labels truncate with an ellipsis so a long error
// cannot widen the window, but a truncating label's MinSize is a single
// "…": in a box layout they collapsed to exactly that. This layout gives
// each label the width of its text instead, capped so the other parts keep
// their room, and lets the status take whatever is left while idle.
type statusBarLayout struct {
	status  *widget.Label     // run phase, or the last result while idle
	bar     fyne.CanvasObject // visible only while a run goes
	info    fyne.CanvasObject // fixed-width figures cell beside the bar
	hint    *widget.Label     // why Start is blocked; hidden while running
	actions fyne.CanvasObject // Start / Cancel
}

func (l *statusBarLayout) objects() []fyne.CanvasObject {
	return []fyne.CanvasObject{l.status, l.bar, l.info, l.hint, l.actions}
}

// textWidth is the width a label needs to show its whole text.
func textWidth(label *widget.Label) float32 {
	if label.Text == "" || !label.Visible() {
		return 0
	}
	// +1 absorbs rounding so a fitting text never ellipsizes.
	return fyne.MeasureText(label.Text, theme.TextSize(), label.TextStyle).Width + 2*theme.InnerPadding() + 1
}

func (l *statusBarLayout) Layout(_ []fyne.CanvasObject, size fyne.Size) {
	gap := theme.Padding()
	place := func(o fyne.CanvasObject, x, width float32) {
		o.Move(fyne.NewPos(x, 0))
		o.Resize(fyne.NewSize(max(width, 0), size.Height))
	}
	actionsW := l.actions.MinSize().Width
	place(l.actions, size.Width-actionsW, actionsW)
	right := size.Width - actionsW - gap // free space ends here

	if l.bar.Visible() {
		infoW := l.info.MinSize().Width
		place(l.info, right-infoW, infoW)
		right -= infoW + gap
		statusW := min(textWidth(l.status), right/3)
		place(l.status, 0, statusW)
		place(l.bar, statusW+gap, right-statusW-gap)
		return
	}
	if hintW := min(textWidth(l.hint), right/2); hintW > 0 {
		place(l.hint, right-hintW, hintW)
		right -= hintW + gap
	}
	place(l.status, 0, right)
}

func (l *statusBarLayout) MinSize(_ []fyne.CanvasObject) fyne.Size {
	height := float32(0)
	for _, o := range l.objects() {
		height = max(height, o.MinSize().Height)
	}
	// Room for the figures, the actions and a usable bar.
	width := l.info.MinSize().Width + l.actions.MinSize().Width + 120
	return fyne.NewSize(width, height)
}
