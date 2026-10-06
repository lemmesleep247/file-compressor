package config

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
)

var (
	WorkerCount   int
	MaxRetries    int
	MinSizeKB     int
	MaxFileSizeMB int
	EnableWebP    bool
	DLQBucket     string

	// AllowedBuckets limits which buckets' events are processed. Empty
	// means any bucket except DLQBucket.
	AllowedBuckets []string

	// OutputBucket, when set, switches to non-destructive mode: compressed
	// results are written there under the same key and the source object is
	// never modified. Empty means compress in place.
	OutputBucket string

	// MaxImageMegapixels rejects images whose decoded size would be
	// larger than this (decompression-bomb guard). 0 disables the check.
	MaxImageMegapixels int

	// ImageQuality is the JPEG/WebP quality (1-100). MaxImageDimension, if
	// > 0, downscales images whose longer side exceeds it (pixels).
	// BucketRules overrides these (and EnableWebP) per bucket.
	ImageQuality      int
	MaxImageDimension int
	BucketRules       map[string]BucketRule
	rulesErr          error

	// JobTimeoutSeconds bounds one job's MinIO I/O and processing time.
	JobTimeoutSeconds int

	EnableGenericCompression bool
	GenericCompression       string
	ZstdLevel                int

	DBPath       string
	WebhookToken string

	// JobRetentionDays is how long finished (completed / dead-letter) job
	// rows are kept before being pruned. 0 keeps them forever.
	JobRetentionDays int

	// DashboardToken is the Basic-auth password for the dashboard and
	// /api/*. AllowInsecure lets the service start with either token unset.
	DashboardToken string
	AllowInsecure  bool

	LogDir           string
	LogLevel         string
	LogFormat        string
	LogRetentionDays int
)

func Load() {
	WorkerCount = mustInt("WORKER_COUNT", 5)
	MaxRetries = mustInt("MAX_RETRIES", 3)
	MinSizeKB = mustInt("MIN_IMAGE_SIZE_KB", 100)
	MaxFileSizeMB = mustInt("MAX_FILE_SIZE_MB", 200)
	EnableWebP = os.Getenv("ENABLE_WEBP") == "true"
	DLQBucket = os.Getenv("DLQ_BUCKET")
	OutputBucket = os.Getenv("OUTPUT_BUCKET")
	AllowedBuckets = splitList(os.Getenv("ALLOWED_BUCKETS"))
	MaxImageMegapixels = mustInt("MAX_IMAGE_MEGAPIXELS", 100)
	ImageQuality = mustInt("IMAGE_QUALITY", 75)
	MaxImageDimension = mustInt("MAX_IMAGE_DIMENSION", 0)
	BucketRules, rulesErr = parseRules(os.Getenv("BUCKET_RULES"))
	JobTimeoutSeconds = mustInt("JOB_TIMEOUT_SECONDS", 300)

	EnableGenericCompression = os.Getenv("ENABLE_GENERIC_COMPRESSION") == "true"
	GenericCompression = getenv("GENERIC_COMPRESSION", "zstd")
	ZstdLevel = mustInt("ZSTD_LEVEL", 3)

	DBPath = getenv("DB_PATH", "jobs.db")
	JobRetentionDays = mustInt("JOB_RETENTION_DAYS", 90)
	WebhookToken = os.Getenv("WEBHOOK_TOKEN")
	DashboardToken = os.Getenv("DASHBOARD_TOKEN")
	AllowInsecure = os.Getenv("ALLOW_INSECURE") == "true"

	LogDir = getenv("LOG_DIR", "logs")
	LogLevel = getenv("LOG_LEVEL", "info")
	LogFormat = getenv("LOG_FORMAT", "json")
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

func splitList(v string) []string {
	var out []string
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// BucketRule overrides image settings for one bucket. Unset fields fall
// back to the global values.
type BucketRule struct {
	ImageQuality *int  `json:"image_quality"`
	MaxDimension *int  `json:"max_dimension"`
	WebP         *bool `json:"webp"`
}

// Settings are the effective image settings for one bucket.
type Settings struct {
	ImageQuality int
	MaxDimension int
	WebP         bool
}

// SettingsFor merges the bucket's rule (if any) over the global defaults.
func SettingsFor(bucket string) Settings {
	s := Settings{ImageQuality: ImageQuality, MaxDimension: MaxImageDimension, WebP: EnableWebP}
	if r, ok := BucketRules[bucket]; ok {
		if r.ImageQuality != nil {
			s.ImageQuality = *r.ImageQuality
		}
		if r.MaxDimension != nil {
			s.MaxDimension = *r.MaxDimension
		}
		if r.WebP != nil {
			s.WebP = *r.WebP
		}
	}
	return s
}

// parseRules decodes BUCKET_RULES, a JSON object keyed by bucket name, e.g.
// {"photos":{"image_quality":60,"max_dimension":2048,"webp":true}}.
// Unknown fields are rejected so typos don't silently do nothing.
func parseRules(raw string) (map[string]BucketRule, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.DisallowUnknownFields()
	var m map[string]BucketRule
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("BUCKET_RULES: %w", err)
	}
	return m, nil
}

// Validate reports configuration errors that should stop startup.
func Validate() error {
	if rulesErr != nil {
		return rulesErr
	}
	check := func(where string, quality, dim int) error {
		if quality < 1 || quality > 100 {
			return fmt.Errorf("%s: image quality %d out of range 1-100", where, quality)
		}
		if dim < 0 {
			return fmt.Errorf("%s: max dimension %d must be >= 0", where, dim)
		}
		return nil
	}
	if err := check("global", ImageQuality, MaxImageDimension); err != nil {
		return err
	}
	for bucket := range BucketRules {
		set := SettingsFor(bucket)
		if err := check("BUCKET_RULES["+bucket+"]", set.ImageQuality, set.MaxDimension); err != nil {
			return err
		}
	}
	return nil
}
