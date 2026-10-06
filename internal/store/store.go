// Package store provides a SQLite-backed persistent job queue so that
// queued/in-flight jobs and job history survive a process restart.
package store

import (
	"database/sql"
	"sync"
	"time"

	"github.com/google/uuid"

	_ "modernc.org/sqlite"
)

const (
	StatusQueued     = "queued"
	StatusProcessing = "processing"
	StatusCompleted  = "completed"
	StatusDeadLetter = "dead_letter"
)

type Job struct {
	ID             string `json:"id"`
	Bucket         string `json:"bucket"`
	Object         string `json:"object"`
	Status         string `json:"status"`
	Retries        int    `json:"retries"`
	OriginalSize   int64  `json:"original_size"`
	CompressedSize int64  `json:"compressed_size"`
	Error          string `json:"error,omitempty"`
	CreatedAt      int64  `json:"created_at"`
	UpdatedAt      int64  `json:"updated_at"`
}

type Stats struct {
	Queued               int   `json:"queued"`
	Processing           int   `json:"processing"`
	Completed            int   `json:"completed"`
	DeadLetter           int   `json:"dead_letter"`
	TotalOriginalBytes   int64 `json:"total_original_bytes"`
	TotalCompressedBytes int64 `json:"total_compressed_bytes"`
}

// Store serializes all access behind a mutex. At the throughput this
// service runs at, a single-writer SQLite file is simpler and safer than
// tuning WAL/busy-timeout settings for concurrent writers.
type Store struct {
	db *sql.DB
	mu sync.Mutex
}

