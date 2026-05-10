# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog 1.1.0](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- **Release-time bench workflow** (`.github/workflows/bench.yml`). Manually triggered (`workflow_dispatch`) against a release tag, branch, or SHA; runs the full 8-profile bench matrix on a self-hosted runner (label `bench-runner`) and persists the resulting JSON to the `bench-history` orphan branch under `bench-history/<label>.json`. The orphan branch is self-bootstrapped on first run. A markdown table of source-update / self-heal / ns-flip p99 latencies by profile is rendered into the workflow's step summary, and the bench JSON is uploaded as an artifact. `pull_request` triggers are deliberately omitted — self-hosted runners on public repos are exposed to fork-PR malicious code, so this workflow is operator-driven only. Per-PR shape-break smoke coverage continues to run on `ubuntu-22.04` via `bench-smoke.yml`.

### Changed

- `spec.source.version` is now optional for the core group. Reference core sources by `kind` alone (e.g. `kind: ConfigMap`); the operator resolves the preferred served version via the RESTMapper. Existing manifests with explicit `version: v1` continue to work unchanged. ([#97](https://github.com/projection-operator/projection/pull/97))
- Renamed the `Destination` print column to `Destination-Name` on both `Projection` and `ClusterProjection`. Symmetric with `Source-Name`, matches the underlying JSONPath (`.status.destinationName`), and removes the where-vs-what ambiguity on `ClusterProjection` (where `Destination` sat next to the `Targets` namespace count). Scripts that parse `kubectl get projection` table output by column name need to be updated. ([#97](https://github.com/projection-operator/projection/pull/97))

### Fixed

- A `Projection` or `ClusterProjection` whose source object was never created now reports `SourceResolved=False, reason=SourceNotFound, message="source X/Y not found"`. Previously every source-NotFound case was bucketed as `reason=SourceDeleted` with the message `"source X/Y has been deleted"` — accurate when the source had previously existed and was deleted, but a lie when the source never existed in the first place. The two cases are now distinguished by `status.destinationName`: empty (never resolved) → `SourceNotFound`; populated (we previously projected it) → `SourceDeleted`. The `SourceDeleted` reason value is unchanged for the genuine deletion case; alerts on `Ready=False` continue to fire for both reasons.

## [0.3.0] - 2026-05-07

### ⚠ BREAKING CHANGES

There is **no automatic migration path** for v0.2 → v0.3. The CRD shape changes are wide enough that a clean reinstall is the supported upgrade. Per the project's pre-1.0 stance there are no known adopters; operators carrying v0.2 Projection CRs must reapply them in the new shape.

- **CRD split.** The single `Projection` kind is replaced by two CRDs:
  - `Projection` (namespaced) — single-target. Always writes into the Projection's own `metadata.namespace`. The v0.2 fields `spec.destination.namespace` and `spec.destination.namespaceSelector` are GONE; only `spec.destination.name` (rename override) remains.
  - `ClusterProjection` (cluster-scoped) — fan-out. Carries the multi-target shape that left namespaced `Projection`: `spec.destination.namespaces: [...]` (explicit list, `minItems=1`) XOR `spec.destination.namespaceSelector` (label selector). CEL admission enforces the mutex and that at least one is set.
- **SourceRef shape.** `spec.source.apiVersion` is REMOVED. Replace with separate `spec.source.group` (empty string for the core group) and `spec.source.version` fields. `version` may be omitted on non-core groups so the controller resolves the RESTMapper-preferred served version on every reconcile (the unpinned form previously spelled `apps/*`); the core group requires `version` (CEL admission).
- **Ownership keys renamed and split per CRD.** Destinations carry kind-specific keys so the two reconcilers cannot collide on the same object:
  - Namespaced: annotation `projection.sh/owned-by` → `projection.sh/owned-by-projection` (value `<projection-namespace>/<projection-name>`); new label `projection.sh/owned-by-projection-uid`.
  - Cluster: NEW annotation `projection.sh/owned-by-cluster-projection` (value `<cluster-projection-name>`, no `<ns>/` prefix because ClusterProjection is cluster-scoped); new label `projection.sh/owned-by-cluster-projection-uid`.
- **New cluster-scoped finalizer.** `ClusterProjection` carries `projection.sh/cluster-finalizer`, distinct from the namespaced `projection.sh/finalizer`, so cleanup paths cannot cross-contaminate.
- **`projection_reconcile_total` gained a `kind` label** (`Projection|ClusterProjection`) so dashboards can split namespaced vs cluster reconcile traffic. The addition is additive — pre-v0.3 PromQL like `sum(rate(projection_reconcile_total[5m]))` keeps working — but dashboards or alerts that materialize per-`result` series must update to `sum by (kind, result) (...)` to retain split-by-kind granularity. See [docs/api-stability.md](https://docs.projection.sh/api-stability/) for the full pre-1.0 metric-label stability carve-out.
- **Removed sample.** `config/samples/projection_v1_projection_selector.yaml` (the v0.2 selector-fan-out sample on the now-namespaced `Projection`) is gone with the field. The selector fan-out lives on `ClusterProjection` and is illustrated by `examples/configmap-fan-out-selector.yaml`.

### Added

- **`ClusterProjection` CRD.** Cluster-scoped sibling of `Projection`, carrying both fan-out modes:
  - `spec.destination.namespaces: [...]` — explicit destination list (`minItems=1`).
  - `spec.destination.namespaceSelector` — `metav1.LabelSelector` resolved on every reconcile and re-resolved on namespace events.
  - CEL admission enforces XOR + at-least-one. Status carries `namespacesWritten` / `namespacesFailed` int32 counts plus the standard `Ready` / `SourceResolved` / `DestinationWritten` conditions.
- **`status.destinationName`** on both CRDs. Surfaces the resolved destination name (after applying `spec.destination.name` override or defaulting to `spec.source.name`) on the printcolumn so `kubectl get projections` displays it without chasing the source ref.
- **`status.namespacesWritten` and `status.namespacesFailed`** (int32) on `ClusterProjection`, reporting the per-reconcile rollup counts. `namespacesFailed > 0` flips `DestinationWritten=False` with a (truncated) failure list in the condition message.
- **`ensureDestWatch`** — shared label-filtered watch on each destination GVK, registered lazily on first use by either reconciler. Triggers immediate reconciliation when a destination is manually edited or deleted (including the `kubectl delete <destination>` path), instead of waiting for the periodic resync. Uses the new `projection.sh/owned-by-{projection,cluster-projection}-uid` labels as the indexed watch hint.
- **Helm chart RBAC aggregation.** Three new ClusterRoles, gated by the new `rbac.aggregate` value (default `true`):
  - `<release>-projection-namespaced-edit` — aggregates into `admin` and `edit` so namespace tenants can `create`/`update`/`delete` `projections.projection.sh` in their own namespaces via the standard cluster-aggregated roles.
  - `<release>-projection-namespaced-view` — aggregates into `view` for read-only namespace tenants.
  - `<release>-projection-cluster-admin` — does NOT aggregate. Bound explicitly via ClusterRoleBinding for cluster-admin operators who need to manage `clusterprojections.projection.sh`. Unaffected by the `rbac.aggregate` toggle (it is always rendered).
  Setting `rbac.aggregate: false` suppresses only the two aggregated namespaced roles. The cluster-admin role is always rendered. End-to-end RBAC matrix verified by SubjectAccessReview tests in `internal/controller/rbac_test.go`.
- **`examples/configmap-fan-out-list.yaml`** — explicit-list fan-out via `ClusterProjection.spec.destination.namespaces`. Companion to the existing `examples/configmap-fan-out-selector.yaml`.
- **`projection_watched_dest_gvks`** Prometheus gauge — counts distinct destination GVKs the controller currently watches via `ensureDestWatch`. Companion to the existing `projection_watched_gvks` (source-side).
- **`projection_e2e_seconds`** Prometheus histogram (`{kind, event}` labels) — wall-clock latency from a `Projection` or `ClusterProjection`'s `creationTimestamp` to the first successful destination Create. Companion to the bench harness in `test/bench/`, which measures the same observation externally — production dashboards can now read what the bench reports. The `event` label is reserved for additive values in future minor releases (`source-update`, `self-heal`, `ns-flip-add`, `ns-flip-cleanup`); v0.3.0 emits `event="create"` only. Buckets are locked at v1.0.

### Changed

- **`sourceKey` is now 4-part** (`group/kind/namespace/name`) — the version segment is dropped because source events always carry a resolved GVK while a Projection may reference its source via the unpinned form. Joining on the version-free key keeps both sides in agreement regardless of which served version the apiserver delivered the event for.

### Removed

- **v0.2 namespaced `Projection` selector / multi-target shape.** Both `spec.destination.namespace` and `spec.destination.namespaceSelector` are gone. The fan-out behavior migrated to `ClusterProjection`.
- **`config/samples/projection_v1_projection_selector.yaml`** sample CR.

## [0.2.0] - 2026-05-05

### Project rebrand

This release re-publishes v0.2.0 under the project's neutral identity (zero adopters at the original 22-minute v0.2.0 release, so no migration burden):

- **API group:** `projection.be0x74a.io/v1` → `projection.sh/v1`. Annotation keys (`owned-by`, `projectable`, finalizer) and the `owned-by-uid` label move to the new prefix.
- **Repo:** `github.com/be0x74a/projection` → `github.com/projection-operator/projection`. Old URL redirects for ~90 days.
- **OCI artifacts:** `ghcr.io/be0x74a/projection` → `ghcr.io/projection-operator/projection`; chart at `ghcr.io/projection-operator/charts/projection`.
- **Docs site:** `projection.be0x74a.io` → `docs.projection.sh`. Old domain serves a redirect.
- **Removed:** `hack/migrate-to-v1.sh`, `docs/upgrade.md`, and `test/e2e-upgrade/` — defensive infrastructure for adopters that don't exist. Operators upgrading from any pre-1.0 deployment should perform a clean reinstall under the new identity.

### ⚠ BREAKING CHANGES

- The default **source-mode** is now `allowlist`. Source objects must carry
  the annotation `projection.sh/projectable: "true"` to be
  mirrored. Clusters that prefer the previous blanket-permissive behavior
  can opt in with the controller flag `--source-mode=permissive`
  (Helm value: `sourceMode: permissive`). The annotation value `"false"`
  is always honored as a source-owner veto regardless of mode.
- **Kubernetes Events are now written through `events.k8s.io/v1`** instead of the legacy `core/v1`. Automations using `kubectl get events --field-selector involvedObject.name=<proj>,involvedObject.kind=Projection` should switch to `kubectl get events.events.k8s.io --field-selector regarding.name=<proj>,regarding.kind=Projection`. Event `reason` strings are unchanged.

### Added

- Controller flag `--source-mode=permissive|allowlist` (default
  `allowlist`). Plumbed through the Helm chart as `sourceMode`.
- Source-side annotation `projection.sh/projectable` with values
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
- Helm chart: optional `supportedKinds` values list for narrowing the operator's `ClusterRole` to an explicit allowlist of Kinds. The default preserves pre-v0.2 `*/*` behavior, so existing installs upgrade without change. Regulated deployments can replace the default with a narrow list (empty list = no access beyond the operator's own Projection CRs). See `docs/security.md#1-narrow-the-controllers-rbac-to-the-kinds-you-actually-mirror`.
- `Projection.spec.source.apiVersion` now accepts an unpinned form `<group>/*` (e.g. `apps/*`) that resolves to the cluster's RESTMapper-preferred served version on every reconcile. Eliminates surprise destination garbage-collection when a CRD author promotes `v1beta1` → `v1` and stops serving the old version. Pinned forms (`v1`, `apps/v1`) are unchanged and remain supported as an explicit stability anchor. Bare `*` without a group prefix is rejected by the reconciler (the core group has stable versions, so no unpinned form is needed). The resolved version is surfaced in the `SourceResolved` condition message for unpinned sources (`resolved apps/Deployment to preferred version v1`); pinned forms keep an empty message. See `docs/concepts.md#pinned-vs-preferred-version`.

### Changed

- Finalizer deletion path now scans every namespace to find owned destinations. Necessary for selector-based Projections whose destination set at deletion time may not match the original selector.
- The controller now watches `Namespace` objects so selector-based Projections re-reconcile automatically when the matching set changes.
- `Reconcile` no longer performs a separate pass to add the finalizer — finalizer-add and the first real reconcile happen in a single pass, halving the initial reconcile count per Projection.

### Fixed

- `resolveGVR` now fails fast with a clear message when a `Projection` points at a cluster-scoped Kind (e.g. `Namespace`, `ClusterRole`, `StorageClass`). Previously the dynamic client would issue a malformed URL and surface a confusing 404 as `SourceFetchFailed`; now the same case reports `SourceResolved=False` with message `<apiVersion>/<Kind> is cluster-scoped; projection only mirrors namespaced resources`.
- Destination-side failures no longer double-emit the same Event (once inline per namespace, once again through the failure funnel). Keeps the `action` field populated on the surviving record instead of being stripped by client-go's event aggregation.
- Unicode curly quotes in kubebuilder markers that prevented CRD installation on some apiserver versions.

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
