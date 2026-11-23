package playback

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"image"
	"io"
	"strconv"
	"strings"
)

const (
	// maxHeaderBytes bounds the stream header and each FRAME line so a
	// corrupt stream cannot make the reader buffer without limit.
	maxHeaderBytes = 4 << 10
	// maxFrameSide and maxFramePixels bound the allocation a header can
	// request, mirroring the preview extractor's decode limits.
	maxFrameSide   = 4096
	maxFramePixels = maxFrameSide * maxFrameSide
)

// Header is the parsed YUV4MPEG2 stream header. Playback requests
// 4:2:0 only (-pix_fmt yuv420p), so the chroma layout is fixed.
type Header struct {
	Width, Height  int
	FPSNum, FPSDen int
}

// FrameDuration is the nominal presentation interval of one frame.
func (h Header) FrameDuration() float64 {
	if h.FPSNum <= 0 || h.FPSDen <= 0 {
		return 0
	}
	return float64(h.FPSDen) / float64(h.FPSNum)
}

// ReadHeader parses the "YUV4MPEG2 W.. H.. F..:.. C420.." line.
func ReadHeader(r *bufio.Reader) (Header, error) {
	line, err := readLine(r)
	if err != nil {
		return Header{}, fmt.Errorf("read y4m header: %w", err)
	}
	fields := strings.Fields(line)
	if len(fields) == 0 || fields[0] != "YUV4MPEG2" {
		return Header{}, fmt.Errorf("read y4m header: bad magic %q", truncate(line, 16))
	}
	var h Header
	chromaOK := true
	for _, f := range fields[1:] {
		if f == "" {
			continue
		}
		val := f[1:]
		switch f[0] {
		case 'W':
			h.Width, err = strconv.Atoi(val)
		case 'H':
			h.Height, err = strconv.Atoi(val)
		case 'F':
			num, den, ok := strings.Cut(val, ":")
			if !ok {
				err = fmt.Errorf("bad frame rate %q", val)
				break
			}
			if h.FPSNum, err = strconv.Atoi(num); err == nil {
				h.FPSDen, err = strconv.Atoi(den)
			}
		case 'C':
			// Only 4:2:0 variants (C420, C420jpeg, C420mpeg2,
			// C420paldv, C420p10 is rejected: 8-bit only).
			chromaOK = val == "420" || val == "420jpeg" || val == "420mpeg2" || val == "420paldv"
		}
		if err != nil {
			return Header{}, fmt.Errorf("read y4m header: parse %q: %w", f, err)
		}
	}
	if !chromaOK {
		return Header{}, errors.New("read y4m header: unsupported chroma format (need 8-bit 4:2:0)")
	}
	if h.Width <= 0 || h.Height <= 0 || h.Width > maxFrameSide || h.Height > maxFrameSide || h.Width*h.Height > maxFramePixels {
		return Header{}, fmt.Errorf("read y4m header: invalid dimensions %dx%d", h.Width, h.Height)
	}
	return h, nil
}

// ReadFrame reads the next FRAME into a freshly allocated YCbCr image.
// It returns io.EOF at a clean end of stream and io.ErrUnexpectedEOF
// (wrapped) when the stream ends inside a frame.
func ReadFrame(r *bufio.Reader, h Header) (*image.YCbCr, error) {
	line, err := readLine(r)
	if err != nil {
		if errors.Is(err, io.EOF) && line == "" {
			return nil, io.EOF
		}
		return nil, fmt.Errorf("read y4m frame header: %w", err)
	}
	if !strings.HasPrefix(line, "FRAME") {
		return nil, fmt.Errorf("read y4m frame header: bad marker %q", truncate(line, 16))
	}
	img := image.NewYCbCr(image.Rect(0, 0, h.Width, h.Height), image.YCbCrSubsampleRatio420)
	for _, plane := range [][]byte{img.Y, img.Cb, img.Cr} {
		if _, err := io.ReadFull(r, plane); err != nil {
			if errors.Is(err, io.EOF) {
				err = io.ErrUnexpectedEOF
			}
			return nil, fmt.Errorf("read y4m frame data: %w", err)
		}
	}
	return img, nil
}

// readLine reads up to the next newline (exclusive), bounded by
// maxHeaderBytes. On EOF it returns whatever was read along with the
// error so callers can tell a clean end from a truncated line.
func readLine(r *bufio.Reader) (string, error) {
	var buf bytes.Buffer
	for buf.Len() <= maxHeaderBytes {
		b, err := r.ReadByte()
		if err != nil {
			return buf.String(), err
		}
		if b == '\n' {
			return buf.String(), nil
		}
		buf.WriteByte(b)
	}
	return "", errors.New("line too long")
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}
