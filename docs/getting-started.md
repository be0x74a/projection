# Getting started

This walks through installing the `projection` operator and creating your first `Projection` and `ClusterProjection`.

## Prerequisites

- A Kubernetes cluster (1.32+ required — the CRDs use CEL admission validation, which needs this minimum version).
- `kubectl` configured to talk to it.
- Cluster-admin (for the initial install — the chart creates two CRDs and ClusterRoles).

## Install

### Option 1 — Helm (OCI)

```bash
helm install projection oci://ghcr.io/projection-operator/charts/projection \
  --version 0.3.0 \
  --namespace projection-system --create-namespace
```

### Option 2 — `kubectl apply`

```bash
kubectl apply -f https://github.com/projection-operator/projection/releases/download/v0.3.0/install.yaml
```

Either way, verify the operator is healthy:

```bash
kubectl -n projection-system get deploy
kubectl -n projection-system get pods
```

You should see one `Running` controller pod. If it's `CrashLoopBackOff`, jump to [Troubleshooting](#troubleshooting).

## Source opt-in

`projection` ships with `--source-mode=allowlist` as the default. That means a
source object must carry the annotation `projection.sh/projectable:
"true"` to be mirrored. Without it, the Projection's status reports
`SourceResolved=False reason=SourceNotProjectable` and no destination is
written.

Annotate the source you want to mirror:

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: app-config
  namespace: default
  annotations:
    projection.sh/projectable: "true"
```

The value `"false"` is always honored as a source-owner veto — in any mode,
including `permissive`. If you can't annotate your sources (for example, when
mirroring third-party CRs), flip the operator to
`--source-mode=permissive` (Helm value `sourceMode: permissive`) and any
source is projectable unless explicitly vetoed.

## Your first Projection (single-namespace mirror)

The most common shape is a namespaced `Projection`: one source mirrored into one destination namespace. The destination namespace is **always** the Projection's own `metadata.namespace` — there is no `spec.destination.namespace` field. Write the Projection in the namespace that should receive the copy.

This walkthrough mirrors a `ConfigMap/default/app-config` into the `tenant-a` namespace.

### 1. Create the destination namespace

```bash
kubectl create namespace tenant-a
```

### 2. Create the source ConfigMap (with the projectable opt-in)

```yaml
# source-configmap.yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: app-config
  namespace: default
  annotations:
    projection.sh/projectable: "true"
data:
  log_level: info
  feature_x_enabled: "true"
```

```bash
kubectl apply -f source-configmap.yaml
```

### 3. Create the Projection in `tenant-a`

```yaml
# tenant-a-projection.yaml
apiVersion: projection.sh/v1
kind: Projection
metadata:
  name: app-config-mirror
  namespace: tenant-a            # destination namespace = this
spec:
  source:
    kind: ConfigMap
    namespace: default
    name: app-config
  # destination:
  #   name: shared-app-config    # optional rename; defaults to source.name
```

> **Why no `apiVersion`, `group`, or `version`?** `spec.source.kind` alone
> is enough for core-group resources (`ConfigMap`, `Secret`, `Service`,
> ...). Omitting `version` asks the operator to resolve the preferred
> served version via the RESTMapper on every reconcile — for core that's
> always `v1`. For CRDs that go through a `v1beta1 → v1` promotion,
> omitting `version` lets the source automatically follow the promotion.
> Set `version` explicitly when you want to pin.

```bash
kubectl apply -f tenant-a-projection.yaml
```

### 4. Confirm the projected ConfigMap appears

```bash
kubectl get projections -A
```

```console
NAMESPACE   NAME                KIND        SOURCE-NAMESPACE   SOURCE-NAME   DESTINATION   READY   AGE
tenant-a    app-config-mirror   ConfigMap   default            app-config    app-config    True    3s
```

`Destination` reflects `status.destinationName` — the resolved name after any rename, populated after the first successful write.

Check the destination object itself:

```bash
kubectl get configmap -n tenant-a app-config -o yaml
```

You should see the same `.data` as the source plus the ownership annotation and UID label:

```yaml
metadata:
  annotations:
    projection.sh/owned-by-projection: tenant-a/app-config-mirror
  labels:
    projection.sh/owned-by-projection-uid: <projection-uid>
