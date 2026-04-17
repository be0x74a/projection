# Limitations & roadmap

`projection` is `v0.1.0-alpha`. Be explicit about what it does and doesn't do today.

## Known limitations

### Single destination per `Projection`

One `Projection` CR produces exactly one destination object. If you need to mirror one source into five namespaces, you write five `Projection` CRs. The [multi-destination example](https://github.com/be0x74a/projection/blob/main/examples/multiple-destinations-from-one-source.yaml) shows the pattern; tools like Kustomize / Helm / Argo ApplicationSets handle the generation well.

Trade-off: this keeps per-destination status independent — a `DestinationConflict` in one namespace doesn't mark the others as failed. Multi-destination via label selector is on the roadmap (below).

### Same-cluster only

Source and destination must live in the same cluster. Cross-cluster mirroring is a non-goal for v0 — the failure modes (partial connectivity, credential distribution, resource collisions between clusters) are significant enough to deserve a separate design.

If you need cross-cluster, look at [Admiralty](https://admiralty.io/), [Open Cluster Management](https://open-cluster-management.io/), or Argo CD's multi-cluster patterns.

### Some Kinds need extra stripping rules

A few Kinds carry **apiserver-allocated spec fields** that must be stripped before mirroring (otherwise the create at the destination is rejected as trying to supply an immutable field). This list grows case-by-case as gaps are reported — see the [umbrella issue](https://github.com/be0x74a/projection/issues/32) for the triage queue. Supported today in `droppedSpecFieldsByGVK`:

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

`projection` mirrors only **namespaced** resources (`ConfigMap`, `Secret`, `Service`, `Deployment`, most CRs, etc.). A `Projection` that points at a cluster-scoped Kind (`Namespace`, `ClusterRole`, `ClusterRoleBinding`, `StorageClass`, `CustomResourceDefinition`, `PriorityClass`, …) fails fast with `SourceResolved=False` and the message `<apiVersion>/<Kind> is cluster-scoped; projection only mirrors namespaced resources`. There's no use case that motivated cluster-scoped support so far (there can only be one `Namespace` with a given name in a cluster, so mirroring it doesn't mean anything), and the reconcile/watch plumbing assumes a namespace for the source object.

### Alpha API

The CRD stability is alpha. `projection.be0x74a.io/v1` is the storage version, and we'll serve later versions alongside with conversion when/if they land — but breaking changes to spec shape are allowed pre-1.0. We'll announce changes in the [changelog](https://github.com/be0x74a/projection/blob/main/CHANGELOG.md) and in release notes with migration guidance.

## Roadmap

In rough priority order:

### 1. Multi-destination via label selector

Let one `Projection` target many namespaces:

```yaml
spec:
  destinations:
    namespaceSelector:
      matchLabels:
        tenant-tier: shared
```

Design considerations: per-destination status (one condition array per namespace? a subresource per destination?), conflict aggregation, propagation latency with N destinations.

### 2. Cross-cluster mirroring (opt-in)

Federation-style. Source in cluster A, destination in cluster B. Credential distribution via a secret-backed `ClusterRef`. This is a large piece of work and will be gated behind an explicit flag and a separate CRD.

### 3. OLM bundle for OperatorHub

Package `projection` as an OLM bundle and publish to [OperatorHub.io](https://operatorhub.io/). This is mostly packaging and metadata, not code.

### 4. Kyverno-style transforms in `overlay`

Today `overlay` only merges labels and annotations. Useful additions:

- Patches against `spec`/`data` (JSON patch or strategic merge).
- Template substitution (`{{ .Source.metadata.name }}`).
- Label/annotation *removal* (not just set/override).

Scope to be defined — the north star is "make simple transforms trivial without turning `projection` into a policy engine."

## Getting involved

Found a Kind we should support out of the box, a use case the API doesn't cover, a bug, or a documentation gap? [Open an issue](https://github.com/be0x74a/projection/issues/new). Contributions welcome — see `CONTRIBUTING.md` in the repo.
