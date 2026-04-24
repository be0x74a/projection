# projection

> The Kubernetes CRD for declarative resource mirroring across namespaces — any Kind, conflict-safe, watch-driven.

[![CI](https://github.com/be0x74a/projection/actions/workflows/ci.yml/badge.svg)](https://github.com/be0x74a/projection/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/be0x74a/projection?include_prereleases&sort=semver)](https://github.com/be0x74a/projection/releases)
[![API](https://img.shields.io/badge/API-v1-blue)](docs/api-stability.md)
[![Go Report Card](https://goreportcard.com/badge/github.com/be0x74a/projection)](https://goreportcard.com/report/github.com/be0x74a/projection)
[![OpenSSF Scorecard](https://api.scorecard.dev/projects/github.com/be0x74a/projection/badge)](https://scorecard.dev/viewer/?uri=github.com/be0x74a/projection)
[![OpenSSF Best Practices](https://www.bestpractices.dev/projects/12533/badge)](https://www.bestpractices.dev/projects/12533)
[![License](https://img.shields.io/github/license/be0x74a/projection)](LICENSE)
[![Go Reference](https://pkg.go.dev/badge/github.com/be0x74a/projection.svg)](https://pkg.go.dev/github.com/be0x74a/projection)

`projection` is a Kubernetes operator that mirrors any Kubernetes object — `ConfigMap`, `Secret`, `Service`, your custom resources — from a source location to a destination, declaratively, per resource. Each `Projection` CR is its own first-class object with status conditions, events, and a metric you can alert on. Edits to the source propagate to the destination in roughly **100 milliseconds**.

It exists because every team eventually rebuilds this with a one-off controller or a Kyverno `generate` policy, and neither approach is the right shape. `projection` is meant to be the answer when somebody asks "how do you mirror a `Secret` across namespaces in this cluster?"

## Why projection

|  | projection | [emberstack/Reflector] | Kyverno [`generate`] |
|---|---|---|---|
| Works on **any Kind** | ✓ | ConfigMap & Secret only | ✓ |
| Source-of-truth lives **in a CR you can `kubectl get`** | ✓ (`Projection`) | ✗ (annotations on the source) | ✗ (cluster-wide policy) |
| **Per-resource status** + Kubernetes Events | ✓ | partial | ✗ |
| **Conflict-safe** (refuses to overwrite unowned objects) | ✓ | ✗ | ✗ |
| **Watch-driven** propagation (~100ms) | ✓ | ✓ | ✓ |
| **Admission-time validation** of source fields | ✓ | n/a | ✓ |
| **Prometheus metrics** per reconcile outcome | ✓ | partial | ✓ |
| Footprint | one CRD, one Deployment | one CRD, one Deployment | full policy engine |

[emberstack/Reflector]: https://github.com/emberstack/kubernetes-reflector
[`generate`]: https://kyverno.io/docs/writing-policies/generate/

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
    projection.be0x74a.io/projectable: "true"
data:
  log_level: info
---
apiVersion: projection.be0x74a.io/v1
kind: Projection
metadata:
  name: app-config-into-tenants
  namespace: platform
spec:
  source:
    apiVersion: v1
    kind: ConfigMap
    name: app-config
    namespace: platform
  destination:
    namespace: tenant-a
  overlay:
    labels:
      projected-by: projection
```

```console
$ kubectl get projections -A
NAMESPACE   NAME                       KIND        SOURCE-NAMESPACE   SOURCE-NAME   DESTINATION   READY   AGE
platform    app-config-into-tenants    ConfigMap   platform           app-config    app-config    True    2s

$ kubectl get configmap -n tenant-a app-config -o jsonpath='{.metadata.annotations.projection\.be0x74a\.io/owned-by}'
platform/app-config-into-tenants
```

Edit the source — destination updates within ~100ms.
Delete the `Projection` — destination is removed (only if `projection` still owns it).
Pre-existing object at the destination? `Ready=False reason=DestinationConflict`. We don't overwrite strangers.

## Features

- ✅ **Any Kind** — `RESTMapper`-driven GVR resolution. Works on built-in resources, your CRDs, anything the apiserver knows about.
- ⚡ **Watch-driven** — dynamic informer registration per source GVK. Edits propagate in ~100ms; no periodic polling.
- 🔒 **Conflict-safe** — ownership annotation marks our destinations. We refuse to overwrite objects we don't own and report `DestinationConflict` on status.
- 🧹 **Clean deletion** — finalizer removes the destination on `Projection` deletion. If ownership has been stripped, we leave the object alone.
- 📊 **Observable** — three status conditions (`SourceResolved`, `DestinationWritten`, `Ready`), Kubernetes Events for every state transition, and a `projection_reconcile_total{result}` Prometheus counter.
- 🛡️ **Validated at admission** — `Source` fields are pattern-validated (DNS-1123 names, PascalCase Kinds) so typos fail at `kubectl apply`, not at runtime.
- 🪞 **Smart copy** — strips server-owned metadata, drops `.status`, removes `kubectl.kubernetes.io/last-applied-configuration`, and preserves apiserver-allocated fields like `Service.spec.clusterIP` on update.
- 🪶 **Small** — one CRD, one Deployment, one container. Distroless image, multi-arch (amd64, arm64).

## Quick start

### Helm

```bash
helm install projection oci://ghcr.io/be0x74a/charts/projection \
  --version 0.1.0-alpha.1 \
  --namespace projection-system --create-namespace
```

### `kubectl apply`

```bash
kubectl apply -f https://github.com/be0x74a/projection/releases/download/v0.1.0-alpha.1/install.yaml
```

Then create your first `Projection`:

```bash
kubectl apply -f https://raw.githubusercontent.com/be0x74a/projection/main/examples/configmap-cross-namespace.yaml
kubectl get projections -A
```

## How it works

When you create a `Projection`, the controller resolves the source GVR via the `RESTMapper`, fetches the source object via the dynamic client, builds a sanitized destination object (overlay applied, ownership annotation stamped, server-owned metadata stripped), and creates or updates the destination — but only if `projection` already owns it. The first reconcile also registers a metadata-only watch on the source's GVK, so future edits to *any* source of that Kind enqueue the relevant `Projections` via a field-indexed lookup. Updates that wouldn't change the destination are skipped to avoid noisy events and metric churn.

See [docs/concepts.md](docs/concepts.md) for the full picture, [docs/observability.md](docs/observability.md) for status/events/metrics, and [docs/comparison.md](docs/comparison.md) for the deep comparison vs Reflector and Kyverno.

## Use cases

- **Secrets across namespaces** — distribute a TLS cert from `cert-manager` to multiple application namespaces without manual `kubectl create`.
- **Shared config distribution** — one `ConfigMap` in `platform`, mirrored into each tenant namespace with overlay labels for tenant tagging.
- **Service mirroring** — expose a backend `Service` from one namespace into another without a manual `ExternalName` dance.
- **CR replication** — mirror an `Issuer`, a `KafkaTopic`, or any custom resource between namespaces in the same cluster.

## v0 limitations (and what's planned)

- **Single destination per `Projection`.** Multi-destination fan-out via label selector is on the roadmap; for now declare one `Projection` per destination namespace.
- **Same-cluster only.** Cross-cluster mirroring is a non-goal for v0.
- **A few Kinds need extra care.** `Service`, `PersistentVolumeClaim`, `Pod`, and `Job` have apiserver-allocated spec fields handled out of the box. Other Kinds with similar fields (rare) may need an addition to `droppedSpecFieldsByGVK` — see [limitations](docs/limitations.md#some-kinds-need-extra-stripping-rules).
- **Alpha.** API may change before v1.0.0. CRD storage version is `v1`; future versions will be served alongside with conversion.

## Documentation

- [Getting started](docs/getting-started.md)
- [Concepts](docs/concepts.md)
- [API reference](docs/api-reference.md)
- [CRD behavior and examples](docs/crd-reference.md)
- [Use cases](docs/use-cases.md)
- [Comparison vs alternatives](docs/comparison.md)
- [Observability](docs/observability.md)
- [Security model](docs/security.md)
- [Limitations & roadmap](docs/limitations.md)

## Contributing

Pull requests welcome. See [CONTRIBUTING.md](CONTRIBUTING.md). Be excellent to each other — see [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md).

## Security

Found a vulnerability? Please report it privately via [GitHub Security Advisories](https://github.com/be0x74a/projection/security/advisories/new). See [SECURITY.md](SECURITY.md).

## License

Apache 2.0. See [LICENSE](LICENSE).
