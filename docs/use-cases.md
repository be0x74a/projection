# Use cases

Six worked examples, all shipped as-is in the repo under [`examples/`](https://github.com/be0x74a/projection/tree/main/examples). Each section below quotes the interesting bits; follow the links for the full manifests (the examples include namespace scaffolding, illustrative ConfigMaps/Secrets, etc.).

## 1. `ConfigMap` across namespaces

**File:** [`examples/configmap-cross-namespace.yaml`](https://github.com/be0x74a/projection/blob/main/examples/configmap-cross-namespace.yaml)

The canonical "same config, many namespaces" case. One `Projection` per destination namespace.

```yaml
apiVersion: projection.be0x74a.io/v1
kind: Projection
metadata:
  name: app-config-to-tenant-a
  namespace: default
spec:
  source:
    apiVersion: v1
    kind: ConfigMap
    name: app-config
    namespace: default
  destination:
    namespace: tenant-a
    # name omitted → defaults to source.name ("app-config")
```

**Expected outcome:** `configmap/app-config` appears in `tenant-a` with the same `.data` plus `projection.be0x74a.io/owned-by: default/app-config-to-tenant-a`. Edits to the source propagate in ~100 ms.

**Gotchas:** if `tenant-a` already has a `ConfigMap/app-config` that doesn't carry the ownership annotation, the Projection reports `Ready=False reason=DestinationConflict` and makes no change.

## 2. `Secret` across namespaces

**File:** [`examples/secret-cross-namespace.yaml`](https://github.com/be0x74a/projection/blob/main/examples/secret-cross-namespace.yaml)

The distribute-a-TLS-cert scenario — typically the source `Secret` is authored by `cert-manager`, `external-secrets`, or `sealed-secrets`.

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

## 4. Multiple destinations from one source

**File:** [`examples/multiple-destinations-from-one-source.yaml`](https://github.com/be0x74a/projection/blob/main/examples/multiple-destinations-from-one-source.yaml)

Until label-selector fan-out lands (see [Roadmap](limitations.md#roadmap)), declare one `Projection` per destination.

```yaml
# Snippet — three tenants, three Projections, one source
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

**Expected outcome:** each tenant sees its own copy of `org-policy` tagged with `tenant=<tenant-id>`. Status is per-Projection — a `DestinationConflict` in `tenant-c` doesn't block `tenant-a`/`tenant-b` from reconciling.

**Gotchas:** duplicates your YAML. Kustomize / Helm templating / GitOps generators are your friend here until multi-destination lands.

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
