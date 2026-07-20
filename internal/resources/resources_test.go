package resources

import (
	"encoding/xml"
	"image/color"
	"io"
	"math"
	"strings"
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	fynetest "fyne.io/fyne/v2/test"
)

func TestTrayProgressIconBucketsAndBounds(t *testing.T) {
	tests := []struct {
		name      string
		progress  float64
		wantName  string
		wantRects []string
		wantFill  bool
	}{
		{
			name:     "negative clamps to zero",
			progress: -0.5,
			wantName: "app_icon_tray_progress_000.svg",
			wantFill: false,
		},
		{
			name:     "nan clamps to zero",
			progress: math.NaN(),
			wantName: "app_icon_tray_progress_000.svg",
			wantFill: false,
		},
		{
			name:     "value floors to two percent bucket",
			progress: 0.519,
			wantName: "app_icon_tray_progress_050.svg",
			wantFill: true,
			wantRects: []string{
				`x="144" y="200.0" width="32" height="16.0"`,
				`x="176" y="328.0" width="64" height="32.0"`,
			},
		},
		{
			name:     "exact next bucket",
			progress: 0.52,
			wantName: "app_icon_tray_progress_052.svg",
			wantFill: true,
			wantRects: []string{
				`x="144" y="193.6" width="32" height="22.4"`,
				`x="176" y="328.0" width="64" height="32.0"`,
			},
		},
		{
			name:     "value above one clamps to full",
			progress: 1.5,
			wantName: "app_icon_tray_progress_100.svg",
			wantFill: true,
			wantRects: []string{
				`x="48" y="40.0" width="32" height="32.0"`,
				`x="176" y="328.0" width="64" height="32.0"`,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			icon := TrayProgressIcon(tt.progress)
			if icon.Name() != tt.wantName {
				t.Fatalf("icon name = %q, want %q", icon.Name(), tt.wantName)
			}
			content := string(icon.Content())
			for _, wantRect := range tt.wantRects {
				if !strings.Contains(content, wantRect) {
					t.Fatalf("icon does not contain progress rectangle %q", wantRect)
				}
			}
			if got := strings.Contains(content, `fill="#22C55E"`); got != tt.wantFill {
				t.Fatalf("icon has emerald progress fill = %v, want %v", got, tt.wantFill)
			}
			if strings.Contains(content, trayProgressMarker) {
				t.Fatal("progress marker was not replaced")
			}
		})
	}
}

func TestTrayProgressIconsAreValidSVG(t *testing.T) {
	for _, progress := range []float64{0, 0.5, 1} {
		decoder := xml.NewDecoder(strings.NewReader(string(TrayProgressIcon(progress).Content())))
		for {
			_, err := decoder.Token()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatalf("progress %.1f is invalid XML: %v", progress, err)
			}
		}
	}
}

func TestBaseIconHasProgressMarkerButNoVisibleFill(t *testing.T) {
	content := string(AppIcon().Content())
	if !strings.Contains(content, trayProgressMarker) {
		t.Fatal("base icon does not contain the progress insertion marker")
	}
	if strings.Contains(content, `fill="#22C55E"`) {
		t.Fatal("base icon unexpectedly contains a visible progress fill")
	}
}

func TestTrayProgressIconRendersFillOnlyInsideY(t *testing.T) {
	app := fynetest.NewTempApp(t)
	window := app.NewWindow("tray progress render")
	window.SetPadded(false)
	window.Resize(fyne.NewSquareSize(416))
	icon := canvas.NewImageFromResource(TrayProgressIcon(0.5))
	window.SetContent(icon)

	rendered := window.Canvas().Capture()
	wantGreen := color.NRGBA{R: 0x22, G: 0xC5, B: 0x5E, A: 0xFF}
	pixelAt := func(x, y int) color.Color {
		bounds := rendered.Bounds()
		scaledX := bounds.Min.X + x*bounds.Dx()/416
		scaledY := bounds.Min.Y + y*bounds.Dy()/416
		return color.NRGBAModel.Convert(rendered.At(scaledX, scaledY))
	}
	if got := pixelAt(200, 240); got != wantGreen {
		t.Fatalf("filled stem pixel = %v, want %v", got, wantGreen)
	}
	if got := pixelAt(200, 190); got == wantGreen {
		t.Fatal("Y pixel above the 50% boundary was filled")
	}
	if got := pixelAt(200, 270); got == wantGreen {
		t.Fatal("gap between Y rows was filled")
	}
	if got := pixelAt(20, 240); got == wantGreen {
		t.Fatal("pixel outside the Y silhouette was filled")
	}
}
