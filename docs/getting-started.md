# Getting started

This walks through installing the `projection` operator and creating your first `Projection`.

> **Upgrading from v0.1.0-alpha?** Read the [upgrade guide](upgrade.md) first. v0.2 changes three things that matter to existing installs: the default `sourceMode` flips from `permissive` to `allowlist` (sources need an opt-in annotation), Kubernetes Events now use `events.k8s.io/v1` instead of the legacy `core/v1` (alerting rules need updating), and `Projection.spec.destination` gains the mutually-exclusive `namespaceSelector` field. The migration script at `hack/migrate-to-v1.sh` handles the annotation rollout.

## Prerequisites

- A Kubernetes cluster (1.32+ required — the CRD uses CEL admission validation, which needs this minimum version).
- `kubectl` configured to talk to it.
- Cluster-admin (for the initial install — the chart creates a CRD and a ClusterRole).

## Install

### Option 1 — Helm (OCI)

```bash
helm install projection oci://ghcr.io/projection-operator/charts/projection \
  --version 0.2.0 \
  --namespace projection-system --create-namespace
```

### Option 2 — `kubectl apply`

```bash
kubectl apply -f https://github.com/projection-operator/projection/releases/download/v0.2.0/install.yaml
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
"true"` to be mirrored. Without it, the `Projection` status reports
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

The canonical example at `examples/configmap-cross-namespace.yaml` already
carries the annotation — you don't need to edit anything to follow the next
section.

## Your first Projection

Apply the canonical ConfigMap-across-namespaces example:

```bash
kubectl apply -f https://raw.githubusercontent.com/projection-operator/projection/main/examples/configmap-cross-namespace.yaml
```

This creates:

- A source `ConfigMap/default/app-config`.
- A destination namespace `tenant-a`.
- A `Projection/default/app-config-to-tenant-a` pointing the source into `tenant-a`.

Confirm it worked:

```bash
kubectl get projections -A
```

```console
NAMESPACE   NAME                     KIND        SOURCE-NAMESPACE   SOURCE-NAME   DESTINATION   READY   AGE
default     app-config-to-tenant-a   ConfigMap   default            app-config    app-config    True    3s
```

Check the destination:

```bash
kubectl get configmap -n tenant-a app-config -o yaml
```

You should see the same `.data` as the source plus the ownership annotation:

```yaml
metadata:
  annotations:
    projection.sh/owned-by: default/app-config-to-tenant-a
```

## Sources outside the core group: prefer `<group>/*`

The ConfigMap example above uses `apiVersion: v1` — core group, pinned
version. Core Kubernetes versions are stable, so pinning is fine there. For
sources in any **named group** — built-ins like `apps`, `networking.k8s.io`,
or your own CRDs at `example.com` — reach for the unpinned form instead:

```yaml
apiVersion: projection.sh/v1
kind: Projection
metadata:
  name: my-deployment-mirror
  namespace: default
spec:
  source:
    apiVersion: apps/*         # RESTMapper-preferred served version
    kind: Deployment
    name: my-app
    namespace: source-ns
  destination:
    namespace: dest-ns
```

The `*` sentinel tells the controller to resolve the preferred served version
via the `RESTMapper` on every reconcile. The benefit is most visible against
**CRD sources**: when a CRD author promotes `v1beta1` → `v1` and stops serving
`v1beta1`, the projection picks up the new version automatically on the next
reconcile rather than failing with `SourceResolutionFailed` and
garbage-collecting the destination.

