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

# Three groups of packages:
#   (1) Core: shell + signals + tmux multiplexer + git + ca-certs
#   (2) Developer toolkit so the pod feels like a real workstation —
#       network probes, editors, JSON/archive utilities. Asked for
#       repeatedly in spec §8 of the claude-mode design.
#   (3) Node 22 LTS (from NodeSource) so we can `npm install -g
#       @anthropic-ai/claude-code`. The `claude` CLI is required by
#       the claude-mode feature.
#
# We do everything in one RUN to keep the runtime image as small as
# possible (each apt-get layer would otherwise pin its own caches).
RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      bash ca-certificates procps tini tmux \
      git openssh-client \
      curl wget iputils-ping dnsutils \
      vim-tiny nano less jq unzip xz-utils \
 && curl -fsSL https://deb.nodesource.com/setup_22.x | bash - \
 && apt-get install -y --no-install-recommends nodejs \
 && npm install -g @anthropic-ai/claude-code \
 && apt-get clean \
 && rm -rf /var/lib/apt/lists/* /tmp/* /var/tmp/* /root/.npm \
 && useradd -m -u 1000 -s /bin/bash alfred \
 && mkdir -p /data \
 && chown alfred:alfred /data

# Numeric so Kubernetes' runAsNonRoot check can verify the user without
# introspecting /etc/passwd inside the image.
USER 1000
WORKDIR /home/alfred
COPY --from=go-builder --chown=alfred:alfred /out/alfred-server /usr/local/bin/alfred-server
COPY --chmod=0755 deploy/entrypoint.sh /usr/local/bin/entrypoint.sh
# Claude PreToolUse hook — a small curl-based shell script that
# blocks on the alfred-server bridge until the user decides. See
# internal/claude/bridge.go.
COPY --chmod=0755 deploy/alfred-claude-bridge.sh /usr/local/bin/alfred-claude-bridge

ENV ALFRED_ADDR=":8080"
ENV ALFRED_DATA_DIR="/data"
EXPOSE 8080
VOLUME ["/data"]

# tini supervises the entrypoint shell; the shell runs alfred-server in a
# respawn loop so that killing alfred-server does NOT exit the container.
# tmux server (started by alfred) daemonizes and is reparented to tini,
# so it survives across alfred-server respawns.
ENTRYPOINT ["/usr/bin/tini", "--", "/usr/local/bin/entrypoint.sh"]
