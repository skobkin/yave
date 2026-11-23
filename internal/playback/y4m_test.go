package playback

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestReadHeader(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    Header
		wantErr string
	}{
		{name: "typical ffmpeg header", in: "YUV4MPEG2 W640 H360 F30000:1001 Ip A1:1 C420jpeg XYSCSS=420JPEG\n", want: Header{640, 360, 30000, 1001}},
		{name: "420mpeg2", in: "YUV4MPEG2 W2 H2 F25:1 C420mpeg2\n", want: Header{2, 2, 25, 1}},
		{name: "420paldv", in: "YUV4MPEG2 W2 H2 F25:1 C420paldv\n", want: Header{2, 2, 25, 1}},
		{name: "chroma tag absent means 420", in: "YUV4MPEG2 W4 H2 F1:1\n", want: Header{4, 2, 1, 1}},
		{name: "bad magic", in: "NOTY4M W2 H2\n", wantErr: "bad magic"},
		{name: "444 rejected", in: "YUV4MPEG2 W2 H2 F1:1 C444\n", wantErr: "unsupported chroma"},
		{name: "10-bit rejected", in: "YUV4MPEG2 W2 H2 F1:1 C420p10\n", wantErr: "unsupported chroma"},
		{name: "missing dimensions", in: "YUV4MPEG2 F1:1\n", wantErr: "invalid dimensions"},
		{name: "absurd dimensions", in: "YUV4MPEG2 W999999 H999999 F1:1\n", wantErr: "invalid dimensions"},
		{name: "bad rate", in: "YUV4MPEG2 W2 H2 F30\n", wantErr: "bad frame rate"},
		{name: "bad width", in: "YUV4MPEG2 Wxx H2 F1:1\n", wantErr: "parse"},
		{name: "empty stream", in: "", wantErr: "EOF"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ReadHeader(bufio.NewReader(strings.NewReader(tt.in)))
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("ReadHeader() error = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Errorf("ReadHeader() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestReadHeaderOverlongLine(t *testing.T) {
	_, err := ReadHeader(bufio.NewReader(strings.NewReader(strings.Repeat("A", maxHeaderBytes+10))))
	if err == nil || !strings.Contains(err.Error(), "too long") {
		t.Fatalf("expected a bounded-line error, got %v", err)
	}
}

func TestFrameDuration(t *testing.T) {
	if got := (Header{FPSNum: 25, FPSDen: 1}).FrameDuration(); got != 0.04 {
		t.Errorf("FrameDuration() = %v, want 0.04", got)
	}
	if got := (Header{}).FrameDuration(); got != 0 {
		t.Errorf("zero rate FrameDuration() = %v, want 0", got)
	}
}

func TestReadFrame(t *testing.T) {
	h := Header{Width: 3, Height: 3} // odd size: chroma planes are 2x2
	frame := func(fill byte) []byte {
		data := []byte("FRAME\n")
		data = append(data, bytes.Repeat([]byte{fill}, 9+4+4)...)
		return data
	}

	t.Run("two frames then clean EOF", func(t *testing.T) {
		r := bufio.NewReader(bytes.NewReader(append(frame(7), frame(9)...)))
		for _, want := range []byte{7, 9} {
			img, err := ReadFrame(r, h)
			if err != nil {
				t.Fatal(err)
			}
			if img.Y[0] != want || img.Cr[3] != want || len(img.Cb) != 4 {
				t.Errorf("frame fill %d decoded wrongly: Y0=%d CrLast=%d Cb=%d", want, img.Y[0], img.Cr[3], len(img.Cb))
			}
		}
		if _, err := ReadFrame(r, h); !errors.Is(err, io.EOF) {
			t.Errorf("expected clean EOF, got %v", err)
		}
	})
	t.Run("frame parameters after the marker are accepted", func(t *testing.T) {
		data := append([]byte("FRAME Ixyz\n"), bytes.Repeat([]byte{1}, 17)...)
		if _, err := ReadFrame(bufio.NewReader(bytes.NewReader(data)), h); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("truncated frame data", func(t *testing.T) {
		data := frame(1)[:12]
		_, err := ReadFrame(bufio.NewReader(bytes.NewReader(data)), h)
		if !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Errorf("expected ErrUnexpectedEOF, got %v", err)
		}
	})
	t.Run("missing frame marker", func(t *testing.T) {
		_, err := ReadFrame(bufio.NewReader(strings.NewReader("GARBAGE\n")), h)
		if err == nil || !strings.Contains(err.Error(), "bad marker") {
			t.Errorf("expected a bad-marker error, got %v", err)
		}
	})
}
