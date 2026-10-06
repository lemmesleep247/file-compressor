package config

import (
	"os"
	"strconv"
)

var (
	WorkerCount   int
	MaxRetries    int
	MinSizeKB     int
	MaxFileSizeMB int
	EnableWebP    bool
	DLQBucket     string

	EnableGenericCompression bool
	GenericCompression       string
	ZstdLevel                int

	DBPath       string
	WebhookToken string

	LogDir           string
	LogRetentionDays int
)

func Load() {
	WorkerCount = mustInt("WORKER_COUNT", 5)
	MaxRetries = mustInt("MAX_RETRIES", 3)
	MinSizeKB = mustInt("MIN_IMAGE_SIZE_KB", 100)
	MaxFileSizeMB = mustInt("MAX_FILE_SIZE_MB", 200)
	EnableWebP = os.Getenv("ENABLE_WEBP") == "true"
	DLQBucket = os.Getenv("DLQ_BUCKET")

	EnableGenericCompression = os.Getenv("ENABLE_GENERIC_COMPRESSION") == "true"
	GenericCompression = getenv("GENERIC_COMPRESSION", "zstd")
	ZstdLevel = mustInt("ZSTD_LEVEL", 3)

	DBPath = getenv("DB_PATH", "jobs.db")
	WebhookToken = os.Getenv("WEBHOOK_TOKEN")

	LogDir = getenv("LOG_DIR", "logs")
	LogRetentionDays = mustInt("LOG_RETENTION_DAYS", 30)
}

func mustInt(k string, d int) int {
	if v, err := strconv.Atoi(os.Getenv(k)); err == nil {
		return v
	}
	return d
}

func getenv(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
