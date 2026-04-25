# Use cases

Six worked examples, all shipped as-is in the repo under [`examples/`](https://github.com/be0x74a/projection/tree/main/examples). Each section below quotes the interesting bits; follow the links for the full manifests (the examples include namespace scaffolding, illustrative ConfigMaps/Secrets, etc.).

> **A note on source opt-in.** The controller ships with `--source-mode=allowlist` as the default, so every source object below must carry `projection.be0x74a.io/projectable: "true"` for the destination to be written. The example files in [`examples/`](https://github.com/be0x74a/projection/tree/main/examples) already include the annotation. If you can't annotate the source — third-party CRs, controller-managed Secrets — flip the operator to `permissive` mode (Helm value `sourceMode: permissive`); the source-owner veto (`="false"`) still works in that mode. See [Source opt-in](getting-started.md#source-opt-in) for the longer explanation.

## 1. `ConfigMap` fan-out across namespaces

**File:** [`examples/configmap-fan-out-selector.yaml`](https://github.com/be0x74a/projection/blob/main/examples/configmap-fan-out-selector.yaml)

The canonical "same config, many namespaces" case. One `Projection` mirrors the source into every namespace carrying a matching label.

```yaml
apiVersion: projection.be0x74a.io/v1
kind: Projection
metadata:
  name: app-config-fanout
  namespace: default
spec:
  source:
    apiVersion: v1
    kind: ConfigMap
    name: app-config
    namespace: default
  destination:
    namespaceSelector:
      matchLabels:
        projection.be0x74a.io/mirror: "true"
    # name omitted → defaults to source.name ("app-config") in every matching namespace
```

**Expected outcome:** `configmap/app-config` appears in every labeled namespace with the same `.data` plus `projection.be0x74a.io/owned-by: default/app-config-fanout`. Edits to the source propagate to all destinations in ~100 ms. Labeling a new namespace triggers a reconcile and the destination appears there too; removing the label cleans up the destination.

**Gotchas:** if any matching namespace already has an unowned `ConfigMap/app-config`, the Projection reports `DestinationWritten=False reason=DestinationConflict` for that namespace (other namespaces still get their destinations). The conflict message identifies which namespace is problematic.

**When to pick single namespace instead:** if you need per-destination overlays (e.g. a `tenant:` label distinct per namespace), one `Projection` per destination is still the right shape — see [`examples/multiple-destinations-from-one-source.yaml`](https://github.com/be0x74a/projection/blob/main/examples/multiple-destinations-from-one-source.yaml).

## 2. `Secret` across namespaces

**File:** [`examples/secret-cross-namespace.yaml`](https://github.com/be0x74a/projection/blob/main/examples/secret-cross-namespace.yaml)

The distribute-a-TLS-cert scenario — typically the source `Secret` is authored by `cert-manager`, `external-secrets`, or `sealed-secrets`. When the destination is a single namespace you can set `namespace` directly; when the cert should reach every labeled namespace, use `namespaceSelector` as in use case 1.

```yaml
apiVersion: projection.be0x74a.io/v1
kind: Projection
metadata:
  name: tls-into-app-prod
  namespace: cert-manager
spec:
  source:
    apiVersion: v1
    kind: Secret
    name: shared-tls
    namespace: cert-manager
  destination:
    namespace: app-prod
    name: tls
```

**Expected outcome:** `Secret/tls` exists in `app-prod` with the source's `type: kubernetes.io/tls` and data. Rotating the source Secret (e.g. cert-manager renewing) propagates.

**Gotchas:** `projection` only mirrors. It does **not** encrypt, rotate, or audit access. Cluster-level Secret protections (encryption at rest, RBAC) still apply to the destination exactly as they would to any other Secret.

## 3. `Service` mirror

**File:** [`examples/service-mirror.yaml`](https://github.com/be0x74a/projection/blob/main/examples/service-mirror.yaml)

Demonstrates **Kind-aware stripping**. A `Service`'s `spec.clusterIP` is apiserver-allocated; naively copying it to another namespace would fail with `spec.clusterIP: field is immutable`.

```yaml
apiVersion: projection.be0x74a.io/v1
kind: Projection
metadata:
  name: api-into-team-b
  namespace: default
spec:
  source:
    apiVersion: v1
    kind: Service
    name: api
    namespace: default
  destination:
    namespace: team-b
```

**Expected outcome:** `Service/api` appears in `team-b` with a **fresh** `clusterIP`, its own `clusterIPs`, `ipFamilies`, `ipFamilyPolicy` — all allocated by the apiserver on create. Ports, selector, and `type` are copied verbatim.

```bash
kubectl get svc -n default api -o jsonpath='{.spec.clusterIP}'   # e.g. 10.96.X.X
kubectl get svc -n team-b  api -o jsonpath='{.spec.clusterIP}'   # 10.96.Y.Y (different)
```

**Gotchas:** the destination `Service` has its own endpoints — if the `selector` doesn't match any Pods in `team-b`, the destination `Service` will have no endpoints. This is usually what you want for `type: ExternalName`-style workflows; it's rarely what you want for `ClusterIP`. Think about whether you really need `Service` mirroring or whether an `ExternalName` pointing at the source FQDN is a better fit.

## 4. Per-destination overlays

**File:** [`examples/multiple-destinations-from-one-source.yaml`](https://github.com/be0x74a/projection/blob/main/examples/multiple-destinations-from-one-source.yaml)

Use case 1 (`namespaceSelector` fan-out) gives every destination the *same* overlay — labels and annotations are evaluated once, then stamped on every copy. When each destination needs a **different** overlay (a tenant tag, an environment label, a per-team annotation), declare one `Projection` per destination instead. A separate `Projection` is also the right shape when the destinations don't share a label predicate — three pre-existing namespaces created by other teams, for example, with no shared marker for the selector to match on.

```yaml
# Snippet — three tenants, three Projections, one source. Each destination
# gets its own overlay so the projected ConfigMap carries the right tenant tag.
- name: org-policy-tenant-a
  spec:
    destination: { namespace: tenant-a }
    overlay: { labels: { tenant: tenant-a } }
- name: org-policy-tenant-b
  spec:
    destination: { namespace: tenant-b }
    overlay: { labels: { tenant: tenant-b } }
- name: org-policy-tenant-c
  spec:
    destination: { namespace: tenant-c }
    overlay: { labels: { tenant: tenant-c } }
```

**Expected outcome:** each tenant sees its own copy of `org-policy` tagged with `tenant=<tenant-id>`. Status is per-Projection — a `DestinationConflict` in `tenant-c` doesn't block `tenant-a`/`tenant-b` from reconciling, and each Projection has its own `Ready` condition you can `kubectl wait` on independently.

**Gotchas:** more YAML to maintain. Kustomize / Helm templating / GitOps generators are the answer; the per-Projection shape is what unlocks the per-destination overlay, not a workaround for missing fan-out.

**When to pick selector fan-out instead:** if every destination wants the same overlay and you can label the namespaces, [use case 1](#1-configmap-fan-out-across-namespaces) is the simpler shape — one `Projection` instead of N.

## 5. Overlay labels

**File:** [`examples/with-overlay-labels.yaml`](https://github.com/be0x74a/projection/blob/main/examples/with-overlay-labels.yaml)

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

## 6. Overlay annotations

**File:** [`examples/with-overlay-annotations.yaml`](https://github.com/be0x74a/projection/blob/main/examples/with-overlay-annotations.yaml)

Same merge rules as labels — use it to stamp provenance:

```yaml
spec:
  overlay:
    annotations:
      mirror.example.com/source: platform/feature-flags
      mirror.example.com/team: platform-eng
```

**Expected outcome:** destination annotations include both overlay entries plus the always-stamped `projection.be0x74a.io/owned-by: <proj-ns>/<proj-name>`.

**Gotchas:**

- Don't try to set `projection.be0x74a.io/owned-by` via overlay — the controller overwrites it on every reconcile.
- `kubectl.kubernetes.io/last-applied-configuration` is always stripped on the destination (carrying it would break three-way merge on later `kubectl apply` calls against the destination).
