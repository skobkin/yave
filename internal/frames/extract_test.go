package frames

import (
	"context"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// writeJPEG encodes a small valid JPEG for fake ffmpeg executables to
// emit, exercising the real decode path.
func writeJPEG(t *testing.T, dir, name string, width, height int) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x), G: uint8(y), B: 128, A: 255})
		}
	}
	path := filepath.Join(dir, name)
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err := jpeg.Encode(file, img, nil); err != nil {
		t.Fatal(err)
	}
	return path
}

// writeFakeFFmpeg installs a shell script recording its arguments and
// running `body`. POSIX-only, like the discovery test helpers.
func writeFakeFFmpeg(t *testing.T, dir, name, body string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("test helper is a POSIX shell script")
	}
	argsFile := filepath.Join(dir, name+".args")
	script := "#!/bin/sh\n" +
		`printf '%s\n' "$@" >> ` + strconv.Quote(argsFile) + "\n" +
		body + "\n"
	return writeExecutable(t, dir, name, script)
}

func writeExecutable(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil { //nolint:gosec // G306: test helper script must be executable
		t.Fatal(err)
	}
	return path
}

// readArgs returns the whitespace-separated arguments the fake recorded
// across its invocations.
func readArgs(t *testing.T, argsFile string) []string {
	t.Helper()
	data, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Fields(string(data))
}

func TestThumbnailExtractsAndDecodes(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	jpegPath := writeJPEG(t, dir, "frame.jpg", 64, 32)
	fake := writeFakeFFmpeg(t, dir, "ffmpeg", `cat `+strconv.Quote(jpegPath))

	extractor := NewExtractor(fake)
	frame, err := extractor.Thumbnail(context.Background(), "/tmp/input.mkv", 5*time.Second, 96)
	if err != nil {
		t.Fatal(err)
	}
	if frame.Image == nil || frame.Image.Bounds().Dx() != 64 || frame.Image.Bounds().Dy() != 32 {
		t.Fatalf("unexpected decoded image: %v", frame.Image)
	}
	if frame.Index != -1 {
		t.Errorf("standalone extraction must use Index -1, got %d", frame.Index)
	}

	args := readArgs(t, filepath.Join(dir, "ffmpeg.args"))
	for _, want := range []string{
		"-ss", "5.000",
		"-map", "0:v:0",
		"-frames:v", "1",
		"-vf", "scale=-2:96",
		"pipe:1",
	} {
		if !slicesContains(args, want) {
			t.Errorf("args missing %q: %v", want, args)
		}
	}
	if args[len(args)-1] != "pipe:1" {
		t.Errorf("output must be piped to stdout, tail is %v", args[len(args)-4:])
	}
}

func TestPreviewPreservesAspectAndCapsWidth(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	jpegPath := writeJPEG(t, dir, "frame.jpg", 48, 16)
	fake := writeFakeFFmpeg(t, dir, "ffmpeg", `cat `+strconv.Quote(jpegPath))

	frame, err := NewExtractor(fake).Preview(context.Background(), "/tmp/input.mkv", 0, 320)
	if err != nil {
		t.Fatal(err)
	}
	if frame.Image == nil || frame.Image.Bounds().Dx() != 48 || frame.Image.Bounds().Dy() != 16 {
		t.Fatalf("unexpected decoded image: %v", frame.Image)
	}
	args := readArgs(t, filepath.Join(dir, "ffmpeg.args"))
	if !slicesContains(args, "scale=min(320,iw):-2") {
		t.Errorf("args missing width-capped aspect-preserving scale: %v", args)
	}
}

func TestExtractionGuards(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	fake := writeFakeFFmpeg(t, dir, "ffmpeg", `exit 0`)
	if _, err := NewExtractor(fake).Thumbnail(context.Background(), "/tmp/in.mkv", 0, 0); err == nil {
		t.Error("zero height must be rejected without running ffmpeg")
	}
	if _, err := NewExtractor(fake).Preview(context.Background(), "/tmp/in.mkv", 0, -5); err == nil {
		t.Error("negative width must be rejected without running ffmpeg")
	}
	if _, err := NewExtractor("").Thumbnail(context.Background(), "/tmp/in.mkv", 0, 96); err == nil {
		t.Error("empty executable must be rejected")
	}
	if _, err := NewExtractor(fake).Thumbnail(context.Background(), "   ", 0, 96); err == nil {
		t.Error("empty input path must be rejected")
	}
}

