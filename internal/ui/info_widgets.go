package ui

import (
	"image/color"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

// Building blocks of the Input tab's information panels. Colors are read
// from the theme on every Refresh (not captured at construction) so the
// panels follow light/dark switches.

// themedRect is a rounded rectangle filled with a theme color: the
// background of a panel or of the poster frame.
type themedRect struct {
	widget.BaseWidget
	colorName fyne.ThemeColorName
}

func newThemedRect(name fyne.ThemeColorName) *themedRect {
	r := &themedRect{colorName: name}
	r.ExtendBaseWidget(r)
	return r
}

func (r *themedRect) CreateRenderer() fyne.WidgetRenderer {
	rect := canvas.NewRectangle(color.Transparent)
	rr := &themedRectRenderer{owner: r, rect: rect}
	rr.Refresh()
	return rr
}

type themedRectRenderer struct {
	owner *themedRect
	rect  *canvas.Rectangle
}

func (r *themedRectRenderer) Layout(size fyne.Size)        { r.rect.Resize(size) }
func (r *themedRectRenderer) MinSize() fyne.Size           { return fyne.Size{} }
func (r *themedRectRenderer) Objects() []fyne.CanvasObject { return []fyne.CanvasObject{r.rect} }
func (r *themedRectRenderer) Destroy()                     {}

func (r *themedRectRenderer) Refresh() {
	r.rect.FillColor = theme.ColorForWidget(r.owner.colorName, r.owner)
	r.rect.CornerRadius = theme.SizeForWidget(theme.SizeNameInputRadius, r.owner)
	r.rect.Refresh()
}

// newSurface frames content on a rounded, slightly raised background.
func newSurface(content fyne.CanvasObject) fyne.CanvasObject {
	return container.NewStack(newThemedRect(theme.ColorNameInputBackground), container.NewPadded(content))
}

// badge is a small rounded pill ("HDR (PQ)", "VAAPI") tinted by its
// importance: High uses the primary color, Success/Warning/Danger their
// status colors, anything else a neutral tint.
type badge struct {
	widget.BaseWidget
	text       string
	importance widget.Importance
	// tips/tip, when set, show tip in a hover tooltip.
	tips *tooltipLayer
	tip  string
}

func newBadge(text string, importance widget.Importance) *badge {
	b := &badge{text: text, importance: importance}
	b.ExtendBaseWidget(b)
	return b
}

// newTipBadge is a badge with a hover tooltip.
func newTipBadge(tips *tooltipLayer, text, tip string, importance widget.Importance) *badge {
	b := newBadge(text, importance)
	b.tips, b.tip = tips, tip
	return b
}

var _ desktop.Hoverable = (*badge)(nil)

// MouseIn starts the tooltip delay.
func (b *badge) MouseIn(*desktop.MouseEvent) {
	if b.tips != nil && b.tip != "" {
		b.tips.schedule(b, func() string { return b.tip }, tooltipDelay)
	}
}

// MouseMoved is required by desktop.Hoverable.
func (b *badge) MouseMoved(*desktop.MouseEvent) {}

// MouseOut hides the tooltip.
func (b *badge) MouseOut() {
	if b.tips != nil {
		b.tips.cancel()
	}
}

func (b *badge) CreateRenderer() fyne.WidgetRenderer {
	r := &badgeRenderer{owner: b, rect: canvas.NewRectangle(color.Transparent), text: canvas.NewText(b.text, color.Black)}
	r.Refresh()
	return r
}

type badgeRenderer struct {
	owner *badge
	rect  *canvas.Rectangle
	text  *canvas.Text
}

const (
	badgePadX = 7
	badgePadY = 2
)

func (r *badgeRenderer) Layout(size fyne.Size) {
	r.rect.Resize(size)
	textSize := r.text.MinSize()
	r.text.Move(fyne.NewPos((size.Width-textSize.Width)/2, (size.Height-textSize.Height)/2))
	r.text.Resize(textSize)
}

func (r *badgeRenderer) MinSize() fyne.Size {
	return r.text.MinSize().Add(fyne.NewSize(2*badgePadX, 2*badgePadY))
}

func (r *badgeRenderer) Objects() []fyne.CanvasObject { return []fyne.CanvasObject{r.rect, r.text} }
func (r *badgeRenderer) Destroy()                     {}

func (r *badgeRenderer) Refresh() {
	tint := theme.ColorNameForeground
	alpha := uint8(0x24)
	switch r.owner.importance {
	case widget.HighImportance:
		tint, alpha = theme.ColorNamePrimary, 0x40
	case widget.SuccessImportance:
		tint, alpha = theme.ColorNameSuccess, 0x40
	case widget.WarningImportance:
		tint, alpha = theme.ColorNameWarning, 0x40
	case widget.DangerImportance:
		tint, alpha = theme.ColorNameError, 0x40
	}
	fill, _ := color.NRGBAModel.Convert(theme.ColorForWidget(tint, r.owner)).(color.NRGBA)
	fill.A = alpha
	r.rect.FillColor = fill
	r.text.Text = r.owner.text
	r.text.Color = theme.ColorForWidget(theme.ColorNameForeground, r.owner)
	r.text.TextSize = theme.SizeForWidget(theme.SizeNameCaptionText, r.owner)
	r.text.TextStyle = fyne.TextStyle{Bold: true}
	r.rect.CornerRadius = r.MinSize().Height / 2
	r.rect.Refresh()
	r.text.Refresh()
}

// badgeRow lays badges out left to right; nil when there are none.
func badgeRow(badges ...*badge) fyne.CanvasObject {
	if len(badges) == 0 {
		return nil
	}
	objects := make([]fyne.CanvasObject, len(badges))
	for i, b := range badges {
		// Centered at its own height: a row stretches its children.
		objects[i] = container.NewCenter(b)
	}
	return container.NewHBox(objects...)
}

// compactTheme tightens the paddings of the text inside the information
// panels: every Label/RichText otherwise carries inner padding that makes
// a table of facts twice as tall as the same lines of plain text. Every
// other lookup goes to the app theme at call time, so a theme switch is
// still honored.
type compactTheme struct{}

func (compactTheme) base() fyne.Theme { return fyne.CurrentApp().Settings().Theme() }

func (t compactTheme) Color(n fyne.ThemeColorName, v fyne.ThemeVariant) color.Color {
	return t.base().Color(n, v)
}
func (t compactTheme) Font(s fyne.TextStyle) fyne.Resource     { return t.base().Font(s) }
func (t compactTheme) Icon(n fyne.ThemeIconName) fyne.Resource { return t.base().Icon(n) }

func (t compactTheme) Size(n fyne.ThemeSizeName) float32 {
	switch n {
	case theme.SizeNameInnerPadding:
		return 2
	case theme.SizeNamePadding:
		return 2
	}
	return t.base().Size(n)
}

func compact(content fyne.CanvasObject) fyne.CanvasObject {
	return container.NewThemeOverride(content, compactTheme{})
}

// tableLayout arranges objects row-major into cols aligned columns. Every
// column but the last is as wide as its widest visible cell; the last
// column takes the remaining width (its cells may wrap). A row is as tall
// as its tallest cell; cells align to its first line (see Layout).
type tableLayout struct{ cols int }

const tableColumnGap = 10

func (t tableLayout) columnWidths(objects []fyne.CanvasObject) []float32 {
	widths := make([]float32, t.cols)
	for i, o := range objects {
		col := i % t.cols
		if col == t.cols-1 || !o.Visible() {
			continue
		}
		widths[col] = max(widths[col], o.MinSize().Width)
	}
	return widths
}

// fixedWidth is the total width of every column but the last, gaps
// included; columns with no visible content take no gap.
func (t tableLayout) fixedWidth(widths []float32) float32 {
	var total float32
	for _, w := range widths[:t.cols-1] {
		if w > 0 {
			total += w + tableColumnGap
		}
	}
	return total
}

func (t tableLayout) Layout(objects []fyne.CanvasObject, size fyne.Size) {
	widths := t.columnWidths(objects)
	lastX := t.fixedWidth(widths)
	lastW := max(size.Width-lastX, 0)
	var y float32
	for start := 0; start < len(objects); start += t.cols {
		row := objects[start:min(start+t.cols, len(objects))]
		// Size the last cell first: a wrapping label's height depends
		// on its width.
		if len(row) == t.cols {
			last := row[t.cols-1]
			last.Resize(fyne.NewSize(lastW, last.MinSize().Height))
		}
		// lineH is the height of one text line in this row: the tallest
		// fixed-column cell (those never wrap). Cells shorter than a line
		// (icons) center on the first line; the rest top-align, so a
		// label sits beside the first line of wrapped details.
		var height, lineH float32
		for col, o := range row {
			if !o.Visible() {
				continue
			}
			height = max(height, o.MinSize().Height)
			if col < t.cols-1 {
				lineH = max(lineH, o.MinSize().Height)
			}
		}
		var x float32
		for col, o := range row {
			w := widths[col]
			if col == t.cols-1 {
				x, w = lastX, lastW
			}
			h := o.MinSize().Height
			o.Move(fyne.NewPos(x, y+max(lineH-h, 0)/2))
			o.Resize(fyne.NewSize(w, h))
			if widths[col] > 0 {
				x += widths[col] + tableColumnGap
			}
		}
		y += height
	}
}

func (t tableLayout) MinSize(objects []fyne.CanvasObject) fyne.Size {
	widths := t.columnWidths(objects)
	var height, lastW float32
	for start := 0; start < len(objects); start += t.cols {
		var rowH float32
		for i, o := range objects[start:min(start+t.cols, len(objects))] {
			if !o.Visible() {
				continue
			}
			rowH = max(rowH, o.MinSize().Height)
			if i == t.cols-1 {
				lastW = max(lastW, min(o.MinSize().Width, 120))
			}
		}
		height += rowH
	}
	return fyne.NewSize(t.fixedWidth(widths)+lastW, height)
}

// mutedText is a single-run RichText in the placeholder color: labels
// and secondary details.
func mutedText(text string, wrap bool) *widget.RichText {
	rt := widget.NewRichText(&widget.TextSegment{
		Text:  text,
		Style: widget.RichTextStyle{ColorName: theme.ColorNamePlaceHolder},
	})
	if wrap {
		rt.Wrapping = fyne.TextWrapWord
	}
	return rt
}

// plainText is a Label, word-wrapped when wrap is set.
func plainText(text string, wrap bool) *widget.Label {
	l := widget.NewLabel(text)
	if wrap {
		l.Wrapping = fyne.TextWrapWord
	}
	return l
}
