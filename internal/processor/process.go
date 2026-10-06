package processor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"

	"file-compressor/internal/config"
	"file-compressor/internal/storage"

	"github.com/minio/minio-go/v7"
)

// ProcessObject downloads bucket/object, compresses it if it qualifies,
// and re-uploads it in place. It returns the original and final sizes
// (equal when the object was skipped) so callers can track savings.
func ProcessObject(bucket, object string) (originalSize int64, finalSize int64, err error) {
	data, info, err := storage.Download(bucket, object)
	if err != nil {
		return 0, 0, err
	}
	originalSize = int64(len(data))

	if config.MaxFileSizeMB > 0 && originalSize > int64(config.MaxFileSizeMB)*1024*1024 {
		return originalSize, originalSize, nil
	}

	if originalSize < int64(config.MinSizeKB)*1024 {
		return originalSize, originalSize, nil
	}

	hash := sha256.Sum256(data)
	hashStr := hex.EncodeToString(hash[:])

	if info.UserMetadata["X-Amz-Meta-Compressed-Hash"] == hashStr {
		return originalSize, originalSize, nil
	}

	ct := http.DetectContentType(data)

	var out []byte
	var outCT string

	if isImage(ct) {
		out, outCT, err = CompressImage(data, ct)
	} else {
		if !config.EnableGenericCompression {
			return originalSize, originalSize, nil
		}

		switch config.GenericCompression {
		case "zstd":
			out, err = CompressZSTD(data)
			outCT = ct
		default:
			return originalSize, originalSize, nil
		}
	}

	if err != nil {
		return originalSize, originalSize, err
	}

	// Skip the upload if compression didn't actually shrink the file.
	if int64(len(out)) >= originalSize {
		return originalSize, originalSize, nil
	}

	meta := map[string]string{
		"compressed-hash": hashStr,
	}

	if err := storage.Upload(bucket, object, out, outCT, meta); err != nil {
		return originalSize, originalSize, err
	}

	return originalSize, int64(len(out)), nil
}

func SendToDLQ(bucket, object string, procErr error) error {
	// Ignore the error here: it's almost always "bucket already exists"
	// after the first call, which is fine to proceed past.
	_ = storage.Client.MakeBucket(
		context.Background(),
		config.DLQBucket,
		minio.MakeBucketOptions{},
	)

	return storage.Upload(
		config.DLQBucket,
		bucket+"_"+object+".txt",
		[]byte(procErr.Error()),
		"text/plain",
		nil,
	)
}

func isImage(ct string) bool {
	return ct == "image/jpeg" || ct == "image/png"
}
