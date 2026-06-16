# Deploy — Headless Alfred

One target: the **Oracle k3s box** (same cluster as NowYouSeeMe). The
shape: single-pod Deployment behind Traefik, **no public TLS, no public
DNS** — you reach it from a client by sshing a port-forward tunnel and
using `/etc/hosts` to point `alfred.local` at your local end of the
tunnel. Same pattern NYSM uses today.

CI deploys on every push to `main`; manual deploys are a one-liner.

---

## Continuous deploy (the everyday path)

`git push origin main` triggers `.github/workflows/deploy-oracle.yml`:

1. GHA runner checks out the repo + loads `ORACLE_SSH_KEY` into ssh-agent.
2. `rsync` to `oracle:/root/headless-alfred/`.
3. SSH to oracle → `scripts/build-on-oracle.sh` (nerdctl into k3s containerd).
4. SSH to oracle → `scripts/deploy-oracle.sh` (`helm upgrade --install`).
5. Force `kubectl rollout restart` (image tag is `:local`, k8s can't tell the underlying digest changed).
6. Smoke `kubectl exec deploy/alfred -- wget -qO- :8080/healthz`.
7. Any failure → `helm rollback` to the snapshotted previous revision.

Watch a run: GitHub → Actions → "Deploy to Oracle k3s".

---

## Required GitHub Secrets

Add under **Settings → Secrets and variables → Actions → New repository secret**.

Reused from the NYSM deploy (only add if missing):

| Secret | What it is |
|---|---|
| `ORACLE_SSH_KEY` | Private SSH key, full file contents. Public counterpart must be in `oracle:/root/.ssh/authorized_keys`. |
| `ORACLE_KNOWN_HOSTS` | Output of `ssh-keyscan -t ed25519,rsa <oracle-host>` from a trusted machine. Pins the host key so a compromised DNS can't MITM the SSH session. |
| `ORACLE_HOST` | Oracle public IP or hostname. |
| `ORACLE_USER` | SSH user. `root` matches the NYSM setup. |

New for Alfred:

| Secret | What it is |
|---|---|
| `ALFRED_USER` | Single-user login username. |
| `ALFRED_PASSWORD` | Login password. |
| `ALFRED_TOKEN` | Long-random bearer token the frontend sends as `Authorization: Bearer <...>` after login. Treat as a password. Generate with `openssl rand -hex 32`. |

> Anthropic API key is **not** a secret here — users paste it into the
> in-app `Claude credentials` dialog after first login. It lands in
> `~/.claude/.credentials.json` inside the pod, persisted to the PVC.

---

## Client setup (your Mac, your phone, any laptop)

The pod has no public DNS. You reach it via SSH port-forwarding +
local hostname.

### 1. `/etc/hosts`

```
127.0.0.1   alfred.local
```

(Phone/iPad: not directly possible — use Tailscale instead, see below.)

### 2. SSH tunnel (run on each client)

```bash
ssh -N -L 80:127.0.0.1:80 root@<oracle-host>
```

Background it:

```bash
ssh -fN -L 80:127.0.0.1:80 root@<oracle-host>
```

If local port 80 is taken (a webserver, etc.) bind a high port and
adjust the URL:

```bash
ssh -fN -L 8888:127.0.0.1:80 root@<oracle-host>
# then visit http://alfred.local:8888/
```

`Host: alfred.local` is what Traefik routes on, so the hostname has
to match — pick a different port, not a different hostname.

### 3. Open

`http://alfred.local/` (or `:8888` per above). Log in with
`ALFRED_USER` / `ALFRED_PASSWORD`. Done.

### Phone access (Tailscale alternative)

iOS/Android can't edit `/etc/hosts`. Two practical options:

- **Tailscale Funnel/Serve.** Install Tailscale on the oracle box,
  expose the k3s Traefik ingress over Tailscale, install the
  Tailscale app on your phone. You get an `https://*.ts.net`
  hostname that "just works" without certs or port-forwards. Out
  of scope for this README.
- **SSH client app** (Termius, Blink, etc.) that supports
  port-forwarding. Slower to set up; not as fluid as Tailscale.

---

## Manual deploy (from your laptop, no CI)

```bash
ssh root@<oracle-host>
cd /root/headless-alfred   # or rsync your local repo first
bash scripts/build-on-oracle.sh
ALFRED_USER='admin' ALFRED_PASSWORD='...' ALFRED_TOKEN='...' \
  bash scripts/deploy-oracle.sh
```

The deploy script refuses to run without the three env vars set —
this is on purpose, the chart's `values.yaml` ships `REPLACE_ME`
placeholders and we never want those to reach the cluster.

---

## Ops cheatsheet

All from the oracle box (or `ssh root@<oracle-host> -- <cmd>`).

```bash
# Tail logs
kubectl -n alfred logs -f deploy/alfred

# Shell into the pod
kubectl -n alfred exec -it deploy/alfred -- bash

# Force restart (no code change)
kubectl -n alfred rollout restart deploy/alfred

# Inspect /data usage
kubectl -n alfred exec deploy/alfred -- df -h /data
kubectl -n alfred exec deploy/alfred -- du -sh /data/*

# Helm release history
helm -n alfred history alfred

# Manual rollback to revision N
helm -n alfred rollback alfred N

# Get current Helm values (what's actually live)
helm -n alfred get values alfred

# Nuke everything (uninstall + drop PVC + drop namespace)
helm -n alfred uninstall alfred
kubectl -n alfred delete pvc alfred-data
kubectl delete namespace alfred
```

---

## Files

```
deploy/
├── README.md                       ← you are here
├── alfred-claude-bridge.sh         baked into the image, used by Claude PreToolUse hook
├── entrypoint.sh                   pid 1 inside the container (respawn loop for alfred-server)
└── helm/alfred/
    ├── Chart.yaml
    ├── values.yaml                 defaults (with REPLACE_ME auth)
    ├── values-oracle.yaml          oracle overlay (traefik ingressClass, alfred.local host)
    └── templates/
        ├── _helpers.tpl
        ├── deployment.yaml         single replica, Recreate strategy, /healthz + /readyz probes
        ├── service.yaml            ClusterIP :80 → pod :8080
        ├── ingress.yaml            traefik, host alfred.local, TLS optional via tls.enabled
        ├── pvc.yaml                5Gi default
        └── secret-auth.yaml        ALFRED_USER/PASSWORD/TOKEN

scripts/
├── build-on-oracle.sh              nerdctl build into k3s containerd
└── deploy-oracle.sh                helm upgrade --install with auth env preflight

.github/workflows/
└── deploy-oracle.yml               push-to-main → CI deploy
```

---

## If you need public TLS later

Steps if you ever drop the SSH-tunnel model in favor of public HTTPS:

1. Point a real DNS `A` record at the oracle box.
2. Install cert-manager on the cluster.
3. Create a `letsencrypt-prod` ClusterIssuer (HTTP-01 via Traefik).
4. In `values-oracle.yaml`:
   - `ingress.tls.enabled: true`
   - `ingress.tls.clusterIssuer: letsencrypt-prod`
   - `ingress.host: alfred.<your-domain>`
5. Redeploy. The chart already supports the toggle.

But: the SSH-tunnel pattern is **simpler, has a smaller attack surface,
and works exactly as well for personal use**. Defer this work until you
actually need public access.
