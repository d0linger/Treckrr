package server

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"strings"
	"testing"
	"time"
)

// TestProcessPhotoReencodesToJPEG proves a PNG upload is re-encoded to JPEG
// (which drops any EXIF), and that a non-image is rejected.
func TestProcessPhotoReencodesToJPEG(t *testing.T) {
	// A small PNG.
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	for x := 0; x < 8; x++ {
		for y := 0; y < 8; y++ {
			img.Set(x, y, color.RGBA{R: uint8(x * 30), G: 100, B: 200, A: 255})
		}
	}
	var pngBuf bytes.Buffer
	if err := png.Encode(&pngBuf, img); err != nil {
		t.Fatalf("png encode: %v", err)
	}

	out, err := processPhoto(pngBuf.Bytes())
	if err != nil {
		t.Fatalf("processPhoto: %v", err)
	}
	// The output must decode as JPEG.
	if _, err := jpeg.Decode(bytes.NewReader(out)); err != nil {
		t.Errorf("output is not valid JPEG: %v", err)
	}
	if _, format, err := image.DecodeConfig(bytes.NewReader(out)); err != nil || format != "jpeg" {
		t.Errorf("output format = %q (err %v), want jpeg", format, err)
	}

	// A non-image is rejected.
	if _, err := processPhoto([]byte("not an image at all")); err == nil {
		t.Errorf("expected error for non-image input")
	}
}

// TestProcessPhotoOversizedRejected verifies that an image whose DECLARED
// dimensions exceed maxPhotoPixels is refused before it is decoded — the point
// being that a 12 MiB upload must not be allowed to expand into hundreds of MB
// of pixels first.
//
// The checksum below is computed, not invented. With a wrong one the PNG decoder
// rejects the header for a bad CRC and processPhoto returns that error instead,
// so the test would pass without the size check ever running — it would stay
// green even if the guard were deleted outright. Hence both the real CRC and the
// assertion on the specific error rather than on "some error happened".
func TestProcessPhotoOversizedRejected(t *testing.T) {
	// PNG signature (8) + IHDR length (4) + "IHDR" (4) + width (4) + height (4)
	// + bit depth, color type, compression, filter, interlace (5) + CRC32 (4).
	// 7000 x 7000 = 49 MP, above the 40 MP ceiling.
	header := []byte{
		0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n',
		0x00, 0x00, 0x00, 0x0d,
		'I', 'H', 'D', 'R',
		0x00, 0x00, 0x1b, 0x58, // width  7000
		0x00, 0x00, 0x1b, 0x58, // height 7000
		0x08, 0x02, 0x00, 0x00, 0x00,
		0x16, 0xf9, 0x2c, 0xd8, // crc32("IHDR" + the 13 bytes above)
	}
	// Guard the guard: if this ever stops decoding, the test below would pass for
	// the wrong reason again.
	cfg, _, err := image.DecodeConfig(bytes.NewReader(header))
	if err != nil {
		t.Fatalf("header no longer decodes (%v) — fix the CRC, or this test proves nothing", err)
	}
	if cfg.Width != 7000 || cfg.Height != 7000 {
		t.Fatalf("header declares %dx%d, want 7000x7000", cfg.Width, cfg.Height)
	}

	_, err = processPhoto(header)
	if !errors.Is(err, errPhotoTooLarge) {
		t.Errorf("processPhoto = %v, want errPhotoTooLarge", err)
	}
}

// TestParseGermanDecimalRejectsOverlongInput pins the cost guard in
// parseGermanDecimal. big.Int's base-10 parse is superlinear — a megabyte of
// digits, which limitBody permits in one field, takes seconds of CPU.
func TestParseGermanDecimalRejectsOverlongInput(t *testing.T) {
	if got := parseGermanDecimal(strings.Repeat("9", maxDecimalLen+1)); !got.IsZero() {
		t.Errorf("over-long input parsed to %s, want 0", got)
	}
	// A number a person could actually type still parses.
	if got := parseGermanDecimal("1234,56"); got.String() != "1234.56" {
		t.Errorf("ordinary input = %s, want 1234.56", got)
	}
	start := time.Now()
	_ = parseGermanDecimal(strings.Repeat("9", 1_000_000))
	if d := time.Since(start); d > 50*time.Millisecond {
		t.Errorf("a megabyte of digits took %v — the guard is not in front of the parse", d)
	}
}
