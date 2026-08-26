package server

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"testing"
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

// TestProcessPhotoOversizedRejected verifies that an image with dimensions exceeding
// maxPhotoPixels is rejected.
func TestProcessPhotoOversizedRejected(t *testing.T) {
	// Build a valid PNG header with width 7000 and height 7000 (49 MP > 40 MP).
	// PNG signature (8) + IHDR length (4) + IHDR tag (4) + width (4) + height (4) + 5 bytes + CRC (4)
	header := []byte{
		0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n',
		0x00, 0x00, 0x00, 0x0d,
		'I', 'H', 'D', 'R',
		0x00, 0x00, 0x1b, 0x58, // Width: 7000
		0x00, 0x00, 0x1b, 0x58, // Height: 7000
		0x08, 0x02, 0x00, 0x00, 0x00,
		0x2c, 0x72, 0xc1, 0xee, // CRC
	}
	if _, err := processPhoto(header); err == nil {
		t.Errorf("expected error for oversized image input, got nil")
	}
}
