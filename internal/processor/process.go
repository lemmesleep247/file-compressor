package processor

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"image"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"file-compressor/internal/config"
	"file-compressor/internal/storage"

	"github.com/minio/minio-go/v7"
)

// CompressedSuffix marks the sibling objects written by generic (zstd)
// compression. Objects with this suffix are never processed again.
const CompressedSuffix = ".zst"

// hashMetaKey is the user-metadata key holding the SHA-256 of the bytes
// *this service wrote* to the object. Seeing that hash on an object whose
// content still hashes to it means the object is already our output —
// which is what stops our own re-upload's webhook from looping.
const hashMetaKey = "compressed-hash"

// permanentError marks a failure that retrying cannot fix.
type permanentError struct{ err error }

func (e permanentError) Error() string { return e.err.Error() }
func (e permanentError) Unwrap() error { return e.err }

// IsPermanent reports whether err should skip the retry budget.
func IsPermanent(err error) bool {
	var p permanentError
	return errors.As(err, &p)
}

// Output is the result of compressing one object.
type Output struct {
	Data        []byte
	ContentType string
	// Key is where Data should be written: the source key for images
	// (replaced in place) or key+".zst" for generic compression (the
	// original is left untouched, since zstd bytes aren't a valid file of
	// the original type).
	Key  string
	Hash string // SHA-256 of Data
}

// InPlace reports whether the output replaces the source object.
func (o *Output) InPlace(object string) bool { return o.Key == object }

// Compress decides what to do with data and does it. It returns nil (and
// no error) when the object should be left alone. storedHash is the
// compressed-hash metadata currently on the object, if any.
func Compress(object string, data []byte, storedHash string, set config.Settings) (*Output, error) {
	log := slog.With("object", object)

	if strings.HasSuffix(object, CompressedSuffix) {
		log.Debug("skip: compressed sibling")
		return nil, nil
	}
	if int64(len(data)) < int64(config.MinSizeKB)*1024 {
		log.Debug("skip: below minimum size", "bytes", len(data), "min_kb", config.MinSizeKB)
		return nil, nil
	}

	if storedHash != "" && storedHash == sha256Hex(data) {
		log.Debug("skip: already our output (hash matches)")
		return nil, nil
	}

	ct := http.DetectContentType(data)

	var out []byte
	var outCT, outKey string
	var err error
	start := time.Now()

	if isImage(ct) {
		if tooLarge(data) {
			log.Warn("skip: oversized image", "max_megapixels", config.MaxImageMegapixels)
			return nil, nil
		}
		log.Debug("compressing image", "content_type", ct, "quality", set.ImageQuality, "max_dimension", set.MaxDimension, "webp", set.WebP)
		out, outCT, err = CompressImage(data, ct, set)
		outKey = object
	} else {
		if !config.EnableGenericCompression || config.GenericCompression != "zstd" || alreadyCompressed(ct) {
			log.Debug("skip: no applicable compression", "content_type", ct, "generic_enabled", config.EnableGenericCompression, "already_compressed", alreadyCompressed(ct))
			return nil, nil
		}
		log.Debug("compressing with zstd", "content_type", ct, "level", config.ZstdLevel)
		out, err = CompressZSTD(data)
		outCT = "application/zstd"
		outKey = object + CompressedSuffix
	}
	if err != nil {
		return nil, err
	}

	// Not worth writing if it didn't shrink.
	if len(out) >= len(data) {
		log.Debug("skip: output not smaller", "original_bytes", len(data), "output_bytes", len(out))
		return nil, nil
	}

	log.Debug("compressed", "original_bytes", len(data), "output_bytes", len(out), "output_key", outKey, "duration_ms", time.Since(start).Milliseconds())
	return &Output{Data: out, ContentType: outCT, Key: outKey, Hash: sha256Hex(out)}, nil
}

