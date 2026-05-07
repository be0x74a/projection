# Upgrading from v0.2 to v0.3

This is a runbook, not a tutorial. Each section is one operation you have to perform; do them in order.

## Summary of breaking changes

- **CRD split.** The single v0.2 `Projection` CRD became two: namespaced `Projection` (single-target, in its own namespace) and cluster-scoped `ClusterProjection` (fan-out across many namespaces). Selector and cross-namespace single-target use cases move from `Projection` to `ClusterProjection`.
- **`SourceRef` refactor.** `spec.source.apiVersion: <g>/<v>` is replaced with separate `spec.source.group: <g>` and `spec.source.version: <v>` fields. CEL admission requires `version` when `group` is empty. Non-core groups can omit `version` for preferred-version lookup via the RESTMapper.
- **Ownership-annotation rename.** Destinations are now stamped with `projection.sh/owned-by-projection: <ns>/<name>` (namespaced CRD) or `projection.sh/owned-by-cluster-projection: <name>` (cluster CRD). The v0.2 key `projection.sh/owned-by` is no longer recognized.
- **New UID labels.** Destinations also carry `projection.sh/owned-by-projection-uid: <uid>` (or `…-cluster-projection-uid` for cluster). These are watch hints; the annotation remains the authoritative ownership signal.
- **New cluster finalizer.** `ClusterProjection` carries `projection.sh/cluster-finalizer`; the v0.2 `projection.sh/finalizer` finalizer continues to apply only to namespaced `Projection`.
- **Metric label addition.** `projection_reconcile_total` gained a `kind` label (`Projection` | `ClusterProjection`). Pre-v1.0 metric labels are not API-tier; existing PromQL keeps working but loses granularity unless you split by `kind`.
- **No automated migration.** The v0.3 controller does not recognize v0.2 finalizers, ownership annotations, or the v0.2 SourceRef shape. You must clean up v0.2 Projections via the v0.2 controller before upgrading, then recreate them in the v0.3 shape after.

## Prerequisites

Before upgrading the operator, **delete every v0.2 `Projection`** so the v0.2 controller can run its finalizer and clean up destinations.

```bash
# 1. Delete every v0.2 Projection. This blocks until the v0.2 finalizer
#    has run on each (which may take a moment per CR).
kubectl delete projections -A --all

# 2. Confirm they're really gone — the finalizer needs the controller, so
#    if anything is stuck "Terminating", you have a stuck-CRD problem to
#    resolve before continuing.
kubectl get projections -A
# No resources found.
```

If you skip this step and upgrade the operator anyway, the v0.3 controller will not recognize the v0.2 `projection.sh/finalizer` on those CRs (the v0.2 codepath that processed it is gone), the namespaced reconciler does still own the same finalizer name so eventually it'll clean up, but any v0.2 destinations whose `projection.sh/owned-by` annotation was *not* migrated will be stranded — the v0.3 controller does not recognize that annotation key and will leave the destinations alone (and a re-applied v0.3 CR will hit `DestinationConflict` against them). Cleaning up the v0.2 CRs first via the v0.2 controller is safer.

If you've already upgraded out of order and have stuck CRs, see [Pre-existing destinations](#pre-existing-destinations) below for the manual recovery.

## Migration operations

You'll perform between one and three operations per v0.2 Projection, depending on which v0.2 shape it used. The table below maps the four pre-existing shapes onto v0.3 outcomes:

| v0.2 shape                                                   | v0.3 outcome                                                                |
| ------------------------------------------------------------ | --------------------------------------------------------------------------- |
| `destination.namespace` matches `metadata.namespace`         | Drop `destination.namespace`. Stay a namespaced `Projection`.               |
| `destination.namespace` differs from `metadata.namespace`    | Convert to `ClusterProjection` with `namespaces: [<that-namespace>]`.       |
| `destination.namespaceSelector` set                          | Convert to `ClusterProjection` with the same selector.                      |
| Any of the above                                             | Additionally rewrite `source.apiVersion` to `source.group` + `source.version`. |

In all cases, the SourceRef rewrite (Op 1) applies. Op 2 / Op 3 / Op 4 cover the destination-shape changes and are mutually exclusive — pick the one that matches your v0.2 CR.

