# Security

`projection` is a small operator with a large blast radius, because a `Projection` CR can reference any Kind the apiserver knows about. This page explains the trade-offs and how to tighten them for production.

## RBAC scope

The operator ships with a `ClusterRole` granting it `*/*` — every `verb` on every `resource` in every API group. The controller-source marker:

```go
// +kubebuilder:rbac:groups="*",resources="*",verbs=get;list;watch;create;update;patch;delete
```

…is what generates that ClusterRole in `config/rbac/`. The reason it's that broad: a `Projection` can point at any Kind (including CRDs the operator doesn't know about at build time), so any narrower default would ship broken for the long tail of use cases.

The trade-off is real: a misconfigured or malicious `Projection` can cause the controller to **read** any Secret in the cluster and **write** it to a different namespace. Anyone who can create `Projection` CRs in *any* namespace can effectively exfiltrate data across namespaces they otherwise couldn't access directly.

## Source projectability policy

The primary user-facing defense is the **source projectability policy**, documented in detail in [Concepts § 7](concepts.md#7-source-projectability-policy). The defaults:

- **`--source-mode=allowlist`** (default). Sources must carry the annotation `projection.be0x74a.io/projectable: "true"` to be mirrored. A `Projection` pointing at an unannotated source gets `SourceResolved=False reason=SourceNotProjectable` in status.
- **Source owner veto**: annotation value `"false"` is *always* honored regardless of mode. Post-hoc veto garbage-collects the existing destination.

This shifts the trust model from "anyone with Projection-create rights reads everything" to "source owners decide what's projectable." Clusters that want the historic wide-open behavior can set `--source-mode=permissive` explicitly.

Note this is a **policy** control, not an isolation boundary (the controller still has cluster-wide read RBAC). Pair it with admission policy (Kyverno, OPA) constraining *who* can add the `projectable=true` annotation for defense-in-depth.

## Hardening recommendations

### 1. Narrow the controller's RBAC to the Kinds you actually mirror

If you only ever project `ConfigMap` and `Secret`, replace the stock ClusterRole with a narrowed one. Example:

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: projection-manager
rules:
  - apiGroups: [""]
    resources: ["configmaps", "secrets"]
    verbs: ["get", "list", "watch", "create", "update", "patch", "delete"]
  - apiGroups: ["projection.be0x74a.io"]
    resources: ["projections"]
    verbs: ["get", "list", "watch", "create", "update", "patch", "delete"]
  - apiGroups: ["projection.be0x74a.io"]
    resources: ["projections/status"]
    verbs: ["get", "update", "patch"]
  - apiGroups: ["projection.be0x74a.io"]
    resources: ["projections/finalizers"]
    verbs: ["update"]
```

A Projection referencing any other Kind will then fail with `SourceResolutionFailed` or `SourceFetchFailed`, which is the correct outcome — it's cluster policy, not silent behavior.

The stock chart accepts an override for the controller ClusterRole; see the chart `values.yaml` `rbac.clusterRoleRules` field.

### 2. Restrict who can create `Projection` CRs in which namespaces

Controlling *who can mirror what* is as important as the controller's RBAC. Two common patterns:

**Kubernetes RBAC** — the simplest fix. Only grant `create` on `projections.projection.be0x74a.io` to the namespaces / service accounts that actually need it:

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: projection-author
  namespace: platform
rules:
  - apiGroups: ["projection.be0x74a.io"]
    resources: ["projections"]
    verbs: ["get", "list", "watch", "create", "update", "patch", "delete"]
```

Bind it only to the platform team's namespaces/SAs. Everyone else cannot create Projections.

**Admission policies** — Kyverno or OPA Gatekeeper for fine-grained rules:

- Deny Projections whose `spec.source.namespace` is not in an allowlist.
- Deny Projections whose `spec.source.kind` is `Secret` unless the creator is in a specific group.
- Require `overlay.labels.tenant` to match the Projection's own namespace.

These rules run at admission time, so they fail `kubectl apply`, not at reconcile.

### 3. NetworkPolicy

The controller only talks to the apiserver. Restrict its egress to exactly that:

```yaml
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: projection-controller-egress
  namespace: projection-system
spec:
  podSelector:
    matchLabels:
      control-plane: controller-manager
  policyTypes: [Egress]
  egress:
    - to:
        - namespaceSelector:
            matchLabels:
              kubernetes.io/metadata.name: kube-system
          podSelector:
            matchLabels:
              component: kube-apiserver
      ports:
        - protocol: TCP
          port: 6443
    # DNS
    - to:
        - namespaceSelector:
            matchLabels:
              kubernetes.io/metadata.name: kube-system
          podSelector:
            matchLabels:
              k8s-app: kube-dns
      ports:
        - protocol: UDP
          port: 53
        - protocol: TCP
          port: 53
```

Adjust selectors for your cluster. The chart ships an optional NetworkPolicy under `config/network-policy/` you can enable.

## Image supply chain

Release images are pushed to `ghcr.io/be0x74a/projection` and **cosign-signed** with GitHub's OIDC keyless workflow. Verify before pulling:

```bash
cosign verify ghcr.io/be0x74a/projection:v0.1.0-alpha \
  --certificate-identity-regexp "https://github.com/be0x74a/projection/.github/workflows/.*" \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```

The Helm chart is published to `oci://ghcr.io/be0x74a/charts/projection` and signed with the same workflow:

```bash
cosign verify oci://ghcr.io/be0x74a/charts/projection:0.1.0-alpha \
  --certificate-identity-regexp "https://github.com/be0x74a/projection/.github/workflows/.*" \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```

Running images are **distroless**, **multi-arch** (`amd64`, `arm64`), **non-root**, with `readOnlyRootFilesystem: true` in the supplied Deployment.

## Audit trail

Every destination write is observable from three places:

- **Kubernetes Events** on the `Projection` (`Projected`, `Updated`, `DestinationConflict`, ...). See [Observability](observability.md#2-kubernetes-events).
- **Status conditions** on the `Projection` — `lastTransitionTime` tells you when each state changed.
- **Cluster audit logs** capture every `Create`/`Update`/`Delete` the controller does on destination objects, with the controller's service account as the subject.

Together these are enough to answer "who created this object, when, and on whose behalf?" without any extra tooling.

## Reporting vulnerabilities

Privately via [GitHub Security Advisories](https://github.com/be0x74a/projection/security/advisories/new). See `SECURITY.md` in the repo for the process.
