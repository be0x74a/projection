# Getting started

This walks through installing the `projection` operator and creating your first `Projection`.

> **Upgrading from v0.1.0-alpha?** Read the [upgrade guide](upgrade.md) first — v0.2 introduces a source-annotation requirement that needs a one-time migration.

## Prerequisites

- A Kubernetes cluster (1.32+ required — the CRD uses CEL admission validation, which needs this minimum version).
- `kubectl` configured to talk to it.
- Cluster-admin (for the initial install — the chart creates a CRD and a ClusterRole).

## Install

### Option 1 — Helm (OCI)

```bash
helm install projection oci://ghcr.io/be0x74a/charts/projection \
  --version 0.1.0-alpha \
  --namespace projection-system --create-namespace
```

### Option 2 — `kubectl apply`

```bash
kubectl apply -f https://github.com/be0x74a/projection/releases/download/v0.1.0-alpha/install.yaml
```

Either way, verify the operator is healthy:

```bash
kubectl -n projection-system get deploy
kubectl -n projection-system get pods
```

You should see one `Running` controller pod. If it's `CrashLoopBackOff`, jump to [Troubleshooting](#troubleshooting).

## Source opt-in

`projection` ships with `--source-mode=allowlist` as the default. That means a
source object must carry the annotation `projection.be0x74a.io/projectable:
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
    projection.be0x74a.io/projectable: "true"
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
kubectl apply -f https://raw.githubusercontent.com/be0x74a/projection/main/examples/configmap-cross-namespace.yaml
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
    projection.be0x74a.io/owned-by: default/app-config-to-tenant-a
```

## Mirroring a CRD: prefer `apps/*`

The ConfigMap example above uses `apiVersion: v1` — core group, pinned version.
Core Kubernetes versions are stable, so pinning is fine there. For **CRD
sources**, reach for the unpinned form instead:

```yaml
apiVersion: projection.be0x74a.io/v1
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
via the `RESTMapper` on every reconcile. When a CRD author promotes
`v1beta1` → `v1` and stops serving `v1beta1`, the projection picks up the new
version automatically on the next reconcile rather than failing with
`SourceResolutionFailed` and garbage-collecting the destination.

The resolved version is reported in the `SourceResolved` condition message,
so you can always see which version your projection is currently on:

```bash
kubectl describe projection my-deployment-mirror \
  | grep -A2 SourceResolved
# → Message: resolved apps/Deployment to preferred version v1
```

Pinning (`apps/v1`) is still available when you want an explicit stability
anchor, e.g. while validating a new CRD version. There is no unpinned form for
the core group — `v1` is the only form accepted there.

## Watch propagation

Edit the source and watch the destination update almost immediately:

```bash
kubectl -n default patch configmap app-config --type merge \
  -p '{"data":{"log_level":"debug"}}'

# A beat later:
kubectl get configmap -n tenant-a app-config -o jsonpath='{.data.log_level}'
# → debug
```

Propagation goes through the dynamic source watch registered on the first reconcile, so the round trip is typically well under 200 ms.

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

The finalizer `projection.be0x74a.io/finalizer` is what guarantees this cleanup.

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
    projection.be0x74a.io/owned-by=<projection-ns>/<projection-name>
  ```

  The next reconcile will then update the destination to match the source.

### `Ready=False reason=SourceFetchFailed`

The operator could find the GVR but not the object. Check that `spec.source.{apiVersion, kind, namespace, name}` actually exist. RBAC issues also surface here — remember the controller reads the source via the dynamic client.

### `Ready=False reason=SourceResolutionFailed`

The apiserver doesn't know the Kind. Typo in `apiVersion`/`kind`, or a CRD that isn't installed yet.

### Destination has stale data

Check the `Updated` / `Projected` events:

```bash
kubectl -n <projection-ns> get events \
  --field-selector involvedObject.name=<projection-name> \
  --sort-by=.lastTimestamp
```

If the last event is recent and the destination still looks wrong, the controller's diff-skip logic may consider it already in sync — see the `needsUpdate` behavior in [Concepts](concepts.md#6-reconcile-lifecycle).

## Next

- [Concepts](concepts.md) — how source/destination/overlay/ownership fit together.
- [Use cases](use-cases.md) — six worked examples.
- [API reference](api-reference.md) — field-by-field spec generated from `api/v1/projection_types.go`.
- [CRD behavior and examples](crd-reference.md) — cross-field invariants, condition reasons, YAML examples.