### Op 1: rewrite the `SourceRef` on every CR

Every v0.2 `Projection` (and its v0.3 successor) needs `source.apiVersion` rewritten to `source.group` plus `source.version`. The rules:

| v0.2 form                            | v0.3 form                                       | Notes                                                                                          |
| ------------------------------------ | ----------------------------------------------- | ---------------------------------------------------------------------------------------------- |
| `apiVersion: v1`                     | `group: ""`, `version: v1`                      | Core API. CEL admission requires `version` when `group` is empty.                              |
| `apiVersion: apps/v1`                | `group: apps`, `version: v1`                    | Pinned to a specific served version.                                                           |
| `apiVersion: apps/*`                 | `group: apps` (omit `version`)                  | Unpinned form — RESTMapper-preferred served version. Picks up CRD promotions.                  |
| `apiVersion: networking.k8s.io/v1`   | `group: networking.k8s.io`, `version: v1`       | Pinned named-group source.                                                                     |
| `apiVersion: example.com/v1beta1`    | `group: example.com`, `version: v1beta1`        | Pinned CRD source.                                                                             |
| `apiVersion: example.com/*`          | `group: example.com` (omit `version`)           | Unpinned CRD source. Recommended default for CRDs whose authors actively promote versions.     |

The v0.2 unpinned form `<group>/*` is what becomes the v0.3 "omit `version`" form. The CEL admission rule `size(self.group) != 0 || size(self.version) != 0` rejects the empty-group + empty-version combination — you cannot omit `version` for the core API.

**Before** (v0.2):

```yaml
apiVersion: projection.sh/v1
kind: Projection
metadata:
  name: app-config-mirror
  namespace: tenant-a
spec:
  source:
    apiVersion: v1
    kind: ConfigMap
    namespace: platform
    name: app-config
  destination:
    namespace: tenant-a              # this matches metadata.namespace — Op 4
```

**After** (v0.3):

```yaml
apiVersion: projection.sh/v1
kind: Projection
metadata:
  name: app-config-mirror
  namespace: tenant-a
spec:
  source:
    group: ""                        # was: apiVersion: v1
    version: v1                      # split out
    kind: ConfigMap
    namespace: platform
    name: app-config
  # destination dropped — Op 4 (next section)
```

For an `apps/v1` source the rewrite is the same shape:

```yaml
# v0.2
spec:
  source:
    apiVersion: apps/v1
    kind: Deployment
    ...

# v0.3
spec:
  source:
    group: apps
    version: v1
    kind: Deployment
    ...
```

For a v0.2 unpinned `apps/*` source, the v0.3 form drops `version` entirely:

```yaml
# v0.2
spec:
  source:
    apiVersion: apps/*               # unpinned
    kind: Deployment
    ...

# v0.3
spec:
  source:
    group: apps                      # version omitted → preferred-version lookup
    kind: Deployment
    ...
```

### Op 2: selector-based v0.2 Projections become `ClusterProjection`s

Any v0.2 `Projection` that used `spec.destination.namespaceSelector` becomes a cluster-scoped `ClusterProjection` in v0.3. Strip `metadata.namespace` (cluster-scoped resources have no namespace), change `kind: Projection` to `kind: ClusterProjection`, and the body is otherwise identical — the selector copies over verbatim.

**Before** (v0.2):

```yaml
apiVersion: projection.sh/v1
kind: Projection
metadata:
  name: shared-config-fanout
  namespace: platform                # ← strip this on v0.3
spec:
  source:
    apiVersion: v1
    kind: ConfigMap
    namespace: platform
    name: app-config
  destination:
    namespaceSelector:
      matchLabels:
        projection.sh/mirror: "true"
```

**After** (v0.3):

```yaml
apiVersion: projection.sh/v1
kind: ClusterProjection              # was: Projection
metadata:
  name: shared-config-fanout
  # namespace removed — cluster-scoped
spec:
  source:
    group: ""                        # Op 1 rewrite
    version: v1
    kind: ConfigMap
    namespace: platform
    name: app-config
  destination:
    namespaceSelector:               # selector copies over verbatim
      matchLabels:
        projection.sh/mirror: "true"
```

