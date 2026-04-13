# projection

> The Kubernetes CRD for declarative resource mirroring across namespaces — any Kind, conflict-safe, watch-driven.

`projection` is a Kubernetes operator that mirrors any Kubernetes object — `ConfigMap`, `Secret`, `Service`, your custom resources — from a source location to a destination, declaratively, per resource. Each `Projection` CR is its own first-class object with status conditions, events, and a Prometheus metric you can alert on. Edits to the source propagate to the destination in roughly **100 milliseconds**.

It exists because every team eventually rebuilds this with a one-off controller or a Kyverno `generate` policy, and neither approach is the right shape. `projection` is meant to be the answer when somebody asks *"how do you mirror a Secret across namespaces in this cluster?"*

## Why projection

|                                                           | projection          | emberstack/Reflector          | Kyverno `generate`   |
| --------------------------------------------------------- | ------------------- | ----------------------------- | -------------------- |
| Works on **any Kind**                                     | yes                 | ConfigMap & Secret only       | yes                  |
| Source-of-truth in a **CR you can `kubectl get`**         | yes (`Projection`)  | no (annotations on the source)| no (cluster policy)  |
| **Per-resource status** + Kubernetes Events               | yes                 | partial                       | no                   |
| **Conflict-safe** (refuses to overwrite unowned objects)  | yes                 | no                            | no                   |
| **Watch-driven** propagation (~100 ms)                    | yes                 | yes                           | yes                  |
| **Admission-time validation** of source fields            | yes                 | n/a                           | yes                  |
| **Prometheus metrics** per reconcile outcome              | yes                 | partial                       | yes                  |
| Footprint                                                 | one CRD + Deployment| one CRD + Deployment          | full policy engine   |

See [vs alternatives](comparison.md) for the full comparison.

## 60-second demo

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: app-config
  namespace: platform
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
NAMESPACE   NAME                      KIND        SOURCE-NAMESPACE   SOURCE-NAME   DESTINATION   READY
platform    app-config-into-tenants   ConfigMap   platform           app-config    app-config    True

$ kubectl get configmap -n tenant-a app-config \
    -o jsonpath='{.metadata.annotations.projection\.be0x74a\.io/owned-by}'
platform/app-config-into-tenants
```

- Edit the source — the destination updates within ~100 ms.
- Delete the `Projection` — the destination is removed (only if `projection` still owns it).
- Pre-existing object at the destination? `Ready=False reason=DestinationConflict`. We don't overwrite strangers.

## Features at a glance

- **Any Kind** — `RESTMapper`-driven GVR resolution.
- **Watch-driven** — dynamic informer registration per source GVK, not a periodic polling loop.
- **Conflict-safe** — `projection.be0x74a.io/owned-by` annotation marks our destinations.
- **Clean deletion** — finalizer removes the destination on `Projection` deletion; leaves it alone if ownership has been stripped.
- **Observable** — three status conditions (`SourceResolved`, `DestinationWritten`, `Ready`), Kubernetes Events for every state transition, and a `projection_reconcile_total{result}` counter.
- **Validated at admission** — source fields are pattern-validated, so typos fail at `kubectl apply`, not at runtime.
- **Smart copy** — strips server-owned metadata, drops `.status`, preserves apiserver-allocated fields like `Service.spec.clusterIP` on update.
- **Small** — one CRD, one Deployment, one container. Distroless image, multi-arch.

## Next

Install it and create your first `Projection`: [Getting started](getting-started.md).
