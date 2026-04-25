# CRD behavior and examples

This page covers cross-field invariants, controller-side condition reasons, the finalizer/annotation the operator manages, and fully worked YAML examples for `projection.be0x74a.io/v1` `Projection`. For the field-by-field API schema — types, validation rules, defaults — see the auto-generated [API reference](api-reference.md), which is regenerated from `api/v1/projection_types.go` by `make docs-ref` and verified in CI.

- **API group**: `projection.be0x74a.io`
- **API version**: `v1` (storage version; alpha stability — see [Limitations](limitations.md))
- **Kind**: `Projection`
- **Scope**: Namespaced

## `source.apiVersion` forms

`source.apiVersion` accepts three forms:

| Form       | Semantics                                               |
| ---------- | ------------------------------------------------------- |
| `v1`       | Core group, pinned to v1.                               |
| `apps/v1`  | Named group, pinned to v1.                              |
| `apps/*`   | Named group, RESTMapper-preferred served version.       |

The pinned forms (e.g. `v1`, `apps/v1`) lock the projection to an exact version — useful as a stability anchor when a CRD is mid-migration and you don't yet trust the new version. The unpinned form `<group>/*` follows the cluster: when a CRD author promotes `v1beta1` → `v1` and stops serving `v1beta1`, projection picks up the new preferred version on the next reconcile rather than reporting `SourceResolutionFailed` and garbage-collecting destinations. The unpinned form is the recommended default for sources outside the core group, and especially valuable for CRDs. The same syntax works for any named group — `apps/*`, `networking.k8s.io/*`, `example.com/*`.

The `*` sentinel is invalid without a group prefix (there is no unpinned form for the core group, which has stable versions). The regex accepts `*` for simplicity; the reconciler rejects the bare `*` case and reports `SourceResolved=False reason=SourceResolutionFailed`.

The resolved version is surfaced in the `SourceResolved` condition message:

```bash
kubectl get projection <name> -o jsonpath='{.status.conditions[?(@.type=="SourceResolved")].message}'
# → resolved apps/Deployment to preferred version v1
```

Examples:

```yaml
# Mirror a ConfigMap (core group, pinned)
source:
  apiVersion: v1
  kind: ConfigMap
```

```yaml
# Mirror a Deployment (named group, pinned)
source:
  apiVersion: apps/v1
  kind: Deployment
```

```yaml
# Mirror a custom resource, following the cluster's preferred version
source:
  apiVersion: example.com/*
  kind: Widget
```

## Destination invariants

Setting both `namespace` and `namespaceSelector` is rejected by the reconciler with `DestinationWritten=False reason=InvalidSpec`. (This invariant is enforced controller-side rather than via CEL for cross-apiserver-version compatibility.)

When `namespaceSelector` is set, the controller projects into every matching namespace and cleans up destinations in namespaces that stop matching (e.g. a label is removed). Deletion of the Projection cleans up every owned destination across all namespaces.

Note: the destination `Kind` is always the same as the source `Kind` — there is no transformation.

## Overlay invariants

The controller always stamps `projection.be0x74a.io/owned-by: <projection-ns>/<projection-name>` on the destination, regardless of overlay. Do not attempt to set this key via overlay — it will be overwritten by the controller on every reconcile.

## `status.conditions`

The standard `metav1.Condition` array. The controller maintains three condition types:

| Type                 | True reason(s)       | False reason(s)                                                                                                   | Unknown reason(s)       |
| -------------------- | -------------------- | ----------------------------------------------------------------------------------------------------------------- | ----------------------- |
| `SourceResolved`     | `Resolved`           | `SourceResolutionFailed` (RESTMapper can't find Kind), `SourceFetchFailed` (transient fetch error), `SourceDeleted` (source 404 — owned destinations cleaned up) | —                       |
| `DestinationWritten` | `Projected`          | `DestinationCreateFailed`, `DestinationUpdateFailed`, `DestinationFetchFailed`, `DestinationConflict`, `NamespaceResolutionFailed`, `DestinationWriteFailed`, `InvalidSpec` | `SourceNotResolved`     |
| `Ready`              | `Projected`          | Mirrors whichever of `SourceResolved` or `DestinationWritten` failed, with the same reason and message            | —                       |

`DestinationWritten=Unknown reason=SourceNotResolved` specifically means the source-side step failed, so a destination write was never attempted.

For selector-based Projections, `DestinationWritten` is a rollup across all matched namespaces. If every matched namespace succeeds, the condition is `True`. If any fail, it's `False` with a reason from the failure set (or the generic `DestinationWriteFailed` when reasons differ across namespaces); the message lists the failed namespaces. Per-namespace detail is surfaced via Events.

`DestinationWritten=False reason=InvalidSpec` means both `namespace` and `namespaceSelector` were set — fix the spec.
`DestinationWritten=False reason=NamespaceResolutionFailed` means the label selector was malformed.

## Printer columns

`kubectl get projections` (and `-A`) surfaces:

| Column             | JSONPath                                                     |
| ------------------ | ------------------------------------------------------------ |
| `Kind`             | `.spec.source.kind`                                          |
| `Source-Namespace` | `.spec.source.namespace`                                     |
| `Source-Name`      | `.spec.source.name`                                          |
| `Destination`      | `.spec.destination.name`                                     |
| `Ready`            | `.status.conditions[?(@.type=='Ready')].status`              |
| `Age`              | `.metadata.creationTimestamp`                                |

## Finalizers and annotations the controller manages

| Name                                      | Where                  | Purpose                                                                                          |
| ----------------------------------------- | ---------------------- | ------------------------------------------------------------------------------------------------ |
| `projection.be0x74a.io/finalizer`         | `Projection.metadata`  | Blocks deletion until the controller has cleaned up the destination object (if it still owns it). |
| `projection.be0x74a.io/owned-by`          | Destination annotations | Marks the destination as owned by `<projection-ns>/<projection-name>`. Used for conflict detection on every reconcile and before destination cleanup. |

## Fully-spelled-out example

```yaml
apiVersion: projection.be0x74a.io/v1
kind: Projection
metadata:
  name: app-config-to-tenant-a
  namespace: platform
spec:
  source:
    apiVersion: v1
    kind: ConfigMap
    name: app-config
    namespace: platform
  destination:
    namespace: tenant-a
    name: shared-app-config      # optional; defaults to source.name
  overlay:
    labels:
      tenant: tenant-a
      projected-by: projection
    annotations:
      mirror.example.com/source: platform/app-config
status:
  conditions:
    - type: SourceResolved
      status: "True"
      reason: Resolved
      message: ""
      lastTransitionTime: "2026-04-13T10:00:00Z"
    - type: DestinationWritten
      status: "True"
      reason: Projected
      message: ""
      lastTransitionTime: "2026-04-13T10:00:00Z"
    - type: Ready
      status: "True"
      reason: Projected
      message: ""
      lastTransitionTime: "2026-04-13T10:00:00Z"
```

## Fan-out example (one source → many destinations)

```yaml
apiVersion: projection.be0x74a.io/v1
kind: Projection
metadata:
  name: shared-config-fanout
  namespace: platform
spec:
  source:
    apiVersion: v1
    kind: ConfigMap
    name: shared-config
    namespace: platform
  destination:
    # namespace is omitted — namespaceSelector picks the destinations
    namespaceSelector:
      matchLabels:
        projection.be0x74a.io/mirror: "true"
  overlay:
    labels:
      projected-by: projection
```

Every namespace carrying the label `projection.be0x74a.io/mirror=true` gets a copy. Adding or removing the label on a namespace triggers a reconcile (via the controller's namespace watch) and the destination set adjusts automatically.
