package storage

import (
	"bytes"
	"context"
	"io"
	"os"
	"strings"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

var Client *minio.Client

func InitMinio() error {
	c, err := minio.New(os.Getenv("MINIO_ENDPOINT"), &minio.Options{
		Creds: credentials.NewStaticV4(
			os.Getenv("MINIO_ACCESS_KEY"),
			os.Getenv("MINIO_SECRET_KEY"),
			"",
		),
		Secure: os.Getenv("MINIO_USE_SSL") == "true",
	})
	if err != nil {
		return err
	}
	Client = c
	return nil
}

// Ping checks that MinIO is reachable and the credentials are accepted.
// AccessDenied still counts as healthy: a restricted access key may not be
// allowed to list buckets, but the server answered and recognised the key.
func Ping(ctx context.Context) error {
	_, err := Client.ListBuckets(ctx)
	if err != nil && minio.ToErrorResponse(err).Code == "AccessDenied" {
		return nil
	}
	return err
}

// EnsureBucket creates the bucket if it doesn't exist yet.
func EnsureBucket(ctx context.Context, bucket string) error {
	exists, err := Client.BucketExists(ctx, bucket)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	return Client.MakeBucket(ctx, bucket, minio.MakeBucketOptions{})
}

// List streams every object under prefix. The channel is closed when
// listing finishes or ctx is cancelled; check ObjectInfo.Err on each item.
func List(ctx context.Context, bucket, prefix string) <-chan minio.ObjectInfo {
	return Client.ListObjects(ctx, bucket, minio.ListObjectsOptions{Prefix: prefix, Recursive: true})
}

// Stat returns object metadata without downloading the body.
func Stat(ctx context.Context, bucket, object string) (minio.ObjectInfo, error) {
	return Client.StatObject(ctx, bucket, object, minio.StatObjectOptions{})
}

// Download reads the whole object. If etag is non-empty the read fails
// with a PreconditionFailed error when the object has changed since it was
// stat'ed, so the caller never mixes metadata and bytes from two versions.
func Download(ctx context.Context, bucket, object, etag string) ([]byte, error) {
	opts := minio.GetObjectOptions{}
	if etag != "" {
		if err := opts.SetMatchETag(etag); err != nil {
			return nil, err
		}
	}

	obj, err := Client.GetObject(ctx, bucket, object, opts)
	if err != nil {
		return nil, err
	}
	defer obj.Close()

	var buf bytes.Buffer
	if _, err := io.Copy(&buf, obj); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Upload writes data to bucket/object. If ifMatchETag is non-empty the
// write only succeeds when the current object still has that ETag, so a
// newer upload made while we were compressing is never overwritten.
func Upload(ctx context.Context, bucket, object string, data []byte, contentType string, meta map[string]string, ifMatchETag string) error {
	opts := minio.PutObjectOptions{
		ContentType:  contentType,
		UserMetadata: meta,
	}
	if ifMatchETag != "" {
		opts.SetMatchETag(ifMatchETag)
	}

	_, err := Client.PutObject(ctx, bucket, object, bytes.NewReader(data), int64(len(data)), opts)
	return err
}

// IsPreconditionFailed reports whether err is MinIO's answer to a failed
// If-Match / ETag condition, i.e. the object changed underneath us.
func IsPreconditionFailed(err error) bool {
	return minio.ToErrorResponse(err).Code == "PreconditionFailed"
}

// IsNotFound reports whether the object (or bucket) no longer exists.
func IsNotFound(err error) bool {
	switch minio.ToErrorResponse(err).Code {
	case "NoSuchKey", "NoSuchBucket":
		return true
	}
	return false
}

// MetaValue looks up a user-metadata value regardless of how the client
// library happens to key it (with or without the X-Amz-Meta- prefix,
// any capitalization).
func MetaValue(meta map[string]string, key string) string {
	for k, v := range meta {
		k = strings.TrimPrefix(strings.ToLower(k), "x-amz-meta-")
		if k == strings.ToLower(key) {
			return v
		}
	}
	return ""
}
