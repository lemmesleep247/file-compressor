package processor

import (
	"file-compressor/internal/config"

	"github.com/klauspost/compress/zstd"
)

func CompressZSTD(data []byte) ([]byte, error) {
	enc, err := zstd.NewWriter(
		nil,
		zstd.WithEncoderLevel(zstd.EncoderLevelFromZstd(config.ZstdLevel)),
	)
	if err != nil {
		return nil, err
	}
	return enc.EncodeAll(data, make([]byte, 0)), nil
}
