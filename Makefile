.PHONY: run build test

TAGS := nodynamic

run:
	go run -tags $(TAGS) ./cmd/server

build:
	go build -tags $(TAGS) -o bin/server ./cmd/server

test:
	go test -tags $(TAGS) ./...
