# API stability

`projection` commits to the v1 API at v1.0.0. This page describes what that means — what is stable, what may change, and the policy for introducing v2.

## The commitment

`projection.sh/v1` is **permanent**. Once v1.0.0 is tagged:

- No field in the CRD schema (across both `Projection` and `ClusterProjection`) will be renamed, removed, or have its semantics changed.
- Existing condition types, condition reasons, event reasons, event actions, and metric names will not be renamed or repurposed.
- Annotation and label keys under `projection.sh/*` will not be renamed or have their value semantics changed.

Breaking changes to the API land as `projection.sh/v2`, served alongside v1 via a conversion webhook.

## Pre-v1.0 vs. post-v1.0

We are still pre-v1.0. The API surface is allowed to change with a minor-version bump and a documented migration path; v0.3.0 is itself a worked example of that policy in practice. **After v1.0**, breaking changes require a major-version bump and a formal deprecation cycle (see [Deprecation policy](#deprecation-policy) below).

The list of "what is covered" below describes the post-v1.0 surface — the surface we will commit to. Anything in the [version history](#version-history) under a pre-v1.0 release was free to change at minor-bump time, and several things did, including in v0.3.0. Future minor releases between now and v1.0.0 may carry additional breaking changes; they will be documented in the [changelog](https://github.com/projection-operator/projection/blob/main/CHANGELOG.md).

## What is covered

### CRD schema

Two CRDs at `projection.sh/v1`: `Projection` (namespaced, single-target) and `ClusterProjection` (cluster-scoped, fan-out). The fields of each CRD's `.spec` and `.status` listed in [`crd-reference.md`](crd-reference.md) are permanent. New optional fields may be added; existing fields are not removed or renamed.

The shared `SourceRef` shape (`group`, `version`, `kind`, `namespace`, `name`) is part of both CRDs and equally permanent. Within that shape:

- `spec.source.group` — optional. Empty means the core group.
- `spec.source.version` — optional for any group. Empty means the operator resolves the preferred served version via the RESTMapper on every reconcile (for the core group, currently always `v1`). Set explicitly to pin.

These two fields' optionality is a v1.0 commitment: tightening either back to required would be a breaking change and is forbidden post-v1.0. Existing manifests with explicit `version: v1` for any group continue to work unchanged.

The remaining CEL admission rule (`namespaces` ⊕ `namespaceSelector` mutex on `ClusterProjection.destination`) is stable.

### Annotation and label keys

| Key                                                       | Writer       | Meaning                                                                                                                                                |
| --------------------------------------------------------- | ------------ | ------------------------------------------------------------------------------------------------------------------------------------------------------ |
| `projection.sh/owned-by-projection`                       | controller   | Destination annotation: `<projection-namespace>/<projection-name>`. **Authoritative ownership signal** for namespaced Projection. Refused-to-overwrite gate. |
| `projection.sh/owned-by-projection-uid`                   | controller   | Destination label: the value is the owning Projection's `metadata.uid` (RFC-4122 UUID, 36 chars). **Watch hint only** — never trusted alone for access decisions. Used by `ensureDestWatch` and label-selector cleanup paths; every label-driven list verifies the annotation again before any write or delete. |
| `projection.sh/owned-by-cluster-projection`               | controller   | Destination annotation: `<cluster-projection-name>` (no `<ns>/` prefix — ClusterProjection is cluster-scoped). **Authoritative ownership signal** for ClusterProjection. |
| `projection.sh/owned-by-cluster-projection-uid`           | controller   | Destination label: the owning ClusterProjection's `metadata.uid`. Same watch-hint discipline as the namespaced UID label. |
| `projection.sh/projectable`                               | source owner | Opt-in / opt-out policy gate. **Strictly binary:** `"true"` = opt-in, `"false"` = veto, any other value (including missing or empty string) = "not opted in" under `allowlist` mode / "projectable by default" under `permissive` mode. Source-owner vetoes (`"false"`) are always honored regardless of mode. |
| `projection.sh/finalizer`                                 | controller   | Finalizer on namespaced `Projection` CRs. Cleans up the destination on deletion. |
| `projection.sh/cluster-finalizer`                         | controller   | Finalizer on `ClusterProjection` CRs. Cleans up every owned destination across the cluster on deletion. |

Annotations and labels under `bench.projection.sh/*` are **internal, diagnostic-only, not part of the v1 API**. Their presence, names, and value formats may change without notice.

### Status conditions

Three condition types on `.status.conditions`, identical for both CRDs:

- `SourceResolved` — the controller located and validated the source.
- `DestinationWritten` — the destination was created, updated, or already in sync. For `ClusterProjection`, this is a rollup across all target namespaces; per-namespace counts are exposed as `status.namespacesWritten` and `status.namespacesFailed`.
- `Ready` — aggregate; `True` iff both above are `True`.

**Condition reasons:** the list documented in [`observability.md`](observability.md#reasons-youll-see) is permanent. New reasons may be added without a breaking change; consumers should tolerate unknown reason strings. Existing reasons will not be renamed or have their meaning changed.

The success-side reason strings, called out for automation that switches on them: `Resolved` (on `SourceResolved=True`) and `Projected` (on `DestinationWritten=True` and `Ready=True`).

### Events

Events are written through the **`events.k8s.io/v1`** API. This wire format is part of the v1 commitment — consumers can rely on `events.k8s.io/v1` semantics (the `regarding` field, the `action` field, the deduplication-via-series model) and on the `kubectl get events.events.k8s.io --field-selector regarding.name=...,regarding.kind=Projection|ClusterProjection` query shape working across all v1.x releases.

Event `reason` strings documented in [`observability.md`](observability.md#2-kubernetes-events) are permanent (same rules as condition reasons). Event `action` verbs (`Create`, `Update`, `Delete`, `Get`, `Validate`, `Resolve`, `Write`) are permanent. New events may be added.

### Prometheus metrics

| Metric                          | Labels                                                                              |
| ------------------------------- | ----------------------------------------------------------------------------------- |
| `projection_reconcile_total`    | `kind={Projection,ClusterProjection}`, `result={success,conflict,source_error,destination_error}` |
| `projection_e2e_seconds`        | `kind={Projection,ClusterProjection}`, `event={create}` — additive label values reserved for future minor releases (`source-update`, `self-heal`, `ns-flip-add`, `ns-flip-cleanup`); buckets locked at v1.0 |
| `projection_watched_gvks`       | (none) — distinct **source** GVKs the controller currently watches                  |
| `projection_watched_dest_gvks`  | (none) — distinct **destination** GVKs the controller currently watches via `ensureDestWatch` |

Metric names and existing label values are permanent. New labels may be added (existing PromQL stays valid — see the [pre-v1.0 metric label carve-out](#pre-v10-metric-label-stability-carve-out) below for the exact rule). New metrics may be added.

#### Pre-v1.0 metric label stability carve-out

**Pre-v1.0 metric *labels* are not API-tier.** Minor releases may add new labels (with new label values appearing on existing metrics), as v0.3.0 did with the `kind` label on `projection_reconcile_total`. Existing labels are not renamed or removed without a major-version bump.

The v0.2 `projection_reconcile_total` had no `kind` label; in v0.3.0 it was added so that namespaced and cluster reconcile traffic can be split in dashboards. The mechanic is **additive, not destructive**:

- Pre-v0.3 PromQL like `sum(rate(projection_reconcile_total[5m]))` still works — Prometheus aggregates over labels you don't mention.
- Per-result aggregation (`sum by (result) (rate(projection_reconcile_total[5m]))`) also still works — each `result` value still gets its own line, just split internally by `kind` until you sum it back together.
- Dashboards and alerts that *want* split-by-kind granularity should add `by (kind, result)` (or just `by (kind)` if `result` is not relevant). The recommended alert templates in [Observability § Sample alerts](observability.md#sample-alerts) show the post-v0.3.0 split-by-kind shape.

If a pre-v0.3.0 dashboard relied on the implicit single-line `projection_reconcile_total{result="success"}` series, the same series is still emitted — it's just that the operator now writes two of them, one per kind, and PromQL aggregates them back together unless you explicitly split. No query has to change to keep working; queries get *more* expressive if you opt in.

The same rule will apply to future label additions (and to entirely new metrics): pre-v1.0 we may add labels, observe how scrapers react, and adjust dashboards based on what works. Post-v1.0, label additions remain additive and non-breaking, but require a deliberate documentation entry in the changelog.

### CLI flags

Projection-specific flags are permanent:

- `--source-mode=allowlist|permissive`
- `--requeue-interval=<duration>`
- `--leader-election-lease-duration=<duration>`
- `--selector-write-concurrency=<int>`

Flags inherited from the kubebuilder scaffold (`--metrics-bind-address`, `--leader-elect`, `--enable-http2`, etc.) follow upstream's contract; we do not make independent promises about them.

### RBAC

The operator's default `ClusterRole` grants `resources="*"` / `verbs="*"` because a Projection or ClusterProjection targets any Kind. This default is stable. The optional `supportedKinds` Helm value narrows RBAC without changing the default — additive, non-breaking. The accepted shapes are stable: `supportedKinds: [{apiGroup, resources}]` with the v1.x semantics described in [`security.md`](security.md#1-narrow-the-controllers-rbac-to-the-kinds-you-actually-mirror), including the `supportedKinds: []` inert mode (controller has no access beyond its own `Projection` / `ClusterProjection` CRs). Helm chart values themselves are tracked under the chart's own semver (see [What is NOT covered](#what-is-not-covered)) — the *behavior* of `supportedKinds` is a v1 commitment, the chart-key name is not.

## What is NOT covered

Free to change in any release, including patch releases:

- Helm chart values (tracked under the chart's own semver — see `charts/projection/Chart.yaml`).
- Log format, log messages, and error message wording.
- Internal Go package layout, controller internals, test helpers.
- Generated code (DeepCopy, manifests).
- The `bench.projection.sh/*` annotation prefix.
- Pre-v1.0 metric *labels* — see the [carve-out](#pre-v10-metric-label-stability-carve-out) above. (Post-v1.0, labels are covered.)

## Deprecation policy

When v2 is introduced:

- v1 continues to be served for **at least 3 minor releases or 12 months after v2 ships**, whichever is longer. Matches Kubernetes upstream's beta-to-GA cadence.
- A conversion webhook translates between v1 and v2 so existing `Projection` and `ClusterProjection` resources keep working transparently.
- Deprecation is announced in the CHANGELOG and via a log line when the controller observes a v1 object in a cluster that also has v2 installed.

## Version history

This is the standing record of breaking changes between minor pre-v1.0 releases.

| Version  | Breaking changes                                                                                                                                                                                                                       |
| -------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Unreleased | **Rescinded:** the SourceRef `XValidation` rule `size(self.group) != 0 || size(self.version) != 0` (introduced in PR #76 with the message "version is required when group is empty"). `spec.source.version` is now optional for any group, including core. Pure permission grant — manifests with explicit `version: v1` continue to validate. |
| `v0.3.0` | Single CRD split into `Projection` (namespaced, single-target) and `ClusterProjection` (cluster-scoped, fan-out). `SourceRef.apiVersion` replaced with separate `group` + `version` fields (CEL admission requires `version` when `group` is empty). Ownership annotation renamed from `projection.sh/owned-by` to `projection.sh/owned-by-projection` (namespaced) and `projection.sh/owned-by-cluster-projection` (cluster). New UID labels stamped on every destination (`projection.sh/owned-by-projection-uid` and `projection.sh/owned-by-cluster-projection-uid`). New cluster finalizer (`projection.sh/cluster-finalizer`) on `ClusterProjection`. `projection_reconcile_total` gained a `kind` label; new `projection_watched_dest_gvks` gauge and `projection_e2e_seconds` histogram added. |
| `v0.2.0` | Ownership annotation renamed and source-projectability policy introduced (`projection.sh/projectable` annotation, `--source-mode=allowlist|permissive`). Default mode is `allowlist`. Events moved from `core/v1` to `events.k8s.io/v1`.                  |
| `v0.1.0` | Initial public release. Single CRD `projections.projection.sh/v1` with `destination.namespace` and `destination.namespaceSelector` fields on the same CRD.                                                                              |

The full per-release log lives in the [CHANGELOG](https://github.com/projection-operator/projection/blob/main/CHANGELOG.md). The table above tracks only items that affect API consumers; chart-only changes, internal refactors, and bug fixes are not listed here.

## How we enforce this

- **Schema golden test** (`api/v1/projection_types_golden_test.go`, `api/v1/clusterprojection_types_golden_test.go`) compares the rendered CRDs against committed `api/v1/testdata/*.golden.yaml`. Any change to `api/v1/*.go` that affects either CRD schema fails this test until the golden is consciously regenerated. Regenerate via `make update-crd-golden`.
- **This page is the record.** Anything not listed here is not promised.
