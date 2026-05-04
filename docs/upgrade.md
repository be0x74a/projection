# Upgrading from v0.1.0-alpha to v0.2.0

This guide is for clusters already running `projection` v0.1.0-alpha.1. If you're installing for the first time, use the [getting-started guide](getting-started.md) instead.

v0.2.0 introduces three breaking changes that require migration action. See the [CHANGELOG](https://github.com/projection-operator/projection/blob/main/CHANGELOG.md) for the full list of additive features shipped in the same release.

1. **Source opt-in is now required by default.** The new `allowlist` source-mode ignores sources that don't carry `projection.sh/projectable="true"`. The migration script below annotates your existing sources; alternatively you can opt into permissive mode for v0.1-compatible behavior.
2. **Kubernetes Events moved to `events.k8s.io/v1`.** Any automation using `kubectl get events --field-selector involvedObject.name=...` against Projections must switch to the new API and field names.
3. **Source deletion now cleans up destinations.** Previously, deleting a source left destinations orphaned (the Projection reported `SourceFetchFailed` indefinitely). In v0.2 the controller removes every owned destination when the source goes away. No migration is needed — but if your runbook depended on the old behavior, adjust. Note that **existing orphans from v0.1 are not retroactively cleaned up** — they're cleaned up the next time their source returns 404, which for already-deleted sources means never. If you have known orphans, delete them by hand or recreate-and-redelete the source to trigger cleanup.

Estimated time for the migration: under five minutes, zero-downtime.

!!! warning "Order matters"
    Run the annotation script **before** `helm upgrade`. If you upgrade first, v0.2 reconciles will flap unannotated sources to `SourceNotProjectable` and garbage-collect their destinations until the annotation lands.

!!! info "Upgrade-window risk"
    The migration script annotates the sources referenced by the Projections that exist at the moment it runs. A Projection created **after** the script's `--apply` run but **before** `helm upgrade` may reference a source the script never saw — that source will be missing the annotation under v0.2 and the Projection will surface `SourceNotProjectable` until you re-annotate or re-run the script. Practical mitigation: keep the window short (run the two commands back-to-back), or freeze new Projection creates for the duration of the upgrade.

## 1. Annotate your sources (recommended)

Download and run the migration script in dry-run mode to see the plan:

```bash
curl -O https://raw.githubusercontent.com/projection-operator/projection/main/hack/migrate-to-v1.sh
chmod +x migrate-to-v1.sh
./migrate-to-v1.sh
# Despite the file name, this script is the v0.1→v0.2 cutover helper —
# it predates the v0.2 versioning decision. It will be renamed in a
# future release; the migration steps it performs are unchanged.
```

The script prints a table of planned annotations and counts of skipped sources (already annotated, opted out via `projectable="false"`, or missing from the cluster):

```
NAMESPACE    APIVERSION     KIND           NAME                           ACTION
platform     v1             ConfigMap      app-config                     annotate (projectable=true)
tenant-a     v1             Secret         db-creds                       skip (already annotated)

Plan: 1 to annotate, 1 already annotated, 0 opted out, 0 not found.
Re-run with --apply to execute.
```

Review the plan; when it matches your expectations, apply:

```bash
./migrate-to-v1.sh --apply
```

The script is idempotent — running it twice leaves the cluster in the same state.

Then upgrade:

```bash
helm upgrade projection oci://ghcr.io/projection-operator/charts/projection \
    --version 0.2.0 \
    -n projection-system
```

Verify the operator deployment rolls out and your Projections stay `Ready=True`:

```bash
kubectl -n projection-system rollout status deploy/projection --timeout=180s
kubectl get projections -A
```

## 2. …or opt into permissive mode (escape hatch)

If annotating every source is infeasible — for example in clusters where source objects are created by workloads you don't control — set `sourceMode: permissive` in your values and skip the migration script entirely:

```bash
helm upgrade projection oci://ghcr.io/projection-operator/charts/projection \
    --version 0.2.0 \
    -n projection-system \
    --set sourceMode=permissive
```

In permissive mode, any source is projectable unless explicitly opted out with `projection.sh/projectable="false"` on the source object. Source-owner vetoes are honored in both modes.

**Trade-offs:**

- **Pros:** zero migration work; behavior identical to v0.1 for Projections.
- **Cons:** any source in the cluster becomes mirror-able by default. In multi-tenant clusters where different teams create Projections that target each other's namespaces, allowlist mode is the safer default — source owners must explicitly consent to being mirrored.

The switch is idempotent: you can flip back to allowlist later by running the migration script and removing the values flag. No data migration is required either way.

## 3. Update your event queries

The controller now writes Events through `events.k8s.io/v1` instead of the legacy `core/v1` API. Event `reason` strings are unchanged; field names and the API path are not.

**Before (v0.1.0-alpha):**

```bash
kubectl get events \
    --field-selector involvedObject.name=<projection>,involvedObject.kind=Projection
```

**After (v0.2.0):**

```bash
kubectl get events.events.k8s.io \
    --field-selector regarding.name=<projection>,regarding.kind=Projection
```

Scripts, dashboards, or alerts that query Projection events need this rewrite. See [observability.md](observability.md#2-kubernetes-events) for the canonical query examples.

## Rolling back

If something goes wrong, `helm rollback projection` returns the cluster to the previous chart revision. No custom rollback procedure is needed for the annotation migration — it's additive and doesn't require reverting, since v0.1.0-alpha ignores unknown annotations. CRD schema changes introduced by v0.2 are not reverted by `helm rollback` (Helm does not touch CRDs on upgrade or rollback); this is safe because v0.1 ignores fields it does not recognise.

**One rollback gotcha worth knowing.** Any Projection created or edited under v0.2 that uses `spec.destination.namespaceSelector` (the new selector-based fan-out field) will have that field silently stripped on rollback — the v0.1 apiserver schema doesn't know it exists. The Projection becomes a no-op (no destination, no fan-out). Rolling back to v0.1 is therefore safe for v0.1-shaped Projections, but not for ones that adopted v0.2 features. If you're piloting selector fan-out, be ready to recreate those Projections by hand if you roll back.

The new `supportedKinds` Helm value defaults to `[{apiGroup: "*", resources: ["*"]}]` on upgrade — preserving the pre-v0.2 cluster-admin-equivalent ClusterRole — so an existing install picks up zero RBAC change automatically. Narrowing it is opt-in and unrelated to the version bump (see [security.md](security.md#1-narrow-the-controllers-rbac-to-the-kinds-you-actually-mirror)).

## Post-upgrade verification

After the operator deployment rolls out:

```bash
# Confirm every Projection is Ready=True
kubectl get projections -A -o json | jq -r '.items[] |
    select(.status.conditions[]? | select(.type=="Ready" and .status!="True")) |
    "\(.metadata.namespace)/\(.metadata.name)"'
```

If that query returns any names, inspect them with the [troubleshooting guide](troubleshooting.md). Expected reasons immediately after upgrade: `SourceNotProjectable` on any source missed by the migration script (rare — indicates a Projection was added between the dry-run and `--apply`).

Full reference for conditions and events lives in [observability.md](observability.md).
