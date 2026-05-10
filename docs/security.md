# Security

`projection` is a small operator with a large blast radius, because a `Projection` or `ClusterProjection` CR can reference any Kind the apiserver knows about. This page explains the trade-offs and how to tighten them for production.

The v0.3.0 split into two CRDs — namespaced `Projection` (single-target, in its own namespace) and cluster-scoped `ClusterProjection` (fan-out across multiple namespaces) — is a security-relevant design choice, not just an ergonomic one. The two CRDs sit on different RBAC tiers by design; the rest of this page explains how that lands in practice.

## RBAC scope

The operator ships with a `ClusterRole` granting it `*/*` — every `verb` on every `resource` in every API group. The controller-source marker:

```go
// +kubebuilder:rbac:groups="*",resources="*",verbs=get;list;watch;create;update;patch;delete
```

…is what generates that ClusterRole in `config/rbac/`. The reason it's that broad: a `Projection` or `ClusterProjection` can point at any Kind (including CRDs the operator doesn't know about at build time), so any narrower default would ship broken for the long tail of use cases.

The trade-off is real: a misconfigured or malicious CR can cause the controller to **read** any Secret in the cluster and **write** it to a different namespace. Anyone who can create `ClusterProjection` CRs can effectively exfiltrate data across namespaces they otherwise couldn't access directly. Anyone who can create `Projection` CRs in a namespace can mirror any source the controller can read into that namespace — bounded by the namespace they have authority in, but still a Secret-disclosure risk if the target Kind is sensitive.

## RBAC aggregation defaults

The Helm chart ships three ClusterRoles in v0.3.0 to make Projection authorship usable without forcing every cluster admin to hand-roll bindings:

| ClusterRole                                  | Aggregation labels                            | What it grants                                                                  |
| -------------------------------------------- | --------------------------------------------- | ------------------------------------------------------------------------------- |
| `<release>-projection-namespaced-edit`       | aggregates into `admin` AND `edit`            | CRUD (incl. `deletecollection`) on `projections.projection.sh` in any namespace |
| `<release>-projection-namespaced-view`       | aggregates into `view`                        | Read-only on `projections.projection.sh`                                        |
| `<release>-projection-cluster-admin`         | none (NOT aggregated, by design)              | CRUD on `clusterprojections.projection.sh`                                      |

The first two roles use Kubernetes' [ClusterRole aggregation](https://kubernetes.io/docs/reference/access-authn-authz/rbac/#aggregated-clusterroles) feature: any subject already bound to the standard `admin`, `edit`, or `view` roles automatically picks up the matching Projection privileges. Cluster admins don't need to teach tenants a new RBAC model; the operator slots into the model the cluster already uses.

The third role — `<release>-projection-cluster-admin` — is **not** aggregated. It must be explicitly bound by a cluster admin via a ClusterRoleBinding to whichever subjects should hold the authority to create cluster-scoped Projections. We'll come back to why in the next section.

### Why projection-cluster-admin is not aggregated

ClusterProjection is cluster-scoped: a single ClusterProjection can write destinations across every namespace in the cluster (or every namespace matching a label selector). Aggregating `clusterprojections` CRUD into the standard `admin` role would silently widen every namespace-admin in the cluster into a cluster-tier subject — anyone who can do anything in their namespace would now be able to fan a source object out across the entire cluster.

That's a footgun. A platform team that wants to give `tenant-a` full authority over their namespace expects "full authority over `tenant-a`" — not "full authority over `tenant-a` plus the ability to mirror any Secret the controller can read into every other namespace." If we aggregated into `admin`, every binding to `admin` would become a privilege-escalation vector the operator owner did not intend.

So the chart deliberately separates the two RBAC tiers:

- **Namespace tenants** automatically gain `Projection` (namespaced, single-target) authority via aggregation. Their authority extends only as far as their existing namespace authority does.
- **ClusterProjection** authority requires an explicit, deliberate ClusterRoleBinding from a cluster admin. There is no path by which a tenant can stumble into it.

