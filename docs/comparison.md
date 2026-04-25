# vs alternatives

There are two mature tools that overlap with `projection`: [emberstack/Reflector](https://github.com/emberstack/kubernetes-reflector) and [Kyverno `generate`](https://kyverno.io/docs/writing-policies/generate/). Both are excellent and deployed widely. This page is about when each is the right choice.

## At a glance

|                                          | projection                              | emberstack/Reflector                        | Kyverno `generate`                                   |
| ---------------------------------------- | --------------------------------------- | ------------------------------------------- | ---------------------------------------------------- |
| **Scope of supported Kinds**             | Any (RESTMapper-driven)                 | `ConfigMap`, `Secret` only                  | Any                                                  |
| **Source-of-truth shape**                | A `Projection` CR per mirror            | Annotations on the source object            | A cluster-wide `ClusterPolicy`/`Policy`              |
| **Multi-namespace fan-out**              | Yes, via `destination.namespaceSelector` (label-based) | Yes, via `reflection-auto-namespaces` (regex-based) | Yes, via policy match selectors                      |
| **Per-mirror status / conditions**       | Yes (`Ready`, `SourceResolved`, `DestinationWritten` — rollup for selector-based) | Partial (reflected on source annotations) | No (policy-level, not mirror-level)         |
| **Kubernetes Events per outcome**        | `Projected`, `Updated`, `DestinationConflict`, `SourceFetchFailed`, ... (per-namespace for fan-out) | Limited | Policy-engine events                                 |
| **Conflict semantics**                   | Refuses to overwrite unowned objects; reports `DestinationConflict` | Overwrites | Configurable via `synchronize`, generally overwrites |
| **Watch-driven propagation**             | Yes, dynamic per-GVK metadata-only watch | Yes                                         | Yes                                                  |
| **Admission-time source validation**     | Yes (pattern-validated source fields)   | n/a                                         | Yes                                                  |
| **Prometheus metrics**                   | `projection_reconcile_total{result}`    | Partial                                     | Rich policy-engine metrics                           |
| **Operational footprint**                | One CRD + Deployment                    | One CRD + Deployment                        | Full Kyverno control plane (several controllers)     |
| **Cluster-wide RBAC surface**            | `*/*` by default; narrowable per-Kind via Helm `supportedKinds` | Namespace-restrictable (scope narrower)     | `*/*` (policy engine)                                |

## Source-of-truth model

The biggest difference is **where the rule lives**.

- **Reflector** puts the rule on the *source object*: you annotate a Secret with `reflector.v1.k8s.emberstack.com/reflection-allowed: "true"` and `reflector.v1.k8s.emberstack.com/reflection-auto-namespaces: "tenant-.*"`. The "who's mirroring this?" question is answered by listing annotations on the source.
- **Kyverno** puts the rule in a *cluster-wide policy*: one `ClusterPolicy` can generate mirrored objects based on selectors, triggers, and JMESPath expressions. Reading "how did this object get here?" means finding the right policy.
- **projection** puts the rule in a *per-mirror CR*: one `Projection` maps one source to either one destination or — with `destination.namespaceSelector` — to every namespace matching a label. Reading "how did this object get here?" means `kubectl describe projection` on any suspect namespace.

Consequence: `projection` is the easiest to diff in GitOps (each mirror is its own YAML file) and the easiest to reason about per-resource (`kubectl get projections -A` is the full inventory). Reflector is the easiest when you already manage the *source* in GitOps and don't want to create another object per destination. Kyverno is the most powerful when the mirror rule needs to match on dozens of sources at once.

## Supported Kinds

Reflector supports `ConfigMap` and `Secret`. That's deliberate — the code is tuned for their semantics. If all you ever mirror is Secrets (the common case!), you don't need anything more, and Reflector's annotation-on-source UX is genuinely simpler than maintaining a separate `Projection` CR per mirror.

`projection` and Kyverno both work on any Kind. `projection` does this via the `RESTMapper` plus Kind-specific stripping rules for fields the apiserver allocates (currently `Service`, `PersistentVolumeClaim`, `Pod`, and `Job`; see [Limitations](limitations.md#some-kinds-need-extra-stripping-rules) for the full list and the bar for adding new entries). Kyverno does it via a generic generate rule with optional variable substitution — strictly more general, at the cost of having to think in policy terms.

## Status and observability

Per-mirror status is where the split is clearest.

- **`projection`** exposes three conditions (`SourceResolved`, `DestinationWritten`, `Ready`) per `Projection`, plus Events per state transition, plus a `projection_reconcile_total{result}` counter labeled by outcome. You can alert on conflicts cheaply. See [Observability](observability.md).
- **Reflector** reports reflection state via annotations on the source (e.g. `reflector.v1.k8s.emberstack.com/reflects`). Useful, but not a first-class condition you can query via `kubectl wait`.
- **Kyverno** has rich policy-engine metrics and reports — but they're keyed on the *policy*, not the individual generated object. To answer "did the `shared-tls` Secret land in `tenant-a`?" you still `kubectl get` the destination.

If you want to put `kubectl wait --for=condition=Ready` in a CI pipeline or chart hook, `projection` is designed for that.

## Conflict semantics

- **`projection`**: refuses to overwrite destinations it doesn't own. Reports `DestinationConflict` on status and as an event. This is the default and deliberate.
- **Reflector**: generally overwrites (with some safeguards).
- **Kyverno `generate`**: with `synchronize: true`, Kyverno keeps the destination in sync; it will overwrite drift.

`projection`'s opposite-default is a feature when you adopt it alongside other tooling, but it's not strictly better — overwrite-by-default is correct for the common Reflector use case (the source-side annotation is itself a deliberate "mirror this" signal, and a stale unowned destination is usually a bug to clobber, not preserve). Pick the conflict semantics that match your trust model.

## Watch model and propagation latency

All three are watch-driven; steady-state propagation is sub-second in all three. `projection` registers metadata-only watches per source GVK lazily (no cost until you create a `Projection` for a Kind) and uses a field indexer to map source events to Projections in O(1). Measured end-to-end source-edit-to-destination-update on Kind sits at ~16 ms p50 / ~25 ms p99 for single-destination Projections and ~36–76 ms (first-to-last destination) p50 for selector fan-out at 100 namespaces — see [Scale](scale.md) for the full numbers, methodology, and caveats.

## Operational surface

- **`projection`**: one CRD, one Deployment, one container. Distroless, multi-arch. ClusterRole is `*/*` (see [Security](security.md) for narrowing recommendations).
- **Reflector**: one CRD, one Deployment. RBAC naturally narrower (reads/writes ConfigMaps/Secrets only).
- **Kyverno**: multi-controller control plane, admission webhooks, report controllers. Significantly more surface, but you probably already run it.

## When to pick which

- **You already run Reflector and only mirror Secrets.** Keep Reflector. The added per-mirror CR isn't worth the churn — though note that `projection`'s `destination.namespaceSelector` gives you Reflector-style fan-out with per-namespace status.
- **You already run Kyverno and want to mirror *based on source labels* (one policy generating many destinations from multiple sources).** Stick with Kyverno — `projection` doesn't do source selectors yet, only destination namespace selectors.
- **You want the mirror rule to be a first-class, diffable, per-destination object you can `kubectl get` and wait on — and you need Kinds beyond ConfigMap/Secret.** That's `projection`.
- **You want conflict-safe-by-default** (refuses to overwrite unowned objects). That's `projection`; the others generally don't do this.
- **You want per-mirror status conditions and a Prometheus counter you can alert on.** That's `projection`.
- **Cross-cluster mirroring.** None of these three today. Consider [Admiralty](https://admiralty.io/), [KubeFed](https://github.com/kubernetes-retired/kubefed) (retired but concepts still inform alternatives), or [Cluster API](https://cluster-api.sigs.k8s.io/) + GitOps.

## Where `projection` is the wrong choice

The flip side of the section above. There are real cases where the right answer isn't `projection`, and pretending otherwise wastes your time:

- **You only need ConfigMap/Secret mirroring and don't already run Reflector.** Reflector is the simpler shape: annotation-on-source instead of a separate CR, narrower RBAC scope (it only needs ConfigMap/Secret access), and a much smaller behavioral surface. The "any Kind" generality `projection` brings is a cost — a broader RBAC default (`*/*` until you narrow it via `supportedKinds`), a larger CRD schema, and the conceptual overhead of "this is a generic mirror operator" — that you don't need to pay.

- **You need conditional mirroring or per-source policy logic.** "Mirror this Secret only into namespaces labeled `tier=prod` *and* whose annotations include `mirror=enabled`" — that's a Kyverno `generate` policy, not a `projection`. Kyverno's match/exclude/preconditions language exists precisely for this; `projection`'s `namespaceSelector` is a single label-selector and nothing else. Trying to encode multi-condition logic by maintaining a fleet of Projections is the wrong shape.

- **You need to mirror Kinds beyond mirroring** — generate a `RoleBinding` from a `ServiceAccount` annotation, derive a `NetworkPolicy` from a `Namespace` label, materialize a `Job` per `ConfigMap` create. `projection` only mirrors Kind-to-same-Kind; it doesn't transform or derive. Kyverno's `generate` plus a context block does this in one rule.

- **Multi-condition selector logic at the destination.** Selector fan-out gives you one rolled-up `DestinationWritten` condition for the whole fan-out, not per-destination conditions you can `kubectl wait` on independently. Kyverno reports per-policy and per-rule status, which is closer to per-destination than `projection` is today.

- **You're on a cluster older than Kubernetes 1.32.** The destination CRD uses a CEL `XValidation` rule that requires apiserver ≥ 1.32 to evaluate `optional` fields correctly. The reconciler enforces the same invariant as defense-in-depth, so older clusters work — but the better admission UX (early `kubectl apply` rejection instead of a runtime status flip) needs the version floor. Reflector and Kyverno don't have this constraint.

- **You're allergic to a per-resource CR per mirror.** `projection`'s shape is "one `Projection` per mirror" (or one per fan-out, with `namespaceSelector`). If you're mirroring 200 Secrets across 50 namespaces and you don't want to template 200 `Projection` YAMLs (or 1 fan-out + 199 single-destination), Reflector's annotate-the-source UX is genuinely less ceremony.

- **You need cross-cluster.** `projection` is same-cluster only and that's a non-goal for v1. None of the three tools here do cross-cluster — see the bullet above.

- **You're chasing absolute throughput at extreme fan-out.** `projection` caps selector fan-out at 16 concurrent destination writes (compile-time, no flag yet). At thousands of matching namespaces this becomes the bottleneck. Tunable in a future release; today, sized for clusters in the low hundreds of namespaces.
