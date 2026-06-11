.PHONY: test test-unit test-integration tidy build smoke

test: test-unit test-integration

test-unit:
	go test -race -short ./internal/...

test-integration:
	go test -race -run Integration ./internal/...

tidy:
	go mod tidy

build:
	go build -o bin/alfred-server ./cmd/alfred-server

smoke: build
	./scripts/smoke.sh
