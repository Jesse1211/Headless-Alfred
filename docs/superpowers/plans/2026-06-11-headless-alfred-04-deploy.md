# Headless Alfred — Plan 4: Container & K8s Deploy

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Package the Go binary + React `dist/` into one container image, and produce the k8s manifests that deploy it to `agent.jesseliu.me` via Traefik + cert-manager.

**Architecture:** Three-stage Dockerfile (node build → go build → minimal runtime). Final image is `debian:bookworm-slim` because the application spawns bash inside the container — distroless/scratch won't work. Manifests: Namespace, Secret (template, real one created out-of-band), PVC, Deployment (single replica), Service, Ingress.

**Tech Stack:** Docker BuildKit, k3s, Traefik (k3s default), cert-manager (cluster prereq).

**Spec sections covered:** §3 (Pod + PVC), §8 (Secret), §10 (HTTPS via Traefik), §13 (Deployment).

**Depends on:** Plan 2 (Go module + main.go), Plan 3 (`web/dist/` produces React build).

---

## File Structure

```
Dockerfile
.dockerignore
deploy/
├── Makefile                     # build, push, kubectl targets (separate from root Makefile)
├── manifests/
│   ├── namespace.yaml
│   ├── secret.example.yaml      # template; real Secret created with kubectl create
│   ├── pvc.yaml
│   ├── deployment.yaml
│   ├── service.yaml
│   └── ingress.yaml
└── README.md                    # operator runbook
```

---

## Task 1: .dockerignore

**Files:**
- Create: `.dockerignore`

- [ ] **Step 1.1: Write .dockerignore**

Create `.dockerignore`:
```
# Don't ship local development artifacts.
.git/
.github/
docs/
deprecated/
*.md
LICENSE

# Source artifacts to skip — built fresh inside the image.
**/node_modules/
**/dist/
/bin/
/coverage.out

# Local test data
/tmp/
/data/

# Secrets must never be in the build context.
deploy/manifests/secret.yaml
.env
.env.*

# Editor + OS
.idea/
.vscode/
.DS_Store

# Keep Plan 3's tests out of the image build context (they're not needed).
**/*_test.go
**/*.test.ts
**/*.test.tsx
```

- [ ] **Step 1.2: Commit**

```bash
git add .dockerignore
git commit -m "chore: dockerignore"
```

---

## Task 2: Multi-stage Dockerfile

**Files:**
- Create: `Dockerfile`

- [ ] **Step 2.1: Write Dockerfile**

Create `Dockerfile`:
```dockerfile
# syntax=docker/dockerfile:1.7

# ---- Stage 1: build React dist ----
FROM node:20-alpine AS web-builder
WORKDIR /web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build
# Outputs to /web/dist

# ---- Stage 2: build Go binary with embedded dist ----
FROM golang:1.22-bookworm AS go-builder
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
# Build a static-ish binary; CGO disabled is sufficient because we use no CGO deps.
ARG VERSION=dev
RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/root/go/pkg/mod \
    CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -trimpath -ldflags="-s -w -X main.Version=${VERSION}" \
    -o /out/alfred-server ./cmd/alfred-server

# ---- Stage 3: runtime ----
# Needs bash because the shell module spawns it. Slim Debian gives us bash +
# coreutils + a sane SSL trust store.
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
```

- [ ] **Step 2.2: Build the image**

```bash
docker build -t headless-alfred:dev .
```

Expected: image build completes. Final image around 100-150 MB.

- [ ] **Step 2.3: Run the image locally for a sanity smoke**

```bash
docker run --rm -d --name alfred-smoke \
  -e ALFRED_USER=admin \
  -e ALFRED_PASSWORD=test \
  -e ALFRED_TOKEN=devtoken \
  -p 18081:8080 \
  -v /tmp/alfred-data:/data \
  headless-alfred:dev

# Wait then probe.
sleep 1
curl -sf http://127.0.0.1:18081/readyz
TOKEN=$(curl -sf -X POST http://127.0.0.1:18081/api/login \
  -H 'Content-Type: application/json' \
  -d '{"user":"admin","password":"test"}' | grep -oE '"token":"[^"]+"' | cut -d'"' -f4)
echo "Got token: $TOKEN"
[ "$TOKEN" = "devtoken" ] || { echo "FAIL"; docker logs alfred-smoke; }
docker stop alfred-smoke
```

Expected output: `ok` then `Got token: devtoken`.

- [ ] **Step 2.4: Commit**

```bash
git add Dockerfile
git commit -m "feat: multi-stage Dockerfile producing single image"
```

---

## Task 3: K8s manifests — Namespace, PVC, Secret template

