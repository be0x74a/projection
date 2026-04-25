# CRD behavior and examples

This page covers cross-field invariants, controller-side condition reasons, the finalizer/annotation the operator manages, and fully worked YAML examples for `projection.be0x74a.io/v1` `Projection`. For the field-by-field API schema — types, validation rules, defaults — see the auto-generated [API reference](api-reference.md), which is regenerated from `api/v1/projection_types.go` by `make docs-ref` and verified in CI.

- **API group**: `projection.be0x74a.io`
- **API version**: `v1` (storage version; stability commitments documented in [API stability](api-stability.md))
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
| `SourceResolved`     | `Resolved`           | `SourceResolutionFailed` (RESTMapper can't find Kind, cluster-scoped Kind, or bare `*`), `SourceFetchFailed` (transient fetch error), `SourceDeleted` (source 404 — owned destinations cleaned up), `SourceOptedOut` (source has `projectable="false"`), `SourceNotProjectable` (allowlist mode, source missing `projectable="true"`) | —                       |
| `DestinationWritten` | `Projected`          | `DestinationCreateFailed`, `DestinationUpdateFailed`, `DestinationFetchFailed`, `DestinationConflict`, `NamespaceResolutionFailed`, `DestinationWriteFailed`, `InvalidSpec` | `SourceNotResolved`     |
| `Ready`              | `Projected`          | Mirrors whichever of `SourceResolved` or `DestinationWritten` failed, with the same reason and message            | —                       |

`DestinationWritten=Unknown reason=SourceNotResolved` specifically means the source-side step failed, so a destination write was never attempted.

For selector-based Projections, `DestinationWritten` is a rollup across all matched namespaces. If every matched namespace succeeds, the condition is `True`. If any fail, it's `False` with a reason from the failure set (or the generic `DestinationWriteFailed` when reasons differ across namespaces); the message lists the failed namespaces. Per-namespace detail is surfaced via Events.

`DestinationWritten=False reason=InvalidSpec` means both `namespace` and `namespaceSelector` were set — fix the spec.
`DestinationWritten=False reason=NamespaceResolutionFailed` means the label selector was malformed.

## Printer columns

`kubectl get projections` (and `-A`) surfaces:

| Column                 | JSONPath                                                     | Default? |
| ---------------------- | ------------------------------------------------------------ | -------- |
| `Kind`                 | `.spec.source.kind`                                          | yes      |
| `Source-Namespace`     | `.spec.source.namespace`                                     | yes      |
| `Source-Name`          | `.spec.source.name`                                          | yes      |
| `Destination`          | `.spec.destination.name`                                     | yes      |
| `Destination-Selector` | `.spec.destination.namespaceSelector.matchLabels`            | with `-o wide` (priority=1) |
| `Ready`                | `.status.conditions[?(@.type=='Ready')].status`              | yes      |
| `Age`                  | `.metadata.creationTimestamp`                                | yes      |

The CRD also exposes the short name `proj`, so `kubectl get proj` works as a shorthand for `kubectl get projections`.

## Finalizers and annotations the controller manages

| Name                                      | Where                  | Purpose                                                                                          |
| ----------------------------------------- | ---------------------- | ------------------------------------------------------------------------------------------------ |
| `projection.be0x74a.io/finalizer`         | `Projection.metadata`  | Blocks deletion until the controller has cleaned up the destination object (if it still owns it). |
| `projection.be0x74a.io/owned-by`          | Destination annotations | Marks the destination as owned by `<projection-ns>/<projection-name>`. Used for conflict detection on every reconcile and before destination cleanup. |
| `projection.be0x74a.io/owned-by-uid`      | Destination labels      | The owning Projection's `metadata.uid`. Lets cleanup paths find owned destinations via a single cluster-wide `List(LabelSelector)` instead of walking namespaces. The annotation above is still verified after the label-driven list as a belt-and-braces guard. |
| `projection.be0x74a.io/projectable`       | Source annotations *(read by controller, written by source owners)* | Source-side opt-in/veto. `"true"` = opt-in (required under default `sourceMode=allowlist`). `"false"` = veto (always honored regardless of mode; flipping a previously-projected source to `"false"` garbage-collects the destination). Any other value is treated as "not opted in" under allowlist, "projectable by default" under permissive. |

## Stripped fields by Kind

Some Kinds carry apiserver-allocated spec fields the controller strips before writing the destination. These fields would either be rejected on create (`spec.clusterIP: field is immutable`) or carry meaningless values across namespaces. The current set:

| Kind                                  | Stripped fields                                                                                                                                                           |
| ------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `v1/Service`                          | `spec.clusterIP`, `spec.clusterIPs`, `spec.ipFamilies`, `spec.ipFamilyPolicy`                                                                                             |
| `v1/PersistentVolumeClaim`            | `spec.volumeName`                                                                                                                                                         |
| `v1/Pod`                              | `spec.nodeName`                                                                                                                                                           |
| `batch/v1/Job`                        | `spec.selector`, plus the auto-generated `controller-uid` / `batch.kubernetes.io/controller-uid` / `batch.kubernetes.io/job-name` labels on `spec.template.metadata.labels`. Jobs created with `spec.manualSelector: true` are a known limitation — the controller's stripping logic assumes the apiserver-managed selector path. |

On update, the controller copies these fields from the existing destination back onto the desired object before issuing the `Update`, so an `Update` of a `Service` whose `clusterIP` we stripped at build time isn't rejected for trying to clear an immutable field.

If you hit `field is immutable` errors for a Kind not in the table above, the controller is likely missing an entry in `droppedSpecFieldsByGVK` — see [CONTRIBUTING.md](https://github.com/be0x74a/projection/blob/main/CONTRIBUTING.md#adding-a-kind-to-droppedspecfieldsbygvk) for the path to add one, and please [open an issue](https://github.com/be0x74a/projection/issues/new).

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