```

The annotation is the authoritative ownership signal — it's what the controller checks before every write or delete. The UID label is a watch hint used by `ensureDestWatch` (a manual `kubectl delete` of the destination triggers an immediate reconcile that recreates it).

## Cluster-scoped fan-out with `ClusterProjection`

When you need to mirror the same source into multiple namespaces, use `ClusterProjection`. It's cluster-scoped, fan-out only, and offers two ways to specify the target set: an explicit list, or a label selector.

The trade-off:

- **Explicit list** (`namespaces: [a, b, c]`) — small, stable, reviewable in YAML. PR diffs show exactly which namespaces are in scope. Use this when the target set rarely changes and a human approves additions.
- **Selector** (`namespaceSelector.matchLabels`) — auto-grows. A new namespace created with the matching label is picked up by the next reconcile. Use this when namespace creation is itself automated.

See [Concepts → Destination](concepts.md#2-destination) for the deeper discussion.

### Example A: explicit list

```yaml
# fanout-list.yaml
apiVersion: projection.sh/v1
kind: ClusterProjection
metadata:
  name: shared-config-fanout
spec:
  source:
    kind: ConfigMap
    namespace: default
    name: app-config
  destination:
    namespaces:                  # listType=set, minItems=1
      - tenant-a
      - tenant-b
    name: shared-app-config      # optional rename
  overlay:
    labels:
      projected-by: projection
```

```bash
kubectl create namespace tenant-a tenant-b 2>/dev/null || true
kubectl apply -f fanout-list.yaml

kubectl get clusterprojections
```

```console
NAME                   KIND        SOURCE-NAMESPACE   SOURCE-NAME   DESTINATION         TARGETS   READY   AGE
shared-config-fanout   ConfigMap   default            app-config    shared-app-config   2         True    4s
```

`Targets` is `status.namespacesWritten` — the count of namespaces where the destination was successfully written on the last reconcile. `Failed` (visible with `-o wide`) shows `status.namespacesFailed`.

### Example B: label selector

```yaml
# fanout-selector.yaml
apiVersion: projection.sh/v1
kind: ClusterProjection
metadata:
  name: shared-config-fanout
spec:
  source:
    kind: ConfigMap
    namespace: default
    name: app-config
  destination:
    namespaceSelector:
      matchLabels:
        projection.sh/mirror: "true"
    name: shared-app-config
  overlay:
    labels:
      projected-by: projection
```

Label the namespaces that should receive the copy:

```bash
kubectl label namespace tenant-a projection.sh/mirror=true
kubectl label namespace tenant-b projection.sh/mirror=true
kubectl apply -f fanout-selector.yaml
```

Adding the label to a new namespace triggers an immediate reconcile and the destination appears. Removing the label deletes the destination from that namespace.

`namespaces` and `namespaceSelector` are mutually exclusive (CEL admission rejects setting both) and one must be set (CEL admission rejects setting neither). `namespaces` cannot be the empty list (CRD `minItems=1`).

## Sources outside the core group

The examples above use bare `kind: ConfigMap` — core-group sources resolved via the RESTMapper. For sources in any **named group** — built-ins like `apps`, `networking.k8s.io`, or your own CRDs at `example.com` — `group` is required and `version` is optional. Two forms work:

### Pinned named-group source

Pin to an explicit version when you want a stability anchor (e.g. while validating a new CRD version):

```yaml
apiVersion: projection.sh/v1
kind: Projection
metadata:
  name: my-deployment-mirror
  namespace: dest-ns
spec:
  source:
    group: apps
    version: v1                  # pinned
    kind: Deployment
    namespace: source-ns
    name: my-app
```

### Unpinned named-group source (preferred-version lookup)

Omit `version` to let the controller resolve the preferred served version via the `RESTMapper` on every reconcile:

```yaml
apiVersion: projection.sh/v1
kind: Projection
metadata:
  name: widget-mirror
  namespace: tenant-a
spec:
  source:
    group: example.com           # named group
    # version omitted            → preferred-version lookup
    kind: Widget
    namespace: source-ns
    name: my-widget
```

The benefit is most visible against **CRD sources**: when a CRD author promotes `v1beta1` → `v1` and stops serving `v1beta1`, the projection picks up the new version automatically on the next reconcile rather than failing with `SourceResolutionFailed` and garbage-collecting the destination.

The same preferred-version lookup is what powers the bare `kind: ConfigMap` form for core sources — for the core group, the preferred version is always `v1`, so the resolved GVR is stable. Set `version` explicitly when you want to pin to a specific served version.

As with the ConfigMap example above, the source object must carry
`projection.sh/projectable: "true"` if the controller is running in
allowlist mode (the default). See [Source opt-in](#source-opt-in) above.

The resolved version is reported in the `SourceResolved` condition message,
so you can always see which version your projection is currently on:

```bash
kubectl get projection widget-mirror -n tenant-a \
  -o jsonpath='{.status.conditions[?(@.type=="SourceResolved")].message}'
