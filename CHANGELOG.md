# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog 1.1.0](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### ⚠ BREAKING CHANGES

- The default **source-mode** is now `allowlist`. Source objects must carry
  the annotation `projection.be0x74a.io/projectable: "true"` to be
  mirrored. Clusters that prefer the previous blanket-permissive behavior
  can opt in with the controller flag `--source-mode=permissive`
  (Helm value: `sourceMode: permissive`). The annotation value `"false"`
  is always honored as a source-owner veto regardless of mode.

### Added

- Controller flag `--source-mode=permissive|allowlist` (default
  `allowlist`). Plumbed through the Helm chart as `sourceMode`.
- Source-side annotation `projection.be0x74a.io/projectable` with values
  `"true"` (opt-in) and `"false"` (opt-out veto).
- New `SourceResolved=False` reasons: `SourceOptedOut` (source annotated
  `"false"`) and `SourceNotProjectable` (source lacks `"true"` annotation
  in allowlist mode). When a previously-projected source opts out, the
  existing destination is garbage-collected.
- Unit tests for `checkSourceProjectable` and two new envtest specs for
  the allowlist and opt-out paths.

## [0.1.0-alpha] - 2026-04-13

### Added
- Initial release.
- `Projection` CRD (`projection.be0x74a.io/v1`) with `spec.source`, `spec.destination`, `spec.overlay`.
- Reconciler that mirrors any Kubernetes Kind from a source to a destination namespace.
- Dynamic source watches: edits propagate in ~100ms (no periodic polling).
- Conflict-safe ownership via `projection.be0x74a.io/owned-by` annotation.
- Finalizer-based cleanup (deletes only destinations we own).
- Status conditions: `SourceResolved`, `DestinationWritten`, `Ready`.
- Kubernetes Events on reconcile outcomes (`Projected`, `Updated`, `DestinationConflict`, etc.).
- Prometheus metric `projection_reconcile_total{result}` exposed on `:8443/metrics`.
- CRD admission validation (DNS-1123 names, PascalCase Kinds).
- Kind-aware spec field stripping (Service `clusterIP`, PVC `volumeName`, Pod `nodeName`).
- Diff-before-update: no-op reconciles emit no events or metrics.

### Known limitations
- One destination per Projection (no label-selector fan-out).
- Same-cluster only.
- Kinds with apiserver-allocated spec fields beyond Service/PVC/Pod may need additional stripping rules.