**RBAC reminder:** creating a `ClusterProjection` requires the `<release>-projection-cluster-admin` ClusterRole, which is **not** aggregated. A cluster admin must explicitly bind it. See [Security § Why projection-cluster-admin is not aggregated](security.md#why-projection-cluster-admin-is-not-aggregated).

### Op 3: cross-namespace single-target v0.2 Projections become `ClusterProjection`s

A v0.2 `Projection` whose `spec.destination.namespace` differs from its own `metadata.namespace` was a cross-namespace single-target mirror. In v0.3, namespaced `Projection` cannot write outside its own namespace — there is no `destination.namespace` field on the namespaced CRD. Convert these to a `ClusterProjection` with `namespaces: [<that-namespace>]`.

**Before** (v0.2):

```yaml
apiVersion: projection.sh/v1
kind: Projection
metadata:
  name: tls-into-app-prod
  namespace: cert-manager            # ← Projection lives here
spec:
  source:
    apiVersion: v1
    kind: Secret
    namespace: cert-manager
    name: shared-tls
  destination:
    namespace: app-prod              # ← but writes here (cross-namespace)
    name: tls
```

**After** (v0.3):

```yaml
apiVersion: projection.sh/v1
kind: ClusterProjection              # was: Projection
metadata:
  name: tls-into-app-prod
  # namespace removed — cluster-scoped
spec:
  source:
    group: ""                        # Op 1 rewrite
    version: v1
    kind: Secret
    namespace: cert-manager
    name: shared-tls
  destination:
    namespaces: [app-prod]           # was: destination.namespace
    name: tls                        # rename preserved
```

A single-element `namespaces:` list is the v0.3 way to express "fan out to exactly one namespace that isn't the source's." `minItems=1` rejects an empty list, so this is a valid `ClusterProjection`.

If you'd rather keep this as a namespaced `Projection`, you can — but the destination namespace must equal `metadata.namespace`. Move the Projection itself to the destination namespace:

```yaml
# Alternative v0.3 shape — namespaced Projection in the destination namespace.
apiVersion: projection.sh/v1
kind: Projection
metadata:
  name: tls-into-app-prod
  namespace: app-prod                # ← move the Projection to app-prod
spec:
  source:
    group: ""
    version: v1
    kind: Secret
    namespace: cert-manager
    name: shared-tls
  destination:
    name: tls                        # rename preserved
```

This shape is the right call if `app-prod`'s tenant should own the mirror going forward (they can `kubectl edit` it, status conditions surface in `app-prod`, finalizer cleans up locally). It's not the right call if the source-namespace team wanted control of the mirror — keep the `ClusterProjection` shape in that case.

### Op 4 (implicit): same-namespace v0.2 Projections drop `destination.namespace`

A v0.2 `Projection` whose `spec.destination.namespace` equals its own `metadata.namespace` is the simplest case. In v0.3 the namespaced CRD has no `destination.namespace` field at all — the destination namespace is structurally always `metadata.namespace`. Just drop the field.

**Before** (v0.2):

```yaml
apiVersion: projection.sh/v1
kind: Projection
metadata:
  name: app-config-mirror
  namespace: tenant-a
spec:
  source:
    apiVersion: v1
    kind: ConfigMap
    namespace: platform
    name: app-config
  destination:
    namespace: tenant-a              # ← drop this; matches metadata.namespace
    name: shared-app-config          # rename preserved if any
```

**After** (v0.3):

```yaml
apiVersion: projection.sh/v1
kind: Projection
metadata:
  name: app-config-mirror
  namespace: tenant-a
spec:
  source:
    group: ""                        # Op 1 rewrite
    version: v1
    kind: ConfigMap
    namespace: platform
    name: app-config
  destination:
    name: shared-app-config          # rename preserved if any
  # No destination.namespace — destination namespace is metadata.namespace.
```

If the v0.2 CR had no rename (i.e. `destination.name` was unset), you can drop the entire `destination:` block. The v0.3 namespaced CRD's `destination` field is optional; omit it and the destination defaults to `source.name` in `metadata.namespace`.

## Verification after upgrade

After the operator is upgraded and the rewritten CRs are applied, check both kinds:

```bash
# Namespaced Projections
kubectl get projections -A
# All should show READY=True after a beat. Anything else, drill in:
kubectl describe projection <name> -n <ns>

# Cluster-scoped ClusterProjections
kubectl get clusterprojections
# Same — READY=True is the goal. Targets/Failed columns surface fan-out status:
kubectl get clusterprojections -o wide

# Spot-check that destinations were actually written
kubectl get configmap -A -l projection.sh/owned-by-projection-uid
kubectl get configmap -A -l projection.sh/owned-by-cluster-projection-uid
```

If you see any `READY=False`:

```bash
# Get the per-condition detail
kubectl get projection <name> -n <ns> \
  -o jsonpath='{range .status.conditions[*]}{.type}={.status} reason={.reason} msg={.message}{"\n"}{end}'

# Or for a ClusterProjection
kubectl get clusterprojection <name> \
  -o jsonpath='{range .status.conditions[*]}{.type}={.status} reason={.reason} msg={.message}{"\n"}{end}'
```

Common failure modes after upgrade are documented in [Troubleshooting](troubleshooting.md). The most likely ones in a freshly-upgraded cluster:

- `DestinationConflict` — a v0.2 destination still carries the old `projection.sh/owned-by` annotation, so the v0.3 controller refuses to overwrite it. See [Pre-existing destinations](#pre-existing-destinations) below.
- `SourceNotProjectable` / `SourceOptedOut` — unrelated to the upgrade; the source is missing or vetoes the `projection.sh/projectable` annotation. See [Source opt-in](getting-started.md#source-opt-in).
- `SourceResolutionFailed` — typo in the rewritten `group` or `version`, or you tried to omit `version` for the core group (CEL rejects it).

## Caveats

### GitOps caveat (Argo / Flux)

If your v0.2 Projection YAMLs live in a GitOps repo and Argo CD or Flux replays them on every sync, they will be applied against the v0.3 admission webhook *before* you've had a chance to rewrite them. The v0.3 admission validator rejects the v0.2 SourceRef shape (it will fail CEL on missing `version` or missing `group`/`version`), and any cross-namespace `destination.namespace` on a `Projection` simply doesn't exist as a field — apply will fail with "unknown field" errors.

You have two choices:

1. **Rewrite first, upgrade second.** Edit the YAMLs in your GitOps repo to the v0.3 shape, commit, let the v0.2 controller (still installed at this point) accept the v0.2 schema for the part it understands and ignore the new fields harmlessly… *no, that doesn't work either* — the v0.2 admission validator will reject the v0.3 shape too. So this only works if you can pause the GitOps reconciliation between the v0.3 rewrite commit and the v0.3 operator install.

2. **Pause GitOps, upgrade in a maintenance window.** Suspend Argo / Flux reconciliation. Delete v0.2 Projections via the v0.2 controller (per [Prerequisites](#prerequisites)). Upgrade the operator. Apply the rewritten YAMLs by hand or via a separate one-shot sync. Re-enable GitOps reconciliation against the v0.3-shaped repo state.

The cleanest of the two is option 2, with a maintenance window of however long it takes to rewrite + verify. The operations themselves are mechanical — string replacement on `apiVersion:` and `destination.namespace:` — but make sure the GitOps controller isn't fighting you while you do them.

### Ownership-grep caveat

If you have monitoring, alerting, or backup tooling that grep for the literal annotation key `projection.sh/owned-by` (without the new `-projection` or `-cluster-projection` suffix), update it. The v0.3 keys are:

- `projection.sh/owned-by-projection` — set by namespaced `Projection`. Value: `<projection-ns>/<projection-name>`.
- `projection.sh/owned-by-cluster-projection` — set by `ClusterProjection`. Value: `<cluster-projection-name>` (no `<ns>/` prefix).

The new UID labels (`projection.sh/owned-by-projection-uid`, `projection.sh/owned-by-cluster-projection-uid`) are entirely new and weren't grep-able in v0.2.

**Example — finding all owned destinations:**

```bash
# v0.2
kubectl get configmap -A -o json | \
  jq '.items[] | select(.metadata.annotations["projection.sh/owned-by"]) | {ns: .metadata.namespace, name: .metadata.name}'

# v0.3 — equivalent, both kinds at once via the UID labels (cheap and indexed)
kubectl get configmap -A -l projection.sh/owned-by-projection-uid -o json | \
  jq '.items[] | {ns: .metadata.namespace, name: .metadata.name, owner: .metadata.annotations["projection.sh/owned-by-projection"]}'
kubectl get configmap -A -l projection.sh/owned-by-cluster-projection-uid -o json | \
  jq '.items[] | {ns: .metadata.namespace, name: .metadata.name, owner: .metadata.annotations["projection.sh/owned-by-cluster-projection"]}'
```

The label-driven query is cheaper than annotation-grep: the apiserver indexes labels but not annotations, so `kubectl get -l <label>` runs against an index where `kubectl get | jq '.metadata.annotations["…"]'` walks every object.

### Helm chart upgrade

The v0.3 chart adds three new ClusterRoles for namespace-tenant self-service:

- `<release>-projection-namespaced-edit` — aggregates into `admin` AND `edit`.
- `<release>-projection-namespaced-view` — aggregates into `view`.
- `<release>-projection-cluster-admin` — **not** aggregated. Cluster admins must explicitly bind it via a ClusterRoleBinding to whichever subjects should hold cluster-tier ClusterProjection authority.

If your cluster has unusual RBAC conventions (custom `admin`/`edit`/`view` ClusterRoles, or aggregation labels you've taken over for other purposes), set `rbac.aggregate=false` on the chart values. The two `*-namespaced-*` roles will not be rendered; you bind whatever role grants Projection CRUD by hand.

`<release>-projection-cluster-admin` is **always rendered** regardless of `rbac.aggregate` — the flag only controls aggregation labels on the namespaced roles, not whether the cluster role exists at all.

See [Security § RBAC aggregation defaults](security.md#rbac-aggregation-defaults) for the full breakdown.

### Pre-existing destinations

A v0.2 destination carries the old `projection.sh/owned-by: <ns>/<name>` annotation. The v0.3 controller doesn't recognize this annotation and treats the destination as unowned. When the v0.3 CR (rewritten per the Ops above) tries to write its destination, it sees an unowned object at the target name and reports `Ready=False reason=DestinationConflict`.

You have two options. Pick one:

#### Option A: Delete the v0.2 destinations and let v0.3 recreate them

The simplest option, especially if you can tolerate a brief gap in the destination's existence (e.g. the source data is unchanged, the destination consumer auto-recovers, or there's no live consumer yet).

```bash
# Find all v0.2-owned destinations
kubectl get <kind> -A -o json | \
  jq -r '.items[] | select(.metadata.annotations["projection.sh/owned-by"]) | "\(.metadata.namespace) \(.metadata.name)"' | \
  while read ns name; do kubectl -n "$ns" delete <kind> "$name"; done
```

Replace `<kind>` with each Kind you mirror (`configmap`, `secret`, etc.). The v0.3 controller's reconciler will recreate the destination on the next reconcile (within `requeueInterval` — default 30s).

#### Option B: Migrate the annotation key with a one-shot script

Avoids the destination-deletion gap. Rewrites the v0.2 annotation key to the v0.3 key on every owned destination, *and* stamps the new UID label. (The v0.3 controller updates the destination on the next reconcile and would do the rename there anyway — but if a consumer is actively reading the destination and you don't want to wait the full requeue interval, this is the path.)

You need to know which v0.3 CR owns each destination — the script picks `projection` (namespaced) for v0.2 CRs whose `destination.namespace` matches `metadata.namespace`, and `cluster-projection` for the others. If you've already rewritten the v0.2 CRs into v0.3 CRs (per Op 1–4), you can derive the mapping from the v0.3 CRs themselves and the destinations' kind+namespace+name.

```bash
# Sketch — adapt for your set of mirrored Kinds.
# This walks every ConfigMap with the legacy annotation, looks up the v0.3 CR
# (Projection or ClusterProjection) that should own it, and rewrites the
# annotation key (and adds the UID label).
#
# WARNING: this is a one-shot — run it ONCE per v0.3 upgrade and verify.

kubectl get configmap -A -o json | \
  jq -c '.items[] | select(.metadata.annotations["projection.sh/owned-by"])' | \
  while read -r dst; do
    legacy=$(echo "$dst" | jq -r '.metadata.annotations["projection.sh/owned-by"]')
    dst_ns=$(echo "$dst" | jq -r '.metadata.namespace')
    dst_name=$(echo "$dst" | jq -r '.metadata.name')
    # Decide whether the legacy owner became a Projection or a ClusterProjection.
    # Convention: <ns>/<name> in legacy. If the new Projection in <ns> exists,
    # use it; otherwise look for a ClusterProjection of that <name>.
    proj_ns=$(echo "$legacy" | cut -d/ -f1)
    proj_name=$(echo "$legacy" | cut -d/ -f2)
    if kubectl -n "$proj_ns" get projection "$proj_name" >/dev/null 2>&1; then
      uid=$(kubectl -n "$proj_ns" get projection "$proj_name" -o jsonpath='{.metadata.uid}')
      kubectl -n "$dst_ns" annotate configmap "$dst_name" \
        "projection.sh/owned-by-projection=$proj_ns/$proj_name" \
        "projection.sh/owned-by-" \
        --overwrite
      kubectl -n "$dst_ns" label configmap "$dst_name" \
        "projection.sh/owned-by-projection-uid=$uid" \
        --overwrite
    elif kubectl get clusterprojection "$proj_name" >/dev/null 2>&1; then
      uid=$(kubectl get clusterprojection "$proj_name" -o jsonpath='{.metadata.uid}')
      kubectl -n "$dst_ns" annotate configmap "$dst_name" \
        "projection.sh/owned-by-cluster-projection=$proj_name" \
        "projection.sh/owned-by-" \
        --overwrite
      kubectl -n "$dst_ns" label configmap "$dst_name" \
        "projection.sh/owned-by-cluster-projection-uid=$uid" \
        --overwrite
    else
      echo "skip: $dst_ns/$dst_name has no v0.3 owner candidate"
    fi
  done
```

The trailing `projection.sh/owned-by-` (note the dash) in `kubectl annotate` removes the legacy key in the same patch.

Run on a single test destination first; verify with `kubectl describe` that the new annotation and label are present and the legacy key is gone; then run the full sweep.

After either Option A or Option B, the v0.3 controller's next reconcile sees a destination it owns (per the new annotation), runs `needsUpdate` (which will be a no-op if Option B was used or if Option A's recreate matched the source bit-for-bit), and you're done.

## Rolling back

Rollback to v0.2 is supported **only if no `ClusterProjection` has been created on the v0.3 cluster.** A `ClusterProjection` carries the new `projection.sh/cluster-finalizer` finalizer, which the v0.2 controller does not recognize and therefore cannot remove. Downgrade with active `ClusterProjection`s and you'll have stuck `Terminating` cluster-scoped CRs that the v0.2 controller can't clean up.

If you need to roll back:

1. **Delete every `ClusterProjection`** via the v0.3 controller. Confirm `kubectl get clusterprojections` is empty.
2. **Delete every namespaced `Projection`** that's been rewritten to the v0.3 SourceRef shape. The v0.2 controller will not recognize the new `source.group`/`source.version` fields. Confirm `kubectl get projections -A` is empty.
3. **Downgrade the operator** to v0.2 (`helm upgrade --version 0.2.x …` or re-apply the v0.2 install YAML).
4. **Recreate Projections** in the v0.2 SourceRef shape. (This is essentially Op 1 in reverse.)
5. **Reapply your v0.2 YAMLs** from your GitOps repo, or from whatever pre-upgrade source-of-truth you preserved.

If you preserved your pre-upgrade YAMLs, step 5 is just `kubectl apply -f`. If you didn't, you'll have to recreate them by hand from the v0.3 CRs (which is mechanical: undo Op 1's `group`+`version` rewrite back to `apiVersion`, and either set `destination.namespace` for the cross-namespace single-target case or restore the selector for the fan-out case).
