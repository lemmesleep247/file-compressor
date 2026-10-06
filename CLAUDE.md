# CLAUDE.md

Go service that receives MinIO bucket-notification webhooks (`POST /minio-event`),
queues jobs in SQLite, and compresses the uploaded objects (images, plus zstd for
other files) with a worker pool. See README.md for the full flow and env table.

## Commands

Always build with `-tags nodynamic` (avoids gen2brain/webp's dynamic-library loader).

- `make run` / `make build` / `make test` (Windows: `scripts/run.ps1`, `scripts/build.ps1`)
- Direct `go` invocations must include `-tags nodynamic`.

## Layout

- `cmd/server/main.go` — entrypoint, routes, wiring
- `internal/config` — env-var loading (`config.Load`)
- `internal/storage/minio.go` — MinIO client, download/upload
- `internal/queue` — webhook handler (`HandleEvent`) and worker pool
- `internal/store` — SQLite job store (`jobs.db`)
- `internal/processor` — image (`image.go`), zstd (`zstd.go`), orchestration (`process.go`)
- `internal/api`, `internal/api/dashboard` — HTTP API and dashboard UI
- `internal/logging` — day-wise log files in `LOG_DIR`

## MinIO authentication

- Authenticate to MinIO with an **access key + secret key** only
  (`MINIO_ACCESS_KEY` / `MINIO_SECRET_KEY`, static V4 creds in `storage.InitMinio`).
- Do not introduce username/password or login-id style config
  (e.g. `MINIO_ROOT_USER`/`MINIO_ROOT_PASSWORD`). Create a dedicated service
  account / access key in MinIO instead of reusing root credentials.
- Config is via env vars (`.env`, gitignored). Never commit real keys; keep
  `.env.example` limited to placeholders.
- `/minio-event` is separately protected by `WEBHOOK_TOKEN`.

## Conventions

- Config lives in `internal/config`; add new env vars there and in both
  `.env.example` and the README table.
- Logging uses `log/slog`.