func TestExtractionFFmpegFailure(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	fake := writeFakeFFmpeg(t, dir, "ffmpeg", `echo 'some ffmpeg error' >&2
exit 1`)
	_, err := NewExtractor(fake).Thumbnail(context.Background(), "/tmp/in.mkv", time.Second, 96)
	if err == nil {
		t.Fatal("expected an error from a failing ffmpeg")
	}
	if !strings.Contains(err.Error(), "some ffmpeg error") {
		t.Errorf("error must include the stderr tail: %v", err)
	}
}

func TestExtractionNonJPEGOutput(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	fake := writeFakeFFmpeg(t, dir, "ffmpeg", `printf 'definitely not a jpeg'`)
	if _, err := NewExtractor(fake).Preview(context.Background(), "/tmp/in.mkv", 0, 320); err == nil {
		t.Fatal("expected a decode error for non-JPEG output")
	}
}

func TestExtractionOversizedDimensions(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// A JPEG header claiming huge dimensions: DecodeConfig must reject
	// it before any large allocation. Crafting one by truncating a
	// valid JPEG after its SOF marker keeps the test fast.
	jpegPath := writeJPEG(t, dir, "big.jpg", 32, 32)
	fake := writeFakeFFmpeg(t, dir, "ffmpeg", `head -c 400 `+strconv.Quote(jpegPath)+` 2>/dev/null || cat `+strconv.Quote(jpegPath))
	if _, err := NewExtractor(fake).Thumbnail(context.Background(), "/tmp/in.mkv", 0, 96); err == nil {
		t.Fatal("expected an error for truncated/invalid image data")
	}
}

func TestExtractionCancellation(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("test helper is a POSIX shell script")
	}
	dir := t.TempDir()
	// Flood both pipes, then idle: cancellation must kill the process
	// so both drains reach EOF and Wait returns — no deadlock on full
	// pipes.
	fake := writeFakeFFmpeg(t, dir, "ffmpeg", `head -c 4000000 /dev/zero
head -c 4000000 /dev/zero 1>&2
sleep 5 >/dev/null 2>&1`)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	_, err := NewExtractor(fake).Thumbnail(ctx, "/tmp/in.mkv", 0, 96)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected a cancellation error")
	}
	if elapsed > 3*time.Second {
		t.Fatalf("cancellation took %s — pipes likely deadlocked", elapsed)
	}
}

func TestExtractionStdoutOverflow(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("test helper is a POSIX shell script")
	}
	dir := t.TempDir()
	// Emit more than maxImageBytes (8 MiB), then idle: the extraction
	// must stop buffering at the limit, cancel the process, drain, and
	// return the bounded oversize error promptly instead of waiting on
	// a blocked writer.
	fake := writeFakeFFmpeg(t, dir, "ffmpeg", `head -c 9000000 /dev/zero
sleep 5 >/dev/null 2>&1`)
	start := time.Now()
	_, err := NewExtractor(fake).Thumbnail(context.Background(), "/tmp/in.mkv", 0, 96)
	elapsed := time.Since(start)
	if err == nil || !strings.Contains(err.Error(), "image limit") {
		t.Fatalf("expected the oversize error, got %v", err)
	}
	if elapsed > 3*time.Second {
		t.Fatalf("overflow handling took %s — process was not cancelled", elapsed)
	}
}

func TestLimitedTail(t *testing.T) {
	t.Parallel()
	tail := &limitedTail{limit: 8}
	tail.Write([]byte("1234567890abcdefghij"))
	if got := tail.String(); got != "…cdefghij" {
		t.Errorf("tail = %q, want %q", got, "…cdefghij")
	}
	small := &limitedTail{limit: 100}
	small.Write([]byte("short"))
	if got := small.String(); got != "short" {
		t.Errorf("tail = %q, want %q", got, "short")
	}
}

func slicesContains(args []string, want string) bool {
	for _, arg := range args {
		if arg == want {
			return true
		}
	}
	return false
}
