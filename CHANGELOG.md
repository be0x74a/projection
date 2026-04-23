# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog 1.1.0](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Fixed

- `resolveGVR` now fails fast with a clear message when a `Projection` points at a cluster-scoped Kind (e.g. `Namespace`, `ClusterRole`, `StorageClass`). Previously the dynamic client would issue a malformed URL and surface a confusing 404 as `SourceFetchFailed`; now the same case reports `SourceResolved=False` with message `<apiVersion>/<Kind> is cluster-scoped; projection only mirrors namespaced resources`.
- Destination-side failures no longer double-emit the same Event (once inline per namespace, once again through the failure funnel). Keeps the `action` field populated on the surviving record instead of being stripped by client-go's event aggregation.
- Unicode curly quotes in kubebuilder markers that prevented CRD installation on some apiserver versions.

### ⚠ BREAKING CHANGES

- The default **source-mode** is now `allowlist`. Source objects must carry
  the annotation `projection.be0x74a.io/projectable: "true"` to be
  mirrored. Clusters that prefer the previous blanket-permissive behavior
  can opt in with the controller flag `--source-mode=permissive`
  (Helm value: `sourceMode: permissive`). The annotation value `"false"`
  is always honored as a source-owner veto regardless of mode.
- **Kubernetes Events are now written through `events.k8s.io/v1`** instead of the legacy `core/v1`. Automations using `kubectl get events --field-selector involvedObject.name=<proj>,involvedObject.kind=Projection` should switch to `kubectl get events.events.k8s.io --field-selector regarding.name=<proj>,regarding.kind=Projection`. Event `reason` strings are unchanged.

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
- **Multi-destination fan-out** via `spec.destination.namespaceSelector` (a `metav1.LabelSelector`). One Projection mirrors its source into every namespace matching the selector; destinations are added and removed as namespaces gain or lose the matching label. Mutually exclusive with `spec.destination.namespace`.
- Events now carry an `action` verb alongside `reason`, taxonomised as `Create` / `Update` / `Delete` / `Get` / `Validate` / `Resolve` / `Write`. Visible via `kubectl get events.events.k8s.io -o wide` or `-o yaml`.
- New Event reasons: `StaleDestinationDeleted` (Normal — selector no longer matches a previously-owned destination's namespace), `NamespaceResolutionFailed` (Warning — the selector failed to resolve), `DestinationWriteFailed` (Warning — rollup when multiple namespaces fail with different reasons), `InvalidSpec` (Warning — `namespace` and `namespaceSelector` both set).
- Sample CR `config/samples/projection_v1_projection_selector.yaml` and example `examples/configmap-fan-out-selector.yaml` demonstrating selector-based fan-out.
- Six new integration specs covering the fan-out path: happy path, late namespace addition, stale cleanup, deletion cleanup, partial failure, and mutual-exclusion CEL validation.
- Kind-aware spec field stripping for `batch/v1 Job` (`spec.selector` plus the auto-generated `controller-uid` / `batch.kubernetes.io/controller-uid` / `batch.kubernetes.io/job-name` labels on `spec.template.metadata.labels`). Jobs created with `spec.manualSelector: true` are a known limitation. Part of the `droppedSpecFieldsByGVK` umbrella track (#32).
- Helm chart: opt-in `ServiceMonitor`, `NetworkPolicy` (egress lockdown), and `PodDisruptionBudget` templates, each gated by `serviceMonitor.enabled` / `networkPolicy.enabled` / `podDisruptionBudget.enabled` in `values.yaml`. Chart-level `helm-unittest` tests and a `chart-test` CI job added (#33).
- Two CLI flags for operational tuning: `--requeue-interval` (default `30s`, plumbed as chart value `requeueInterval`) controls reconciliation cadence, and `--leader-election-lease-duration` (default `15s`, plumbed as `leaderElection.leaseDuration`) controls leader-election failover timing. Defaults preserve pre-existing behavior — zero change for existing deployments. See `docs/observability.md#4-operational-tuning` for tuning guidance. (#34)
- Auto-generated `docs/api-reference.md` driven by [elastic/crd-ref-docs](https://github.com/elastic/crd-ref-docs). Regenerate via `make docs-ref`; a CI drift-check (`docs-ref` job) fails if `docs/api-reference.md` diverges from `api/v1/projection_types.go`. `docs/crd-reference.md` retains narrative content (invariants, condition reasons, examples). (#35)
- Source deletion triggers destination cleanup. When a Projection's source returns 404 from the apiserver, the controller deletes all owned destinations (single or selector-based fan-out), sets `SourceResolved=False reason=SourceDeleted`, and emits a single `Warning SourceDeleted` event. Other source-fetch errors (transient connectivity, RBAC blips, 5xx) keep the `SourceFetchFailed` behavior and do not cause destination churn. (#36)
- E2e and integration coverage for operational failure modes (part of #36): source-namespace Terminating (regression guard — reconcile stays healthy while source still exists), destination-namespace Terminating (surfaces `DestinationCreateFailed` without busy-looping), non-existent source Kind (surfaces `SourceResolutionFailed`), and shared-watch idempotency when multiple Projections reference the same source GVK (verified via a real-manager envtest spec). (#36)
- `hack/migrate-to-v1.sh`: annotation migration script for `v0.1.0-alpha` users upgrading to v0.2. See `docs/upgrade.md`.

### Changed

- Finalizer deletion path now scans every namespace to find owned destinations. Necessary for selector-based Projections whose destination set at deletion time may not match the original selector.
- The controller now watches `Namespace` objects so selector-based Projections re-reconcile automatically when the matching set changes.
- `Reconcile` no longer performs a separate pass to add the finalizer — finalizer-add and the first real reconcile happen in a single pass, halving the initial reconcile count per Projection.

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