# → resolved example.com/Widget to preferred version v1
```

## Watch propagation

Edit the source and watch the destination update almost immediately:

```bash
kubectl -n default patch configmap app-config --type merge \
  -p '{"data":{"log_level":"debug"}}'

# A beat later:
kubectl get configmap -n tenant-a app-config -o jsonpath='{.data.log_level}'
# → debug
```

Propagation goes through the dynamic source watch registered on the first reconcile, so the round trip is typically well under 200 ms. For ClusterProjection fan-out the controller writes destinations in parallel with a concurrency cap of 16, so one source edit propagates to many namespaces in roughly the same time as a single destination would take.

The controller also registers destination-side watches via `ensureDestWatch` — `kubectl delete` of a destination triggers an immediate reconcile that recreates it, without waiting for the periodic requeue.

## The `Ready` condition

The reconciler stamps three conditions on every Projection and ClusterProjection: `SourceResolved`, `DestinationWritten`, and `Ready`. Inspect them:

```bash
kubectl -n tenant-a get projection app-config-mirror \
  -o jsonpath='{range .status.conditions[*]}{.type}={.status} reason={.reason} msg={.message}{"\n"}{end}'
```

Healthy output:

```text
SourceResolved=True reason=Resolved msg=
DestinationWritten=True reason=Projected msg=
Ready=True reason=Projected msg=
```

For ClusterProjection, also check the per-namespace counters:

```bash
kubectl get clusterprojection shared-config-fanout \
  -o jsonpath='{.status.namespacesWritten}/{.status.namespacesFailed}{"\n"}'
# → 2/0
```

## Cleanup

Delete the Projection or ClusterProjection — the destination is removed with it (as long as `projection` still owns it):

```bash
# Single Projection
kubectl -n tenant-a delete projection app-config-mirror
kubectl -n tenant-a get configmap app-config
# Error from server (NotFound): ...

# ClusterProjection
kubectl delete clusterprojection shared-config-fanout
kubectl get configmap -n tenant-a shared-app-config
# Error from server (NotFound): ...
```

The finalizers `projection.sh/finalizer` (namespaced) and `projection.sh/cluster-finalizer` (cluster) are what guarantee this cleanup.

## Uninstalling the operator

Order matters. The controller is the only thing that can clear its own finalizers from each Projection/ClusterProjection — uninstall it before they're gone and they'll get stuck in `Terminating`, which in turn blocks `kubectl delete crd` until you intervene by hand.

```bash
# 1. Delete every Projection and ClusterProjection. The controller cleans up
#    each owned destination as the finalizer runs.
kubectl delete projection,clusterprojection --all -A

# 2. Confirm they're really gone (the finalizer can take a moment).
kubectl get projection,clusterprojection -A
# No resources found.

# 3. Uninstall the operator.
helm uninstall projection -n projection-system
# (Or, for the install.yaml path: kubectl delete -f install.yaml)

# 4. Helm 3 does not delete CRDs on uninstall. Remove them explicitly:
kubectl delete crd projections.projection.sh clusterprojections.projection.sh
```

Already uninstalled out of order and your CRD delete is hanging? See [CRD deletion is stuck after `helm uninstall`](#crd-deletion-is-stuck-after-helm-uninstall).

## Debugging helper

The repo ships a one-shot snapshot script that dumps operator logs, events, projection statuses, and (optionally) the source/destination objects:

```bash
# Overall view
./hack/observe.sh

