.PHONY: test test-unit test-integration tidy build smoke ws-smoke web-build embed-web image image-push

test: test-unit test-integration

test-unit:
	go test -race -short ./internal/...

test-integration:
	go test -race -run Integration ./internal/...

tidy:
	go mod tidy

# Build the frontend production bundle and put it where the Go binary embeds from.
web-build:
	cd web && npm run build

embed-web: web-build
	find internal/static/dist -mindepth 1 -delete
	cp -R web/dist/. internal/static/dist/

build:
	go build -o bin/alfred-server ./cmd/alfred-server

# REST-only smoke (curl through the binary).
smoke: build
	./scripts/smoke.sh

# Full live integration: REST login + WS run + verify persisted output.
# Requires the frontend dist to be embedded; pipe through `embed-web` first
# if you've changed frontend code.
ws-smoke: build
	go run ./scripts/ws-smoke

image:
	./scripts/build-image.sh

image-push:
	./scripts/build-image.sh --push
