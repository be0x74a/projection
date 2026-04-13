# vs alternatives

There are two mature tools that overlap with `projection`: [emberstack/Reflector](https://github.com/emberstack/kubernetes-reflector) and [Kyverno `generate`](https://kyverno.io/docs/writing-policies/generate/). Both are excellent and deployed widely. This page is about when each is the right choice.

## At a glance

|                                          | projection                              | emberstack/Reflector                        | Kyverno `generate`                                   |
| ---------------------------------------- | --------------------------------------- | ------------------------------------------- | ---------------------------------------------------- |
| **Scope of supported Kinds**             | Any (RESTMapper-driven)                 | `ConfigMap`, `Secret` only                  | Any                                                  |
| **Source-of-truth shape**                | A `Projection` CR per mirror            | Annotations on the source object            | A cluster-wide `ClusterPolicy`/`Policy`              |
| **Per-mirror status / conditions**       | Yes (`Ready`, `SourceResolved`, `DestinationWritten`) | Partial (reflected on source annotations) | No (policy-level, not mirror-level)         |
| **Kubernetes Events per outcome**        | `Projected`, `Updated`, `DestinationConflict`, `SourceFetchFailed`, ... | Limited       | Policy-engine events                                 |
| **Conflict semantics**                   | Refuses to overwrite unowned objects; reports `DestinationConflict` | Overwrites | Configurable via `synchronize`, generally overwrites |
| **Watch-driven propagation**             | Yes, dynamic per-GVK metadata-only watch | Yes                                         | Yes                                                  |
| **Admission-time source validation**     | Yes (pattern-validated source fields)   | n/a                                         | Yes                                                  |
| **Prometheus metrics**                   | `projection_reconcile_total{result}`    | Partial                                     | Rich policy-engine metrics                           |
| **Operational footprint**                | One CRD + Deployment                    | One CRD + Deployment                        | Full Kyverno control plane (several controllers)     |
| **Cluster-wide RBAC surface**            | `*/*` (because any Kind)                | Namespace-restrictable (scope narrower)     | `*/*` (policy engine)                                |

## Source-of-truth model

The biggest difference is **where the rule lives**.

- **Reflector** puts the rule on the *source object*: you annotate a Secret with `reflector.v1.k8s.emberstack.com/reflection-allowed: "true"` and `reflector.v1.k8s.emberstack.com/reflection-auto-namespaces: "tenant-.*"`. The "who's mirroring this?" question is answered by listing annotations on the source.
- **Kyverno** puts the rule in a *cluster-wide policy*: one `ClusterPolicy` can generate mirrored objects based on selectors, triggers, and JMESPath expressions. Reading "how did this object get here?" means finding the right policy.
- **projection** puts the rule in a *per-mirror CR*: one `Projection` = one source → one destination. Reading "how did this object get here?" means `kubectl describe projection` on any suspect namespace.

Consequence: `projection` is the easiest to diff in GitOps (each mirror is its own YAML file) and the easiest to reason about per-resource (`kubectl get projections -A` is the full inventory). Reflector is the easiest when you already manage the *source* in GitOps and don't want to create another object per destination. Kyverno is the most powerful when the mirror rule needs to match on dozens of sources at once.

## Supported Kinds

Reflector supports `ConfigMap` and `Secret`. That's deliberate — the code is tuned for their semantics. If all you ever mirror is Secrets (the common case!), you don't need anything more.

`projection` and Kyverno both work on any Kind. `projection` does this via the `RESTMapper` plus Kind-specific stripping rules (currently `Service`, `PVC`, `Pod`; see [Limitations](limitations.md#known-limitations) for Kinds that may need future additions). Kyverno does it via a generic generate rule with optional variable substitution.

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

If you've ever had a tool overwrite an operator-owned object and break something, you know why `projection` takes the opposite default.

## Watch model and propagation latency

All three are watch-driven; steady-state propagation is sub-second in all three. `projection` registers metadata-only watches per source GVK lazily (no cost until you create a `Projection` for a Kind) and uses a field indexer to map source events to Projections in O(1). In practice ~100 ms source-edit-to-destination-update on a typical cluster.

## Operational surface

- **`projection`**: one CRD, one Deployment, one container. Distroless, multi-arch. ClusterRole is `*/*` (see [Security](security.md) for narrowing recommendations).
- **Reflector**: one CRD, one Deployment. RBAC naturally narrower (reads/writes ConfigMaps/Secrets only).
- **Kyverno**: multi-controller control plane, admission webhooks, report controllers. Significantly more surface, but you probably already run it.

## When to pick which

- **You already run Reflector and only mirror Secrets.** Keep Reflector. The added per-mirror CR isn't worth the churn.
- **You already run Kyverno and want to mirror *based on source labels* (one policy generating many destinations).** Stick with Kyverno — `projection` doesn't do source selectors yet.
- **You want the mirror rule to be a first-class, diffable, per-destination object you can `kubectl get` and wait on — and you need Kinds beyond ConfigMap/Secret.** That's `projection`.
- **You want conflict-safe-by-default** (refuses to overwrite unowned objects). That's `projection`; the others generally don't do this.
- **You want per-mirror status conditions and a Prometheus counter you can alert on.** That's `projection`.
- **Cross-cluster mirroring.** None of these three today. Consider [Admiralty](https://admiralty.io/), [KubeFed](https://github.com/kubernetes-retired/kubefed) (retired but concepts still inform alternatives), or [Cluster API](https://cluster-api.sigs.k8s.io/) + GitOps.

The overlap isn't zero-sum — several clusters end up running `projection` alongside Reflector during a migration, because a per-mirror CR is a cleaner artifact to import into GitOps than a Reflector-annotated source.