This split makes the namespaced `Projection` CRD *structurally* safer for tenant self-service: the chart's aggregation defaults push tenants into the namespace-confined CRD, and the cluster-confined CRD remains gated behind a binding nobody can grant accidentally.

### The `rbac.aggregate` Helm value

The chart's `rbac.aggregate` value (default `true`) controls **only the aggregation labels** on the namespaced roles:

```yaml
# values.yaml — defaults
rbac:
  aggregate: true
```

When `rbac.aggregate=true`:
- `<release>-projection-namespaced-edit` and `<release>-projection-namespaced-view` are rendered with the standard `rbac.authorization.k8s.io/aggregate-to-{admin,edit,view}: "true"` labels.
- Subjects bound to the standard `admin` / `edit` / `view` roles automatically gain Projection privileges.
- `<release>-projection-cluster-admin` is rendered (as always — see below) and remains unaggregated.

When `rbac.aggregate=false`:
- `<release>-projection-namespaced-edit` and `<release>-projection-namespaced-view` are **not rendered**. Namespace tenants do **not** automatically gain `Projection` access; you have to bind whatever role grants them Projection CRUD by hand.
- `<release>-projection-cluster-admin` is **still rendered**. The flag is orthogonal to its existence — `rbac.aggregate=false` means "I want explicit RBAC for namespace-tier Projection too," not "I don't want any of this chart's RBAC."

The flag is the right knob for clusters that want completely explicit RBAC (no aggregation surprises) or for environments where the standard `admin`/`edit`/`view` roles have been customized in ways that make additional aggregations risky.

### Tenant self-service: a worked example

Consider a multi-tenant cluster where:

- A platform team installs the chart with default values (`rbac.aggregate=true`).
- A namespace `tenant-a` exists, and Alice is a Kubernetes user bound to the standard `edit` ClusterRole in `tenant-a`:

  ```yaml
  apiVersion: rbac.authorization.k8s.io/v1
  kind: RoleBinding
  metadata:
    name: alice-edit
    namespace: tenant-a
  roleRef:
    apiGroup: rbac.authorization.k8s.io
    kind: ClusterRole
    name: edit
  subjects:
    - kind: User
      name: alice
      apiGroup: rbac.authorization.k8s.io
  ```

What Alice can do, automatically:

- **Create, list, update, delete `Projection` CRs in `tenant-a`.** Aggregation of `<release>-projection-namespaced-edit` into `edit` makes this work without any additional binding. Alice's authority is structurally confined to `tenant-a`: a Projection she creates writes into `tenant-a` (the only namespace it can write to), so she cannot fan data out into peer tenants by editing the CR.
- **Read `Projection` CRs cluster-wide?** Only in namespaces where she's bound to `view`/`edit`/`admin`. The `<release>-projection-namespaced-view` ClusterRole aggregates into `view`, but she still needs a binding to `view` (or higher) in the namespace she wants to read.

What Alice cannot do, deliberately:

- **Create a `ClusterProjection`.** Cluster-scoped Projections require the `<release>-projection-cluster-admin` ClusterRole, which is not aggregated. Without an explicit ClusterRoleBinding to that role (which a cluster admin would have to grant deliberately), `kubectl apply -f clusterprojection.yaml` is rejected by the apiserver with a `forbidden` error — exactly as intended.

This is the property that makes the chart's defaults safe for tenant self-service: granting `edit` in a namespace gives a tenant useful Projection authority without ever escalating them past their own namespace. If the platform team later wants Alice to be a cluster admin for Projections too, they grant her an explicit binding to `<release>-projection-cluster-admin`; nobody backs into that authority by accident.

## Source projectability policy

