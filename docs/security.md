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

The chart ships a `supportedKinds` value that narrows the operator's ClusterRole from the default `*/*` to an explicit allowlist. Every entry becomes a discrete RBAC rule with the full verb set (get, list, watch, create, update, patch, delete — a Projection needs both read on its source and write on its destination).

**Strict — read+write for two core-group Kinds:**

```yaml
# values.yaml
supportedKinds:
  - apiGroup: ""
    resources: [configmaps, secrets]
```

**Moderate — any resource in a trusted group** (useful when your cluster defines custom CRDs under a single group):

```yaml
supportedKinds:
  - apiGroup: projection.be0x74a.io
    resources: ["*"]
```

**Default** (preserves pre-v0.2 behavior — equivalent to the stock `*/*` ClusterRole):

```yaml
supportedKinds:
  - apiGroup: "*"
    resources: ["*"]
```

**Disable entirely** — the operator can reconcile its own `Projection` CRs but cannot read or write any other Kind. A Projection targeting an external Kind fails with `SourceResolved=False reason=SourceFetchFailed` (`forbidden`):

```yaml
supportedKinds: []
```

#### Wildcard semantics

`*` is allowed in both `apiGroup` and `resources`, with the conventional RBAC meaning:

| Entry | Grants |
| --- | --- |
| `apiGroup: ""` / `resources: [configmaps]` | ConfigMap in the core group only |
| `apiGroup: "*"` / `resources: ["*"]` | Every resource in every group (equivalent to the default) |
| `apiGroup: projection.be0x74a.io` / `resources: ["*"]` | Every resource in the `projection.be0x74a.io` group |
| `supportedKinds: []` | Nothing beyond the operator's own `Projection` CRs |

Note the subtle distinction: `apiGroup: ""` means the **core API group only** (ConfigMap, Secret, Pod, …), while `apiGroup: "*"` means **every group including core**.

#### Choosing an allowlist

1. Enumerate the Kinds currently projected in your cluster:

   ```bash
   kubectl get projections -A -o json \
     | jq -r '.items[].spec.source | "\(.apiVersion) \(.kind)"' \
     | sort -u
   ```

2. Look up each Kind's plural resource name and API group:

   ```bash
   kubectl api-resources | grep <Kind>
   ```

3. Populate `supportedKinds` with one entry per API group, listing the plural resource names.

4. Deploy and verify:

   ```bash
   helm upgrade projection oci://ghcr.io/be0x74a/charts/projection -f values.yaml
   kubectl auth can-i get configmaps \
     --as=system:serviceaccount:projection-system:projection
   ```

#### Trade-offs

- **Audit-ready ClusterRole** — reviewers see exactly which Kinds the operator can touch.
- **Defense in depth** — a rogue Projection cannot target a high-privilege Kind (`Secret` in an unrelated namespace, say) unless you have explicitly allowlisted it.
- **Adding a new projectable Kind requires a chart redeploy.** Acceptable in regulated environments where chart changes go through change-management anyway.
- **`forbidden` errors have two causes** — narrowed RBAC or a genuinely missing resource. See [troubleshooting.md](troubleshooting.md#sourcefetchfailed) for the diagnostic path.

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
