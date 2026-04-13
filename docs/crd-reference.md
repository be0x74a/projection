# CRD reference

This is the hand-written reference for `projection.be0x74a.io/v1` `Projection`. It mirrors the validation markers in `api/v1/projection_types.go` exactly.

- **API group**: `projection.be0x74a.io`
- **API version**: `v1` (storage version; alpha stability — see [Limitations](limitations.md))
- **Kind**: `Projection`
- **Scope**: Namespaced

## `spec.source` (required)

Identifies the object to mirror. All four fields are required.

| Field        | Type   | Required | Default | Validation                                                                   | Description                                          |
| ------------ | ------ | -------- | ------- | ---------------------------------------------------------------------------- | ---------------------------------------------------- |
| `apiVersion` | string | yes      | —       | `MinLength=1`, pattern `^([a-z0-9.-]+/)?v[0-9]+((alpha|beta)[0-9]+)?$`       | e.g. `v1`, `apps/v1`, `cert-manager.io/v1`.          |
| `kind`       | string | yes      | —       | `MinLength=1`, pattern `^[A-Z][A-Za-z0-9]*$` (PascalCase)                    | e.g. `ConfigMap`, `Secret`, `Certificate`.           |
| `name`       | string | yes      | —       | `MinLength=1`, `MaxLength=63`, pattern `^[a-z0-9]([-a-z0-9]*[a-z0-9])?$` (DNS-1123) | Name of the source object.                     |
| `namespace`  | string | yes      | —       | `MinLength=1`, `MaxLength=63`, pattern `^[a-z0-9]([-a-z0-9]*[a-z0-9])?$` (DNS-1123) | Namespace of the source object.                |

## `spec.destination` (optional)

Where the mirrored object is written. Both fields are optional; leaving them unset produces sensible defaults.

| Field       | Type   | Required | Default                     | Validation                                                                   | Description                                          |
| ----------- | ------ | -------- | --------------------------- | ---------------------------------------------------------------------------- | ---------------------------------------------------- |
| `namespace` | string | no       | The Projection's own namespace | `MaxLength=63`, pattern `^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`                | Namespace to project into.                           |
| `name`      | string | no       | `spec.source.name`          | `MaxLength=63`, pattern `^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`                    | Name at the destination.                             |

Note: the destination `Kind` is always the same as the source `Kind` — there is no transformation.

## `spec.overlay` (optional)

Metadata applied on top of the copied source. Overlay entries win on key conflict with source metadata. The overlay never touches `spec`/`data`/etc.; it is metadata only.

| Field         | Type                | Required | Default | Description                                                                                      |
| ------------- | ------------------- | -------- | ------- | ------------------------------------------------------------------------------------------------ |
| `labels`      | `map[string]string` | no       | `{}`    | Merged with the source's `metadata.labels`. Overlay wins on conflict.                            |
| `annotations` | `map[string]string` | no       | `{}`    | Merged with the source's `metadata.annotations`. Overlay wins on conflict.                       |

The controller always stamps `projection.be0x74a.io/owned-by: <projection-ns>/<projection-name>` on the destination, regardless of overlay. Do not attempt to set this key via overlay — it will be overwritten by the controller on every reconcile.

## `status.conditions`

The standard `metav1.Condition` array. The controller maintains three condition types:

| Type                 | True reason(s)       | False reason(s)                                                                                                   | Unknown reason(s)       |
| -------------------- | -------------------- | ----------------------------------------------------------------------------------------------------------------- | ----------------------- |
| `SourceResolved`     | `Resolved`           | `SourceResolutionFailed`, `SourceFetchFailed`                                                                     | —                       |
| `DestinationWritten` | `Projected`          | `DestinationCreateFailed`, `DestinationUpdateFailed`, `DestinationFetchFailed`, `DestinationConflict`             | `SourceNotResolved`     |
| `Ready`              | `Projected`          | Mirrors whichever of `SourceResolved` or `DestinationWritten` failed, with the same reason and message            | —                       |

`DestinationWritten=Unknown reason=SourceNotResolved` specifically means the source-side step failed, so a destination write was never attempted.

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
