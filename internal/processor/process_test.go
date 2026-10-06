package processor

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"math/rand"
	"testing"

	"file-compressor/internal/config"
)

func setup(t *testing.T) {
	t.Helper()
	config.MinSizeKB = 1
	config.MaxImageMegapixels = 100
	config.EnableGenericCompression = true
	config.GenericCompression = "zstd"
	config.ZstdLevel = 3
	config.EnableWebP = false
	config.ImageQuality = 75
	config.MaxImageDimension = 0
	config.BucketRules = nil
}

func compress(object string, data []byte, hash string) (*Output, error) {
	return Compress(object, data, hash, config.SettingsFor(""))
}

func noisyJPEG(t *testing.T, w, h, quality int) []byte {
	t.Helper()
	rng := rand.New(rand.NewSource(1))
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{uint8(rng.Intn(256)), uint8(x), uint8(y), 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: quality}); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// The regression this guards: the stored hash must be of our *output*, so
// when our own re-upload triggers a second job, that job recognises the
// object as already processed instead of re-encoding it again.
func TestCompressIsIdempotentOnOwnOutput(t *testing.T) {
	setup(t)
	src := noisyJPEG(t, 300, 300, 100)

	first, err := compress("a.jpg", src, "")
	if err != nil || first == nil {
		t.Fatalf("first pass: out=%v err=%v", first, err)
	}
	if !first.InPlace("a.jpg") || len(first.Data) >= len(src) {
		t.Fatalf("expected smaller in-place output, got %d >= %d", len(first.Data), len(src))
	}

	// Second job sees the compressed bytes plus the metadata we wrote.
	second, err := compress("a.jpg", first.Data, first.Hash)
	if err != nil {
		t.Fatal(err)
	}
	if second != nil {
		t.Fatal("re-processed our own output")
	}
}

func TestCompressSkipsUnchangedNotWorthIt(t *testing.T) {
	setup(t)
	config.MinSizeKB = 100
	if out, _ := compress("a.jpg", noisyJPEG(t, 50, 50, 90), ""); out != nil {
		t.Fatal("compressed a file below MIN_IMAGE_SIZE_KB")
	}
}

func TestGenericCompressionWritesSiblingNotInPlace(t *testing.T) {
	setup(t)
	data := bytes.Repeat([]byte("hello compressible world\n"), 2000)

	out, err := compress("logs/report.txt", data, "")
	if err != nil || out == nil {
		t.Fatalf("out=%v err=%v", out, err)
	}
	if out.Key != "logs/report.txt.zst" || out.InPlace("logs/report.txt") {
		t.Fatalf("zstd output must not replace the original, key=%q", out.Key)
	}
	if out.ContentType != "application/zstd" {
		t.Fatalf("content type = %q", out.ContentType)
	}
}

func TestSkipsOwnSiblingAndPrecompressedTypes(t *testing.T) {
	setup(t)
	data := bytes.Repeat([]byte("hello compressible world\n"), 2000)

	if out, _ := compress("report.txt.zst", data, ""); out != nil {
		t.Fatal("processed a .zst sibling")
	}

	zipLike := append([]byte("PK\x03\x04"), bytes.Repeat([]byte{0}, 4000)...)
	if out, _ := compress("a.zip", zipLike, ""); out != nil {
		t.Fatal("zstd'd a zip")
	}
}

func TestOversizedImageIsSkippedNotDecoded(t *testing.T) {
	setup(t)
	config.MaxImageMegapixels = 1 // 1 MP limit, image below is 1.44 MP
	src := noisyJPEG(t, 1200, 1200, 95)

	out, err := compress("big.jpg", src, "")
	if err != nil || out != nil {
		t.Fatalf("expected silent skip, out=%v err=%v", out, err)
	}
}

func TestCorruptImageIsPermanentError(t *testing.T) {
	setup(t)
	// PNG signature + junk: sniffs as image/png but cannot decode.
	data := append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{7}, 4000)...)

	_, err := compress("bad.png", data, "")
	if err == nil || !IsPermanent(err) {
		t.Fatalf("err=%v, want permanent", err)
	}
}