# Deep dive on a specific Projection
./hack/observe.sh app-config-mirror tenant-a
```

See [Observability](observability.md) for the three signals the operator exposes (conditions, events, metrics).

## Troubleshooting

### `Ready=False reason=DestinationConflict`

Intentional. An object with the same `Kind/namespace/name` as your destination already exists and is **not** owned by this Projection. We refuse to overwrite it — stamping the ownership annotation on someone else's object could silently break the original owner.

Resolve by one of:

- Point the Projection at a different destination name (or move it to a different namespace, in the namespaced case).
- Delete the pre-existing object (if you're sure nothing else owns it).
- Manually add the ownership annotation if you truly want `projection` to take over:

  ```bash
  # For a namespaced Projection
  kubectl -n <dst-ns> annotate <kind> <name> \
    projection.sh/owned-by-projection=<projection-ns>/<projection-name>

  # For a ClusterProjection
  kubectl -n <dst-ns> annotate <kind> <name> \
    projection.sh/owned-by-cluster-projection=<cluster-projection-name>
  ```

  The next reconcile will then update the destination to match the source. (The UID label is also stamped at that point.)

### `Ready=False reason=SourceFetchFailed`

The operator could find the GVR but not the object. Check that `spec.source.{group, version, kind, namespace, name}` actually identify an object in the cluster. RBAC issues also surface here — remember the controller reads the source via the dynamic client.

### `Ready=False reason=SourceResolutionFailed`

The apiserver doesn't know the Kind. Typo in `group`/`version`/`kind`, a CRD that isn't installed yet, or the source Kind is **cluster-scoped** (`Namespace`, `ClusterRole`, `StorageClass`, …) — `projection` only mirrors namespaced resources and rejects cluster-scoped Kinds with a clear message.

### `Ready=False reason=SourceDeleted`

The source object returned 404 from the apiserver. Every destination owned by this Projection or ClusterProjection has been cleaned up automatically; the Projection itself is left in place so you can recreate the source later (recreating it triggers a fresh reconcile that re-projects). If you intended to remove the Projection too, `kubectl delete projection <name>` (or `kubectl delete clusterprojection <name>`) — the finalizer will short-circuit the cleanup since destinations are already gone.

### `Ready=False reason=SourceNotProjectable`

The controller is in the default `allowlist` mode and the source object lacks `projection.sh/projectable: "true"`. Annotate the source, or switch the operator to `permissive` mode (Helm value `sourceMode: permissive`).

### `Ready=False reason=SourceOptedOut`

The source carries `projection.sh/projectable: "false"` — the source owner has explicitly vetoed projection. The destination, if one existed, has been garbage-collected. Honor the veto, or coordinate with the source owner.

### `Ready=False reason=NamespaceResolutionFailed`

A `ClusterProjection`'s `destination.namespaceSelector` failed to evaluate (e.g. malformed selector, RBAC issue listing namespaces). Inspect the condition message for detail. The namespaced `Projection` cannot produce this reason — it has no selector.

### Destination has stale data

Check the `Updated` / `Projected` events. The operator writes Events through `events.k8s.io/v1` rather than the legacy `core/v1`:

```bash
# Namespaced Projection
kubectl -n <projection-ns> get events.events.k8s.io \
  --field-selector regarding.name=<projection-name>,regarding.kind=Projection \
  --sort-by=.metadata.creationTimestamp

# ClusterProjection (Events for cluster-scoped objects land in the operator's namespace by default)
kubectl get events.events.k8s.io -A \
  --field-selector regarding.name=<cluster-projection-name>,regarding.kind=ClusterProjection \
  --sort-by=.metadata.creationTimestamp
```

Each event carries an `action` verb (`Create`/`Update`/`Delete`/`Get`/`Validate`/`Resolve`/`Write`) alongside the `reason` — visible via `-o wide` or `-o yaml`.

If the last event is recent and the destination still looks wrong, the controller's diff-skip logic may consider it already in sync — see the `needsUpdate` behavior in [Concepts](concepts.md#6-reconcile-lifecycle).

### CRD deletion is stuck after `helm uninstall`

`kubectl delete crd projections.projection.sh` (or `clusterprojections.projection.sh`) hangs. Cause: one or more CRs still carry their finalizer, and the controller — the only thing that can remove it — was uninstalled before they were cleaned up. The apiserver waits for every instance to terminate before deleting the CRD, and the instances cannot terminate without the controller.

Strip the finalizer from every remaining instance by hand. There are two finalizer names to handle — one per CRD:

```bash
# Namespaced Projections (finalizer: projection.sh/finalizer)
kubectl get projection -A -o name | \
  xargs -I {} kubectl patch {} --type=merge -p '{"metadata":{"finalizers":[]}}'

# ClusterProjections (finalizer: projection.sh/cluster-finalizer)
kubectl get clusterprojection -o name | \
  xargs -I {} kubectl patch {} --type=merge -p '{"metadata":{"finalizers":[]}}'
```

Then re-issue the CRD deletes:

```bash
kubectl delete crd projections.projection.sh clusterprojections.projection.sh
```

This bypass skips the destination-cleanup the finalizers normally run, so any destinations the Projections previously created stay in place — owned by nothing. Garbage-collect them by hand if you want them gone. To avoid this in future, follow the order in [Uninstalling the operator](#uninstalling-the-operator).

## Next

- [Concepts](concepts.md) — how source/destination/overlay/ownership fit together; the namespaced/cluster CRD split.
- [Use cases](use-cases.md) — worked examples.
- [API reference](api-reference.md) — field-by-field spec generated from `api/v1/*.go`.
- [CRD behavior and examples](crd-reference.md) — cross-field invariants, condition reasons, YAML examples for both CRDs.
