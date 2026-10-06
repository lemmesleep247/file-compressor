package storage

import (
	"bytes"
	"context"
	"io"
	"os"

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

func Download(bucket, object string) ([]byte, minio.ObjectInfo, error) {
	obj, err := Client.GetObject(context.Background(), bucket, object, minio.GetObjectOptions{})
	if err != nil {
		return nil, minio.ObjectInfo{}, err
	}
	defer obj.Close()

	info, _ := obj.Stat()

	var buf bytes.Buffer
	_, err = io.Copy(&buf, obj)
	return buf.Bytes(), info, err
}

func Upload(bucket, object string, data []byte, contentType string, meta map[string]string) error {
	_, err := Client.PutObject(
		context.Background(),
		bucket,
		object,
		bytes.NewReader(data),
		int64(len(data)),
		minio.PutObjectOptions{
			ContentType:  contentType,
			UserMetadata: meta,
		},
	)
	return err
}
