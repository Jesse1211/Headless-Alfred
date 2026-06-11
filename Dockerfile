# syntax=docker/dockerfile:1.7

# ---- Stage 1: build React dist ----
FROM node:20-alpine AS web-builder
WORKDIR /web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build
# Output is in /web/dist/

# ---- Stage 2: build Go binary with embedded dist ----
FROM golang:1.25-bookworm AS go-builder
WORKDIR /src
# Pre-cache deps.
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/root/go/pkg/mod \
    go mod download
# Copy everything else.
COPY . .
# Drop in the built React assets where internal/static expects them.
RUN rm -rf internal/static/dist && mkdir -p internal/static/dist
COPY --from=web-builder /web/dist/ internal/static/dist/
# Make sure go:embed has at least one file (it does — the dist contents).
ARG VERSION=dev
ARG TARGETOS=linux
ARG TARGETARCH=amd64
RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/root/go/pkg/mod \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w -X main.Version=${VERSION}" \
    -o /out/alfred-server ./cmd/alfred-server

# ---- Stage 3: runtime ----
# bash + tini + ca-certificates. Distroless/scratch can't host this app
# because the shell package spawns bash inside the container.
FROM debian:bookworm-slim AS runtime
RUN apt-get update \
 && apt-get install -y --no-install-recommends bash ca-certificates tini \
 && rm -rf /var/lib/apt/lists/* \
 && useradd -m -u 1000 -s /bin/bash alfred \
 && mkdir -p /data \
 && chown alfred:alfred /data

USER alfred
WORKDIR /home/alfred
COPY --from=go-builder --chown=alfred:alfred /out/alfred-server /usr/local/bin/alfred-server

ENV ALFRED_ADDR=":8080"
ENV ALFRED_DATA_DIR="/data"
EXPOSE 8080
VOLUME ["/data"]

# tini supervises the Go process so SIGTERM is forwarded cleanly.
ENTRYPOINT ["/usr/bin/tini", "--", "/usr/local/bin/alfred-server"]
