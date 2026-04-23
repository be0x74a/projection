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

<!-- entries filled in subsequent tasks -->