// ProcessObject compresses bucket/object if it qualifies. It returns the
// original and final sizes of the *source object* (equal when it was
// skipped or left in place), so callers can track real savings.
func ProcessObject(ctx context.Context, bucket, object string) (originalSize int64, finalSize int64, err error) {
	log := slog.With("bucket", bucket, "object", object)

	info, err := storage.Stat(ctx, bucket, object)
	if err != nil {
		if storage.IsNotFound(err) {
			log.Debug("object no longer exists, nothing to do")
			return 0, 0, nil // deleted before we got to it; nothing to do
		}
		return 0, 0, err
	}
	originalSize = info.Size

	// Decide from metadata before pulling bytes into memory.
	if config.MaxFileSizeMB > 0 && originalSize > int64(config.MaxFileSizeMB)*1024*1024 {
		log.Info("skip: file exceeds MAX_FILE_SIZE_MB", "bytes", originalSize, "max_mb", config.MaxFileSizeMB)
		return originalSize, originalSize, nil
	}
	if originalSize < int64(config.MinSizeKB)*1024 {
		log.Debug("skip: below minimum size", "bytes", originalSize, "min_kb", config.MinSizeKB)
		return originalSize, originalSize, nil
	}

	log.Debug("downloading", "bytes", originalSize, "etag", info.ETag)
	dlStart := time.Now()
	data, err := storage.Download(ctx, bucket, object, info.ETag)
	if err != nil {
		if storage.IsPreconditionFailed(err) || storage.IsNotFound(err) {
			log.Debug("object changed or removed while downloading; a new event will handle it")
			return originalSize, originalSize, nil // replaced or deleted; a replacement's own event queues a new job
		}
		return originalSize, originalSize, err
	}
	log.Debug("downloaded", "duration_ms", time.Since(dlStart).Milliseconds())

	out, err := Compress(object, data, storage.MetaValue(info.UserMetadata, hashMetaKey), config.SettingsFor(bucket))
	if err != nil {
		return originalSize, originalSize, err
	}
	if out == nil {
		return originalSize, originalSize, nil
	}

	dest := Destination(bucket, object, out)
	etag := ""
	if dest.IfMatch {
		etag = info.ETag
	}
	meta := map[string]string{hashMetaKey: out.Hash}

	log.Debug("uploading", "dest_bucket", dest.Bucket, "dest_key", out.Key, "bytes", len(out.Data), "content_type", out.ContentType, "if_match", dest.IfMatch)
	if err := storage.Upload(ctx, dest.Bucket, out.Key, out.Data, out.ContentType, meta, etag); err != nil {
		if storage.IsPreconditionFailed(err) {
			log.Debug("source changed during processing; upload skipped, a new event will handle it")
			return originalSize, originalSize, nil
		}
		return originalSize, originalSize, err
	}

	if dest.Replaces {
		return originalSize, int64(len(out.Data)), nil
	}
	return originalSize, originalSize, nil
}

// Dest says where an Output goes and how that affects the job's numbers.
type Dest struct {
	Bucket string
	// IfMatch: the write overwrites the source, so guard it with the
	// source's ETag.
	IfMatch bool
	// Replaces: the output is what consumers should use instead of the
	// source (in-place image, or any output-bucket copy), so the size
	// difference counts as a saving. A same-bucket .zst sibling does not.
	Replaces bool
}

// Destination applies OUTPUT_BUCKET: when set, every output goes there and
// the source is never touched; otherwise images replace the source in
// place and generic output becomes a sibling.
func Destination(bucket, object string, out *Output) Dest {
	if config.OutputBucket != "" {
		return Dest{Bucket: config.OutputBucket, Replaces: true}
	}
	inPlace := out.InPlace(object)
	return Dest{Bucket: bucket, IfMatch: inPlace, Replaces: inPlace}
}

func SendToDLQ(ctx context.Context, bucket, object string, procErr error) error {
	// Ignore the error here: it's almost always "bucket already exists"
	// after the first call, which is fine to proceed past.
	_ = storage.Client.MakeBucket(ctx, config.DLQBucket, minio.MakeBucketOptions{})

	return storage.Upload(
		ctx,
		config.DLQBucket,
		bucket+"_"+object+".txt",
		[]byte(procErr.Error()),
		"text/plain",
		nil,
		"",
	)
}

func isImage(ct string) bool {
	return ct == "image/jpeg" || ct == "image/png"
}

// alreadyCompressed reports content types where zstd won't help.
func alreadyCompressed(ct string) bool {
	switch {
	case strings.HasPrefix(ct, "video/"), strings.HasPrefix(ct, "audio/"), strings.HasPrefix(ct, "font/"):
		return true
	}
	switch ct {
	case "application/zip", "application/x-gzip", "application/gzip", "application/x-rar-compressed",
		"application/zstd", "application/x-7z-compressed", "application/wasm", "image/gif", "image/webp":
		return true
	}
	return false
}

// tooLarge guards against decompression bombs: a small PNG can decode to
// gigabytes of pixels. It only reads the image header.
func tooLarge(data []byte) bool {
	if config.MaxImageMegapixels <= 0 {
		return false
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return false // let the real decode report the error
	}
	return int64(cfg.Width)*int64(cfg.Height) > int64(config.MaxImageMegapixels)*1_000_000
}

func sha256Hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