**Files:**
- Create: `deploy/manifests/namespace.yaml`
- Create: `deploy/manifests/pvc.yaml`
- Create: `deploy/manifests/secret.example.yaml`

- [ ] **Step 3.1: namespace.yaml**

Create `deploy/manifests/namespace.yaml`:
```yaml
apiVersion: v1
kind: Namespace
metadata:
  name: alfred
```

- [ ] **Step 3.2: pvc.yaml**

Create `deploy/manifests/pvc.yaml`:
```yaml
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: alfred-data
  namespace: alfred
spec:
  accessModes:
    - ReadWriteOnce
  resources:
    requests:
      storage: 1Gi
```

- [ ] **Step 3.3: secret.example.yaml**

Create `deploy/manifests/secret.example.yaml`:
```yaml
# Template only. DO NOT apply this file.
# Create the real Secret out-of-band with values that are NEVER committed:
#
#   kubectl -n alfred create secret generic alfred-secret \
#     --from-literal=ALFRED_USER=admin \
#     --from-literal=ALFRED_PASSWORD='<your-strong-password>' \
#     --from-literal=ALFRED_TOKEN=$(openssl rand -hex 32)
#
# The Deployment expects exactly these three keys to exist in the Secret.
apiVersion: v1
kind: Secret
metadata:
  name: alfred-secret
  namespace: alfred
type: Opaque
stringData:
  ALFRED_USER: "CHANGEME"
  ALFRED_PASSWORD: "CHANGEME"
  ALFRED_TOKEN: "CHANGEME"
```

- [ ] **Step 3.4: Commit**

```bash
git add deploy/manifests/namespace.yaml deploy/manifests/pvc.yaml deploy/manifests/secret.example.yaml
git commit -m "feat(deploy): namespace, PVC, secret template"
```

---

## Task 4: Deployment + Service

**Files:**
- Create: `deploy/manifests/deployment.yaml`
- Create: `deploy/manifests/service.yaml`

- [ ] **Step 4.1: deployment.yaml**

Create `deploy/manifests/deployment.yaml`:
```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: alfred
  namespace: alfred
  labels:
    app: alfred
spec:
  # Exactly one replica: shell state and the PVC (RWO) are process-local.
  replicas: 1
  strategy:
    # Recreate so the old Pod releases the PVC before the new one mounts it.
    type: Recreate
  selector:
    matchLabels:
      app: alfred
  template:
    metadata:
      labels:
        app: alfred
    spec:
      securityContext:
        runAsNonRoot: true
        fsGroup: 1000
      containers:
        - name: alfred
          image: ghcr.io/jesseliu/headless-alfred:dev
          imagePullPolicy: IfNotPresent
          env:
            - name: ALFRED_ADDR
              value: ":8080"
            - name: ALFRED_DATA_DIR
              value: "/data"
            - name: ALFRED_USER
              valueFrom:
                secretKeyRef:
                  name: alfred-secret
                  key: ALFRED_USER
            - name: ALFRED_PASSWORD
              valueFrom:
                secretKeyRef:
                  name: alfred-secret
                  key: ALFRED_PASSWORD
            - name: ALFRED_TOKEN
              valueFrom:
                secretKeyRef:
                  name: alfred-secret
                  key: ALFRED_TOKEN
          ports:
            - name: http
              containerPort: 8080
          readinessProbe:
            httpGet:
              path: /readyz
              port: http
            initialDelaySeconds: 1
            periodSeconds: 5
            timeoutSeconds: 2
          livenessProbe:
            httpGet:
              path: /healthz
              port: http
            initialDelaySeconds: 5
            periodSeconds: 10
            timeoutSeconds: 2
          resources:
            requests:
              cpu: "100m"
              memory: "128Mi"
            limits:
              cpu: "1"
              memory: "1Gi"
          volumeMounts:
            - name: data
              mountPath: /data
          securityContext:
            allowPrivilegeEscalation: false
            capabilities:
              drop:
                - ALL
            readOnlyRootFilesystem: false  # bash uses /tmp; cheap to allow
      volumes:
        - name: data
          persistentVolumeClaim:
            claimName: alfred-data
```

- [ ] **Step 4.2: service.yaml**

Create `deploy/manifests/service.yaml`:
```yaml
apiVersion: v1
kind: Service
metadata:
  name: alfred
  namespace: alfred
spec:
  type: ClusterIP
  selector:
    app: alfred
  ports:
    - name: http
      port: 8080
      targetPort: http
```

- [ ] **Step 4.3: Validate YAML locally**

```bash
kubectl --dry-run=client -f deploy/manifests/deployment.yaml apply
kubectl --dry-run=client -f deploy/manifests/service.yaml apply
```

