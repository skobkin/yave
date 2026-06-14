package ui

import (
	"fmt"
	"math"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"

	"git.skobk.in/skobkin/yave/internal/validation"
)

type integerSlider struct {
	slider   *widget.Slider
	minLabel *widget.Label
	value    *widget.Label
	maxLabel *widget.Label
	object   *fyne.Container
}

func newIntegerSlider(r validation.IntegerRange, initial int, changed func(string)) *integerSlider {
	control := &integerSlider{
		slider:   widget.NewSlider(float64(r.Min), float64(r.Max)),
		minLabel: widget.NewLabel(fmt.Sprint(r.Min)),
		value:    widget.NewLabel(""),
		maxLabel: widget.NewLabel(fmt.Sprint(r.Max)),
	}
	control.slider.Step = 1
	control.slider.SetValue(float64(r.Clamp(initial)))
	control.updateValueLabel()
	control.slider.OnChanged = func(float64) {
		control.updateValueLabel()
		changed("")
	}
	control.object = container.NewBorder(
		nil,
		nil,
		control.minLabel,
		control.maxLabel,
		container.NewBorder(nil, nil, nil, control.value, control.slider),
	)
	return control
}

func (s *integerSlider) Object() fyne.CanvasObject {
	return s.object
}

func (s *integerSlider) SetRange(r validation.IntegerRange) {
	s.slider.Min = float64(r.Min)
	s.slider.Max = float64(r.Max)
	s.minLabel.SetText(fmt.Sprint(r.Min))
	s.maxLabel.SetText(fmt.Sprint(r.Max))
	s.SetValue(r.Clamp(s.Value()))
	s.slider.Refresh()
}

func (s *integerSlider) SetValue(value int) {
	s.slider.SetValue(float64(value))
	s.updateValueLabel()
}

func (s *integerSlider) Value() int {
	return int(math.Round(s.slider.Value))
}

func (s *integerSlider) Enable() {
	s.slider.Enable()
}

func (s *integerSlider) Disable() {
	s.slider.Disable()
}

func (s *integerSlider) Hide() {
	s.object.Hide()
}

func (s *integerSlider) Show() {
	s.object.Show()
}

func (s *integerSlider) Visible() bool {
	return s.object.Visible()
}

func (s *integerSlider) updateValueLabel() {
	s.value.SetText(fmt.Sprint(s.Value()))
}
