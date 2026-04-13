# Release checklist

Steps that have to happen *outside* the repo (on GitHub itself, on your
container registry, etc.) when publishing `projection`. The repo's CI handles
the rest.

## One-time GitHub setup

After pushing the repo to `https://github.com/be0x74a/projection`:

### Repo metadata

```
gh repo edit be0x74a/projection \
  --description "The Kubernetes CRD for declarative resource mirroring across namespaces — any Kind, conflict-safe, watch-driven." \
  --homepage "https://be0x74a.github.io/projection" \
  --add-topic kubernetes \
  --add-topic kubernetes-operator \
  --add-topic kubernetes-controller \
  --add-topic crd \
  --add-topic mirror \
  --add-topic configmap \
  --add-topic secret \
  --add-topic reflector \
  --add-topic golang \
  --add-topic kubebuilder
```

### Repo settings to flip in the UI

- **Settings → General → Features:** enable Issues, Discussions, Sponsorships (optional).
- **Settings → Pages:** Source = "GitHub Actions" (the `docs.yml` workflow deploys mkdocs).
- **Settings → Branches → Add rule for `main`:** require status checks (at minimum the `lint`, `test`, `build`, `e2e` jobs from `ci.yml`); require linear history; require signed commits if you want.
- **Settings → Code security:** enable Dependabot alerts, security updates, secret scanning.
- **Settings → Actions → General:** allow GoReleaser to read/write packages (`Workflow permissions: Read and write`, `Allow GitHub Actions to create and approve PRs: enabled`).
- **Settings → Pages → Custom domain (optional):** if you wire up `projection.dev` later.

### Social preview image

Upload a 1280×640 PNG at **Settings → Social preview**. Until you have a real one, the placeholder logo at `docs/assets/logo.svg` is referenced in the README — that's enough to get going.

## First release

Tag and push:

```
git tag -a v0.1.0-alpha -m "First public alpha release"
git push origin v0.1.0-alpha
```

The `release.yml` workflow will:
1. Build multi-arch binaries (linux/amd64, linux/arm64, darwin/amd64, darwin/arm64) via GoReleaser.
2. Build and push the multi-arch container image to `ghcr.io/be0x74a/projection:v0.1.0-alpha` and `:latest`.
3. Sign the image with cosign (keyless, OIDC).
4. Generate an SBOM (SPDX) and attach it to the image.
5. Create a GitHub Release with auto-generated notes from Conventional Commits.
6. Attach `dist/install.yaml` to the Release.

The `helm-release.yml` workflow will package and push the chart to `oci://ghcr.io/be0x74a/charts/projection` at the same tag.

## After the release

- **Mark the package public:** at `https://github.com/be0x74a/packages/container/projection/settings`, set visibility to Public. Same for the Helm chart at `https://github.com/be0x74a/packages/container/charts%2Fprojection/settings`.
- **Submit to artifacthub.io:** add the OCI Helm repo at `oci://ghcr.io/be0x74a/charts`. Auto-discovers versions thereafter.
- **Pin the repo** on your GitHub profile.

## Pre-release smoke test

Before the public announcement, install the published artifacts on a fresh Kind cluster:

```
kind create cluster --name projection-smoke
kubectl apply -f https://github.com/be0x74a/projection/releases/download/v0.1.0-alpha/install.yaml
# Wait for the operator
kubectl -n projection-system rollout status deploy/projection-controller-manager
# Apply an example
kubectl apply -f https://raw.githubusercontent.com/be0x74a/projection/v0.1.0-alpha/examples/configmap-cross-namespace.yaml
kubectl get projections -A
# Verify
kubectl get cm -n tenant-a app-config -o jsonpath='{.metadata.annotations.projection\.be0x74a\.io/owned-by}'
kind delete cluster --name projection-smoke
```

If anything fails here, fix it before announcing.

## Soft-launch channels

Not in the repo's scope, but a checklist for posting:

- Show HN: title `Show HN: projection — declarative resource mirroring CRD for Kubernetes`. Lead with the comparison vs Reflector.
- r/kubernetes: same content, more relaxed tone.
- CNCF Slack `#operators` and `#sig-apimachinery`.
- Twitter/X / Mastodon / Bluesky thread with the demo from the README.
- Submit to `awesome-kubernetes` and `awesome-operators` lists.