Expected: prints `deployment.apps/alfred configured (dry run)` etc., no errors.

- [ ] **Step 4.4: Commit**

```bash
git add deploy/manifests/deployment.yaml deploy/manifests/service.yaml
git commit -m "feat(deploy): Deployment + Service"
```

---

## Task 5: Ingress (Traefik + cert-manager)

**Files:**
- Create: `deploy/manifests/ingress.yaml`

- [ ] **Step 5.1: ingress.yaml**

Create `deploy/manifests/ingress.yaml`:
```yaml
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: alfred
  namespace: alfred
  annotations:
    # cert-manager will provision a Let's Encrypt cert into the secret named
    # in spec.tls[0].secretName. This requires the named ClusterIssuer
    # to be pre-installed (see deploy/README.md).
    cert-manager.io/cluster-issuer: letsencrypt-prod
    # Traefik: force HTTPS by redirecting plain HTTP requests.
    traefik.ingress.kubernetes.io/router.entrypoints: websecure
    traefik.ingress.kubernetes.io/router.tls: "true"
spec:
  ingressClassName: traefik
  tls:
    - hosts:
        - agent.jesseliu.me
      secretName: alfred-tls
  rules:
    - host: agent.jesseliu.me
      http:
        paths:
          - path: /
            pathType: Prefix
            backend:
              service:
                name: alfred
                port:
                  number: 8080
```

- [ ] **Step 5.2: Add HTTP → HTTPS redirect (Traefik IngressRoute)**

The plain `Ingress` above tells Traefik to serve on `websecure` (443) only. Requests to `:80` would normally 404. Add a Traefik-specific IngressRoute that redirects port 80 → port 443 for this host. (Optional: most users come via HTTPS directly once they bookmark the URL, but the redirect is polite.)

Append to `deploy/manifests/ingress.yaml`:
```yaml
---
apiVersion: traefik.io/v1alpha1
kind: IngressRoute
metadata:
  name: alfred-http-redirect
  namespace: alfred
spec:
  entryPoints:
    - web
  routes:
    - match: Host(`agent.jesseliu.me`)
      kind: Rule
      services:
        - name: noop@internal
          kind: TraefikService
      middlewares:
        - name: redirect-https
---
apiVersion: traefik.io/v1alpha1
kind: Middleware
metadata:
  name: redirect-https
  namespace: alfred
spec:
  redirectScheme:
    scheme: https
    permanent: true
```

- [ ] **Step 5.3: Validate**

```bash
kubectl --dry-run=client -f deploy/manifests/ingress.yaml apply
```

Expected: dry-run succeeds.

- [ ] **Step 5.4: Commit**

```bash
git add deploy/manifests/ingress.yaml
git commit -m "feat(deploy): Ingress with HTTPS-only and cert-manager"
```

---

## Task 6: deploy/Makefile and operator README

**Files:**
- Create: `deploy/Makefile`
- Create: `deploy/README.md`

- [ ] **Step 6.1: deploy/Makefile**

Create `deploy/Makefile`:
```makefile
# Targets for building and shipping the image.
# Run from repo root: `make -C deploy <target>`.

REGISTRY ?= ghcr.io/jesseliu
IMAGE    ?= $(REGISTRY)/headless-alfred
TAG      ?= $(shell git rev-parse --short HEAD)
FULL     := $(IMAGE):$(TAG)

.PHONY: image push apply rollout-restart logs

image:
	docker build -t $(FULL) -t $(IMAGE):latest --build-arg VERSION=$(TAG) ..

push: image
	docker push $(FULL)
	docker push $(IMAGE):latest

apply:
	kubectl apply -f manifests/namespace.yaml
	kubectl apply -f manifests/pvc.yaml
	kubectl apply -f manifests/service.yaml
	kubectl apply -f manifests/deployment.yaml
	kubectl apply -f manifests/ingress.yaml
	@echo "Reminder: ensure alfred-secret exists (see README)."

rollout-restart:
	kubectl -n alfred rollout restart deployment/alfred
	kubectl -n alfred rollout status deployment/alfred

logs:
	kubectl -n alfred logs -f deployment/alfred
```

- [ ] **Step 6.2: deploy/README.md (operator runbook)**

