# CRD behavior and examples

This page covers cross-field invariants, controller-side condition reasons, the finalizers/annotations the operator manages, and fully worked YAML examples for both CRDs in `projection.sh/v1`. For the field-by-field API schema — types, validation rules, defaults — see the auto-generated [API reference](api-reference.md), which is regenerated from `api/v1/*.go` by `make docs-ref` and verified in CI.

`projection.sh/v1` ships two CRDs:

| CRD                 | Scope       | Short name | Destinations                                       |
| ------------------- | ----------- | ---------- | -------------------------------------------------- |
| `Projection`        | Namespaced  | `proj`     | Single — Projection's own `metadata.namespace`.    |
| `ClusterProjection` | Cluster     | `cproj`    | Fan-out — explicit list, or selector.              |

Both CRDs share the same `source`, `overlay`, ownership keys (with different suffixes), and reconcile model. Apiserver floor is **Kubernetes ≥ 1.32**, required by the CEL admission rules below.

## `source` (shared)

The `SourceRef` struct is the same for both CRDs:

| Field               | Type   | Required | Notes                                                                              |
| ------------------- | ------ | -------- | ---------------------------------------------------------------------------------- |
| `source.group`      | string | yes (may be empty) | `""` for the core API; otherwise a DNS-subdomain group name.            |
| `source.version`    | string | conditional       | Required when `source.group == ""`. Optional otherwise.                  |
| `source.kind`       | string | yes      | PascalCase. Pattern-validated.                                                     |
| `source.namespace`  | string | yes      | DNS-1123. Source must be a namespaced object.                                      |
| `source.name`       | string | yes      | DNS-1123.                                                                          |

### `source` CEL rule

```
size(self.group) != 0 || size(self.version) != 0
```

If the group is empty (core API), the version field must be set. Setting `group: ""` and leaving `version` empty fails at `kubectl apply` with a CEL violation.

For non-core groups, omitting `version` triggers preferred-version lookup via the `RESTMapper`. Pinning a specific version (`group: apps`, `version: v1`) locks the projection to that version regardless of CRD promotions in the cluster.

### `source.version` semantics

| Form                                | Resolution                                                                     |
| ----------------------------------- | ------------------------------------------------------------------------------ |
| `group: ""`, `version: v1`          | Core group, pinned to v1. (Only form for core.)                                |
| `group: apps`, `version: v1`        | Named group, pinned to v1.                                                     |
| `group: apps` (`version` omitted)   | Named group, RESTMapper-preferred served version. Follows CRD promotions.      |
| `group: ""` (`version` omitted)     | Rejected by CEL admission.                                                     |

The resolved version is surfaced in the `SourceResolved` condition message:

```bash
kubectl get projection <name> -o jsonpath='{.status.conditions[?(@.type=="SourceResolved")].message}'
# → resolved apps/Deployment to preferred version v1
```

The unpinned form is the recommended default for sources outside the core group, especially valuable for CRDs: when an author promotes `v1beta1` → `v1` and stops serving `v1beta1`, projection picks up the new preferred version on the next reconcile rather than reporting `SourceResolutionFailed` and garbage-collecting destinations.

---

# `Projection` (namespaced)

- **API group**: `projection.sh`
- **API version**: `v1` (storage version; stability commitments documented in [API stability](api-stability.md))
- **Kind**: `Projection`
- **Scope**: Namespaced
- **Short name**: `proj`

A `Projection` mirrors one source object into the Projection's own namespace.

## Spec

