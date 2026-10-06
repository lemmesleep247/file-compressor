# Runs the tests with the build tag that avoids gen2brain/webp's
# platform-specific dynamic-library loader (see README: "Why -tags nodynamic").
# Extra arguments are passed through, e.g.: ./scripts/test.ps1 ./internal/api/... -v
if ($args.Count -eq 0) { $args = @("./...") }
go test -tags nodynamic @args
