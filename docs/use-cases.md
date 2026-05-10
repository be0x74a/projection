# Use cases

Worked examples drawn from the patterns shipped in [`examples/`](https://github.com/projection-operator/projection/tree/main/examples). Each section below quotes the interesting bits; follow the links for the full manifests (the example files include namespace scaffolding, illustrative ConfigMaps/Secrets, and the `projection.sh/projectable: "true"` annotation on every source).

> **A note on source opt-in.** The controller ships with `--source-mode=allowlist` as the default, so every source object below must carry `projection.sh/projectable: "true"` for the destination to be written. The example files in [`examples/`](https://github.com/projection-operator/projection/tree/main/examples) already include the annotation. If you can't annotate the source — third-party CRs, controller-managed Secrets — flip the operator to `permissive` mode (Helm value `sourceMode: permissive`); the source-owner veto (`="false"`) still works in that mode. See [Source opt-in](getting-started.md#source-opt-in) for the longer explanation.

## Pick the right CRD for your shape

`projection.sh/v1` ships two CRDs. Which one fits depends on the destination shape, not the Kind being mirrored:

| Use case                                          | CRD                  | Why this fits                                                                                          |
| ------------------------------------------------- | -------------------- | ------------------------------------------------------------------------------------------------------ |
| Mirror one source into one destination namespace, with optional rename | `Projection` | Namespaced. The destination namespace is always the Projection's own — the simplest shape. |
| Tenant self-serves an in-namespace mirror of a shared source | `Projection` | The chart's `rbac.aggregate=true` default lets a namespace `edit`-bound subject create Projections in their own namespace; the CR's shape structurally confines the destination to that namespace. |
| Per-destination overlays (different labels/annotations per copy) | `Projection` × N | One Projection per destination namespace, each with its own overlay. Each gets independent status. |
| Fan out one source into a fixed list of namespaces | `ClusterProjection`  | Cluster-scoped. `destination.namespaces: [a, b, c]` is reviewable in YAML; PR diffs show exactly who's in scope. |
| Fan out one source into every namespace matching a label selector | `ClusterProjection` | `destination.namespaceSelector` auto-grows as new matching namespaces appear. |
| Single rename + selector fan-out for the same source on the same cluster | Both, side-by-side | One namespaced `Projection` for the renamed in-place copy; one `ClusterProjection` for the fan-out. Each CR carries its own status, RBAC tier, and ownership keys. |

The general rule: if the destination is one namespace and that namespace is *the Projection's own*, use `Projection`. If the destination spans multiple namespaces, use `ClusterProjection`. If the destinations need *different* overlays, you want multiple `Projection`s (one per destination), not a single `ClusterProjection`.

## 1. `ConfigMap` fan-out across namespaces (selector)

**File:** [`examples/configmap-fan-out-selector.yaml`](https://github.com/projection-operator/projection/blob/main/examples/configmap-fan-out-selector.yaml)

The canonical "same config, many namespaces" case — and the textbook `ClusterProjection` use case. One CR mirrors the source into every namespace carrying a matching label.

```yaml
apiVersion: projection.sh/v1
kind: ClusterProjection
metadata:
  name: app-config-fanout
spec:
  source:
    kind: ConfigMap
    name: app-config
    namespace: platform
  destination:
    namespaceSelector:
      matchLabels:
        projection.sh/mirror: "true"
    # name omitted → defaults to source.name ("app-config") in every matching namespace
```

**Expected outcome:** `configmap/app-config` appears in every labeled namespace with the same `.data` plus `projection.sh/owned-by-cluster-projection: app-config-fanout`. Edits to the source propagate to all destinations in ~100 ms. Labeling a new namespace triggers a reconcile and the destination appears there too; removing the label cleans up the destination.

**Gotchas:** if any matching namespace already has an unowned `ConfigMap/app-config`, the ClusterProjection reports `DestinationWritten=False reason=DestinationConflict` for that namespace (other namespaces still get their destinations). The condition message names which namespaces conflicted, and `status.namespacesFailed` carries the count.

**RBAC reminder:** creating a `ClusterProjection` requires the `<release>-projection-cluster-admin` ClusterRole, which is **not** aggregated into the standard `admin`/`edit` roles. A cluster admin must explicitly bind it. See [Security § Why projection-cluster-admin is not aggregated](security.md#why-projection-cluster-admin-is-not-aggregated).

**When to pick a different shape instead:** if you need per-destination overlays (each tenant gets a different `tenant:` label, say), [use case 4](#4-per-destination-overlays-namespaced-projections) is the right shape — one namespaced `Projection` per destination instead. If your target list is small, fixed, and rarely changes, [use case 2](#2-configmap-fan-out-across-namespaces-explicit-list) (an explicit `namespaces:` list) is the more reviewable shape.

## 2. `ConfigMap` fan-out across namespaces (explicit list)

**File:** [`examples/configmap-fan-out-list.yaml`](https://github.com/projection-operator/projection/blob/main/examples/configmap-fan-out-list.yaml)

Same shape as use case 1, but the target set is enumerated by name rather than matched by label. Use this when the destination set is small, stable, and known up front, and when adding/removing a tenant should be a deliberate edit-and-apply rather than a side effect of a label flip somewhere else in the cluster.

```yaml
apiVersion: projection.sh/v1
kind: ClusterProjection
metadata:
  name: app-config-fanout
spec:
  source:
    kind: ConfigMap
    name: app-config
    namespace: platform
  destination:
    namespaces: [tenant-a, tenant-b, tenant-c]   # listType=set; minItems=1
    # name omitted → defaults to source.name in every listed namespace
```

`namespaces` and `namespaceSelector` are mutually exclusive (CEL admission rejects setting both) and one of them must be set (CEL admission rejects setting neither). The `minItems=1` rule means `namespaces: []` is also rejected — an empty list isn't a valid empty set.

**Expected outcome:** the destination appears only in the named namespaces. Removing a name from the list and reapplying deletes the destination from that namespace on the next reconcile. The CR's `status.namespacesWritten` counts successes; `status.namespacesFailed` counts failures.

**When to pick selector instead:** if the target set should auto-grow as new namespaces are created and labeled, switch to `namespaceSelector`. The list form's strength — that adding a target requires editing the CR — is also its limitation.

## 3. Single-namespace `Secret` mirror (with rename)

**File:** [`examples/secret-cross-namespace.yaml`](https://github.com/projection-operator/projection/blob/main/examples/secret-cross-namespace.yaml)

The distribute-a-TLS-cert-to-one-app scenario — typically the source `Secret` is authored by `cert-manager`, `external-secrets`, or `sealed-secrets` in some platform namespace, and one application namespace needs a copy under a Kubernetes-default name (`tls`).

When the destination is a single namespace, `Projection` is the right shape. The destination namespace is **always** the Projection's own `metadata.namespace` — write the Projection in the namespace that should receive the copy. The `destination.name` field optionally renames it.

```yaml
apiVersion: projection.sh/v1
kind: Projection
metadata:
  name: tls-mirror
  namespace: app-prod              # destination namespace = this
spec:
  source:
    kind: Secret
    name: shared-tls
    namespace: cert-manager
  destination:
    name: tls                      # rename from "shared-tls" → "tls"
```

**Expected outcome:** `Secret/tls` exists in `app-prod` with the source's `type: kubernetes.io/tls` and data plus the ownership annotation `projection.sh/owned-by-projection: app-prod/tls-mirror`. Rotating the source (e.g. cert-manager renewing) propagates within ~100 ms.

**Gotchas:** `projection` only mirrors. It does **not** encrypt, rotate, or audit access. Cluster-level Secret protections (encryption at rest, RBAC) still apply to the destination exactly as they would to any other Secret.

**Tenant self-service:** because the destination namespace is structurally the Projection's own namespace, a tenant who has `edit` on `app-prod` can author this Projection themselves (with `rbac.aggregate=true`, which is the chart default). They cannot widen the destination scope by editing the spec — there's no `destination.namespace` field.

**When to pick `ClusterProjection` instead:** if the cert needs to land in *many* namespaces, see [use case 1](#1-configmap-fan-out-across-namespaces-selector). The same shape works for Secrets.

## 4. Per-destination overlays (namespaced Projections)

**File:** [`examples/multiple-destinations-from-one-source.yaml`](https://github.com/projection-operator/projection/blob/main/examples/multiple-destinations-from-one-source.yaml)

`ClusterProjection` applies one overlay uniformly to every destination — every fan-out copy gets the same labels and annotations. When each destination needs a **different** overlay (a per-tenant label, an environment-specific annotation, a per-team provenance tag), declare one namespaced `Projection` per destination instead.

This is also the right shape when the destinations don't share a label predicate (three pre-existing namespaces created by other teams, no shared marker for a selector to match on) and a single `ClusterProjection.namespaces` list with one shared overlay won't satisfy you.

```yaml
# Snippet — three tenants, three Projections, one source. Each Projection
# lives in its destination namespace; each carries its own overlay so
# the projected ConfigMap is tagged with the right tenant identity.
- apiVersion: projection.sh/v1
  kind: Projection
  metadata: { name: org-policy, namespace: tenant-a }
  spec:
    source: { kind: ConfigMap, namespace: platform, name: org-policy }
    overlay: { labels: { tenant: tenant-a } }
- apiVersion: projection.sh/v1
  kind: Projection
  metadata: { name: org-policy, namespace: tenant-b }
  spec:
    source: { kind: ConfigMap, namespace: platform, name: org-policy }
    overlay: { labels: { tenant: tenant-b } }
- apiVersion: projection.sh/v1
  kind: Projection
  metadata: { name: org-policy, namespace: tenant-c }
  spec:
    source: { kind: ConfigMap, namespace: platform, name: org-policy }
    overlay: { labels: { tenant: tenant-c } }
```

**Expected outcome:** each tenant sees its own copy of `org-policy` tagged with `tenant=<tenant-id>`. Status is per-Projection — a `DestinationConflict` in `tenant-c` doesn't block `tenant-a`/`tenant-b` from reconciling, and each Projection has its own `Ready` condition you can `kubectl wait` on independently.

**Gotchas:** more YAML to maintain. Kustomize / Helm templating / GitOps generators are the answer; the per-Projection shape is what unlocks both per-destination overlays *and* per-destination status, neither of which `ClusterProjection`'s rollup gives you. (Per-target overlays are an explicit non-goal for `ClusterProjection` in v0.3.0 — the two-CRD split exists in part to keep the fan-out CR simple while leaving the per-destination-overlay pattern fully supported via this shape.)

**Tenant self-service:** as in use case 3, each Projection lives in its destination namespace and is structurally confined to it. If `tenant-a`, `tenant-b`, and `tenant-c` are owned by different teams, each team writes their own Projection in their own namespace — no cluster-tier coordination required.

**When to pick selector fan-out instead:** if every destination wants the same overlay and you can label the namespaces, [use case 1](#1-configmap-fan-out-across-namespaces-selector) is the simpler shape — one CR instead of N. The trade-off is uniform overlay vs. per-destination customization.

## 5. Tenant self-service (namespaced Projection via `rbac.aggregate`)

**Pattern, no separate example file** — the shape is the same as use case 3, but with the RBAC story called out explicitly.

The chart's `rbac.aggregate=true` default (see [Security § RBAC aggregation defaults](security.md#rbac-aggregation-defaults)) aggregates `<release>-projection-namespaced-edit` into the standard Kubernetes `edit` and `admin` ClusterRoles. So a tenant who already holds `edit` in their namespace automatically gains CRUD on `Projection` resources in that namespace — no extra binding, no extra learning curve. They can self-serve mirrors of any source the controller can read into their own namespace.

The structural confinement matters: a `Projection` author can mirror *any* source the controller has access to *into* their own namespace, but they cannot mirror *out* of their namespace. There is no `destination.namespace` field on the namespaced CRD; whatever Projection they author writes into `metadata.namespace`, never elsewhere. That property is what makes the chart's aggregation defaults safe to ship.

```yaml
# Tenant self-service — a tenant with `edit` in `team-a` writes this directly.
apiVersion: projection.sh/v1
kind: Projection
metadata:
  name: shared-base-config
  namespace: team-a               # destination namespace = this
spec:
  source:
    kind: ConfigMap
    name: base-config
    namespace: platform
  # destination.name omitted → defaults to "base-config"
  overlay:
    labels:
      team: team-a
```

**Expected outcome:** `team-a` gets its own copy of `base-config`, tagged with `team=team-a`, owned by `team-a/shared-base-config`. The platform team didn't have to grant any cluster-tier authority to make this work — `edit` in `team-a` was enough.

**Gotchas:** the source must still be opted into projection (`projection.sh/projectable: "true"` under default allowlist mode), so the **source owner** retains veto. Tenant self-service is about destination authorship, not source consent; the projectability annotation is the source-side gate.

**RBAC contrast:** if the same tenant tried to author a `ClusterProjection` instead, the apiserver would reject the create with `forbidden` — `<release>-projection-cluster-admin` is not aggregated and has not been bound to them. That's the RBAC tier separation working as designed.

## 6. Hybrid: single rename + selector fan-out for the same source

**Pattern, no example file** — combine the previous shapes when one source needs both a single in-place mirror with a custom name *and* a fan-out with the source name across many namespaces.

For example: a TLS root CA lives in `cert-manager` as `cluster-root-ca`, every tenant namespace needs a copy under the same name (`cluster-root-ca`) for ingress trust, and one specific application namespace needs the same Secret renamed to `tls-ca-bundle` because the application's helm chart hardcodes that name.

```yaml
# 1. ClusterProjection — fan out to every tenant namespace under the source name.
apiVersion: projection.sh/v1
kind: ClusterProjection
metadata:
  name: cluster-root-ca-fanout
spec:
  source:
    kind: Secret
    name: cluster-root-ca
    namespace: cert-manager
  destination:
    namespaceSelector:
      matchLabels:
        projection.sh/mirror-root-ca: "true"
    # destination.name omitted → "cluster-root-ca" in every tenant
---
# 2. Projection — single rename in the application namespace.
apiVersion: projection.sh/v1
kind: Projection
metadata:
  name: cluster-root-ca-rename
  namespace: app-prod
spec:
  source:
    kind: Secret
    name: cluster-root-ca
    namespace: cert-manager
  destination:
    name: tls-ca-bundle               # rename for app-prod's chart
```

**Expected outcome:** every tenant namespace labeled `projection.sh/mirror-root-ca=true` carries `Secret/cluster-root-ca` (owned by the ClusterProjection), and `app-prod` *additionally* carries `Secret/tls-ca-bundle` (owned by the Projection). The two ownership annotations are different (`projection.sh/owned-by-cluster-projection` for the fan-out copies, `projection.sh/owned-by-projection` for the rename), so they never collide — the ownership-key namespace is per-CRD.

**Gotchas:**

- The two CRs reference the same source. Editing the source triggers reconciles on both. Both have their own status conditions; both have their own events.
- If the application namespace `app-prod` is *also* labeled `projection.sh/mirror-root-ca=true`, it gets *two* destinations: the unrenamed `cluster-root-ca` from the ClusterProjection, and the renamed `tls-ca-bundle` from the Projection. They don't conflict (different names) — but if you didn't want both, either change the label or change the rename.

**Why two CRs instead of one?** Because the two destinations have different shapes: the fan-out wants the source name in many namespaces; the rename wants a different name in one namespace. There is no single CR shape in v0.3.0 that does both at once, deliberately — `ClusterProjection.destination.name` is uniform across all targets, and `Projection.destination` is single-target. The two-CR split is what the v0.3.0 namespaced/cluster CRD distinction is for.

## 7. `Service` mirror (Kind-aware stripping)

**File:** [`examples/service-mirror.yaml`](https://github.com/projection-operator/projection/blob/main/examples/service-mirror.yaml)

Demonstrates **Kind-aware stripping**. A `Service`'s `spec.clusterIP` is apiserver-allocated; naively copying it to another namespace would fail with `spec.clusterIP: field is immutable`.

```yaml
apiVersion: projection.sh/v1
kind: Projection
metadata:
  name: api-mirror
  namespace: team-b                # destination namespace = this
spec:
  source:
    kind: Service
    name: api
    namespace: default
```

**Expected outcome:** `Service/api` appears in `team-b` with a **fresh** `clusterIP`, its own `clusterIPs`, `ipFamilies`, `ipFamilyPolicy` — all allocated by the apiserver on create. Ports, selector, and `type` are copied verbatim.

```bash
kubectl get svc -n default api -o jsonpath='{.spec.clusterIP}'   # e.g. 10.96.X.X
kubectl get svc -n team-b  api -o jsonpath='{.spec.clusterIP}'   # 10.96.Y.Y (different)
```

**Gotchas:** the destination `Service` has its own endpoints — if the `selector` doesn't match any Pods in `team-b`, the destination `Service` will have no endpoints. This is usually what you want for `type: ExternalName`-style workflows; it's rarely what you want for `ClusterIP`. Think about whether you really need `Service` mirroring or whether an `ExternalName` pointing at the source FQDN is a better fit.

## 8. Overlay labels

**File:** [`examples/with-overlay-labels.yaml`](https://github.com/projection-operator/projection/blob/main/examples/with-overlay-labels.yaml)

Tag the destination with tenant/team/environment labels your observability stack can select on.

```yaml
spec:
  # ... source pointing at a ConfigMap with labels {env: prod, tenant: shared}
  overlay:
    labels:
      projected-by: projection
      tenant: tenant-a          # overrides source's {tenant: shared}
```

**Expected outcome:** the destination carries `{env: prod, projected-by: projection, tenant: tenant-a}`. Source value of `tenant: shared` is overridden because overlay wins on conflict.

**Gotchas:** label *removals* are not expressible. You can only set or override. If you want the destination to *lose* a source label, you can't do that with overlay alone today.

## 9. Overlay annotations

**File:** [`examples/with-overlay-annotations.yaml`](https://github.com/projection-operator/projection/blob/main/examples/with-overlay-annotations.yaml)

Same merge rules as labels — use it to stamp provenance:

```yaml
spec:
  overlay:
    annotations:
      mirror.example.com/source: platform/feature-flags
      mirror.example.com/team: platform-eng
```

**Expected outcome:** destination annotations include both overlay entries plus the always-stamped ownership annotation (`projection.sh/owned-by-projection: <ns>/<name>` for namespaced, `projection.sh/owned-by-cluster-projection: <name>` for cluster).

**Gotchas:**

- Don't try to set the ownership annotation via overlay — the controller overwrites it on every reconcile.
- `kubectl.kubernetes.io/last-applied-configuration` is always stripped on the destination (carrying it would break three-way merge on later `kubectl apply` calls against the destination).