| Field                    | Type        | Required | Notes                                                  |
| ------------------------ | ----------- | -------- | ------------------------------------------------------ |
| `spec.source`            | `SourceRef` | yes      | See [`source` (shared)](#source-shared) above.         |
| `spec.destination.name`  | string      | no       | Rename override. Defaults to `source.name`.            |
| `spec.overlay.labels`    | map[string]string | no | Labels merged on top of source metadata. Overlay wins. |
| `spec.overlay.annotations` | map[string]string | no | Annotations merged on top of source metadata. Overlay wins. |

Note: there is no `spec.destination.namespace` and no `spec.destination.namespaceSelector`. The destination namespace is **always** the Projection's own `metadata.namespace`. Use `ClusterProjection` for fan-out across multiple namespaces.

## Status

| Field                       | Type                  | Notes                                                                       |
| --------------------------- | --------------------- | --------------------------------------------------------------------------- |
| `status.conditions`         | `[]metav1.Condition`  | Standard conditions array. See [`status.conditions` (both CRDs)](#statusconditions-both-crds) below. |
| `status.destinationName`    | string                | The resolved destination name (after rename). Populated after first successful write. |

## Print columns

`kubectl get projections` (and `-A`, also as `proj`) surfaces:

| Column             | JSONPath                                                          | Default? |
| ------------------ | ----------------------------------------------------------------- | -------- |
| `Kind`             | `.spec.source.kind`                                               | yes      |
| `Source-Group`     | `.spec.source.group`                                              | with `-o wide` (priority=1) |
| `Source-Namespace` | `.spec.source.namespace`                                          | yes      |
| `Source-Name`      | `.spec.source.name`                                               | yes      |
| `Destination`      | `.status.destinationName`                                         | yes      |
| `Ready`            | `.status.conditions[?(@.type=='Ready')].status`                   | yes      |
| `Age`              | `.metadata.creationTimestamp`                                     | yes      |

`Destination` reflects the **resolved** destination name from status — the rename applied (or the source name when no rename is set), populated after the first successful write.

## Worked example

```yaml
apiVersion: projection.sh/v1
kind: Projection
metadata:
  name: app-config-mirror
  namespace: tenant-a            # destination namespace = this
spec:
  source:
    group: ""
    version: v1
    kind: ConfigMap
    namespace: platform
    name: app-config
  destination:
    name: shared-app-config      # optional rename; defaults to source.name
  overlay:
    labels:
      tenant: tenant-a
      projected-by: projection
    annotations:
      mirror.example.com/source: platform/app-config
status:
  destinationName: shared-app-config
  conditions:
    - type: SourceResolved
      status: "True"
      reason: Resolved
      message: ""
      lastTransitionTime: "2026-05-07T10:00:00Z"
    - type: DestinationWritten
      status: "True"
      reason: Projected
      message: ""
      lastTransitionTime: "2026-05-07T10:00:00Z"
    - type: Ready
      status: "True"
      reason: Projected
      message: ""
      lastTransitionTime: "2026-05-07T10:00:00Z"
```

---

# `ClusterProjection` (cluster-scoped)

- **API group**: `projection.sh`
- **API version**: `v1`
- **Kind**: `ClusterProjection`
- **Scope**: Cluster
- **Short name**: `cproj`

A `ClusterProjection` is fan-out only: it writes the same destination object into multiple namespaces, either an explicit list or every namespace matching a label selector.

## Spec

| Field                                | Type                | Required          | Notes                                                                                  |
| ------------------------------------ | ------------------- | ----------------- | -------------------------------------------------------------------------------------- |
| `spec.source`                        | `SourceRef`         | yes               | See [`source` (shared)](#source-shared) above.                                         |
| `spec.destination.namespaces`        | `[]string` (set)    | conditional       | Explicit target namespace list. `+listType=set`, `minItems=1`. Mutex with `namespaceSelector`. |
| `spec.destination.namespaceSelector` | `metav1.LabelSelector` | conditional    | Match every namespace whose labels satisfy the selector. Mutex with `namespaces`.      |
| `spec.destination.name`              | string              | no                | Rename override applied in every target namespace. Defaults to `source.name`.          |
| `spec.overlay.labels`                | map[string]string   | no                | Labels merged on top of source metadata. Overlay wins.                                 |
| `spec.overlay.annotations`           | map[string]string   | no                | Annotations merged on top of source metadata. Overlay wins.                            |

## CEL admission rules on `spec.destination`

Two rules are enforced at admission time:

```
!(has(self.namespaces) && has(self.namespaceSelector))
```

`namespaces` and `namespaceSelector` are mutually exclusive — setting both is rejected.

```
has(self.namespaces) || has(self.namespaceSelector)
```

At least one must be set — empty `destination` is rejected.

`namespaces` carries `+listType=set` (no duplicates) and `minItems=1` (cannot be the empty list). The `minItems` rule is what keeps `namespaces: []` from satisfying the at-least-one rule.

## Status

| Field                          | Type                  | Notes                                                                       |
| ------------------------------ | --------------------- | --------------------------------------------------------------------------- |
| `status.conditions`            | `[]metav1.Condition`  | Standard conditions array.                                                  |
| `status.destinationName`       | string                | The resolved destination name (after rename). Populated after first successful write. |
| `status.namespacesWritten`     | int32                 | Count of target namespaces where the destination was successfully written on the last reconcile. |
| `status.namespacesFailed`      | int32                 | Count of target namespaces where the write failed on the last reconcile. Per-namespace detail surfaces via Events. |

## Print columns

`kubectl get clusterprojections` (also as `cproj`) surfaces:

| Column             | JSONPath                                                                      | Default? |
| ------------------ | ----------------------------------------------------------------------------- | -------- |
| `Kind`             | `.spec.source.kind`                                                           | yes      |
| `Source-Group`     | `.spec.source.group`                                                          | with `-o wide` (priority=1) |
| `Source-Namespace` | `.spec.source.namespace`                                                      | yes      |
| `Source-Name`      | `.spec.source.name`                                                           | yes      |
| `Destination`      | `.status.destinationName`                                                     | yes      |
| `Targets`          | `.status.namespacesWritten`                                                   | yes      |
| `Failed`           | `.status.namespacesFailed`                                                    | with `-o wide` (priority=1) |
| `Selector`         | `.spec.destination.namespaceSelector.matchLabels`                             | with `-o wide` (priority=1) |
| `Ready`            | `.status.conditions[?(@.type=='Ready')].status`                               | yes      |
| `Age`              | `.metadata.creationTimestamp`                                                 | yes      |

`Selector` is empty for list-based ClusterProjections; `Targets` shows the count for either form.

## Worked example: explicit list

```yaml
apiVersion: projection.sh/v1
kind: ClusterProjection
metadata:
  name: shared-config-fanout
spec:
  source:
    group: ""
    version: v1
    kind: ConfigMap
    namespace: platform
    name: app-config
  destination:
    namespaces:
      - tenant-a
      - tenant-b
      - tenant-c
    name: shared-app-config       # optional rename; same name applied in each target
  overlay:
    labels:
      projected-by: projection
status:
  destinationName: shared-app-config
  namespacesWritten: 3
  namespacesFailed: 0
  conditions:
    - type: SourceResolved
      status: "True"
      reason: Resolved
      message: ""
      lastTransitionTime: "2026-05-07T10:00:00Z"
    - type: DestinationWritten
      status: "True"
      reason: Projected
      message: ""
      lastTransitionTime: "2026-05-07T10:00:00Z"
    - type: Ready
      status: "True"
      reason: Projected
      message: ""
      lastTransitionTime: "2026-05-07T10:00:00Z"
```

## Worked example: selector

```yaml
apiVersion: projection.sh/v1
kind: ClusterProjection
metadata:
  name: shared-config-fanout
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
    name: shared-app-config
  overlay:
    labels:
      projected-by: projection
```

Every namespace carrying the label `projection.sh/mirror=true` gets a copy. Adding the label to a new namespace triggers a reconcile and the destination appears; removing it deletes the destination.

---

## `status.conditions` (both CRDs)

Both CRDs use the standard `metav1.Condition` array. The controller maintains three condition types. The reasons are shared.

| Type                 | True reason(s)       | False reason(s)                                                                                                   | Unknown reason(s)       |
| -------------------- | -------------------- | ----------------------------------------------------------------------------------------------------------------- | ----------------------- |
| `SourceResolved`     | `Resolved`           | `SourceResolutionFailed` (RESTMapper can't find Kind, cluster-scoped Kind), `SourceFetchFailed` (transient fetch error), `SourceDeleted` (source 404 — owned destinations cleaned up), `SourceOptedOut` (source has `projectable="false"`), `SourceNotProjectable` (allowlist mode, source missing `projectable="true"`) | —                       |
| `DestinationWritten` | `Projected`          | `DestinationCreateFailed`, `DestinationUpdateFailed`, `DestinationFetchFailed`, `DestinationConflict`, `NamespaceResolutionFailed`, `DestinationWriteFailed` | `SourceNotResolved`     |
| `Ready`              | `Projected`          | Mirrors whichever of `SourceResolved` or `DestinationWritten` failed, with the same reason and message            | —                       |

`DestinationWritten=Unknown reason=SourceNotResolved` specifically means the source-side step failed, so a destination write was never attempted.

For `ClusterProjection`, `DestinationWritten` is a rollup across all target namespaces. If every target succeeds, the condition is `True`. If any fail, it's `False` with a reason from the failure set (or the generic `DestinationWriteFailed` when reasons differ across namespaces); the message lists the failed namespaces. `status.namespacesWritten` and `status.namespacesFailed` are the canonical counts. Per-namespace detail surfaces via Events.

`DestinationWritten=False reason=NamespaceResolutionFailed` means the label selector was malformed or namespace listing failed.

> Note: pre-v0.3.0 surfaced `DestinationWritten=False reason=InvalidSpec` when the v0.2 mutex (`destination.namespace` xor `destination.namespaceSelector`) was violated at runtime. v0.3.0 removes that runtime check entirely — the namespaced `Projection` no longer has a destination-namespace mutex (its destination is always the Projection's own namespace), and the `ClusterProjection` mutex is enforced by CEL admission so a violation never reaches the reconciler.

## Stripped fields by Kind (both CRDs)

Some Kinds carry apiserver-allocated spec fields the controller strips before writing the destination. These fields would either be rejected on create (`spec.clusterIP: field is immutable`) or carry meaningless values across namespaces. The current set:

| Kind                                  | Stripped fields                                                                                                                                                           |
| ------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `v1/Service`                          | `spec.clusterIP`, `spec.clusterIPs`, `spec.ipFamilies`, `spec.ipFamilyPolicy`                                                                                             |
| `v1/PersistentVolumeClaim`            | `spec.volumeName`                                                                                                                                                         |
| `v1/Pod`                              | `spec.nodeName`                                                                                                                                                           |
| `batch/v1/Job`                        | `spec.selector`, plus the auto-generated `controller-uid` / `batch.kubernetes.io/controller-uid` / `batch.kubernetes.io/job-name` labels on `spec.template.metadata.labels`. Jobs created with `spec.manualSelector: true` are a known limitation — the controller's stripping logic assumes the apiserver-managed selector path. |

On update, the controller copies these fields from the existing destination back onto the desired object before issuing the `Update`, so an `Update` of a `Service` whose `clusterIP` we stripped at build time isn't rejected for trying to clear an immutable field.

If you hit `field is immutable` errors for a Kind not in the table above, the controller is likely missing an entry in `droppedSpecFieldsByGVK` — see [CONTRIBUTING.md](https://github.com/projection-operator/projection/blob/main/CONTRIBUTING.md#adding-a-kind-to-droppedspecfieldsbygvk) for the path to add one, and please [open an issue](https://github.com/projection-operator/projection/issues/new).

## Finalizers and ownership keys

| Name                                                 | Where                       | Owner                | Purpose                                                                                          |
| ---------------------------------------------------- | --------------------------- | -------------------- | ------------------------------------------------------------------------------------------------ |
| `projection.sh/finalizer`                            | `Projection.metadata`       | `Projection`         | Blocks deletion until the controller has cleaned up the destination (if it still owns it).      |
| `projection.sh/cluster-finalizer`                    | `ClusterProjection.metadata`| `ClusterProjection`  | Blocks deletion until the controller has cleaned up every owned destination across the cluster. |
| `projection.sh/owned-by-projection`                  | Destination annotations     | `Projection`         | Marks the destination as owned by `<projection-ns>/<projection-name>`. **Authoritative** ownership signal — checked on every write and delete. |
| `projection.sh/owned-by-projection-uid`              | Destination labels          | `Projection`         | The owning Projection's `metadata.uid`. Used by destination-side watches and label-selector cleanup paths. **Watch hint only** — never trusted alone for access decisions; the annotation is verified after every label-driven list. |
| `projection.sh/owned-by-cluster-projection`          | Destination annotations     | `ClusterProjection`  | Marks the destination as owned by `<cluster-projection-name>`. (No `<ns>/` prefix — ClusterProjection is cluster-scoped.) **Authoritative** ownership signal. |
| `projection.sh/owned-by-cluster-projection-uid`      | Destination labels          | `ClusterProjection`  | The owning ClusterProjection's `metadata.uid`. Watch hint, same discipline as the namespaced UID label. |
| `projection.sh/projectable`                          | Source annotations *(read by controller, written by source owners)* | (n/a) | Source-side opt-in/veto. `"true"` = opt-in (required under default `sourceMode=allowlist`). `"false"` = veto (always honored regardless of mode; flipping a previously-projected source to `"false"` garbage-collects every destination). Any other value is treated as "not opted in" under allowlist, "projectable by default" under permissive. |

The controller always stamps the appropriate ownership annotation and UID label on the destination, regardless of overlay. Do not attempt to set these keys via overlay — they will be overwritten on every reconcile.

The discipline is: **annotation is authoritative, UID label is a watch hint.** The controller's `ensureDestWatch` registers a label-filtered watch on the destination GVK so that manual deletion of a destination triggers an immediate reconcile. Cleanup paths use the UID label for an indexed cluster-wide `List(LabelSelector)`, but every candidate's annotation is verified again before any write or delete. A malicious or accidental copy of the UID label onto a stranger's object would not let the controller touch it — the annotation wouldn't match.
