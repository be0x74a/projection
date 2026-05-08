# Troubleshooting

One entry per failure-mode condition reason you may see on a `Projection` or `ClusterProjection`. Healthy reasons (`Resolved`, `Projected`) are not listed — when everything works there is nothing to troubleshoot.

Every entry assumes you have already located the failing condition. If you haven't, start at [observability.md](observability.md#reasons-youll-see) to learn how to read conditions and events, then come back via the reason link.

Each entry begins with an **Applies to:** line so you can tell at a glance which CRD it pertains to. Most reasons fire on both; a few are specific to one or the other.

For operator install/uninstall issues that are not condition failures (e.g. a `kubectl delete crd` that hangs after `helm uninstall`), see the [Troubleshooting section of the getting-started guide](getting-started.md#troubleshooting) — those are operator-lifecycle issues rather than per-CR conditions, so they live with the install/uninstall procedure. The [stuck CRD recovery](#stuck-crd-deletion-or-orphaned-finalizers) entry below covers the per-CR side of the same surface.

## Contents

**`SourceResolved` failures** — the controller could not locate or validate your source object:

- [SourceResolutionFailed](#sourceresolutionfailed)
- [SourceFetchFailed](#sourcefetchfailed)
- [SourceDeleted](#sourcedeleted)
- [SourceOptedOut / SourceNotProjectable](#sourceoptedout-sourcenotprojectable)

**`DestinationWritten` failures** — the controller located the source but could not write the destination:

- [SourceNotResolved](#sourcenotresolved) *(cascade from a `SourceResolved` failure)*
- [InvalidSpec](#invalidspec)
- [NamespaceResolutionFailed](#namespaceresolutionfailed) *(ClusterProjection only)*
- [DestinationFetchFailed](#destinationfetchfailed)
- [DestinationConflict](#destinationconflict)
- [DestinationCreateFailed](#destinationcreatefailed)
- [DestinationUpdateFailed](#destinationupdatefailed)
- [DestinationWriteFailed](#destinationwritefailed) *(ClusterProjection only — heterogeneous-failure rollup)*

**Operational recovery** — not a condition reason, but the procedures live here so you can find them:

- [Stuck CRD deletion or orphaned finalizers](#stuck-crd-deletion-or-orphaned-finalizers)

## How to read events

Every entry below references events the controller emits when a failure surfaces. The controller writes Events through `events.k8s.io/v1`, **not** the legacy `core/v1` API — `kubectl get events` (which reads `core/v1`) won't show them. Use the `events.k8s.io` resource:

```bash
# All events for one Projection, oldest first
kubectl -n <ns> get events.events.k8s.io \
  --field-selector regarding.name=<projection-name>,regarding.kind=Projection \
  --sort-by=.lastTimestamp

# All events for one ClusterProjection (cluster-scoped object events live in `default`)
kubectl -n default get events.events.k8s.io \
  --field-selector regarding.name=<clusterprojection-name>,regarding.kind=ClusterProjection \
  --sort-by=.lastTimestamp

# Just Warnings for either kind, cluster-wide (handy in an on-call shell)
kubectl get events.events.k8s.io -A --field-selector type=Warning \
  | grep -E '(Projection|ClusterProjection)'
```

Each event carries an `action` verb (`Create`/`Update`/`Delete`/`Get`/`Validate`/`Resolve`/`Write`) alongside the `reason`, visible via `-o wide` or `-o yaml`. Successful state transitions (`Projected`, `Updated`, `DestinationDeleted`, `StaleDestinationDeleted`, `DestinationLeftAlone`) are emitted as `Normal` events and aren't covered by this guide — they're documented in [observability.md](observability.md#2-kubernetes-events) instead.

For ClusterProjection partial failures, expect **multiple events per reconcile** — one per affected target namespace, each carrying its own reason and the failing namespace in its message.

## `SourceResolved` failures

### SourceResolutionFailed

**Applies to:** `Projection` and `ClusterProjection`.

The controller tried to translate `source.group`, `source.version`, and `source.kind` into a `GroupVersionResource` and one of two gates refused: either the apiserver's RESTMapper could not find the `{Group, Kind}` mapping at all, or the mapping succeeded but the resolved Kind is cluster-scoped (the controller rejects cluster-scoped Kinds outright because `projection` only mirrors namespaced resources). No `Get` against your source has happened yet — this is a type-system error.

Things that can cause it:

- **The Kind is not registered in the cluster.** A CRD you project from is not installed, or was uninstalled. Confirm with `kubectl api-resources | grep <kind>`.
- **The `group`, `version`, or `kind` is mis-spelled.** The pattern validation on the CRD catches obvious typos at admission, but a Kind that happens to look right syntactically but does not exist slips through.
- **The target Kind is cluster-scoped.** `projection` only mirrors namespaced resources (`Namespace`, `ClusterRole`, `StorageClass`, CRDs themselves, `PriorityClass`, and similar are all rejected). The message will read `<group>/<version>/<kind> is cluster-scoped; projection only mirrors namespaced resources`.
- **You omitted `source.version` for a CRD with multiple served versions and no clear preferred.** When `version:` is omitted, the RESTMapper's preferred-version lookup picks one of the served versions; if your CRD declares more than one served version with no explicit preferred, the pick can be surprising. See the sub-entry below for the remedy.

#### Sub-cause: unpinned version with multiple served versions

The v0.3.0 SourceRef permits omitting `source.version` for any group, including core (`kind: ConfigMap` alone is valid; the operator resolves to `v1` via the RESTMapper). The RESTMapper resolves the omitted version through `RESTMapping(GroupKind)`, which returns the *preferred* version. For core resources the preferred version is always `v1`, so the resolved GVR is stable in practice. The pitfall is on **CRDs with multiple served versions**: if the source CRD has more than one served version and either declares no preferred or its preferred isn't the one that holds your data, you'll see `SourceResolutionFailed` (no mapping for the picked version) or, more confusingly, a successful resolve onto the wrong version with subsequent `SourceFetchFailed` because the data lives on a different served version.

Diagnose:

```bash
# Inspect served versions for the source's CRD.
kubectl get crd <crd-plural>.<group> \
  -o jsonpath='{.spec.versions[?(@.served==true)].name}{"\n"}'

# And which one is the storage version (if explicitly marked).
kubectl get crd <crd-plural>.<group> \
  -o jsonpath='{range .spec.versions[?(@.storage==true)]}{.name}{"\n"}{end}'
```

If the served-version list has more than one entry and none is explicitly preferred, **pin `source.version` explicitly** to the version your data lives on. The version-omission shortcut is intended for the core group (where preferred is always `v1`) and for stable single-version CRDs where the preferred version is unambiguous; multi-version CRDs should always pin.

**Fix:** Install the missing CRD; correct the `group`/`version`/`kind` spelling; pin `source.version` explicitly when the source CRD has multiple served versions; or, if the Kind is genuinely cluster-scoped, `projection` is not the right tool for the job.

### SourceFetchFailed

**Applies to:** `Projection` and `ClusterProjection`.

The GVR resolved and the controller issued a `Get` against the source object, but the apiserver returned an error other than `404 NotFound` (a 404 becomes [`SourceDeleted`](#sourcedeleted) instead — this bucket is everything else).

Typical causes, in rough order of frequency:

- **RBAC.** The controller's `ServiceAccount` lacks `get` on the source Kind. The upstream install grants wildcard `group="*" resource="*"` access by default, so this only shows up if you have narrowed RBAC — either by hand-editing the `ClusterRole` or by setting the Helm chart's [`supportedKinds`](security.md#1-narrow-the-controllers-rbac-to-the-kinds-you-actually-mirror) allowlist without including the source's Kind. Error text includes `cannot get resource <kind> in API group <group>`.
- **Apiserver transient.** 5xx, timeout, connection reset. The controller re-queues; these clear on their own.
- **Admission webhook intercepting `Get`.** Rare, but some validating webhooks are misconfigured to apply to `GET` verbs. Controller logs show the webhook name in the error.

**Fix:** For RBAC, restore the controller's `ClusterRole` to include the Kind you want to project (the upstream install grants wildcard access, so this only applies if you have narrowed it manually). For transient errors, wait — the next reconcile will succeed. For admission interception, fix the webhook's `operations` scope to exclude read verbs.

### SourceDeleted

**Applies to:** `Projection` and `ClusterProjection`.

The source object's `Get` returned `404 NotFound`. The controller treats this as a deterministic state ("source is gone"), not a transient error: it deletes every owned destination and holds the CR at `Ready=False`. No destination is left orphaned. For `ClusterProjection`, this means destinations are cleaned up across every target namespace — the cleanup walks the cluster via the UID label index.

There is only one cause: someone deleted the source.

**Fix:** Two valid responses.

- **Recreate the source.** The controller's dynamic watch for the source GVK picks up the `Added` event and reconciles the CR back to `Ready=True`.
- **Delete the CR.** The finalizer runs but has nothing to do — destinations were already cleaned up when `SourceDeleted` was first emitted — so deletion is immediate.

### SourceOptedOut / SourceNotProjectable

**Applies to:** `Projection` and `ClusterProjection`.

Two distinct reasons that share a policy gate. The source object exists and is resolvable, but it failed the opt-in / opt-out check:

- **`SourceOptedOut`** — the source has `projection.sh/projectable="false"`. This is the source owner's explicit veto; it takes precedence regardless of operator mode.
- **`SourceNotProjectable`** — the operator is running in the default `allowlist` mode and the source is missing `projection.sh/projectable="true"` (or has some other value). In `permissive` mode this reason is never emitted.

The mode is a cluster-wide operator flag (`--source-mode=allowlist|permissive`), not a per-CR setting. It exists so platform teams can choose between "nothing is projected unless sources explicitly opt in" (allowlist, safe default) and "everything is projectable unless explicitly opted out" (permissive, convenience).

When either reason fires, the controller cleans up any destination it previously created — opting out mid-flight is a valid way to withdraw consent.

**Fix:**

- **`SourceOptedOut`:** if you own the source and changed your mind, remove or set the annotation to `"true"`. Otherwise, delete the CR — you cannot override the source owner's veto.
- **`SourceNotProjectable`:** add `projection.sh/projectable="true"` to the source's annotations. Or, if the whole cluster should default to permissive, switch the operator flag — but that is a cluster-wide policy decision, not a per-CR workaround.

## `DestinationWritten` failures

### SourceNotResolved

**Applies to:** `Projection` and `ClusterProjection`.

An unusual reason: it is stamped on `DestinationWritten` with status `Unknown`, not `False`. It is a cascade marker, not an independent failure — the controller sets it whenever a `SourceResolved` failure means the write stage was never attempted.

If you see `SourceNotResolved`, the real failure is on the `SourceResolved` condition. Read that reason and the matching entry above:

- [SourceResolutionFailed](#sourceresolutionfailed)
- [SourceFetchFailed](#sourcefetchfailed)
- [SourceDeleted](#sourcedeleted)
- [SourceOptedOut / SourceNotProjectable](#sourceoptedout-sourcenotprojectable)

**Fix:** resolve the upstream `SourceResolved` failure. `SourceNotResolved` will clear on the next reconcile.

### InvalidSpec

**Applies to:** `ClusterProjection` only in v0.3 (admission-time, so the offending CR usually never makes it past `kubectl apply`).

The apiserver rejected the spec via CEL validation rules on the CRD. The CR either never created (you'll see this as a `kubectl apply` error) or, in the rare case where CEL validation is bypassed, the controller surfaces it as a runtime `DestinationWritten=False reason=InvalidSpec` event. Either way, the cause is one of two structural mistakes:

> Pre-v0.3 SourceRef carried a CEL rule `size(self.group) != 0 || size(self.version) != 0` that rejected `kubectl apply` for any Projection or ClusterProjection with both `source.group` and `source.version` empty. v0.3 drops that rule — `source.version` is now optional for any group, including core. Manifests with explicit `version: v1` continue to validate; `kind: ConfigMap` alone now works too. Old runbooks mentioning an `InvalidSpec` admission error from `source must specify version when group is empty` apply only to pre-v0.3.

#### 1. ClusterProjection.destination with both `namespaces` AND `namespaceSelector` set

**Applies to:** `ClusterProjection` only.

The two destination shapes are mutually exclusive — you target an explicit namespace list, or you fan out via a label selector, never both. The CEL admission rule:

```
spec.destination must set exactly one of namespaces or namespaceSelector
```

The literal admission error from the apiserver looks like this (the message is from the CRD's `x-kubernetes-validations`):

```
The ClusterProjection "..." is invalid: spec.destination: Invalid value: "object":
destination must specify exactly one of namespaces or namespaceSelector, not both
```

**Fix:** decide which destination shape you want and remove the other field.

```yaml
# Option A — explicit namespace list
spec:
  destination:
    namespaces: [tenant-a, tenant-b]
```

…or:

```yaml
# Option B — selector-based fan-out
spec:
  destination:
    namespaceSelector:
      matchLabels:
        tier: tenant
```

#### 2. ClusterProjection.destination with NEITHER `namespaces` NOR `namespaceSelector` set

**Applies to:** `ClusterProjection` only.

The mirror image of the previous error: omitting both fields gives the controller no way to determine which namespaces to write to. The literal admission error:

```
The ClusterProjection "..." is invalid: spec.destination: Invalid value: "object":
destination must specify exactly one of namespaces or namespaceSelector
```

**Fix:** add one of the two destination shapes, as in the previous example.

> The v0.2 `Projection.destination.namespace` xor `destination.namespaceSelector` mutex check is **gone** in v0.3 — `Projection` no longer has either field. Anything in old documentation, scripts, or runbooks mentioning a runtime `InvalidSpec` for that mutex on namespaced `Projection` is stale and applies only to v0.2.

### NamespaceResolutionFailed

**Applies to:** `ClusterProjection` only. (Namespaced `Projection` writes into its own `metadata.namespace`, which exists by definition — it cannot trigger this reason.)

The ClusterProjection's destination namespace set could not be resolved. One of three things happened:

- **`destination.namespaceSelector` is syntactically invalid.** `metav1.LabelSelectorAsSelector` rejected it. This is rare in practice because the CRD schema accepts any `LabelSelector`, but malformed `matchExpressions` (e.g. `operator: In` with an empty `values` list) trip it.
- **The `List` on namespaces failed.** Typically RBAC — the controller needs `list` on `namespaces` at cluster scope, which the upstream install grants. If you have narrowed RBAC, confirm namespace list permission is intact.
- **`destination.namespaces:` references namespaces that don't exist.** The controller refuses to fail open by silently creating into nothing; it surfaces the missing namespaces in the condition message and re-queues until they appear or the CR is updated.

An empty match set from `namespaceSelector` is **not** an error — if your selector matches zero namespaces, reconcile succeeds with nothing to write and you will not see this reason. You will see `Ready=True` with `status.namespacesWritten: 0`, which is its own form of "something's wrong" but not one this doc covers.

**Fix:** check the selector syntax with `kubectl get ns -l '<selector>'`; for the `namespaces` list form, `kubectl get ns <name>` each entry to confirm; verify the operator's `ClusterRole` allows `list` on `namespaces`.

### DestinationFetchFailed

**Applies to:** `Projection` and `ClusterProjection`.

For each target namespace (one for `Projection`, N for `ClusterProjection`), the controller first issues a `Get` to check whether a destination already exists (so it can decide between create and update, and verify ownership). That `Get` failed with an error other than `404 NotFound` (a 404 is the expected "not there yet" case and does not fail).

Typical causes:

- **RBAC.** The controller's `ServiceAccount` lacks `get` on the destination Kind in the target namespace. Same narrowed-RBAC failure mode as [`SourceFetchFailed`](#sourcefetchfailed) — the upstream install grants wildcard access, so this only shows up if you have narrowed RBAC (hand-edit or chart `supportedKinds`).
- **Apiserver transient error.** 5xx, timeout. Clears on requeue.

For ClusterProjection this can fire in some namespaces and not others; see [DestinationWriteFailed](#destinationwritefailed) for how the rollup reason works when failures differ per namespace.

**Fix:** Restore the destination Kind to the controller's `ClusterRole`, or wait for the transient to clear.

### DestinationConflict

**Applies to:** `Projection` and `ClusterProjection`.

The most important entry in this guide. The controller fetched an existing object at the destination coordinates and found that it is **not owned by this CR**. Ownership is established by an annotation that the controller stamps on every destination it creates:

| CRD                 | Ownership annotation                                                          |
| ------------------- | ----------------------------------------------------------------------------- |
| `Projection`        | `projection.sh/owned-by-projection: <projection-namespace>/<projection-name>` |
| `ClusterProjection` | `projection.sh/owned-by-cluster-projection: <clusterprojection-name>`         |

If that annotation is missing or points at a different CR, the controller refuses to update — the object belongs to something or someone else.

This is the invariant that makes `projection` safe to adopt alongside other tooling: we will never silently overwrite an object we didn't create. Conflict-safety is a design property, not a bug.

One cause: an object with the same name and Kind already exists at your chosen destination coordinates, and it was not created by this CR. Typical scenarios:

- Another tool (Helm, Kustomize, Kyverno `generate`, a different Projection or ClusterProjection) manages that name.
- A human created the object directly via `kubectl apply`.
- A previous Projection or ClusterProjection created the object, was deleted, and somebody or something stripped the ownership annotation before you created the new CR.

**Fix:** the resolution is a human decision, not a mechanical one.

- **Delete the pre-existing object** if it is genuinely stale and you want `projection` to take over. Do this knowingly — check `kubectl get <kind>/<name> -o yaml` first to confirm nothing important lives there.
- **Rename the destination.** Set `destination.name` on the CR to a name that doesn't collide.
- **Accept the conflict.** The CR stays at `Ready=False` and does nothing for that destination. This is a legitimate steady state — it means "another tool owns this name; defer to them." For ClusterProjection, the other target namespaces still reconcile normally.

Do **not** manually add the ownership annotation to an object you didn't create. That tells `projection` it can update and delete the object, which would then propagate changes from the source — almost certainly not what you want. The matching UID label (`projection.sh/owned-by-projection-uid` / `projection.sh/owned-by-cluster-projection-uid`) is a watch-filter hint only — copying it onto a stranger's object would not let the controller touch it (the annotation check still vetoes the write); see [Security § Label-trust caveat](security.md#label-trust-caveat-for-ensuredestwatch).

### DestinationCreateFailed

**Applies to:** `Projection` and `ClusterProjection`.

The destination does not yet exist (the preceding `Get` returned 404) and the `Create` call was rejected by the apiserver.

Typical causes:

- **Admission webhook rejection.** A validating or mutating webhook in the target namespace rejected the create. `ResourceQuota` violations surface here (e.g. "exceeded quota: pods"). So do policy engines: Kyverno `validate` policies, OPA Gatekeeper, network policy admission.
- **RBAC.** The controller lacks `create` on the destination Kind. With default RBAC this does not happen; with narrowed RBAC it does.
- **Field-level validation.** The destination object, after overlay application, violates CRD or built-in schema validation. This is rare because the source object itself was admitted at its own create time, but overlays that rewrite fields in invalid ways can trip it.

For ClusterProjection, this fires **per affected namespace** — expect one event per failing namespace, and a rolled-up `DestinationWritten=False` condition whose message lists the failing namespaces (truncated to about five entries with `... and N more`).

**Fix:** read the error message carefully — the apiserver is usually specific about what rejected the create and why. For webhook rejections, the webhook's name is in the error; investigate that policy. For RBAC, widen the `ClusterRole`.

### DestinationUpdateFailed

**Applies to:** `Projection` and `ClusterProjection`.

The destination already exists and is owned by this CR, but the `Update` call was rejected. Same failure surface as [`DestinationCreateFailed`](#destinationcreatefailed) but on the overwrite path, with two additional wrinkles specific to updates:

- **Conflict (409).** Another client modified the destination between our `Get` and our `Update`. The controller re-queues and the next reconcile reads the fresh resourceVersion. Self-clearing; if it persists, some other tool is writing to the destination in a tight loop.
- **Immutable field change.** The controller strips server-assigned fields (`clusterIP`, `volumeName`, `nodeName`) before building the destination and restores them from the existing object before Update, specifically to avoid this. If you see "field is immutable" in the error, it is a bug — the set of preserved fields (`droppedSpecFieldsByGVK` in the controller source) is likely missing an entry. Please [open an issue](https://github.com/projection-operator/projection/issues/new) with the Kind and the field name.

**Fix:** for webhook/RBAC errors, same remedies as [`DestinationCreateFailed`](#destinationcreatefailed). For 409 conflicts, wait one reconcile. For immutable-field errors, file a bug.

### DestinationWriteFailed

**Applies to:** `ClusterProjection` only.

A rollup reason emitted only by `ClusterProjection`. When the destination write fan-out hits failures in multiple namespaces and those failures have **different reasons**, the controller refuses to pick one arbitrarily and surfaces `DestinationWriteFailed` instead. If every failing namespace shares the same underlying reason (all `DestinationConflict`, say), that shared reason is used directly — you only see `DestinationWriteFailed` when the failures are heterogeneous.

The condition `message` lists the failing namespaces (truncated to about five entries: `failed namespaces: ns-a, ns-b, ns-c, ns-d, ns-e and N more`), but the actual causes are only in the per-namespace Events. This is deliberate: a single status message cannot faithfully encode three different failure modes.

**Fix:** drill into Events to see each namespace's actual reason.

```bash
# ClusterProjection events live in the `default` namespace.
kubectl -n default get events.events.k8s.io \
  --field-selector regarding.name=<clusterprojection-name>,regarding.kind=ClusterProjection \
  --sort-by=.lastTimestamp
```

You will see one Warning event per failed namespace, each carrying its own reason (`DestinationConflict`, `DestinationCreateFailed`, etc.) and the namespace in the event message. Resolve each one using the matching entry in this guide. The `status.namespacesFailed` counter on the ClusterProjection is the canonical "how many" — when it reaches zero, the rollup clears.

## Operational recovery

### Stuck CRD deletion or orphaned finalizers

**Applies to:** both CRDs.

`kubectl delete crd projections.projection.sh` (or `clusterprojections.projection.sh`) hangs after a `helm uninstall`, because one or more CRs still carry a finalizer and the controller — the only thing that can remove it — was uninstalled before they were cleaned up. The apiserver waits for every instance to terminate before deleting the CRD, and the instances cannot terminate without the controller.

There are two finalizer names — one per CRD:

| CRD                 | Finalizer                          |
| ------------------- | ---------------------------------- |
| `Projection`        | `projection.sh/finalizer`          |
| `ClusterProjection` | `projection.sh/cluster-finalizer`  |

If the controller is still running, the right thing to do is delete the CRs (or recreate the source so reconcile finishes its work) and let the controller's finalizer clean up. Only when the controller is gone — and you've accepted that no automated cleanup will happen — should you strip finalizers by hand.

> **Warning.** Stripping a finalizer skips the controller's destination cleanup. Any destination objects the CR was responsible for will be left in place. If you care about not leaving orphaned mirrored data behind, redeploy the controller first and let it finalize.

```bash
# Namespaced Projections
kubectl get projections.projection.sh -A -o name | \
  xargs -I {} kubectl patch {} --type=merge -p '{"metadata":{"finalizers":null}}'

# ClusterProjections
kubectl get clusterprojections.projection.sh -o name | \
  xargs -I {} kubectl patch {} --type=merge -p '{"metadata":{"finalizers":null}}'

# Then re-attempt the CRD delete.
kubectl delete crd projections.projection.sh clusterprojections.projection.sh
```

If a single instance is stuck (rather than the whole CRD), the same patch shape applies to that instance:

```bash
kubectl -n <ns> patch projection <name> --type=merge \
  -p '{"metadata":{"finalizers":null}}'

kubectl patch clusterprojection <name> --type=merge \
  -p '{"metadata":{"finalizers":null}}'
```

The full uninstall procedure (run-the-controller-first ordering) lives in [getting-started § Cleanup](getting-started.md#cleanup); this entry is the emergency exit when that procedure was skipped.
