# projection

> The Kubernetes CRDs for declarative resource mirroring across namespaces — any Kind, conflict-safe, watch-driven.

`projection` is a Kubernetes operator that mirrors any Kubernetes object — `ConfigMap`, `Secret`, `Service`, your custom resources — from a source location to a destination, declaratively, per resource. Each `Projection` (namespaced, single-target) or `ClusterProjection` (cluster-scoped, fan-out) is its own first-class object with status conditions, events, and Prometheus metrics you can alert on. Edits to the source propagate to the destination in roughly **100 milliseconds**, and a manual `kubectl delete` of a destination self-heals on the next reconcile via destination watches.

It exists because every team eventually rebuilds this with a one-off controller or a Kyverno `generate` policy, and neither approach is the right shape. `projection` is meant to be the answer when somebody asks *"how do you mirror a Secret across namespaces in this cluster?"* — and to give namespace tenants a self-service path that doesn't need cluster-tier authority for the common case.

## Why projection

|                                                           | projection                          | emberstack/Reflector          | Kyverno `generate`   |
| --------------------------------------------------------- | ----------------------------------- | ----------------------------- | -------------------- |
| Works on **any Kind**                                     | yes                                 | ConfigMap & Secret only       | yes                  |
| Source-of-truth in a **CR you can `kubectl get`**         | yes (`Projection`/`ClusterProjection`) | no (annotations on the source)| no (cluster policy)  |
| **Per-resource status** + Kubernetes Events               | yes                                 | partial                       | no                   |
| **Conflict-safe** (refuses to overwrite unowned objects)  | yes                                 | no                            | no                   |
| **Watch-driven** propagation (~100 ms)                    | yes                                 | yes                           | yes                  |
| **Destination self-healing** on manual `kubectl delete`   | yes                                 | yes                           | yes                  |
| **Admission-time validation** of source fields            | yes                                 | n/a                           | yes                  |
| **Cluster-scoped fan-out CR**                             | yes (`ClusterProjection`)           | no                            | yes (cluster policy) |
| **Namespace-tier self-service** via standard `edit` role  | yes (`rbac.aggregate`)              | no                            | no                   |
| **Prometheus metrics** per reconcile outcome              | yes (`{kind, result}`)              | partial                       | yes                  |
| Footprint                                                 | two CRDs + Deployment               | one CRD + Deployment          | full policy engine   |

See [vs alternatives](comparison.md) for the full comparison.

## 60-second demo

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: app-config
  namespace: platform
  annotations:
    projection.sh/projectable: "true"
data:
  log_level: info
---
apiVersion: projection.sh/v1
kind: Projection
metadata:
  name: app-config-mirror
  namespace: tenant-a
spec:
  source:
    group: ""
    version: v1
    kind: ConfigMap
    namespace: platform
    name: app-config
  overlay:
    labels:
      projected-by: projection
```

```console
$ kubectl get projections -n tenant-a
NAME                KIND        SOURCE-NAMESPACE   SOURCE-NAME   DESTINATION   READY
app-config-mirror   ConfigMap   platform           app-config    app-config    True

$ kubectl get configmap -n tenant-a app-config \
    -o jsonpath='{.metadata.annotations.projection\.sh/owned-by-projection}'
tenant-a/app-config-mirror
```

- Edit the source — the destination updates within ~100 ms.
- `kubectl delete configmap` the destination — `ensureDestWatch` triggers an immediate reconcile and recreates it.
- Delete the `Projection` — the destination is removed (only if `projection` still owns it).
- Pre-existing object at the destination? `Ready=False reason=DestinationConflict`. We don't overwrite strangers.

For fan-out across many namespaces (single source, multiple destinations), use `ClusterProjection`:

```yaml
apiVersion: projection.sh/v1
kind: ClusterProjection
metadata:
  name: app-config-fanout
spec:
  source:
    group: ""
    version: v1
    kind: ConfigMap
    namespace: platform
    name: app-config
  destination:
    namespaceSelector:
      matchLabels:
        projection.sh/mirror: "true"
```

Cluster admins bind the `<release>-projection-cluster-admin` ClusterRole explicitly to whoever should manage `ClusterProjection`s; namespace tenants get `Projection` access automatically via the standard `edit` role aggregation.

## Features at a glance

- **Two CRDs, one operator** — `Projection` (namespaced, single-target) for tenant self-service; `ClusterProjection` (cluster-scoped, fan-out via list or selector) for cluster-tier mirroring.
- **Any Kind** — `RESTMapper`-driven GVR resolution. Source `version` may be omitted on non-core groups so the projection follows CRD version promotions automatically.
- **Watch-driven both ways** — dynamic informer registration per source GVK, plus label-filtered watches on destinations so manual deletes self-heal.
- **Conflict-safe** — `projection.sh/owned-by-projection` (or `…-cluster-projection`) annotations mark our destinations; the controller never overwrites a stranger-owned object.
- **Clean deletion** — distinct finalizers per tier (`projection.sh/finalizer` and `projection.sh/cluster-finalizer`) clean up every owned destination across every namespace before the CR is removed.
- **Observable** — three status conditions (`SourceResolved`, `DestinationWritten`, `Ready`), Kubernetes Events for every state transition, and `projection_reconcile_total{kind,result}` plus `projection_watched_gvks` / `projection_watched_dest_gvks` gauges.
- **Validated at admission** — source fields are pattern-validated and CEL-checked, so typos and shape errors fail at `kubectl apply`, not at runtime.
- **Tenant-friendly RBAC** — chart aggregates `Projection` CRUD into the standard `admin`/`edit` roles by default (`rbac.aggregate=true`); `ClusterProjection` access is gated separately and must be granted explicitly.
- **Smart copy** — strips server-owned metadata, drops `.status`, preserves apiserver-allocated fields like `Service.spec.clusterIP` on update.
- **Small** — two CRDs, one Deployment, one container. Distroless image, multi-arch.

## Next

Install it and create your first `Projection`: [Getting started](getting-started.md). Already on v0.2.x? See [Upgrading](upgrade.md).