As with the ConfigMap example above, the source Deployment must carry
`projection.sh/projectable: "true"` if the controller is running in
allowlist mode (the default). See [Source opt-in](#source-opt-in) above.

The resolved version is reported in the `SourceResolved` condition message,
so you can always see which version your projection is currently on:

```bash
kubectl get projection my-deployment-mirror \
  -o jsonpath='{.status.conditions[?(@.type=="SourceResolved")].message}'
# → resolved apps/Deployment to preferred version v1
```

Pinning (e.g. `apps/v1`, `example.com/v1beta1`) is still available when you
want an explicit stability anchor — useful while validating a new CRD version,
or to deliberately fall behind. There is no unpinned form for the core group
— `v1` is the only form accepted there.

## Watch propagation

Edit the source and watch the destination update almost immediately:

```bash
kubectl -n default patch configmap app-config --type merge \
  -p '{"data":{"log_level":"debug"}}'

# A beat later:
kubectl get configmap -n tenant-a app-config -o jsonpath='{.data.log_level}'
# → debug
```

Propagation goes through the dynamic source watch registered on the first reconcile, so the round trip is typically well under 200 ms. For selector-based fan-out (`destination.namespaceSelector`) the controller writes destinations in parallel with a concurrency cap of 16, so one source edit propagates to many namespaces in roughly the same time as a single destination would take.

## The `Ready` condition

The reconciler stamps three conditions on every `Projection`: `SourceResolved`, `DestinationWritten`, and `Ready`. Inspect them:

```bash
kubectl -n default get projection app-config-to-tenant-a \
  -o jsonpath='{range .status.conditions[*]}{.type}={.status} reason={.reason} msg={.message}{"\n"}{end}'
```

Healthy output:

```text
SourceResolved=True reason=Resolved msg=
DestinationWritten=True reason=Projected msg=
Ready=True reason=Projected msg=
```

## Cleanup

Delete the Projection — the destination is removed with it (as long as `projection` still owns it):

```bash
kubectl -n default delete projection app-config-to-tenant-a
kubectl -n tenant-a get configmap app-config
# Error from server (NotFound): ...
```

The finalizer `projection.sh/finalizer` is what guarantees this cleanup.

## Uninstalling the operator

Order matters. The controller is the only thing that can clear its own finalizer from each Projection — uninstall it before the Projections are gone and they'll get stuck in `Terminating`, which in turn blocks `kubectl delete crd` until you intervene by hand.

```bash
# 1. Delete every Projection across all namespaces. The controller cleans up
#    each owned destination as the finalizer runs.
kubectl delete projection --all -A

# 2. Confirm they're really gone (the finalizer can take a moment).
kubectl get projection -A
# No resources found.

# 3. Uninstall the operator.
helm uninstall projection -n projection-system
# (Or, for the install.yaml path: kubectl delete -f install.yaml)

# 4. Helm 3 does not delete CRDs on uninstall. Remove it explicitly:
kubectl delete crd projections.projection.sh
```

Already uninstalled out of order and your CRD delete is hanging? See [CRD deletion is stuck after `helm uninstall`](#crd-deletion-is-stuck-after-helm-uninstall).

## Debugging helper

The repo ships a one-shot snapshot script that dumps operator logs, events, projection statuses, and (optionally) the source/destination objects:

```bash
# Overall view
./hack/observe.sh

# Deep dive on a specific Projection
./hack/observe.sh app-config-to-tenant-a default
```

See [Observability](observability.md) for the three signals the operator exposes (conditions, events, metrics).

## Troubleshooting

### `Ready=False reason=DestinationConflict`

Intentional. An object with the same `Kind/namespace/name` as your destination already exists and is **not** owned by this `Projection`. We refuse to overwrite it — stamping the ownership annotation on someone else's object could silently break the original owner.

Resolve by one of:

- Point the Projection at a different destination name/namespace.
- Delete the pre-existing object (if you're sure nothing else owns it).
- Manually add the ownership annotation if you truly want `projection` to take over:

  ```bash
  kubectl -n <dst-ns> annotate <kind> <name> \
    projection.sh/owned-by=<projection-ns>/<projection-name>
  ```

  The next reconcile will then update the destination to match the source.

### `Ready=False reason=SourceFetchFailed`

The operator could find the GVR but not the object. Check that `spec.source.{apiVersion, kind, namespace, name}` actually exist. RBAC issues also surface here — remember the controller reads the source via the dynamic client.

### `Ready=False reason=SourceResolutionFailed`

The apiserver doesn't know the Kind. Typo in `apiVersion`/`kind`, a CRD that isn't installed yet, or the source Kind is **cluster-scoped** (`Namespace`, `ClusterRole`, `StorageClass`, …) — `projection` only mirrors namespaced resources and rejects cluster-scoped Kinds with a clear message. Bare `*` is also rejected; use `<group>/*` for the unpinned form.

### `Ready=False reason=SourceDeleted`

The source object returned 404 from the apiserver. Every destination owned by this Projection has been cleaned up automatically; the Projection itself is left in place so you can recreate the source later (recreating it triggers a fresh reconcile that re-projects). If you intended to remove the Projection too, `kubectl delete projection <name>` — the finalizer will short-circuit the cleanup since destinations are already gone.

### `Ready=False reason=SourceNotProjectable`

The controller is in the default `allowlist` mode and the source object lacks `projection.sh/projectable: "true"`. Annotate the source, or switch the operator to `permissive` mode (Helm value `sourceMode: permissive`).

### `Ready=False reason=SourceOptedOut`

The source carries `projection.sh/projectable: "false"` — the source owner has explicitly vetoed projection. The destination, if one existed, has been garbage-collected. Honor the veto, or coordinate with the source owner.

### `Ready=False reason=NamespaceResolutionFailed`

A `destination.namespaceSelector` failed to evaluate (e.g. malformed selector, RBAC issue listing namespaces). Inspect the condition message for detail.

### `Ready=False reason=InvalidSpec`

`destination.namespace` and `destination.namespaceSelector` are both set. They are mutually exclusive — pick one. CEL admission rejects this on apiserver versions ≥ 1.32; on older apiservers the reconciler catches it as a defense-in-depth.

### Destination has stale data

Check the `Updated` / `Projected` events. v0.2 writes Events through `events.k8s.io/v1` rather than the legacy `core/v1`:

```bash
kubectl -n <projection-ns> get events.events.k8s.io \
  --field-selector regarding.name=<projection-name>,regarding.kind=Projection \
  --sort-by=.metadata.creationTimestamp
```

Each event carries an `action` verb (`Create`/`Update`/`Delete`/`Get`/`Validate`/`Resolve`/`Write`) alongside the `reason` — visible via `-o wide` or `-o yaml`.

If the last event is recent and the destination still looks wrong, the controller's diff-skip logic may consider it already in sync — see the `needsUpdate` behavior in [Concepts](concepts.md#6-reconcile-lifecycle).

### CRD deletion is stuck after `helm uninstall`

`kubectl delete crd projections.projection.sh` hangs. Cause: one or more Projection CRs still carry `projection.sh/finalizer`, and the controller — the only thing that can remove it — was uninstalled before they were cleaned up. The apiserver waits for every instance to terminate before deleting the CRD, and the instances cannot terminate without the controller.

Strip the finalizer from every remaining Projection by hand:

```bash
kubectl get projection -A -o name | \
  xargs -I {} kubectl patch {} --type=merge -p '{"metadata":{"finalizers":[]}}'
```

Then re-issue the CRD delete:

```bash
kubectl delete crd projections.projection.sh
```

This bypass skips the destination-cleanup the finalizer normally runs, so any destinations the Projections previously created stay in place — owned by nothing. Garbage-collect them by hand if you want them gone. To avoid this in future, follow the order in [Uninstalling the operator](#uninstalling-the-operator).

## Next

- [Concepts](concepts.md) — how source/destination/overlay/ownership fit together.
- [Use cases](use-cases.md) — six worked examples.
- [API reference](api-reference.md) — field-by-field spec generated from `api/v1/projection_types.go`.
- [CRD behavior and examples](crd-reference.md) — cross-field invariants, condition reasons, YAML examples.