func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)

	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) migrate() error {
	_, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS jobs (
			id              TEXT PRIMARY KEY,
			bucket          TEXT NOT NULL,
			object          TEXT NOT NULL,
			status          TEXT NOT NULL,
			retries         INTEGER NOT NULL DEFAULT 0,
			original_size   INTEGER NOT NULL DEFAULT 0,
			compressed_size INTEGER NOT NULL DEFAULT 0,
			error           TEXT NOT NULL DEFAULT '',
			next_attempt_at INTEGER NOT NULL DEFAULT 0,
			created_at      INTEGER NOT NULL,
			updated_at      INTEGER NOT NULL
		);
		CREATE INDEX IF NOT EXISTS idx_jobs_status ON jobs(status, next_attempt_at);
		CREATE INDEX IF NOT EXISTS idx_jobs_bucket_object ON jobs(bucket, object);
	`)
	return err
}

// Enqueue inserts a new queued job unless one is already queued or
// processing for the same bucket/object, which prevents duplicate MinIO
// webhook deliveries from piling up redundant work.
func (s *Store) Enqueue(bucket, object string) (*Job, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var count int
	err := s.db.QueryRow(
		`SELECT COUNT(1) FROM jobs WHERE bucket = ? AND object = ? AND status IN (?, ?)`,
		bucket, object, StatusQueued, StatusProcessing,
	).Scan(&count)
	if err != nil {
		return nil, false, err
	}
	if count > 0 {
		return nil, false, nil
	}

	now := time.Now().UnixMilli()
	job := &Job{
		ID:        uuid.NewString(),
		Bucket:    bucket,
		Object:    object,
		Status:    StatusQueued,
		CreatedAt: now,
		UpdatedAt: now,
	}

	_, err = s.db.Exec(
		`INSERT INTO jobs (id, bucket, object, status, retries, original_size, compressed_size, error, next_attempt_at, created_at, updated_at)
		 VALUES (?, ?, ?, ?, 0, 0, 0, '', 0, ?, ?)`,
		job.ID, job.Bucket, job.Object, job.Status, job.CreatedAt, job.UpdatedAt,
	)
	if err != nil {
		return nil, false, err
	}
	return job, true, nil
}

// ClaimNext atomically picks the oldest eligible queued job and marks it
// processing. It returns (nil, nil) when there is nothing to do.
func (s *Store) ClaimNext() (*Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().UnixMilli()

	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var j Job
	row := tx.QueryRow(
		`SELECT id, bucket, object, retries, original_size, compressed_size, error, created_at, updated_at
		 FROM jobs WHERE status = ? AND next_attempt_at <= ? ORDER BY created_at ASC LIMIT 1`,
		StatusQueued, now,
	)
	if err := row.Scan(&j.ID, &j.Bucket, &j.Object, &j.Retries, &j.OriginalSize, &j.CompressedSize, &j.Error, &j.CreatedAt, &j.UpdatedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	j.Status = StatusProcessing

	if _, err := tx.Exec(`UPDATE jobs SET status = ?, updated_at = ? WHERE id = ?`, StatusProcessing, now, j.ID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &j, nil
}

func (s *Store) Complete(id string, originalSize, compressedSize int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.Exec(
		`UPDATE jobs SET status = ?, original_size = ?, compressed_size = ?, error = '', updated_at = ? WHERE id = ?`,
		StatusCompleted, originalSize, compressedSize, time.Now().UnixMilli(), id,
	)
	return err
}

// Fail records a processing error. If the retry budget is exhausted the
// job moves to dead_letter (returns deadLettered = true); otherwise it is
// re-queued with an exponential backoff delay capped at 30s.
func (s *Store) Fail(id string, procErr error, maxRetries int) (deadLettered bool, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var retries int
	if err := s.db.QueryRow(`SELECT retries FROM jobs WHERE id = ?`, id).Scan(&retries); err != nil {
		return false, err
	}
	retries++
	now := time.Now().UnixMilli()
	errMsg := ""
	if procErr != nil {
		errMsg = procErr.Error()
	}

	if retries >= maxRetries {
		_, err := s.db.Exec(
			`UPDATE jobs SET status = ?, retries = ?, error = ?, updated_at = ? WHERE id = ?`,
			StatusDeadLetter, retries, errMsg, now, id,
		)
		return true, err
	}

	backoff := time.Duration(retries*retries) * time.Second
	if backoff > 30*time.Second {
		backoff = 30 * time.Second
	}

	_, err = s.db.Exec(
		`UPDATE jobs SET status = ?, retries = ?, error = ?, next_attempt_at = ?, updated_at = ? WHERE id = ?`,
		StatusQueued, retries, errMsg, now+backoff.Milliseconds(), now, id,
	)
	return false, err
}

// RequeueStuckProcessing resets any job left in "processing" state, which
// only happens if the process crashed or was killed mid-job. Reprocessing
// is safe: ProcessObject is idempotent via the compressed-hash metadata
// check, so a job that actually finished right before the crash is simply
// detected as already-compressed and skipped.
func (s *Store) RequeueStuckProcessing() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.Exec(
		`UPDATE jobs SET status = ?, next_attempt_at = 0, updated_at = ? WHERE status = ?`,
		StatusQueued, time.Now().UnixMilli(), StatusProcessing,
	)
	return err
}

func (s *Store) List(status string, limit int) ([]Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if limit <= 0 || limit > 500 {
		limit = 100
	}

	var rows *sql.Rows
	var err error
	if status == "" {
		rows, err = s.db.Query(
			`SELECT id, bucket, object, status, retries, original_size, compressed_size, error, created_at, updated_at
			 FROM jobs ORDER BY updated_at DESC LIMIT ?`, limit,
		)
	} else {
		rows, err = s.db.Query(
			`SELECT id, bucket, object, status, retries, original_size, compressed_size, error, created_at, updated_at
			 FROM jobs WHERE status = ? ORDER BY updated_at DESC LIMIT ?`, status, limit,
		)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var jobs []Job
	for rows.Next() {
		var j Job
		if err := rows.Scan(&j.ID, &j.Bucket, &j.Object, &j.Status, &j.Retries, &j.OriginalSize, &j.CompressedSize, &j.Error, &j.CreatedAt, &j.UpdatedAt); err != nil {
			return nil, err
		}
		jobs = append(jobs, j)
	}
	return jobs, rows.Err()
}

func (s *Store) Stats() (Stats, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var st Stats
	rows, err := s.db.Query(`SELECT status, COUNT(1), COALESCE(SUM(original_size),0), COALESCE(SUM(compressed_size),0) FROM jobs GROUP BY status`)
	if err != nil {
		return st, err
	}
	defer rows.Close()

	for rows.Next() {
		var status string
		var count int
		var origSum, compSum int64
		if err := rows.Scan(&status, &count, &origSum, &compSum); err != nil {
			return st, err
		}
		switch status {
		case StatusQueued:
			st.Queued = count
		case StatusProcessing:
			st.Processing = count
		case StatusCompleted:
			st.Completed = count
			st.TotalOriginalBytes += origSum
			st.TotalCompressedBytes += compSum
		case StatusDeadLetter:
			st.DeadLetter = count
		}
	}
	return st, rows.Err()
}
