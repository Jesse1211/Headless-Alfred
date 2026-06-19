.PHONY: test test-unit test-integration tidy build smoke ws-smoke web-build embed-web image image-push e2e e2e-setup e2e-teardown local-status local-stop local-dev

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
	mkdir -p internal/static/dist
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

# E2E: provision kind cluster, deploy alfred, run scenarios.
# Idempotent — cluster persists across runs. Use e2e-teardown to clean up.
e2e-setup:
	./test/e2e/setup.sh

e2e:
	go test -tags=e2e -v -timeout=10m ./test/e2e/

e2e-teardown:
	./test/e2e/teardown.sh

# Show what alfred-related things are running locally.
local-status:
	@echo "== alfred-server processes =="; pgrep -fl alfred-server || echo "  (none)"
	@echo "== kubectl port-forwards to alfred =="; pgrep -fl "kubectl.*port-forward.*alfred" || echo "  (none)"
	@echo "== ports 8080 / 5173 / 18080 =="; lsof -nP -iTCP -sTCP:LISTEN 2>/dev/null | grep -E ':(8080|5173|18080)' || echo "  (none listening)"
	@echo "== kind clusters =="; kind get clusters 2>/dev/null || echo "  (kind not installed)"

# One-shot: kill any local alfred-server + vite, rebuild frontend +
# embed, rebuild backend, start both on :8080 and :5173. Use this
# whenever you want both URLs to reflect the current source tree.
# Pass FLAGS=--no-build to skip frontend rebuild (Go-only iteration).
local-dev:
	@./scripts/local-dev.sh $(FLAGS)

# Stop local alfred-server + kubectl port-forwards. Leaves kind cluster alone.
# Use `make e2e-teardown` for the kind cluster.
local-stop:
	@pkill -f "go run ./cmd/alfred-server" 2>/dev/null || true
	@pkill -f "bin/alfred-server" 2>/dev/null || true
	@pkill -f "kubectl.*port-forward.*svc/alfred" 2>/dev/null || true
	@if [ -f /tmp/alfred-e2e-pf.pid ]; then kill "$$(cat /tmp/alfred-e2e-pf.pid)" 2>/dev/null || true; rm -f /tmp/alfred-e2e-pf.pid; fi
	@echo "stopped (kind cluster untouched — use 'make e2e-teardown' to delete it)"
