# vs alternatives

There are two mature tools that overlap with `projection`: [emberstack/Reflector](https://github.com/emberstack/kubernetes-reflector) and [Kyverno `generate`](https://kyverno.io/docs/writing-policies/generate/). Both are excellent and deployed widely. This page is about when each is the right choice.

`projection.sh/v1` ships two CRDs — namespaced `Projection` (single-target, in its own namespace) and cluster-scoped `ClusterProjection` (fan-out across namespaces). The split is itself part of the comparison story below: it changes how `projection` lines up against Kyverno's `Policy`/`ClusterPolicy` distinction and how it lines up against Reflector's source-side annotation model.

## At a glance

|                                          | projection                              | emberstack/Reflector                        | Kyverno `generate`                                   |
| ---------------------------------------- | --------------------------------------- | ------------------------------------------- | ---------------------------------------------------- |
| **Scope of supported Kinds**             | Any (RESTMapper-driven)                 | `ConfigMap`, `Secret` only                  | Any                                                  |
| **Source-of-truth shape**                | A `Projection` (namespaced) or `ClusterProjection` (cluster) CR per mirror | Annotations on the source object   | A cluster-wide `ClusterPolicy` / namespaced `Policy` |
| **Cluster-scoped fan-out CR**            | Yes — `ClusterProjection`               | No (Reflector only ships one annotation surface) | Yes — `ClusterPolicy`, but policy-shaped (rule-based) not per-resource |
| **Namespaced single-target CR**          | Yes — `Projection`                      | No (any annotation on the source fans out)  | Yes — `Policy` (still policy-shaped)                 |
| **Tenant self-service for in-namespace mirrors** | Yes — namespaced `Projection` aggregates into `edit` via `rbac.aggregate` | No — Reflector's mirror authority is cluster-tier | No — `Policy` exists but generate semantics still require cluster-tier authority on most builds |
| **Multi-namespace fan-out**              | Yes, via `ClusterProjection` (explicit `namespaces:` list or `namespaceSelector`) | Yes, via `reflection-auto-namespaces` (regex-based) | Yes, via policy match selectors                      |
| **Per-mirror status / conditions**       | Yes (`Ready`, `SourceResolved`, `DestinationWritten` — rollup for fan-out, with `namespacesWritten`/`namespacesFailed` counters) | Partial (reflected on source annotations) | No (policy-level, not mirror-level)         |
| **Kubernetes Events per outcome**        | `Projected`, `Updated`, `DestinationConflict`, `SourceFetchFailed`, ... (per-namespace for ClusterProjection fan-out) | Limited | Policy-engine events |
| **Conflict semantics**                   | Refuses to overwrite unowned objects; reports `DestinationConflict` | Overwrites | Configurable via `synchronize`, generally overwrites |
| **Watch-driven propagation**             | Yes, dynamic per-GVK metadata-only watch on sources; label-filtered watch on destinations | Yes | Yes |
| **Admission-time source validation**     | Yes (pattern-validated source fields, CEL on `SourceRef` and `ClusterProjection.destination`) | n/a | Yes |
| **Prometheus metrics**                   | `projection_reconcile_total{kind,result}`, `projection_watched_gvks`, `projection_watched_dest_gvks` | Partial | Rich policy-engine metrics |
| **Operational footprint**                | Two CRDs + Deployment                   | One CRD + Deployment                        | Full Kyverno control plane (several controllers)     |
| **Cluster-wide RBAC surface**            | `*/*` by default; narrowable per-Kind via Helm `supportedKinds` | Namespace-restrictable (scope narrower) | `*/*` (policy engine) |

## Source-of-truth model

The biggest difference is **where the rule lives**.

- **Reflector** puts the rule on the *source object*: you annotate a Secret with `reflector.v1.k8s.emberstack.com/reflection-allowed: "true"` and `reflector.v1.k8s.emberstack.com/reflection-auto-namespaces: "tenant-.*"`. The "who's mirroring this?" question is answered by listing annotations on the source.
- **Kyverno** puts the rule in a *cluster-wide policy*: one `ClusterPolicy` can generate mirrored objects based on selectors, triggers, and JMESPath expressions. Reading "how did this object get here?" means finding the right policy.
- **projection** puts the rule in a *per-mirror CR*: a `Projection` (namespaced) maps one source to one destination in its own namespace; a `ClusterProjection` (cluster-scoped) maps one source to a fan-out target set. Reading "how did this object get here?" means `kubectl describe` on the relevant CR and grepping the destination's `projection.sh/owned-by-projection` or `projection.sh/owned-by-cluster-projection` annotation.

Consequence: `projection` is the easiest to diff in GitOps (each mirror is its own YAML file) and the easiest to reason about per-resource (`kubectl get projections -A` plus `kubectl get clusterprojections` is the full inventory). Reflector is the easiest when you already manage the *source* in GitOps and don't want to create another object per destination. Kyverno is the most powerful when the mirror rule needs to match on dozens of sources at once.

## How the namespaced/cluster split lines up against Kyverno

Kyverno's `Policy`/`ClusterPolicy` distinction and `projection`'s `Projection`/`ClusterProjection` distinction look superficially similar — both give you "namespaced" and "cluster-scoped" flavors of the same idea. They are not the same shape underneath:

- **Kyverno is *policy-shaped*.** A `ClusterPolicy` with a `generate` rule is a rule engine: `match` blocks select trigger objects, `preconditions` filter on JMESPath expressions and labels, the rule fires across many `(source, destination)` pairs as triggers come and go. One policy can spawn dozens of mirrored objects per fire. The unit of management is the policy.
- **`projection` is *per-resource-shaped*.** A `Projection` or `ClusterProjection` says "this specific source object → one destination (or these specific destinations)." There's no rule language; the source is identified by its concrete `(group, version, kind, namespace, name)` quadruple. The unit of management is the mirror.

The `Projection` / `ClusterProjection` split is therefore not a translation of `Policy` / `ClusterPolicy` — it's a split along the *destination* axis (single-target vs. fan-out), not the *match* axis (one rule fires across many sources). If your need is "for every Secret with label X, mirror it into every namespace with label Y," that's still Kyverno: `projection` won't do source-side selectors. But once the source is fixed (a single named object you want mirrored), `projection`'s split gives you a CR shape that maps cleanly onto how you'd RBAC-tier the work — namespace tenants own their own namespaced mirrors, cluster admins own the fan-out CRs.

## Tenant self-service is structurally safer

The split has a security consequence that's easier to articulate post-v0.3.0 than it was before: **a namespaced `Projection` cannot escape its own namespace.** There is no `destination.namespace` field on a `Projection` — the destination namespace is always the Projection's own `metadata.namespace`. That makes namespace-scoped RBAC on `projections.projection.sh` a *structural* confinement boundary, not just a policy assertion.

Concretely: granting `tenant-a` CRUD on `projections.projection.sh` in `tenant-a` lets that tenant self-serve mirrors of any source the controller can read into `tenant-a`, and only into `tenant-a`. They can't widen the scope by editing the spec — the spec doesn't have a knob to widen. The Helm chart's `rbac.aggregate=true` default takes this further: namespaced Projection CRUD is aggregated into the standard `edit` and `admin` ClusterRoles, so tenants who already have `edit` in their namespace automatically gain `Projection` authority — no extra binding, no extra learning curve.

Compare:

- **Reflector** has only one CRD shape (annotation-on-source), and reflection authority is structurally cluster-tier — annotating a Secret with `reflection-auto-namespaces: "tenant-.*"` writes copies into namespaces the source author has no other authority over. A cluster admin has to either trust source authors with that authority, or wrap Reflector in admission policy that restricts who can add the annotation.
- **Kyverno `generate`** can be authored as a namespaced `Policy`, but generate semantics still typically require cluster-tier authority on the resources being generated. A `Policy` in `tenant-a` that generates a Secret in `tenant-b` is not how `Policy`'s isolation is supposed to work, and admission policy is usually the thing keeping it honest.

`projection`'s namespaced `Projection` is the only one of the three where the CR's shape itself confines what it can do. That property — "the CRD I let tenants create cannot be twisted into cross-tenant data flow" — is what makes the chart's aggregation defaults safe to ship.

The cluster-scoped half of the story is intentionally the opposite. A `ClusterProjection` *can* fan out across the cluster, which is exactly the authority you need for "platform team distributes a TLS root CA into every tenant namespace." That authority deserves to be deliberate: `<release>-projection-cluster-admin` is **not** aggregated and must be explicitly bound. See [Security § Tenant self-service](security.md#tenant-self-service-a-worked-example) for a worked example.

## Supported Kinds

Reflector supports `ConfigMap` and `Secret`. That's deliberate — the code is tuned for their semantics. If all you ever mirror is Secrets (the common case!), you don't need anything more, and Reflector's annotation-on-source UX is genuinely simpler than maintaining a separate `Projection` CR per mirror.

`projection` and Kyverno both work on any Kind. `projection` does this via the `RESTMapper` plus Kind-specific stripping rules for fields the apiserver allocates (currently `Service`, `PersistentVolumeClaim`, `Pod`, and `Job`; see [Limitations](limitations.md#some-kinds-need-extra-stripping-rules) for the full list and the bar for adding new entries). Kyverno does it via a generic generate rule with optional variable substitution — strictly more general, at the cost of having to think in policy terms.

## Status and observability

Per-mirror status is where the split is clearest.

- **`projection`** exposes three conditions (`SourceResolved`, `DestinationWritten`, `Ready`) per `Projection` and per `ClusterProjection`, plus per-namespace counters (`status.namespacesWritten`, `status.namespacesFailed`) on ClusterProjection rollups, plus Events per state transition, plus a `projection_reconcile_total{kind,result}` counter labeled by reconciler and outcome. You can alert on conflicts cheaply, and you can split alerting traffic by `kind=Projection` vs `kind=ClusterProjection` so a misbehaving cluster-tier mirror doesn't drown out namespace-tier signal. See [Observability](observability.md).
- **Reflector** reports reflection state via annotations on the source (e.g. `reflector.v1.k8s.emberstack.com/reflects`). Useful, but not a first-class condition you can query via `kubectl wait`.
- **Kyverno** has rich policy-engine metrics and reports — but they're keyed on the *policy*, not the individual generated object. To answer "did the `shared-tls` Secret land in `tenant-a`?" you still `kubectl get` the destination.

If you want to put `kubectl wait --for=condition=Ready` in a CI pipeline or chart hook, `projection` is designed for that.

## Conflict semantics

- **`projection`**: refuses to overwrite destinations it doesn't own. Ownership is the `projection.sh/owned-by-projection` annotation (or `projection.sh/owned-by-cluster-projection` for ClusterProjection-owned destinations); a UID label assists watches and cleanup but is never the access-decision signal. Reports `DestinationConflict` on status and as an event. This is the default and deliberate.
- **Reflector**: generally overwrites (with some safeguards).
- **Kyverno `generate`**: with `synchronize: true`, Kyverno keeps the destination in sync; it will overwrite drift.

`projection`'s opposite-default is a feature when you adopt it alongside other tooling, but it's not strictly better — overwrite-by-default is correct for the common Reflector use case (the source-side annotation is itself a deliberate "mirror this" signal, and a stale unowned destination is usually a bug to clobber, not preserve). Pick the conflict semantics that match your trust model.

## Watch model and propagation latency

All three are watch-driven; steady-state propagation is sub-second in all three. `projection` registers metadata-only watches per source GVK lazily (no cost until you create a mirror for a Kind) and uses a field indexer to map source events to mirroring CRs in O(1). It also registers label-filtered destination-side watches (`ensureDestWatch`) so a manual `kubectl delete` of a destination triggers an immediate reconcile rather than waiting for a periodic requeue. Measured end-to-end source-edit-to-destination-update on Kind sits at ~16 ms p50 / ~25 ms p99 for single-destination namespaced Projections and ~36–76 ms (first-to-last destination) p50 for ClusterProjection selector fan-out at 100 namespaces — see [Scale](scale.md) for the full numbers, methodology, and caveats.

## Operational surface

- **`projection`**: two CRDs (`Projection` and `ClusterProjection`), one Deployment, one container. Distroless, multi-arch. ClusterRole is `*/*` (see [Security](security.md) for narrowing recommendations).
- **Reflector**: one CRD, one Deployment. RBAC naturally narrower (reads/writes ConfigMaps/Secrets only).
- **Kyverno**: multi-controller control plane, admission webhooks, report controllers. Significantly more surface, but you probably already run it.

## When to pick which

- **You already run Reflector and only mirror Secrets.** Keep Reflector. The added per-mirror CR isn't worth the churn — though note that `ClusterProjection`'s `namespaceSelector` gives you Reflector-style fan-out with per-namespace status and a CR you can `kubectl get`.
- **You already run Kyverno and want to mirror *based on source labels* (one policy generating many destinations from multiple sources).** Stick with Kyverno — `projection` doesn't do source selectors, only destination namespace selectors via `ClusterProjection`.
- **You want the mirror rule to be a first-class, diffable, per-destination object you can `kubectl get` and wait on — and you need Kinds beyond ConfigMap/Secret.** That's `projection`.
- **You want tenants to self-serve same-namespace mirrors without granting them cluster-tier authority.** That's namespaced `Projection` paired with the chart's `rbac.aggregate=true` default. Granting `edit` in a namespace automatically gives the tenant `Projection` CRUD in that namespace — and the CRD's shape (no `destination.namespace`) prevents the tenant from twisting it into cross-namespace authority.
- **You want conflict-safe-by-default** (refuses to overwrite unowned objects). That's `projection`; the others generally don't do this.
- **You want per-mirror status conditions and a Prometheus counter you can alert on.** That's `projection`.
- **Cross-cluster mirroring.** None of these three today. Consider [Admiralty](https://admiralty.io/), [KubeFed](https://github.com/kubernetes-retired/kubefed) (retired but concepts still inform alternatives), or [Cluster API](https://cluster-api.sigs.k8s.io/) + GitOps.

## Where `projection` is the wrong choice

The flip side of the section above. There are real cases where the right answer isn't `projection`, and pretending otherwise wastes your time:

- **You only need ConfigMap/Secret mirroring and don't already run Reflector.** Reflector is the simpler shape: annotation-on-source instead of a separate CR, narrower RBAC scope (it only needs ConfigMap/Secret access), and a much smaller behavioral surface. The "any Kind" generality `projection` brings is a cost — a broader RBAC default (`*/*` until you narrow it via `supportedKinds`), a larger CRD schema (two CRDs, not one), and the conceptual overhead of the namespaced/cluster split — that you don't need to pay.

- **You need conditional mirroring or per-source policy logic.** "Mirror this Secret only into namespaces labeled `tier=prod` *and* whose annotations include `mirror=enabled`" — that's a Kyverno `generate` policy, not a `projection`. Kyverno's match/exclude/preconditions language exists precisely for this; `ClusterProjection`'s `namespaceSelector` is a single label-selector and nothing else. Trying to encode multi-condition logic by maintaining a fleet of CRs is the wrong shape.

- **You need to mirror Kinds beyond mirroring** — generate a `RoleBinding` from a `ServiceAccount` annotation, derive a `NetworkPolicy` from a `Namespace` label, materialize a `Job` per `ConfigMap` create. `projection` only mirrors Kind-to-same-Kind; it doesn't transform or derive. Kyverno's `generate` plus a context block does this in one rule.

- **Per-destination overlays for fan-out.** ClusterProjection applies a single overlay uniformly to every destination — every fan-out copy gets the same labels and annotations. If you need *distinct* overlays per destination (a `tenant: <name>` label that varies by namespace, per-team annotation provenance), one `ClusterProjection` is the wrong shape. Use multiple namespaced `Projection`s instead — one per destination namespace, each with its own overlay. The repository ships [`examples/multiple-destinations-from-one-source.yaml`](https://github.com/projection-operator/projection/blob/main/examples/multiple-destinations-from-one-source.yaml) as the worked pattern. (This is a non-goal for v0.3.0 — per-target overlays on `ClusterProjection` are explicitly not in scope. The split into two CRDs is what makes the per-destination-overlay pattern clean: each `Projection` is a first-class CR with its own status, so a `DestinationConflict` in one tenant doesn't mark the others failed.)

- **Per-destination conditions you can `kubectl wait` on independently.** `ClusterProjection` rolls up its target-set status into a single `DestinationWritten` condition with `namespacesWritten`/`namespacesFailed` counters — you cannot `kubectl wait` on "tenant-a got the destination" specifically without also waiting on the other targets. Kyverno reports per-policy and per-rule status, which is closer to per-destination than `ClusterProjection` is today. The `Projection`-per-destination pattern (above) gets you per-destination conditions at the cost of more YAML.

- **You're on a cluster older than Kubernetes 1.32.** Both CRDs use CEL `XValidation` rules that require apiserver ≥ 1.32 to evaluate `optional` fields correctly (the `SourceRef` empty-group-requires-version rule, the `ClusterProjection.destination` mutex rules). The reconciler enforces the same invariants as defense-in-depth, so older clusters work — but the better admission UX (early `kubectl apply` rejection instead of a runtime status flip) needs the version floor. Reflector and Kyverno don't have this constraint.

- **You're allergic to a per-resource CR per mirror.** `projection`'s shape is "one CR per mirror" — a namespaced `Projection` per single-target mirror, a `ClusterProjection` per fan-out. If you're mirroring 200 Secrets across 50 namespaces and you don't want to template a few hundred CRs, Reflector's annotate-the-source UX is genuinely less ceremony.

- **You need cross-cluster.** `projection` is same-cluster only and that's a non-goal for v1. None of the three tools here do cross-cluster — see the bullet above.

- **You're chasing absolute throughput at extreme fan-out.** `projection` caps `ClusterProjection` selector fan-out at 16 concurrent destination writes per CR by default. The cap is tunable via `--selector-write-concurrency` (Helm value `selectorWriteConcurrency`) — see [observability.md](observability.md#-selector-write-concurrency-selectorwriteconcurrency-default-16) — but the ceiling is ultimately bounded by your kube-apiserver's APF budget rather than this knob, so at thousands of matching namespaces you'll want to validate the cluster can absorb the parallel write load before raising the value.