The primary source-side defense is the **source projectability policy**, documented in detail in [Concepts § 9](concepts.md#9-source-projectability-policy). The defaults:

- **`--source-mode=allowlist`** (default). Sources must carry the annotation `projection.sh/projectable: "true"` to be mirrored. A Projection or ClusterProjection pointing at an unannotated source gets `SourceResolved=False reason=SourceNotProjectable` in status.
- **Source owner veto**: annotation value `"false"` is *always* honored regardless of mode. Post-hoc veto garbage-collects the existing destination(s).

This shifts the trust model from "anyone with `Projection` / `ClusterProjection` create rights reads everything" to "source owners decide what's projectable." Clusters that want the historic wide-open behavior can set `--source-mode=permissive` explicitly.

Note this is a **policy** control, not an isolation boundary (the controller still has cluster-wide read RBAC). Pair it with admission policy (Kyverno, OPA) constraining *who* can add the `projectable=true` annotation for defense-in-depth.

## Hardening recommendations

### 1. Narrow the controller's RBAC to the Kinds you actually mirror

The chart ships a `supportedKinds` value that narrows the operator's ClusterRole from the default `*/*` to an explicit allowlist. Every entry becomes a discrete RBAC rule with the full verb set (get, list, watch, create, update, patch, delete — the controller needs both read on its source and write on its destination).

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
  - apiGroup: projection.sh
    resources: ["*"]
```

**Default** (preserves pre-v0.2 behavior — equivalent to the stock `*/*` ClusterRole):

```yaml
supportedKinds:
  - apiGroup: "*"
    resources: ["*"]
```

**Disable entirely** — the operator can reconcile its own `Projection` and `ClusterProjection` CRs but cannot read or write any other Kind. A CR targeting an external Kind fails with `SourceResolved=False reason=SourceFetchFailed` (`forbidden`):

```yaml
supportedKinds: []
```

#### Wildcard semantics

`*` is allowed in both `apiGroup` and `resources`, with the conventional RBAC meaning:

| Entry | Grants |
| --- | --- |
| `apiGroup: ""` / `resources: [configmaps]` | ConfigMap in the core group only |
| `apiGroup: "*"` / `resources: ["*"]` | Every resource in every group (equivalent to the default) |
| `apiGroup: projection.sh` / `resources: ["*"]` | Every resource in the `projection.sh` group |
| `supportedKinds: []` | Nothing beyond the operator's own `Projection` and `ClusterProjection` CRs |

Note the subtle distinction: `apiGroup: ""` means the **core API group only** (ConfigMap, Secret, Pod, …), while `apiGroup: "*"` means **every group including core**.

#### Choosing an allowlist

1. Enumerate the Kinds currently projected in your cluster:

   ```bash
   {
     kubectl get projections -A -o json
     kubectl get clusterprojections -o json
   } | jq -s '.[].items[].spec.source | "\(.group)/\(.version) \(.kind)"' -r \
     | sort -u
   ```

2. Look up each Kind's plural resource name and API group:

   ```bash
   kubectl api-resources | grep <Kind>
   ```

3. Populate `supportedKinds` with one entry per API group, listing the plural resource names.

4. Deploy and verify:

   ```bash
   helm upgrade projection oci://ghcr.io/projection-operator/charts/projection -f values.yaml
   kubectl auth can-i get configmaps \
     --as=system:serviceaccount:projection-system:projection
   ```

#### Trade-offs

- **Audit-ready ClusterRole** — reviewers see exactly which Kinds the operator can touch.
- **Defense in depth** — a rogue CR cannot target a high-privilege Kind (`Secret` in an unrelated namespace, say) unless you have explicitly allowlisted it.
- **Adding a new projectable Kind requires a chart redeploy.** Acceptable in regulated environments where chart changes go through change-management anyway.
- **`forbidden` errors have two causes** — narrowed RBAC or a genuinely missing resource. See [troubleshooting.md](troubleshooting.md#sourcefetchfailed) for the diagnostic path.

### 2. Restrict who can create CRs

Controlling *who can mirror what* is as important as the controller's RBAC. The chart's [aggregation defaults](#rbac-aggregation-defaults) handle namespaced `Projection` for you (Alice with `edit` in `tenant-a` can write Projections in `tenant-a` and only `tenant-a`); for stricter or finer-grained policy, add admission rules.

**Kubernetes RBAC** — if you've disabled aggregation (`rbac.aggregate=false`) or want to restrict `Projection` access more narrowly than `edit` implies:

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: projection-author
  namespace: platform
rules:
  - apiGroups: ["projection.sh"]
    resources: ["projections"]
    verbs: ["get", "list", "watch", "create", "update", "patch", "delete"]
```

Bind it only to the platform team's namespaces/SAs. Everyone else in `platform` cannot create Projections, even if they have `edit` in the namespace.

For ClusterProjection, the analogous role is `<release>-projection-cluster-admin` — see [Privilege-escalation warning](#privilege-escalation-warning) below before you bind it.

**Admission policies** — Kyverno or OPA Gatekeeper for fine-grained rules:

- Deny `Projection`s whose `spec.source.namespace` is not in an allowlist.
- Deny `Projection`s whose `spec.source.kind` is `Secret` unless the creator is in a specific group.
- Deny `ClusterProjection`s whose `destination.namespaceSelector` is broad (e.g. `matchLabels: {}`, which would match every namespace).
- Require `overlay.labels.tenant` to match the Projection's own namespace.

These rules run at admission time, so they fail `kubectl apply`, not at reconcile.

### 3. Destination ownership annotation

Every destination written by `projection` is stamped with an ownership annotation; the annotation is the controller's safety primitive. On every reconcile, before updating or deleting a destination, the controller checks the annotation against its own coordinates and refuses to touch objects it doesn't own. A would-be conflicting CR (or a buggy human action) cannot silently overwrite an unrelated tool's object — `DestinationConflict` is reported on status instead.

The annotation key depends on which CRD owns the destination:

| Owning CRD          | Annotation                                                                | Example value                  |
| ------------------- | ------------------------------------------------------------------------- | ------------------------------ |
| `Projection`        | `projection.sh/owned-by-projection`                                       | `tenant-a/app-config-mirror`   |
| `ClusterProjection` | `projection.sh/owned-by-cluster-projection`                               | `shared-app-config-fanout`     |

A second marker, the label `projection.sh/owned-by-projection-uid: <uid>` (or `projection.sh/owned-by-cluster-projection-uid` for the cluster CRD), exists for two reasons:

1. **Cleanup paths** (stale-destination cleanup, finalizer sweep) find owned destinations via a single cluster-wide `List(LabelSelector)` instead of walking every namespace.
2. **Destination-side watches** — `ensureDestWatch` registers a label-filtered watch on the destination GVK so that a manual `kubectl delete` of a destination triggers an immediate reconcile.

The label is a watch-filter and indexing hint. The annotation is the authoritative ownership signal.

#### Label-trust caveat for `ensureDestWatch`

The `ensureDestWatch` machinery uses the UID label to register watches and the cleanup paths use it to enumerate owned destinations cheaply. **The UID label is never a sufficient access-decision signal on its own.** Every label-driven list is followed by an annotation check on each candidate before the controller writes or deletes. The discipline is:

- **Annotation = authoritative.** `isOwnedByProjection` / `isOwnedByClusterProjection` (the only thing standing between us and overwriting somebody else's object) reads the annotation, compares to the CR's coordinates, and refuses to act if they don't match.
- **Label = watch-filter hint.** Deciding whether to enqueue a reconcile event, deciding whether an object is a candidate for cleanup. Used to *narrow* what we look at, never to decide *whether* to act.

This matters because the label is a single-key string match — anyone with write access to a destination's metadata could in principle stamp the UID label onto an unrelated object. If they do, what happens? The label-filtered watch fires for that object, the controller loads it, the annotation check sees the wrong owner (or no owner annotation at all), the reconcile no-ops. **At most this costs one wasted reconcile** that spins up, checks the annotation, and exits. It cannot cause the controller to write to or delete a stranger's object.

In other words: the worst-case cost of label spoofing is a small amount of cheap CPU work in the controller. The threat surface that label-spoofing would open up — silent overwrites of stranger objects — is closed by the annotation check, which is the only thing that can authorize a write.

Treat both markers as part of the supported API: don't hand-edit them on objects in production. If you genuinely need to take over an existing object with `projection`, change its annotation deliberately — knowing that the controller will then update and delete it as if it had created it.

### 4. NetworkPolicy

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

Adjust selectors for your cluster. The chart renders an equivalent NetworkPolicy when `networkPolicy.enabled=true` (additional egress rules go in `networkPolicy.extraEgress`); the example above is what you'd write by hand without the chart.

### 5. Restricted Pod Security Standard defaults

The Helm chart ships pod- and container-level `securityContext` defaults that line up with the Kubernetes [restricted Pod Security Standard](https://kubernetes.io/docs/concepts/security/pod-security-standards/#restricted) — `runAsNonRoot: true`, `runAsUser: 65532`, `fsGroup: 65532`, `seccompProfile: RuntimeDefault` at the pod level, and `allowPrivilegeEscalation: false`, `readOnlyRootFilesystem: true`, `capabilities.drop: [ALL]` at the container level. They are exposed as `securityContext.pod` and `securityContext.container` so cluster admins running an even stricter Pod Security Admission policy can override individual fields without losing the rest of the profile. There is no good reason to relax them — the controller is a single Go binary running as PID 1, with no shell and no need to write to the filesystem outside `/tmp`.

### 6. ServiceAccount annotations (IRSA / Workload Identity)

When the controller needs cloud-provider IAM credentials — usually because the source or destination Kind is reconciled from a managed service (e.g. AWS Secrets Manager via `external-secrets`, GCP Secret Manager) and you want to scope the operator's machine identity rather than mount static credentials — set `serviceAccount.annotations` in the chart values. The annotations are passed through verbatim to the operator's `ServiceAccount`:

```yaml
# AWS IRSA
serviceAccount:
  annotations:
    eks.amazonaws.com/role-arn: arn:aws:iam::123456789012:role/projection-controller

# GKE Workload Identity
serviceAccount:
  annotations:
    iam.gke.io/gcp-service-account: projection@my-project.iam.gserviceaccount.com
```

This keeps the IAM/RBAC trust path inside the cluster's own identity primitives rather than introducing a separate credential file the operator has to mount.

## Privilege-escalation warning

Binding `<release>-projection-cluster-admin` to a subject is not a tenant-scoped grant. It is a **cluster-wide read primitive** for any Kind the controller's ClusterRole permits.

Concretely: a subject with CRUD on `clusterprojections.projection.sh` can author a ClusterProjection that:

- References any source the controller can read (with stock `*/*` RBAC, that's any object in the cluster — every Secret, every ConfigMap, every CR).
- Fans the source out into any namespace, or every namespace via a broad `namespaceSelector`.
- Renames the destination to a name the subject controls in their own namespace, where they can `kubectl get` it back.

The source projectability policy ([Concepts § 9](concepts.md#9-source-projectability-policy)) is the per-source defense — sources have to opt in to being mirrored. But projectability is a *policy* control, not an isolation boundary: a privileged user who can also write the `projection.sh/projectable` annotation on sources (e.g. a cluster admin who can edit any object) can defeat it. So in the worst case, granting `<release>-projection-cluster-admin` to a tenant-scoped subject can become a path to disclosure of any source in the cluster that's been opted in.

**Recommendations:**

- **Do not bind `<release>-projection-cluster-admin` to namespace-scoped subjects expecting it to be tenant-bounded.** It isn't. The role's authority is defined by what the *controller* can read, not by what the subject's other RBAC says.
- **Bind it to subjects that are already cluster-scoped** — a platform-engineering group, a designated set of cluster admins. Keep it consistent with how you already provision cluster-tier authority.
- **Pair it with admission policy** if you must grant ClusterProjection authority to a less-privileged subject. Kyverno or Gatekeeper rules that constrain `spec.source.namespace`, `spec.source.kind`, or the `destination.namespaceSelector` shape can pin down what a permitted subject is actually allowed to fan out.
- **Combine with `supportedKinds`** to reduce the controller's read surface in the first place. The smaller the controller's ClusterRole, the smaller the disclosure surface a ClusterProjection-author can reach.

This is the same design point as the [non-aggregation choice](#why-projection-cluster-admin-is-not-aggregated) above, viewed from the binding side: aggregation hides the privilege escalation behind an existing role; explicit binding makes it visible. A cluster admin granting `<release>-projection-cluster-admin` is making a deliberate "this subject can read across tenant boundaries" decision; we want that to be a conversation, not a side effect.

## Image supply chain

Release images are pushed to `ghcr.io/projection-operator/projection` and **cosign-signed** with GitHub's OIDC keyless workflow. Verify before pulling:

```bash
cosign verify ghcr.io/projection-operator/projection:v0.3.1 \
  --certificate-identity-regexp "https://github.com/projection-operator/projection/.github/workflows/.*" \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```

The Helm chart is published to `oci://ghcr.io/projection-operator/charts/projection` and signed with the same workflow:

```bash
cosign verify ghcr.io/projection-operator/charts/projection:0.3.1 \
  --certificate-identity-regexp "https://github.com/projection-operator/projection/.github/workflows/.*" \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```

Running images are **distroless**, **multi-arch** (`amd64`, `arm64`), **non-root**, with `readOnlyRootFilesystem: true` in the supplied Deployment.

## Audit trail

Every destination write is observable from three places:

- **Kubernetes Events** on the `Projection` or `ClusterProjection` (`Projected`, `Updated`, `DestinationConflict`, ...). See [Observability](observability.md#2-kubernetes-events).
- **Status conditions** on the CR — `lastTransitionTime` tells you when each state changed; for ClusterProjection, `status.namespacesWritten` and `status.namespacesFailed` give per-fan-out counts.
- **Cluster audit logs** capture every `Create`/`Update`/`Delete` the controller does on destination objects, with the controller's service account as the subject.

Together these are enough to answer "who created this object, when, and on whose behalf?" without any extra tooling.

### Recommended audit policy

Because `ClusterProjection` writes are cluster-scoped and inherently broader-blast-radius than namespaced `Projection` writes, the recommended audit policy logs the two CRDs at different levels: `RequestResponse` for ClusterProjection (full body, lower volume — every CR is a deliberate cluster-tier action) and `Metadata` for Projection (verb + object identity, higher volume — namespace-tier traffic that's noisier).

```yaml
apiVersion: audit.k8s.io/v1
kind: Policy
omitStages:
  - RequestReceived
rules:
  # ClusterProjection: full request and response, including spec changes,
  # because every change is a cluster-tier authority assertion. Volume is
  # low (most clusters will have tens, not thousands).
  - level: RequestResponse
    resources:
      - group: projection.sh
        resources: ["clusterprojections"]
    verbs: ["create", "update", "patch", "delete", "deletecollection"]

  # Projection: just metadata (who/what/when), not the spec body. These
  # are namespace-tier operations and a busy cluster can have many; the
  # spec is reconstructable from etcd/Git/SCM. Audit volume drops sharply.
  - level: Metadata
    resources:
      - group: projection.sh
        resources: ["projections"]
    verbs: ["create", "update", "patch", "delete", "deletecollection"]

  # Reads at Metadata level — distinguishes "who looked at this" from
  # "who changed this" without ballooning log volume.
  - level: Metadata
    resources:
      - group: projection.sh
        resources: ["projections", "clusterprojections"]
    verbs: ["get", "list", "watch"]
```

Pair with the controller's own structured logs (which carry `controller=projection` vs `controller=clusterprojection`) and Events for a complete picture: audit log = "who changed the CR," controller log + events = "what the controller did because of it."

## Reporting vulnerabilities

Privately via [GitHub Security Advisories](https://github.com/projection-operator/projection/security/advisories/new). See `SECURITY.md` in the repo for the process.
