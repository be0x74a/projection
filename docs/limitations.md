# Limitations & roadmap

`projection` is pre-1.0. The CRDs are at `projection.sh/v1`, and the surface that will be frozen at v1.0.0 is documented in [API stability](api-stability.md). Pre-1.0 minor releases can carry breaking changes — v0.3 ships several (the `Projection` / `ClusterProjection` split, `source.apiVersion` replaced with `source.group` + `source.version`, ownership-annotation renames, the `kind` label on `projection_reconcile_total`) listed in the [changelog](https://github.com/projection-operator/projection/blob/main/CHANGELOG.md). This page is the standing list of "things that don't work today" and the rough roadmap for closing the gaps.

## Known limitations

### Namespaced `Projection` cannot write outside its own namespace

A `Projection` mirrors its source into the Projection's own `metadata.namespace`. There is no `destination.namespace` field on `Projection` — the only knob on `destination` is the optional `name` rename. This is a deliberate tenant-safety property: a namespace-scoped RBAC rule on `projections.projection.sh` becomes a structural confinement boundary, because a Projection author cannot escape their own namespace by editing the spec. A platform team that grants `tenant-a` CRUD on `Projection` resources in `tenant-a` knows that whatever Projections that tenant authors will write into `tenant-a`, never into a peer tenant's namespace.

If you need cross-namespace mirroring, use a `ClusterProjection`. ClusterProjection is cluster-scoped and writes destinations into an explicit list of namespaces (`destination.namespaces: [a, b, c]`) or every namespace matching a `destination.namespaceSelector`. Because the same RBAC story would otherwise undermine tenant safety, the chart does **not** aggregate `clusterprojections` CRUD into the standard admin/edit roles — see the next limitation.

### `ClusterProjection` requires cluster-admin authority to create

The Helm chart ships three ClusterRoles: `<release>-projection-namespaced-edit` and `<release>-projection-namespaced-view` (both aggregated into the standard Kubernetes admin/edit/view roles by default — see [`rbac.aggregate`](security.md#rbac-aggregation-defaults)) and `<release>-projection-cluster-admin` (NOT aggregated, by design). Only the third role grants CRUD on `clusterprojections.projection.sh`, and it must be explicitly bound by a cluster admin via a ClusterRoleBinding to the subjects who should hold that authority.

This is a deliberate footgun avoidance: ClusterProjection writes across namespaces, so silently aggregating it into the standard `admin` role would widen everyone with namespace-admin authority into cluster-tier reach. If you want a tenant or service account to be able to create ClusterProjections, you have to mean it. See [Security](security.md#why-projection-cluster-admin-is-not-aggregated).

### No mixed-mode in one CR

`Projection` is single-target only (one source → one destination, in the Projection's own namespace). `ClusterProjection` is fan-out only (one source → N destinations, across selected namespaces). The two shapes do not combine within a single CR — there is no "single-target with optional fan-out" or "fan-out with one of the targets in a different cluster role." If you genuinely need both shapes for the same source, create both CRs explicitly: a `Projection` in the home namespace plus a `ClusterProjection` for the fan-out destinations.

The split is intentional. Each shape has its own status surface (the namespaced CRD has none of the fan-out counters; the cluster CRD doesn't pretend it can target a single namespace cleanly), its own RBAC tier (namespace-scoped vs cluster-scoped), and its own ownership-annotation key. Combining them would muddle every one of those.

### Selector fan-out applies the same overlay to every destination

A `ClusterProjection` with `destination.namespaceSelector` mirrors its source into every matching namespace and rolls up status into a single `DestinationWritten` condition (with `status.namespacesWritten` and `status.namespacesFailed` carrying the counts). The `overlay` block applies uniformly: every destination gets the same labels and annotations.

If you need **distinct overlays per destination** (different `tenant:` labels, per-namespace annotations, etc.), write one `Projection` per destination instead — one CR per home namespace, each with its own overlay. That pattern also keeps per-destination status independent: a `DestinationConflict` in one namespace doesn't mark the others as failed.

### Same-cluster only

Source and destination must live in the same cluster. Cross-cluster mirroring is a non-goal for v0 — the failure modes (partial connectivity, credential distribution, resource collisions between clusters) are significant enough to deserve a separate design.

If you need cross-cluster, look at [Admiralty](https://admiralty.io/), [Open Cluster Management](https://open-cluster-management.io/), or Argo CD's multi-cluster patterns.

### Some Kinds need extra stripping rules

A few Kinds carry **apiserver-allocated spec fields** that must be stripped before mirroring (otherwise the create at the destination is rejected as trying to supply an immutable field). This list grows case-by-case as gaps are reported — see the [umbrella issue](https://github.com/projection-operator/projection/issues/32) for the triage queue. Supported today in `droppedSpecFieldsByGVK`:

| Kind                        | Stripped fields                                                                                 |
| --------------------------- | ----------------------------------------------------------------------------------------------- |
| `v1 Service`                | `spec.clusterIP`, `spec.clusterIPs`, `spec.ipFamilies`, `spec.ipFamilyPolicy`                    |
| `v1 PersistentVolumeClaim`  | `spec.volumeName`                                                                                |
| `v1 Pod`                    | `spec.nodeName`                                                                                  |
| `batch/v1 Job`              | `spec.selector`, `spec.template.metadata.labels["controller-uid"]`, `spec.template.metadata.labels["batch.kubernetes.io/controller-uid"]`, `spec.template.metadata.labels["batch.kubernetes.io/job-name"]` |

> **Job caveat**: Jobs created with `spec.manualSelector: true` carry a user-authored selector that *should* be mirrored. The strip is unconditional today, so these Jobs will not project correctly — file an issue if you hit this.

If you hit an error like `spec.FIELD: field is immutable` when mirroring a Kind not on this list, you've found a gap — file an issue with the Kind and field, and we'll add an entry. The cost of a missing entry is a clean failure with an actionable error message; the cost of a wrong entry is silently dropping user data. The bar for adding entries is deliberately conservative.

Some Kinds *look* like stripping candidates but aren't:
- **`EndpointSlice` / `Endpoints`**: these are managed by the endpoints controller in the destination namespace, keyed on the Service selector there — a mirrored copy would either be overwritten or sit stale. Mirror the Service instead and let the destination endpoints controller rebuild addresses.
- **CRDs with mutating-webhook-defaulted fields**: installation-specific, can't be captured by a static map. Configure the source to match the destination's defaults, or exclude those CRDs.

### Namespaced resources only

`projection` mirrors only **namespaced** resources (`ConfigMap`, `Secret`, `Service`, `Deployment`, most CRs, etc.). A Projection or ClusterProjection that points at a cluster-scoped Kind (`Namespace`, `ClusterRole`, `ClusterRoleBinding`, `StorageClass`, `CustomResourceDefinition`, `PriorityClass`, …) fails fast with `SourceResolved=False` and the message `<group>/<version>/<Kind> is cluster-scoped; projection only mirrors namespaced resources`. There's no use case that motivated cluster-scoped support so far (there can only be one `Namespace` with a given name in a cluster, so mirroring it doesn't mean anything), and the reconcile/watch plumbing assumes a namespace for the source object.

### Default source-mode silently ignores un-annotated sources

`projection` ships with `--source-mode=allowlist` as the default (since v0.2). A source that does not carry `projection.sh/projectable: "true"` is silently treated as not projectable — the Projection (or ClusterProjection) reports `SourceResolved=False reason=SourceNotProjectable`, and no destination is written. This is the safer default in multi-tenant clusters (source owners must opt their objects in), but it's a UX cliff for first-time users who copy an example without reading the source-side requirement.

If you're mirroring sources you don't control (third-party CRs, controller-managed Secrets) and can't add the annotation, flip the operator to `--source-mode=permissive` (Helm value `sourceMode: permissive`) — every source then becomes projectable unless explicitly vetoed with `projectable: "false"`. The trade-off is documented in [Concepts §9](concepts.md#9-source-projectability-policy).

### Events live on `events.k8s.io/v1`, not `core/v1`

Since v0.2, the controller emits Kubernetes Events through `events.k8s.io/v1`. Tooling that reads the legacy `core/v1` Event resource — including the bare `kubectl get events` command — won't surface them. Read with `kubectl get events.events.k8s.io --field-selector regarding.name=<projection>,regarding.kind=Projection` instead (substitute `regarding.kind=ClusterProjection` for cluster-scoped CRs). [Observability](observability.md#2-kubernetes-events) and [Troubleshooting](troubleshooting.md#how-to-read-events) cover the query shape.

### RBAC narrowing is install-time, not per-source

The chart's `supportedKinds` value lets cluster admins narrow the controller's `ClusterRole` to an explicit Kind allowlist (see [Security § 1](security.md#1-narrow-the-controllers-rbac-to-the-kinds-you-actually-mirror)). Changing the allowlist requires a Helm upgrade — there's no runtime path to grant the controller access to a new Kind without restarting. True dynamic RBAC narrowing per declared source Kind would require admission-webhook plumbing the controller doesn't have today; it's noted as future work in [Concepts § 9](concepts.md#9-source-projectability-policy).

### Pre-1.0 API surface

The CRDs are `projection.sh/v1` and that group/version is the storage version, but the project as a whole is pre-1.0. Breaking changes to fields and behavior are allowed in minor releases until v1.0.0 ships; the [API stability page](api-stability.md) documents what v1.0.0 will commit to. Breaking changes are announced in the [changelog](https://github.com/projection-operator/projection/blob/main/CHANGELOG.md) and in release notes with migration guidance.

## Roadmap

In rough priority order at the time of writing:

### 1. OLM bundle for OperatorHub

Package `projection` as an OLM bundle and publish to [OperatorHub.io](https://operatorhub.io/). This is mostly packaging and metadata, not code, but it unblocks adoption on OpenShift / OKD where the OperatorHub console is the install path. Targeted for the v1.0.0 launch window.

### 2. Cross-cluster mirroring (opt-in)

Federation-style. Source in cluster A, destination in cluster B. Credential distribution via a secret-backed `ClusterRef`. This is a large piece of work and will be gated behind an explicit flag and a separate CRD; not in scope for v1.0.0.

### 3. Kyverno-style transforms in `overlay`

Today `overlay` only merges labels and annotations. Useful additions:

- Patches against `spec`/`data` (JSON patch or strategic merge).
- Template substitution (`{{ .Source.metadata.name }}`).
- Label/annotation *removal* (not just set/override).

Scope to be defined — the north star is "make simple transforms trivial without turning `projection` into a policy engine."

## Getting involved

Found a Kind we should support out of the box, a use case the API doesn't cover, a bug, or a documentation gap? [Open an issue](https://github.com/projection-operator/projection/issues/new). Contributions welcome — see `CONTRIBUTING.md` in the repo.
