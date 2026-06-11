# Headless Alfred — Deploy

Target cluster: **k3s** with the default Traefik + a few extras (see prerequisites). DNS: **`agent.jesseliu.me`**.

All commands below assume you're at the repo root.

---

## Cluster prerequisites (one-time)

These exist outside this app's manifests and must be set up before applying anything in `manifests/`:

### 1. k3s installed with Traefik

k3s ships Traefik enabled by default. If you installed k3s with `--disable=traefik`, re-enable it or swap in a different Ingress controller (Traefik annotations would need to be adapted).

### 2. cert-manager

```bash
kubectl apply -f https://github.com/cert-manager/cert-manager/releases/latest/download/cert-manager.yaml
kubectl -n cert-manager rollout status deployment/cert-manager-webhook
```

### 3. A `letsencrypt-prod` ClusterIssuer using HTTP-01 through Traefik

```yaml
# cluster-issuer.yaml
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

```bash
kubectl apply -f cluster-issuer.yaml
```

### 4. DNS

`agent.jesseliu.me` A record → the cluster node's public IP, propagated **before** the first apply. Without DNS the HTTP-01 challenge cannot succeed and the cert request will sit in `False` forever.

### 5. (Optional) Image-pull secret if GHCR package is private

```bash
echo "$GHCR_PAT" | docker login ghcr.io -u <your-gh-username> --password-stdin
kubectl -n alfred create secret docker-registry ghcr-pull \
  --docker-server=ghcr.io \
  --docker-username=<your-gh-username> \
  --docker-password=$GHCR_PAT
```

Then add to `manifests/deployment.yaml` under `spec.template.spec`:

```yaml
imagePullSecrets:
  - name: ghcr-pull
```

If you make the GHCR package public, skip this entirely.

---

## First-time deploy

```bash
# 1. Namespace must exist before the Secret.
kubectl apply -f deploy/manifests/namespace.yaml

# 2. Create the Secret out-of-band — it is NOT in git.
kubectl -n alfred create secret generic alfred-secret \
  --from-literal=ALFRED_USER=admin \
  --from-literal=ALFRED_PASSWORD='<your strong password>' \
  --from-literal=ALFRED_TOKEN=$(openssl rand -hex 32)

# 3. Build + push the image (override REGISTRY if not yours).
make -C deploy push

# 4. Apply the rest.
make -C deploy apply
```

---

## Verifying after deploy

```bash
make -C deploy status
```

Wait until:
- Deployment `alfred` shows `1/1` available
- `Certificate alfred-tls` Ready=True (cert-manager logs in `cert-manager` namespace if stuck)
- Ingress reports an address

Then:

```bash
curl -I https://agent.jesseliu.me/healthz
# HTTP/2 200
```

---

## Updating the app

After a code change:

```bash
git pull
make -C deploy push           # rebuild + push :SHORT_SHA
make -C deploy set-image      # rolling update Pod to the new image
```

If you need to pin a specific tag:

```bash
make -C deploy push set-image TAG=v1.2.3
```

---

## Rotating credentials

```bash
kubectl -n alfred delete secret alfred-secret
kubectl -n alfred create secret generic alfred-secret \
  --from-literal=ALFRED_USER=admin \
  --from-literal=ALFRED_PASSWORD='<new>' \
  --from-literal=ALFRED_TOKEN=$(openssl rand -hex 32)
make -C deploy rollout-restart
```

The bash session is reset. Tokens issued before rotation become invalid; users must log in again.

---

## Tail logs

```bash
make -C deploy logs
```

JSON lines, one per log entry. `jq` works well: `make -C deploy logs | jq .`

---

## Teardown (keep history)

```bash
make -C deploy teardown
```

This removes Deployment/Service/Ingress/Secret but **keeps** the PVC and Namespace, so the JSON history files survive. Re-apply later to resume.

To truly drop everything including history:

```bash
kubectl delete namespace alfred
```

---

## Common failure modes

| Symptom | Likely cause | Fix |
|---|---|---|
| Pod `CrashLoopBackOff` with "ALFRED_USER ... must all be set" | Secret missing or wrong keys | Recreate Secret per first-deploy step 2 |
| Pod `Pending`, PVC unbound | No storage class / no PV available | `kubectl get pv` and `kubectl describe pvc -n alfred alfred-data` |
| `Certificate` Ready=False forever | DNS not resolving to cluster yet, or wrong ClusterIssuer | `kubectl describe certificate -n alfred alfred-tls` shows the ACME error |
| `503` at `https://agent.jesseliu.me` | Pod not Ready yet, or wrong Service selector | `make -C deploy status` |
| `Bad gateway` on WS connect | Traefik handles WS fine by default — usually means Pod restarted mid-connect. Reconnect logic in the client handles this transparently. | — |
