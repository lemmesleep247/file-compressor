package processor

import (
	"bytes"
	"image"
	"image/jpeg"
	"image/png"
	"file-compressor/internal/config"

	"github.com/gen2brain/webp"
)

func CompressImage(data []byte, ct string) ([]byte, string, error) {
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, "", err
	}

	var buf bytes.Buffer

	if config.EnableWebP {
		err = webp.Encode(&buf, img, webp.Options{Quality: 75})
		return buf.Bytes(), "image/webp", err
	}

	if ct == "image/jpeg" {
		err = jpeg.Encode(&buf, img, &jpeg.Options{Quality: 75})
		return buf.Bytes(), "image/jpeg", err
	}

	err = (&png.Encoder{CompressionLevel: png.BestCompression}).Encode(&buf, img)
	return buf.Bytes(), "image/png", err
}
