# API stability

`projection` commits to the v1 API at v1.0.0. This page describes what that means — what is stable, what may change, and the policy for introducing v2.

## The commitment

`projection.sh/v1` is **permanent**. Once v1.0.0 is tagged:

- No field in the CRD schema will be renamed, removed, or have its semantics changed.
- Existing condition types, condition reasons, event reasons, and metric names will not be renamed or repurposed.
- Annotation and label keys under `projection.sh/*` will not be renamed or have their value semantics changed.

Breaking changes to the API land as `projection.sh/v2`, served alongside v1 via a conversion webhook.

## What is covered

### CRD schema (`projection.sh/v1`)

The fields of `Projection.spec` and `Projection.status` listed in [`crd-reference.md`](crd-reference.md) are permanent. New optional fields may be added; existing fields are not removed or renamed.

### Annotation and label keys

| Key                                             | Writer     | Meaning                                                                                      |
| ----------------------------------------------- | ---------- | -------------------------------------------------------------------------------------------- |
| `projection.sh/owned-by`                | controller | Destination bookkeeping: `<projection-namespace>/<projection-name>`. The controller refuses to overwrite objects lacking this annotation. |
| `projection.sh/owned-by-uid` (label)    | controller | Destination bookkeeping: the value is the owning Projection's `metadata.uid` (RFC-4122 UUID, 36 chars). Enables cluster-wide owned-object listing via `List(LabelSelector)`. |
| `projection.sh/projectable`             | source owner | Opt-in / opt-out policy gate. **Strictly binary:** `"true"` = opt-in, `"false"` = veto, any other value (including missing or empty string) = "not opted in" under `allowlist` mode / "projectable by default" under `permissive` mode. Source-owner vetoes (`"false"`) are always honored regardless of mode. |
| `projection.sh/finalizer`               | controller | Finalizer on the Projection CR. Cleans up destinations on deletion. |

Annotations and labels under `bench.projection.sh/*` are **internal, diagnostic-only, not part of the v1 API**. Their presence, names, and value formats may change without notice.

### Status conditions

Three condition types on `Projection.status.conditions`:

- `SourceResolved` — the controller located and validated the source.
- `DestinationWritten` — the destination was created, updated, or already in sync.
- `Ready` — aggregate; `True` iff both above are `True`.

**Condition reasons:** the list documented in [`observability.md`](observability.md#reasons-youll-see) is permanent. New reasons may be added without a breaking change; consumers should tolerate unknown reason strings. Existing reasons will not be renamed or have their meaning changed.

The success-side reason strings, called out for automation that switches on them: `Resolved` (on `SourceResolved=True`) and `Projected` (on `DestinationWritten=True` and `Ready=True`).

### Events

Events are written through the **`events.k8s.io/v1`** API. This wire format is part of the v1 commitment — consumers can rely on `events.k8s.io/v1` semantics (the `regarding` field, the `action` field, the deduplication-via-series model) and on the `kubectl get events.events.k8s.io --field-selector regarding.name=...` query shape working across all v1.x releases.

Event `reason` strings documented in [`observability.md`](observability.md#2-kubernetes-events) are permanent (same rules as condition reasons). Event `action` verbs (`Create`, `Update`, `Delete`, `Get`, `Validate`, `Resolve`, `Write`) are permanent. New events may be added.

### Prometheus metrics

| Metric                        | Labels                                                     |
| ----------------------------- | ---------------------------------------------------------- |
| `projection_reconcile_total`  | `result={success,conflict,source_error,destination_error}` |
| `projection_watched_gvks`     | (none)                                                     |

Metric names and existing label values are permanent. New labels may be added (existing PromQL stays valid). New metrics may be added.

### CLI flags

Projection-specific flags are permanent:

- `--source-mode=allowlist|permissive`
- `--requeue-interval=<duration>`
- `--leader-election-lease-duration=<duration>`

Flags inherited from the kubebuilder scaffold (`--metrics-bind-address`, `--leader-elect`, `--enable-http2`, etc.) follow upstream's contract; we do not make independent promises about them.

### RBAC

The operator's default `ClusterRole` grants `resources="*"` / `verbs="*"` because a Projection targets any Kind. This default is stable. The optional `supportedKinds` Helm value narrows RBAC without changing the default — additive, non-breaking. The accepted shapes are stable: `supportedKinds: [{apiGroup, resources}]` with the v1.x semantics described in [`security.md`](security.md#1-narrow-the-controllers-rbac-to-the-kinds-you-actually-mirror), including the `supportedKinds: []` inert mode (controller has no access beyond its own `Projection` CRs). Helm chart values themselves are tracked under the chart's own semver (see [What is NOT covered](#what-is-not-covered)) — the *behavior* of `supportedKinds` is a v1 commitment, the chart-key name is not.

## What is NOT covered

Free to change in any release, including patch releases:

- Helm chart values (tracked under the chart's own semver — see `charts/projection/Chart.yaml`).
- Log format, log messages, and error message wording.
- Internal Go package layout, controller internals, test helpers.
- Generated code (DeepCopy, manifests).
- The `bench.projection.sh/*` annotation prefix.

## Deprecation policy

When v2 is introduced:

- v1 continues to be served for **at least 3 minor releases or 12 months after v2 ships**, whichever is longer. Matches Kubernetes upstream's beta-to-GA cadence.
- A conversion webhook translates between v1 and v2 so existing `Projection` resources keep working transparently.
- Deprecation is announced in the CHANGELOG and via a log line when the controller observes a v1 object in a cluster that also has v2 installed.

## How we enforce this

- **Schema golden test** (`api/v1/projection_types_golden_test.go`) compares the rendered CRD against a committed `api/v1/testdata/crd.golden.yaml`. Any change to `api/v1/*.go` that affects the CRD schema fails this test until the golden is consciously regenerated. Regenerate via `make update-crd-golden`.
- **This page is the record.** Anything not listed here is not promised.
