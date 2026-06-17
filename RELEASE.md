# Release flow

Branch model: **`main` is production**, **`next` is the staging area**
for unreleased features.

```
main           ← what's running on oracle k3s right now.
                 Push to main → CI builds + deploys, immediately live.
                 Tagged with semver (v0.1, v0.2, ...) at each release.

next           ← long-lived branch for work-in-progress features.
                 No CI deploy on push. Accumulate commits here until
                 they're worth shipping together.
                 Branch off main; merge back into main when ready.
```

## Daily development

Work happens on `next`:

```bash
git checkout next
# ... change files, commit ...
git push origin next        # safe, doesn't trigger deploy
```

You can push as often as you want; only `main` pushes trigger the
oracle deploy workflow.

If a hotfix is needed straight to prod (skipping next), branch from
main, do the fix, merge back to main directly:

```bash
git checkout main
git checkout -b fix/something
# ... fix ...
git checkout main && git merge fix/something
git push origin main        # ← deploys immediately
git branch -d fix/something
```

Then sync next so the fix doesn't get reverted next time next gets
merged:

```bash
git checkout next && git merge main
```

## Cutting a release

When `next` has enough to ship as a new version:

1. **Bump the version** in `deploy/helm/alfred/Chart.yaml`:

   ```yaml
   appVersion: "v0.2"   # was v0.1
   ```

   Commit this on `next` (or directly on main during the merge — both work).

2. **Add a release entry** to `docs/RELEASES.md` describing what
   changed (one-liner per user-facing change is enough). Commit.

3. **Merge `next` into `main`**:

   ```bash
   git checkout main
   git merge next --no-ff -m "release: v0.2"
   ```

   `--no-ff` keeps a merge commit even when fast-forward is possible,
   so the release boundary is visible in `git log --graph`.

4. **Tag the release**:

   ```bash
   git tag v0.2
   git push origin main v0.2
   ```

5. **CI takes over**: the push to main fires
   `.github/workflows/deploy-oracle.yml`, which rebuilds the image,
   helm-upgrades the chart, and rolls the pod. ~45s, ~10s downtime.

6. **Verify**:

   ```bash
   ssh oracle "KUBECONFIG=/etc/rancher/k3s/k3s.yaml \
     kubectl -n alfred exec deploy/alfred -- alfred-server --version 2>&1 || true"
   # or just: hit the app and look at whatever your new feature was
   ```

7. **Reset next** so it tracks main again:

   ```bash
   git checkout next
   git merge main          # absorbs the release-version-bump commit
   git push origin next
   ```

## Versioning

Manual semver. We decide what counts as a release.

- **Patch (v0.2.1)**: bug fixes only, no new user-visible behaviour.
- **Minor (v0.2.0)**: new features, no breaking changes.
- **Major (v1.0.0)**: anything that would force the user to do
  something different (re-login, re-configure, etc.) — also "I'm
  declaring this stable".

Right now we're pre-1.0 so the rules are softer; bump minor for
roughly any feature ship.

## Rollback

If a deploy breaks:

1. **Fast rollback (kept by CI):** the workflow auto-rollbacks on
   smoke-test failure. Check Actions tab — your run will already be
   showing the rollback step. Nothing to do.

2. **Manual rollback** (deploy succeeded but something's wrong in
   practice):

   ```bash
   ssh oracle "KUBECONFIG=/etc/rancher/k3s/k3s.yaml \
     helm -n alfred history alfred"
   # pick the previous revision number, then:
   ssh oracle "KUBECONFIG=/etc/rancher/k3s/k3s.yaml \
     helm -n alfred rollback alfred <revision>"
   ```

3. **Code rollback** (you want the next deploy to NOT include the
   broken change):

   ```bash
   git checkout main
   git revert <bad-commit-sha>
   git push origin main      # triggers a fresh deploy of the reverted state
   ```

PVC data (sessions, creds, Claude history) is unaffected by any
rollback — `/data` and `/home/alfred` are persistent across deploys.

## Release history

See `docs/RELEASES.md`.
