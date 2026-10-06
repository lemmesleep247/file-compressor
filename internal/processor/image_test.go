package processor

import (
	"bytes"
	"image"
	"image/jpeg"
	"testing"

	"file-compressor/internal/config"
)

// withEXIFOrientation inserts an APP1/EXIF segment carrying orientation o
// right after the JPEG SOI marker.
func withEXIFOrientation(t *testing.T, jpg []byte, o byte) []byte {
	t.Helper()
	exif := []byte("Exif\x00\x00")
	exif = append(exif,
		'I', 'I', 0x2A, 0x00, 0x08, 0x00, 0x00, 0x00, // TIFF header, IFD at 8
		0x01, 0x00, // one entry
		0x12, 0x01, 0x03, 0x00, 0x01, 0x00, 0x00, 0x00, o, 0x00, 0x00, 0x00, // tag 0x0112, SHORT, 1, value
		0x00, 0x00, 0x00, 0x00, // next IFD
	)
	n := len(exif) + 2
	seg := append([]byte{0xFF, 0xE1, byte(n >> 8), byte(n)}, exif...)

	out := append([]byte{}, jpg[:2]...)
	out = append(out, seg...)
	return append(out, jpg[2:]...)
}

func bounds(t *testing.T, data []byte) image.Point {
	t.Helper()
	img, err := jpeg.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	return image.Pt(img.Bounds().Dx(), img.Bounds().Dy())
}

func TestJPEGOrientationParsing(t *testing.T) {
	base := noisyJPEG(t, 20, 10, 90)
	if got := jpegOrientation(base); got != 1 {
		t.Errorf("no EXIF: %d, want 1", got)
	}
	for _, o := range []byte{1, 3, 6, 8} {
		if got := jpegOrientation(withEXIFOrientation(t, base, o)); got != int(o) {
			t.Errorf("orientation %d parsed as %d", o, got)
		}
	}
	if got := jpegOrientation(withEXIFOrientation(t, base, 9)); got != 1 {
		t.Errorf("invalid value 9 parsed as %d, want 1", got)
	}
	if got := jpegOrientation([]byte{1, 2, 3}); got != 1 {
		t.Errorf("garbage: %d", got)
	}
}

func TestCompressBakesInOrientation(t *testing.T) {
	setup(t)
	// 300x100 landscape pixels tagged "rotate 90° CW" must come out 100x300.
	src := withEXIFOrientation(t, noisyJPEG(t, 300, 100, 100), 6)

	out, err := compress("p.jpg", src, "")
	if err != nil || out == nil {
		t.Fatalf("out=%v err=%v", out, err)
	}
	if got := bounds(t, out.Data); got != image.Pt(100, 300) {
		t.Fatalf("output is %v, want 100x300", got)
	}
}

func TestOrientMovesPixels(t *testing.T) {
	// 2x1 image: A B. Rotated 90° CW it must be 1x2 with A on top.
	src := image.NewRGBA(image.Rect(0, 0, 2, 1))
	src.Pix = []byte{10, 0, 0, 255, 20, 0, 0, 255}

	got := orient(src, 6)
	if got.Rect.Dx() != 1 || got.Rect.Dy() != 2 {
		t.Fatalf("size %v", got.Rect)
	}
	if got.Pix[0] != 10 || got.Pix[got.Stride] != 20 {
		t.Fatalf("pixels %v", got.Pix)
	}

	// 180°: B A.
	got = orient(src, 3)
	if got.Pix[0] != 20 || got.Pix[4] != 10 {
		t.Fatalf("rot180 pixels %v", got.Pix)
	}
}

func TestMaxDimensionDownscalesKeepingAspect(t *testing.T) {
	setup(t)
	config.MaxImageDimension = 100
	src := noisyJPEG(t, 400, 200, 95)

	out, err := compress("big.jpg", src, "")
	if err != nil || out == nil {
		t.Fatalf("out=%v err=%v", out, err)
	}
	if got := bounds(t, out.Data); got != image.Pt(100, 50) {
		t.Fatalf("output is %v, want 100x50", got)
	}

	// Never enlarges: a small image keeps its size.
	small := noisyJPEG(t, 80, 40, 100)
	config.MinSizeKB = 0
	out, _ = compress("small.jpg", small, "")
	if out != nil {
		if got := bounds(t, out.Data); got != image.Pt(80, 40) {
			t.Fatalf("small image resized to %v", got)
		}
	}
}

func TestLowerQualityGivesSmallerOutput(t *testing.T) {
	setup(t)
	src := noisyJPEG(t, 300, 300, 100)

	config.ImageQuality = 85
	hi, _ := compress("a.jpg", src, "")
	config.ImageQuality = 40
	lo, _ := compress("a.jpg", src, "")

	if hi == nil || lo == nil || len(lo.Data) >= len(hi.Data) {
		t.Fatalf("quality 40 not smaller than 85: %v vs %v", lo, hi)
	}
}
