# Runs the service with the build tag that avoids gen2brain/webp's
# platform-specific dynamic-library loader (see README: "Windows note").
go run -tags nodynamic ./cmd/server