Create `deploy/README.md`:
```markdown
# Headless Alfred — Deploy

## Cluster prerequisites (one-time)

These must exist before applying any of `manifests/`:

1. **k3s installed** with Traefik enabled (default).
2. **cert-manager installed**:
   ```bash
   kubectl apply -f https://github.com/cert-manager/cert-manager/releases/latest/download/cert-manager.yaml
   ```
3. **A ClusterIssuer named `letsencrypt-prod`** using HTTP-01 challenge through Traefik. Save as `cluster-issuer.yaml` and `kubectl apply -f`:
   ```yaml
   apiVersion: cert-manager.io/v1
   kind: ClusterIssuer
   metadata:
     name: letsencrypt-prod
   spec:
     acme:
       email: you@example.com
       server: https://acme-v02.api.letsencrypt.org/directory
       privateKeySecretRef:
         name: letsencrypt-prod-account-key
       solvers:
         - http01:
             ingress:
               class: traefik
   ```
4. **DNS A record** `agent.jesseliu.me` → cluster node public IP, propagated before first apply.

## First-time deploy

```bash
# 1. Create Namespace + Secret (Secret is the only thing not in git).
kubectl apply -f deploy/manifests/namespace.yaml
kubectl -n alfred create secret generic alfred-secret \
  --from-literal=ALFRED_USER=admin \
  --from-literal=ALFRED_PASSWORD='<your strong password>' \
  --from-literal=ALFRED_TOKEN=$(openssl rand -hex 32)

# 2. Build + push image.
make -C deploy push

# 3. Apply the rest.
make -C deploy apply
```

## Verifying

```bash
kubectl -n alfred get pods
kubectl -n alfred get certificate
# Once Ready=True on the certificate:
curl -I https://agent.jesseliu.me/healthz
```

## Rotating credentials

```bash
kubectl -n alfred delete secret alfred-secret
kubectl -n alfred create secret generic alfred-secret \
  --from-literal=ALFRED_USER=admin \
  --from-literal=ALFRED_PASSWORD='<new>' \
  --from-literal=ALFRED_TOKEN=$(openssl rand -hex 32)
make -C deploy rollout-restart
```

## Updating the app

```bash
git pull
make -C deploy push
kubectl -n alfred set image deployment/alfred alfred=ghcr.io/jesseliu/headless-alfred:<new-tag>
kubectl -n alfred rollout status deployment/alfred
```

## Tail logs

```bash
make -C deploy logs
```
```

- [ ] **Step 6.3: Commit**

```bash
git add deploy/Makefile deploy/README.md
git commit -m "feat(deploy): Makefile and operator runbook"
```

---

## Task 7: GitHub Container Registry (GHCR) auth (one-time, manual)

This is a manual operator step (no commit). Document in the runbook above for your future self.

- [ ] **Step 7.1: Create a GHCR PAT and log in**

```bash
# In a browser: github.com → Settings → Developer settings → Personal access tokens
# Create a classic PAT with scope: write:packages, read:packages, delete:packages.
# Then:
echo "$GHCR_PAT" | docker login ghcr.io -u <your-gh-username> --password-stdin
```

- [ ] **Step 7.2: Verify push works**

```bash
make -C deploy push
```

Expected: image pushed to `ghcr.io/jesseliu/headless-alfred:<sha>`.

- [ ] **Step 7.3: Make the package public OR configure imagePullSecret**

If keeping the GHCR image private:
```bash
kubectl -n alfred create secret docker-registry ghcr-pull \
  --docker-server=ghcr.io \
  --docker-username=<your-gh-username> \
  --docker-password=$GHCR_PAT
```
Then patch `deployment.yaml` to reference it:
```yaml
spec:
  template:
    spec:
      imagePullSecrets:
        - name: ghcr-pull
```

(Add to the manifest now or after a failed pull surfaces the need.)

---

## Self-Review Notes

**Spec coverage:**
- §13 cluster prereqs (cert-manager, ClusterIssuer, DNS): documented in deploy/README.md ✓
- §13 Secret created out-of-band: runbook instructs `kubectl create secret` ✓
- §13 Pod resource limits: 100m/128Mi req, 1 CPU/1 Gi limit ✓
- §13 single replica + RWO PVC + Recreate strategy: deployment.yaml ✓
- §10 HTTPS enforced: Ingress on websecure + Traefik middleware redirect from web ✓

**Known points worth flagging:**
- `cert-manager.io/cluster-issuer` annotation works for k3s when the ClusterIssuer is HTTP-01 + Traefik. If the operator has a different issuer (DNS-01, staging), swap the annotation accordingly. README documents the expected flavour.
- `imagePullPolicy: IfNotPresent` is intentional: avoids surprise upstream pulls and matches Plan 5's kind workflow (which loads images via `kind load`, not from a registry). Operators rolling out a new tag should use `kubectl set image` (which sets a new image ref → triggers pull).

What's deferred to Plan 5:
- E2E setup with kind, port-forward, test suite covering 7 spec scenarios.
