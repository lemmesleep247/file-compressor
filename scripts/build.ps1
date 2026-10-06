# Builds the service with the build tag that avoids gen2brain/webp's
# platform-specific dynamic-library loader (see README: "Windows note").
New-Item -ItemType Directory -Force bin | Out-Null
go build -tags nodynamic -o bin/server.exe ./cmd/server
