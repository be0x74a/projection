# Troubleshooting

One entry per failure-mode condition reason you may see on a `Projection`. Healthy reasons (`Resolved`, `Projected`) are not listed — when everything works there is nothing to troubleshoot.

Every entry assumes you have already located the failing condition. If you haven't, start at [observability.md](observability.md#reasons-youll-see) to learn how to read conditions and events, then come back via the reason link.

## Contents

**`SourceResolved` failures** — the controller could not locate or validate your source object:

- [SourceResolutionFailed](#sourceresolutionfailed)
- [SourceFetchFailed](#sourcefetchfailed)
- [SourceDeleted](#sourcedeleted)
- [SourceOptedOut / SourceNotProjectable](#sourceoptedout--sourcenotprojectable)

**`DestinationWritten` failures** — the controller located the source but could not write the destination:

- [SourceNotResolved](#sourcenotresolved) *(cascade from a `SourceResolved` failure)*
- [InvalidSpec](#invalidspec)
- [NamespaceResolutionFailed](#namespaceresolutionfailed)
- [DestinationFetchFailed](#destinationfetchfailed)
- [DestinationConflict](#destinationconflict)
- [DestinationCreateFailed](#destinationcreatefailed)
- [DestinationUpdateFailed](#destinationupdatefailed)
- [DestinationWriteFailed](#destinationwritefailed) *(rollup across multiple namespaces)*

## `SourceResolved` failures

### SourceResolutionFailed

The controller asked the apiserver's RESTMapper to translate `source.apiVersion` and `source.kind` into a `GroupVersionResource` and the mapper refused. No `Get` against your source has happened yet — this is a type-system error.

Three things can cause it:

- **The Kind is not registered in the cluster.** A CRD you project from is not installed, or was uninstalled. Confirm with `kubectl api-resources | grep <kind>`.
- **The `apiVersion` or `kind` is mis-spelled.** The pattern validation on the CRD catches obvious typos at admission, but a Kind that happens to look right syntactically but does not exist slips through.
- **The target Kind is cluster-scoped.** `projection` only mirrors namespaced resources (`Namespace`, `ClusterRole`, `StorageClass`, CRDs themselves, `PriorityClass`, and similar are all rejected). The message will read `<apiVersion>/<kind> is cluster-scoped; projection only mirrors namespaced resources`.

**Fix:** Install the missing CRD; or correct the `apiVersion`/`kind` spelling; or, if the Kind is genuinely cluster-scoped, `projection` is not the right tool for the job.

### SourceFetchFailed

The GVR resolved and the controller issued a `Get` against the source object, but the apiserver returned an error other than `404 NotFound` (a 404 becomes [`SourceDeleted`](#sourcedeleted) instead — this bucket is everything else).

Typical causes, in rough order of frequency:

- **RBAC.** The controller's `ServiceAccount` lacks `get` on the source Kind. The upstream install grants `"*"/"*"`, so this only shows up when you have narrowed RBAC via the Helm `supportedKinds` values list and forgotten to include the Kind you want to project. Error text includes `cannot get resource <kind> in API group <group>`.
- **Apiserver transient.** 5xx, timeout, connection reset. The controller re-queues; these clear on their own.
- **Admission webhook intercepting `Get`.** Rare, but some validating webhooks are misconfigured to apply to `GET` verbs. Controller logs show the webhook name in the error.

**Fix:** For RBAC, add the Kind to the operator's `ClusterRole` (or the Helm `supportedKinds` list if you use the chart). For transient errors, wait — the next reconcile will succeed. For admission interception, fix the webhook's `operations` scope to exclude read verbs.

### SourceDeleted

The source object's `Get` returned `404 NotFound`. The controller treats this as a deterministic state ("source is gone"), not a transient error: it deletes every owned destination and holds the `Projection` at `Ready=False`. No destination is left orphaned.

There is only one cause: someone deleted the source.

**Fix:** Two valid responses.

- **Recreate the source.** The controller's dynamic watch for the source GVK picks up the `Added` event and reconciles the `Projection` back to `Ready=True`.
- **Delete the `Projection`.** The finalizer runs but has nothing to do — destinations were already cleaned up when `SourceDeleted` was first emitted — so deletion is immediate.

### SourceOptedOut / SourceNotProjectable

Two distinct reasons that share a policy gate. The source object exists and is resolvable, but it failed the opt-in / opt-out check:

- **`SourceOptedOut`** — the source has `projection.be0x74a.io/projectable="false"`. This is the source owner's explicit veto; it takes precedence regardless of operator mode.
- **`SourceNotProjectable`** — the operator is running in the default `allowlist` mode and the source is missing `projection.be0x74a.io/projectable="true"` (or has some other value). In `permissive` mode this reason is never emitted.

The mode is a cluster-wide operator flag (`--source-mode=allowlist|permissive`), not a per-`Projection` setting. It exists so platform teams can choose between "nothing is projected unless sources explicitly opt in" (allowlist, safe default) and "everything is projectable unless explicitly opted out" (permissive, convenience).

When either reason fires, the controller cleans up any destination it previously created — opting out mid-flight is a valid way to withdraw consent.

**Fix:**

- **`SourceOptedOut`:** if you own the source and changed your mind, remove or set the annotation to `"true"`. Otherwise, delete the `Projection` — you cannot override the source owner's veto.
- **`SourceNotProjectable`:** add `projection.be0x74a.io/projectable="true"` to the source's annotations. Or, if the whole cluster should default to permissive, switch the operator flag — but that is a cluster-wide policy decision, not a per-`Projection` workaround.

## `DestinationWritten` failures

### SourceNotResolved

An unusual reason: it is stamped on `DestinationWritten` with status `Unknown`, not `False`. It is a cascade marker, not an independent failure — the controller sets it whenever a `SourceResolved` failure means the write stage was never attempted.

If you see `SourceNotResolved`, the real failure is on the `SourceResolved` condition. Read that reason and the matching entry above:

- [SourceResolutionFailed](#sourceresolutionfailed)
- [SourceFetchFailed](#sourcefetchfailed)
- [SourceDeleted](#sourcedeleted)
- [SourceOptedOut / SourceNotProjectable](#sourceoptedout--sourcenotprojectable)

**Fix:** resolve the upstream `SourceResolved` failure. `SourceNotResolved` will clear on the next reconcile.

### InvalidSpec

The controller rejected the spec before attempting any work. Today there is exactly one trigger: both `destination.namespace` and `destination.namespaceSelector` are set on the same `Projection`. The two fields are mutually exclusive — either you target one namespace by name, or you fan out to every namespace matching a selector, not both.

CEL admission enforces this on apiservers that support it (k8s 1.32+), so most clusters will reject an offending `Projection` at `kubectl apply` time. The reconciler keeps a belt-and-braces runtime check for older apiservers (1.31 and earlier) whose CEL lacks the primitives needed to resolve optional fields reliably.

**Fix:** decide which destination shape you want and remove the other field.

```yaml
# Single destination namespace
spec:
  destination:
    namespace: tenant-a
# …or selector-based fan-out, not both
spec:
  destination:
    namespaceSelector:
      matchLabels:
        tier: tenant
```

### NamespaceResolutionFailed

The `Projection` uses `destination.namespaceSelector` and resolving that selector to a concrete list of namespaces failed. One of two things happened:

- **The selector is syntactically invalid.** `metav1.LabelSelectorAsSelector` rejected it. This is rare in practice because the CRD schema accepts any `LabelSelector`, but malformed `matchExpressions` (e.g. `operator: In` with an empty `values` list) trip it.
- **The `List` on namespaces failed.** Typically RBAC — the controller needs `list` on `namespaces` at cluster scope, which the upstream install grants. If you have narrowed RBAC, confirm namespace list permission is intact.

An empty match set is **not** an error — if your selector matches zero namespaces, reconcile succeeds with nothing to write and you will not see this reason. You will see `Ready=True` with no destinations anywhere, which is its own form of "something's wrong" but not one this doc covers.

**Fix:** check the selector syntax with `kubectl get ns -l '<selector>'` and confirm the operator's `ClusterRole` allows `list` on `namespaces`.

### DestinationFetchFailed

For each target namespace, the controller first issues a `Get` to check whether a destination already exists (so it can decide between create and update, and verify ownership). That `Get` failed with an error other than `404 NotFound` (a 404 is the expected "not there yet" case and does not fail).

Typical causes:

- **RBAC.** The controller's `ServiceAccount` lacks `get` on the destination Kind in the target namespace. Same narrowed-RBAC failure mode as [`SourceFetchFailed`](#sourcefetchfailed) — if you use the Helm chart's `supportedKinds` list, confirm the Kind is listed.
- **Apiserver transient error.** 5xx, timeout. Clears on requeue.

For selector-based `Projection`s this can fire in some namespaces and not others; see [DestinationWriteFailed](#destinationwritefailed) for how the rollup reason works when failures differ per namespace.

**Fix:** widen RBAC for the destination Kind, or wait for the transient to clear.

### DestinationConflict

The most important entry in this guide. The controller fetched an existing object at the destination coordinates and found that it is **not owned by this `Projection`**. Ownership is established by the annotation `projection.be0x74a.io/owned-by: <projection-namespace>/<projection-name>`, which the controller stamps on every destination it creates. If that annotation is missing or points at a different `Projection`, the controller refuses to update — the object belongs to something or someone else.

This is the invariant that makes `projection` safe to adopt alongside other tooling: we will never silently overwrite an object we didn't create. Conflict-safety is a design property, not a bug.

One cause: an object with the same name and Kind already exists at your chosen destination coordinates, and it was not created by this `Projection`. Typical scenarios:

- Another tool (Helm, Kustomize, Kyverno `generate`, a different `Projection`) manages that name.
- A human created the object directly via `kubectl apply`.
- A previous `Projection` created the object, was deleted, and somebody or something stripped the ownership annotation before you created the new `Projection`.

**Fix:** the resolution is a human decision, not a mechanical one.

- **Delete the pre-existing object** if it is genuinely stale and you want `projection` to take over. Do this knowingly — check `kubectl get <kind>/<name> -o yaml` first to confirm nothing important lives there.
- **Rename the destination.** Set `destination.name` on the `Projection` to a name that doesn't collide.
- **Accept the conflict.** The `Projection` stays at `Ready=False` and does nothing. This is a legitimate steady state — it means "another tool owns this name; defer to them."

Do **not** manually add the ownership annotation to an object you didn't create. That tells `projection` it can update and delete the object, which would then propagate changes from the source — almost certainly not what you want.

### DestinationCreateFailed

The destination does not yet exist (the preceding `Get` returned 404) and the `Create` call was rejected by the apiserver.

Typical causes:

- **Admission webhook rejection.** A validating or mutating webhook in the target namespace rejected the create. `ResourceQuota` violations surface here (e.g. "exceeded quota: pods"). So do policy engines: Kyverno `validate` policies, OPA Gatekeeper, network policy admission.
- **RBAC.** The controller lacks `create` on the destination Kind. With default RBAC this does not happen; with narrowed RBAC it does.
- **Field-level validation.** The destination object, after overlay application, violates CRD or built-in schema validation. This is rare because the source object itself was admitted at its own create time, but overlays that rewrite fields in invalid ways can trip it.

**Fix:** read the error message carefully — the apiserver is usually specific about what rejected the create and why. For webhook rejections, the webhook's name is in the error; investigate that policy. For RBAC, widen the `ClusterRole`.

### DestinationUpdateFailed

The destination already exists and is owned by this `Projection`, but the `Update` call was rejected. Same failure surface as [`DestinationCreateFailed`](#destinationcreatefailed) but on the overwrite path, with two additional wrinkles specific to updates:

- **Conflict (409).** Another client modified the destination between our `Get` and our `Update`. The controller re-queues and the next reconcile reads the fresh resourceVersion. Self-clearing; if it persists, some other tool is writing to the destination in a tight loop.
- **Immutable field change.** The controller strips server-assigned fields (`clusterIP`, `volumeName`, `nodeName`) before building the destination and restores them from the existing object before Update, specifically to avoid this. If you see "field is immutable" in the error, it is a bug — the set of preserved fields (`droppedSpecFieldsByGVK` in the controller source) is likely missing an entry. Please [open an issue](https://github.com/be0x74a/projection/issues/new) with the Kind and the field name.

**Fix:** for webhook/RBAC errors, same remedies as [`DestinationCreateFailed`](#destinationcreatefailed). For 409 conflicts, wait one reconcile. For immutable-field errors, file a bug.

### DestinationWriteFailed

A rollup reason, emitted only by selector-based `Projection`s. When the destination write fan-out hits failures in multiple namespaces and those failures have **different reasons**, the controller refuses to pick one arbitrarily and surfaces `DestinationWriteFailed` instead. If every failing namespace shares the same underlying reason (all `DestinationConflict`, say), that shared reason is used directly — you only see `DestinationWriteFailed` when the failures are heterogeneous.

The condition `message` lists the failing namespaces (`failed namespaces: ns-a, ns-b, ns-c`), but the actual causes are only in the per-namespace Events. This is deliberate: a single status message cannot faithfully encode three different failure modes.

**Fix:** drill into Events to see each namespace's actual reason. From [observability.md](observability.md#kubernetes-events):

```bash
kubectl -n <projection-ns> get events.events.k8s.io \
  --field-selector regarding.name=<projection-name>,regarding.kind=Projection \
  --sort-by=.lastTimestamp
```

You will see one Warning event per failed namespace, each carrying its own reason (`DestinationConflict`, `DestinationCreateFailed`, etc.). Resolve each one using the matching entry in this guide.
