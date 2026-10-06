# syntax=docker/dockerfile:1
FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# nodynamic: use the portable WASM webp implementation (see README).
RUN CGO_ENABLED=0 go build -tags nodynamic -trimpath -ldflags="-s -w" -o /out/server ./cmd/server

FROM alpine:3.20
RUN adduser -D -u 10001 app && mkdir /data && chown app /data
USER app
COPY --from=build /out/server /usr/local/bin/server

# Job database and logs live on a volume so the queue survives restarts.
ENV DB_PATH=/data/jobs.db LOG_DIR=/data/logs PORT=8090
VOLUME /data
EXPOSE 8090
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s \
  CMD wget -qO- http://127.0.0.1:${PORT}/healthz >/dev/null || exit 1
ENTRYPOINT ["server"]
