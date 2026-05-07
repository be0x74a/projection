<p align="center">
  <img src="docs/assets/logo.svg" alt="projection logo" width="140">
</p>

<h1 align="center">projection</h1>

<p align="center">The Kubernetes operator for declarative resource mirroring — any Kind, conflict-safe, watch-driven. Namespaced <code>Projection</code> for in-namespace mirrors, cluster-scoped <code>ClusterProjection</code> for fan-out across namespaces.</p>

<p align="center">

[![CI](https://github.com/projection-operator/projection/actions/workflows/ci.yml/badge.svg)](https://github.com/projection-operator/projection/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/projection-operator/projection?include_prereleases&sort=semver)](https://github.com/projection-operator/projection/releases)
[![API](https://img.shields.io/badge/API-v1-blue)](docs/api-stability.md)
[![Go Report Card](https://goreportcard.com/badge/github.com/projection-operator/projection)](https://goreportcard.com/report/github.com/projection-operator/projection)
[![OpenSSF Scorecard](https://api.scorecard.dev/projects/github.com/projection-operator/projection/badge)](https://scorecard.dev/viewer/?uri=github.com/projection-operator/projection)
[![OpenSSF Best Practices](https://www.bestpractices.dev/projects/12533/badge)](https://www.bestpractices.dev/projects/12533)
[![License](https://img.shields.io/github/license/projection-operator/projection)](LICENSE)
[![Go Reference](https://pkg.go.dev/badge/github.com/projection-operator/projection.svg)](https://pkg.go.dev/github.com/projection-operator/projection)

</p>

<p align="center">
  <a href="docs/assets/demo.cast">
    <img src="docs/assets/demo.gif" alt="projection demo: apply a Projection, edit the source, watch the destination ConfigMap update in ~100ms" width="720">
  </a>
</p>

`projection` is a Kubernetes operator that mirrors any Kubernetes object — `ConfigMap`, `Secret`, `Service`, your custom resources — from a source location to a destination, declaratively, per resource. Each mirror is its own first-class CR (a namespaced `Projection` for single-target, a cluster-scoped `ClusterProjection` for fan-out across namespaces) with status conditions, events, and a metric you can alert on. Edits to the source propagate to the destination in roughly **100 milliseconds**.

It exists because every team eventually rebuilds this with a one-off controller or a Kyverno `generate` policy, and neither approach is the right shape. `projection` is meant to be the answer when somebody asks "how do you mirror a `Secret` across namespaces in this cluster?"

## Why projection

|  | projection | [emberstack/Reflector] | Kyverno [`generate`] |
|---|---|---|---|
| Works on **any Kind** | ✓ | ConfigMap & Secret only | ✓ |
| Source-of-truth lives **in a CR you can `kubectl get`** | ✓ (`Projection`, `ClusterProjection`) | ✗ (annotations on the source) | ✗ (cluster-wide policy) |
| **Cluster-scoped fan-out CR** | ✓ (`ClusterProjection`) | ✗ | ✓ but policy-shaped, not per-resource |
| **Tenant self-service for in-namespace mirrors** (no cluster-tier authority) | ✓ (namespaced `Projection`, aggregated into `edit`) | ✗ | ✗ |
| **Per-resource status** + Kubernetes Events | ✓ | partial | ✗ |
| **Conflict-safe** (refuses to overwrite unowned objects) | ✓ | ✗ | ✗ |
| **Watch-driven** propagation (~100ms) | ✓ | ✓ | ✓ |
| **Admission-time validation** of source fields | ✓ | n/a | ✓ |
| **Prometheus metrics** per reconcile outcome | ✓ | partial | ✓ |
| Footprint | two CRDs, one Deployment | one CRD, one Deployment | full policy engine |

[emberstack/Reflector]: https://github.com/emberstack/kubernetes-reflector
[`generate`]: https://kyverno.io/docs/writing-policies/generate/

For the longer comparison — including the cases where Reflector or Kyverno is the better choice — see [docs/comparison.md](docs/comparison.md).

## 60-second demo

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: app-config
  namespace: platform
  annotations:
    # Source opts in to projection (default source-mode is "allowlist").
    # Set to "false" to veto projection as the source owner.
    projection.sh/projectable: "true"
data:
  log_level: info
---
apiVersion: projection.sh/v1
kind: Projection
metadata:
  name: app-config-mirror
  namespace: tenant-a            # destination namespace = this
spec:
  source:
    group: ""                    # core API
    version: v1
    kind: ConfigMap
    name: app-config
    namespace: platform
  overlay:
    labels:
      projected-by: projection
```

```console
$ kubectl get projections -A
NAMESPACE   NAME                KIND        SOURCE-NAMESPACE   SOURCE-NAME   DESTINATION   READY   AGE
tenant-a    app-config-mirror   ConfigMap   platform           app-config    app-config    True    2s

$ kubectl get configmap -n tenant-a app-config -o jsonpath='{.metadata.annotations.projection\.sh/owned-by-projection}'
tenant-a/app-config-mirror
```

Edit the source — destination updates within ~100ms.
Delete the `Projection` — destination is removed (only if `projection` still owns it).
Pre-existing object at the destination? `Ready=False reason=DestinationConflict`. We don't overwrite strangers.

Need to mirror into many namespaces from one source? Use `ClusterProjection` (cluster-scoped) with either `destination.namespaces: [a, b, c]` or `destination.namespaceSelector` — the same source, fanned out, with per-namespace status rolled up into `namespacesWritten` / `namespacesFailed`. See [Getting started](docs/getting-started.md#cluster-scoped-fan-out-with-clusterprojection).

## Features

- **Two CRDs, two RBAC tiers** — namespaced `Projection` for in-namespace single-target mirrors (destination namespace is structurally the Projection's own), cluster-scoped `ClusterProjection` for fan-out (`destination.namespaces: [a, b, c]` or `destination.namespaceSelector`). Tenants can self-serve `Projection` via the chart's `rbac.aggregate=true` default; `ClusterProjection` requires an explicit cluster-admin binding.
- **Any Kind** — `RESTMapper`-driven GVR resolution. Works on built-in resources, your CRDs, anything the apiserver knows about. Source uses split `group` + `version` fields; for non-core groups, omitting `version` triggers preferred-version lookup that follows CRD promotions.
- **Watch-driven** — dynamic informer registration per source GVK on first reference. Edits propagate in ~100ms; no periodic polling. A label-filtered destination-side watch (`ensureDestWatch`) makes manual `kubectl delete` of a destination trigger an immediate reconcile.
- **Fan-out across namespaces** — one `ClusterProjection` mirrors its source into every namespace listed in `destination.namespaces` or matching a `destination.namespaceSelector`. Destinations are added and removed as namespaces gain or lose the matching label. Bounded fan-out concurrency keeps the apiserver healthy at scale.
- **Source-owner consent** — default `sourceMode=allowlist` requires sources to carry `projection.sh/projectable="true"`. Source owners can also veto with `="false"` regardless of mode.
- **Conflict-safe** — `projection.sh/owned-by-projection` (or `projection.sh/owned-by-cluster-projection`) annotation marks our destinations. We refuse to overwrite objects we don't own and report `DestinationConflict` on status. Source deletion (404) automatically cleans up every owned destination.
- **Clean deletion** — finalizers remove destinations on CR deletion. The cluster CRD's finalizer sweeps every owned destination across the cluster; the namespaced CRD's finalizer cleans up its single in-namespace destination. If ownership has been stripped, we leave the object alone.
- **Observable** — three status conditions (`SourceResolved`, `DestinationWritten`, `Ready`), `events.k8s.io/v1` Events with `action` verbs (Create/Update/Delete/Get/Validate/Resolve/Write), per-fan-out counters (`status.namespacesWritten`, `status.namespacesFailed`), and Prometheus metrics (`projection_reconcile_total{kind,result}`, `projection_watched_gvks`, `projection_watched_dest_gvks`).
- **Validated at admission** — `Source` fields are pattern-validated (DNS-1123 names, PascalCase Kinds) so typos fail at `kubectl apply`, not at runtime. CEL enforces `version` required when `group` is empty, and `namespaces` ⊕ `namespaceSelector` mutual exclusion on `ClusterProjection.destination`.
- **Smart copy** — strips server-owned metadata, drops `.status`, removes `kubectl.kubernetes.io/last-applied-configuration`, strips Kind-specific apiserver-allocated spec fields (Service `clusterIP`/`clusterIPs`, PVC `volumeName`, Pod `nodeName`, Job `selector`+controller-uid labels), and preserves them on update.
- **Production-grade Helm chart** — opt-in `ServiceMonitor`, `NetworkPolicy` (egress lockdown), and `PodDisruptionBudget` templates. Three ClusterRoles for tenant self-service vs cluster-tier authority. Operational tuning via `requeueInterval`, `leaderElection.leaseDuration`, and `selectorWriteConcurrency`. RBAC scope narrowable via `supportedKinds`.
- **Small** — two CRDs, one Deployment, one container. Distroless image, multi-arch (amd64, arm64).

## Quick start

### Helm

```bash
helm install projection oci://ghcr.io/projection-operator/charts/projection \
  --version 0.3.0 \
  --namespace projection-system --create-namespace
```

### `kubectl apply`

```bash
kubectl apply -f https://github.com/projection-operator/projection/releases/download/v0.3.0/install.yaml
```

Then create your first `Projection`:

```bash
kubectl apply -f https://raw.githubusercontent.com/projection-operator/projection/main/examples/configmap-cross-namespace.yaml
kubectl get projections -A
```

## How it works

When you create a `Projection` or `ClusterProjection`, the controller resolves the source GVR via the `RESTMapper`, fetches the source object via the dynamic client, builds a sanitized destination object (overlay applied, ownership annotation stamped, server-owned metadata stripped), and creates or updates the destination — but only if `projection` already owns it. The first reconcile also registers a metadata-only watch on the source's GVK, so future edits to *any* source of that Kind enqueue the relevant CRs via a field-indexed lookup. A label-filtered watch on the destination GVK (`ensureDestWatch`) catches manual deletion or drift and triggers an immediate reconcile. Updates that wouldn't change the destination are skipped to avoid noisy events and metric churn. For `ClusterProjection`, fan-out writes are issued in parallel with a configurable concurrency cap.

See [docs/concepts.md](docs/concepts.md) for the full picture, [docs/observability.md](docs/observability.md) for status/events/metrics, and [docs/comparison.md](docs/comparison.md) for the deep comparison vs Reflector and Kyverno.

## Use cases

- **Secrets across namespaces** — distribute a TLS cert from `cert-manager` to many application namespaces with one `ClusterProjection`, or to a single application namespace with a namespaced `Projection`.
- **Shared config distribution** — one `ConfigMap` in `platform`, fanned out into every labeled tenant namespace via `ClusterProjection`'s `namespaceSelector`. Per-destination overlays (a different `tenant:` label per copy) work too — declare one namespaced `Projection` per destination instead.
- **Tenant self-service** — give a namespace owner `edit` on `tenant-a` (chart default `rbac.aggregate=true`) and they can author `Projection`s pulling shared sources into `tenant-a` without any cluster-tier authority.
- **Service mirroring** — expose a backend `Service` from one namespace into another without a manual `ExternalName` dance.
- **CR replication** — mirror an `Issuer`, a `KafkaTopic`, or any custom resource between namespaces in the same cluster.

## Limitations

- **Same-cluster only.** Cross-cluster mirroring is a non-goal for v0.
- **Cluster-scoped Kinds rejected.** `projection` only mirrors namespaced resources. Pointing at a `Namespace`, `ClusterRole`, or `StorageClass` surfaces `SourceResolved=False reason=SourceResolutionFailed` with a clear message.
- **`ClusterProjection` fan-out shares one overlay.** All destinations in a `ClusterProjection` get the same overlay; per-destination overlays require multiple namespaced `Projection`s — see [`examples/multiple-destinations-from-one-source.yaml`](examples/multiple-destinations-from-one-source.yaml).
- **A few Kinds need extra care.** `Service`, `PersistentVolumeClaim`, `Pod`, and `Job` have apiserver-allocated spec fields handled out of the box. Jobs created with `spec.manualSelector: true` are not supported. Other Kinds with similar fields (rare) may need an addition to `droppedSpecFieldsByGVK` — see [limitations](docs/limitations.md#some-kinds-need-extra-stripping-rules).
- **Pre-1.0.** API stability commitments (which fields will not change, how breaking changes are handled) are documented in [docs/api-stability.md](docs/api-stability.md). v0.3.0 is itself a breaking change. CRD storage version is `v1`; future versions will be served alongside with conversion.

## Documentation

- [Getting started](docs/getting-started.md)
- [Concepts](docs/concepts.md)
- [API reference](docs/api-reference.md) (auto-generated from `api/v1/*.go`)
- [CRD behavior and examples](docs/crd-reference.md)
- [Use cases](docs/use-cases.md)
- [Comparison vs alternatives](docs/comparison.md)
- [Observability](docs/observability.md)
- [Security model](docs/security.md)
- [API stability](docs/api-stability.md)
- [Troubleshooting](docs/troubleshooting.md)
- [Scale and benchmarks](docs/scale.md)
- [Limitations & roadmap](docs/limitations.md)

## Contributing

Pull requests welcome. See [CONTRIBUTING.md](CONTRIBUTING.md). Be excellent to each other — see [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md).

## Security

Found a vulnerability? Please report it privately via [GitHub Security Advisories](https://github.com/projection-operator/projection/security/advisories/new). See [SECURITY.md](SECURITY.md).

## License

Apache 2.0. See [LICENSE](LICENSE).
