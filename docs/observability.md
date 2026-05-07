# Observability

`projection` exposes three signals. They are complementary, and you will want to use all three in production.

The condition types, reasons, event reasons, and metric names listed below are part of the [v1 API stability promise](api-stability.md) — they will not be renamed or repurposed. Pre-v1.0 metric *labels* are a documented carve-out: see the [`kind` label note](#a-note-on-pre-v10-metric-label-stability) below.

The same condition vocabulary, event vocabulary, and metric set apply to both `Projection` (namespaced, single-target) and `ClusterProjection` (cluster-scoped, fan-out). Differences between the two are called out inline; everything else is shared.

## 1. Status conditions

Every `Projection` and `ClusterProjection` carries three conditions. They are the primary source of truth — they're what `kubectl wait` and `kubectl get` surface.

| Type                 | Meaning                                                                  |
| -------------------- | ------------------------------------------------------------------------ |
| `SourceResolved`     | Did the controller find the source object?                               |
| `DestinationWritten` | Was the destination created or updated (or already in sync)?             |
| `Ready`              | Aggregate; `True` iff both of the above are `True`.                      |

For `ClusterProjection`, `DestinationWritten` is a **rollup** across every target namespace. If any target namespace fails, the condition is `False` with a message that names the failing namespaces (truncated to about five entries with `... and N more` when more fail). The per-namespace counts are also exposed as `status.namespacesWritten` and `status.namespacesFailed` for use in alerts and dashboards.

### Querying

Minimal — all conditions on one Projection:

```bash
kubectl -n <ns> get projection <name> \
  -o jsonpath='{range .status.conditions[*]}{.type}={.status} reason={.reason} msg={.message}{"\n"}{end}'
```

For ClusterProjection (cluster-scoped, no namespace flag):

```bash
kubectl get clusterprojection <name> \
  -o jsonpath='{range .status.conditions[*]}{.type}={.status} reason={.reason} msg={.message}{"\n"}{end}'
```

Just `Ready`:

```bash
kubectl -n <ns> get projection <name> \
  -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}'
```

Cluster-wide, tabular (both kinds at once):

```bash
{
  kubectl get projections -A -o json
  kubectl get clusterprojections -o json
} | jq -s '.[].items[] | [
    .kind,
    (.metadata.namespace // "-"),
    .metadata.name,
    (.status.conditions[]? | select(.type=="Ready") | .status + " (" + .reason + ")")
  ] | @tsv' -r
```

CI-friendly waits:

```bash
kubectl -n <ns> wait --for=condition=Ready projection/<name> --timeout=60s
kubectl wait --for=condition=Ready clusterprojection/<name> --timeout=60s
```

For ClusterProjection rollup, also surface the partial-failure counters:

```bash
kubectl get clusterprojection <name> \
  -o jsonpath='{.status.namespacesWritten}/{.status.namespacesWritten + .status.namespacesFailed} written'
```

### Reasons you'll see

Each failure-mode reason below links to its entry in the [troubleshooting guide](troubleshooting.md).

| Condition            | Status  | Reason                                                                                                  |
| -------------------- | ------- | ------------------------------------------------------------------------------------------------------- |
| `SourceResolved`     | True    | `Resolved`                                                                                              |
| `SourceResolved`     | False   | [`SourceResolutionFailed`](troubleshooting.md#sourceresolutionfailed) (RESTMapper can't find Kind), [`SourceFetchFailed`](troubleshooting.md#sourcefetchfailed) (object not found / RBAC), [`SourceDeleted`](troubleshooting.md#sourcedeleted), [`SourceOptedOut`](troubleshooting.md#sourceoptedout-sourcenotprojectable) / [`SourceNotProjectable`](troubleshooting.md#sourceoptedout-sourcenotprojectable) |
| `DestinationWritten` | True    | `Projected`                                                                                             |
| `DestinationWritten` | False   | [`InvalidSpec`](troubleshooting.md#invalidspec), [`NamespaceResolutionFailed`](troubleshooting.md#namespaceresolutionfailed) *(ClusterProjection only)*, [`DestinationCreateFailed`](troubleshooting.md#destinationcreatefailed), [`DestinationUpdateFailed`](troubleshooting.md#destinationupdatefailed), [`DestinationFetchFailed`](troubleshooting.md#destinationfetchfailed), [`DestinationConflict`](troubleshooting.md#destinationconflict), [`DestinationWriteFailed`](troubleshooting.md#destinationwritefailed) *(ClusterProjection only — heterogeneous-failure rollup)* |
| `DestinationWritten` | Unknown | [`SourceNotResolved`](troubleshooting.md#sourcenotresolved) (never attempted because source step failed)                                        |
| `Ready`              | True    | `Projected`                                                                                             |
| `Ready`              | False   | Mirrors whichever upstream condition failed (same reason & message)                                     |

## 2. Kubernetes Events

The controller emits Events on every state transition. They are the best way to see *history* — a status condition only shows the current state, but Events show what happened and when. Events are written through the `events.k8s.io/v1` API and carry both a `reason` (categorical tag) and an `action` (the controller verb that produced the event).

For ClusterProjection, partial failures are emitted as **per-namespace events**: a `DestinationCreateFailed` event for namespace `tenant-a`, another for `tenant-b`, etc. The rolled-up `DestinationWritten=False` condition is what `kubectl describe clusterprojection` highlights, but the per-namespace causes only live on the events.

| Event reason              | Type    | Action     | Trigger                                                                           |
| ------------------------- | ------- | ---------- | --------------------------------------------------------------------------------- |
| `Projected`               | Normal  | `Create`   | Destination was created.                                                          |
| `Updated`                 | Normal  | `Update`   | Destination existed and was updated to match source (after `needsUpdate` diff).   |
| `DestinationDeleted`      | Normal  | `Delete`   | Destination was deleted as part of CR deletion or a stale-namespace cleanup.      |
| `DestinationLeftAlone`    | Normal  | `Delete`   | The CR was deleted but the destination no longer carried the ownership annotation.|
| `StaleDestinationDeleted` | Normal  | `Delete`   | A selector-matched destination was removed after its namespace stopped matching (ClusterProjection). |
| `DestinationConflict`     | Warning | `Validate` | Destination existed and was not owned by this Projection or ClusterProjection.    |
| `DestinationCreateFailed` | Warning | `Create`   | Create on destination failed (per-namespace for ClusterProjection).               |
| `DestinationUpdateFailed` | Warning | `Update`   | Update on destination failed (per-namespace for ClusterProjection).               |
| `DestinationFetchFailed`  | Warning | `Get`      | Get on an existing destination failed during reconcile.                           |
| `DestinationWriteFailed`  | Warning | `Write`    | ClusterProjection rollup — multiple target namespaces failed with different reasons. |
| `NamespaceResolutionFailed` | Warning | `Resolve` | ClusterProjection's `namespaceSelector` failed to resolve, or `namespaces:` references namespaces that don't exist. |
| `SourceFetchFailed`       | Warning | `Get`      | Dynamic client `Get` on the source returned an error.                             |
| `SourceResolutionFailed`  | Warning | `Resolve`  | RESTMapper couldn't resolve the source `{group, version, kind}` triple.           |
| `SourceOptedOut` / `SourceNotProjectable` | Warning | `Validate` | Source is missing the `projection.sh/projectable=true` annotation (allowlist mode) or explicitly sets it to `false`. |
| `InvalidSpec`             | Warning | `Validate` | Admission rejected the spec — e.g. SourceRef with empty `group` AND empty `version`, or ClusterProjection.destination with both / neither of `namespaces` and `namespaceSelector` set. See [troubleshooting](troubleshooting.md#invalidspec). |

### Querying

All events for one Projection (note: the legacy `kubectl get events` reads the `core/v1` Event API and will *not* surface these — `projection` writes through `events.k8s.io/v1`, so the resource selector matters):

```bash
kubectl -n <ns> get events.events.k8s.io \
  --field-selector regarding.name=<projection-name>,regarding.kind=Projection \
  --sort-by=.lastTimestamp
```

For ClusterProjection (events on a cluster-scoped object live in the `default` namespace, which is the apiserver's behavior for cluster-scoped object events):

```bash
kubectl -n default get events.events.k8s.io \
  --field-selector regarding.name=<clusterprojection-name>,regarding.kind=ClusterProjection \
  --sort-by=.lastTimestamp
```

All Warnings cluster-wide for either kind (use this in an on-call runbook):

```bash
kubectl get events.events.k8s.io -A --field-selector type=Warning \
  --sort-by=.lastTimestamp \
  | grep -E '(Projection|ClusterProjection)'
```

The `action` is visible via `-o wide` or the full YAML (`-o yaml`), and is a stable verb you can switch on from automation.

## 3. Prometheus metrics

The controller registers three projection-specific metrics on top of the standard controller-runtime ones.

### `projection_reconcile_total`

`CounterVec` labeled by `kind` and `result`:

```
projection_reconcile_total{kind="Projection",result="success"}
projection_reconcile_total{kind="Projection",result="conflict"}
projection_reconcile_total{kind="Projection",result="source_error"}
projection_reconcile_total{kind="Projection",result="destination_error"}
projection_reconcile_total{kind="ClusterProjection",result="success"}
projection_reconcile_total{kind="ClusterProjection",result="conflict"}
projection_reconcile_total{kind="ClusterProjection",result="source_error"}
projection_reconcile_total{kind="ClusterProjection",result="destination_error"}
```

The `kind` label is new in v0.3.0 and lets you split namespaced vs cluster-scoped reconcile traffic in dashboards. A representative query:

```promql
sum by (kind, result) (rate(projection_reconcile_total[5m]))
```

For ClusterProjection, a single reconcile may write into many destination namespaces; a partial failure (some namespaces fail, others succeed) increments the counter as a single `destination_error` for that reconcile, not once per failing namespace. The per-namespace breakdown lives in events, not metrics — see [Partial failures on ClusterProjection](#partial-failures-on-clusterprojection) below.

#### A note on pre-v1.0 metric label stability

The [API stability promise](api-stability.md) covers metric *names* and existing label *values*; new labels can be added. The v0.3.0 `kind` label is one such addition — it does not break any pre-existing PromQL because the v0.2 metric had no `kind` label and any aggregation that didn't mention `kind` continues to work. Pre-v1.0 dashboards should expect this to keep happening: minor releases may add new labels (with new label values appearing on existing metrics), but will not rename or remove existing ones.

If your dashboard relied on the pre-v0.3.0 single-line series `projection_reconcile_total{result="success"}`, that series is still emitted — just with `kind="Projection"` filtering the namespaced reconciler's contribution and `kind="ClusterProjection"` filtering the cluster reconciler's. A query like `sum by (result) (rate(projection_reconcile_total[5m]))` aggregates over both kinds and is the drop-in replacement.

### `projection_watched_gvks`

`Gauge` (no labels). Tracks the number of distinct **source** GVKs the controller currently has dynamic watches registered for. Incremented when a Projection or ClusterProjection references a previously-unseen source GVK.

In v0.3.0 this gauge is shared across the namespaced and cluster reconcilers — it is the union of source GVKs seen by either, deduplicated. (Source watches in v0.3.0 are keyed on `group/kind/namespace/name` with the version dropped from the watch key; two CRs that pin different versions of the same source share one watch.) Use this for capacity planning at scale: each watch has a memory and apiserver cost, so a steady-state value much higher than the number of distinct source Kinds your CRs actually reference can indicate watch leakage.

```
projection_watched_gvks
```

### `projection_watched_dest_gvks`

`Gauge` (no labels). New in v0.3.0. Tracks the number of distinct **destination** GVKs the controller currently has watches registered for via `ensureDestWatch`.

`ensureDestWatch` (see [Concepts § 7](concepts.md#7-watches)) registers a label-filtered watch on each destination GVK so that a manual `kubectl delete` of a destination triggers an immediate reconcile rather than waiting for the next requeue. The watch source is `PartialObjectMetadata` (cheap deserialization — only `metadata` is decoded) and the filter is keyed on the per-CR-kind UID label, so the watch only fires for objects that already carry an ownership UID label.

```
projection_watched_dest_gvks
```

Patterns that suggest a problem:

- **Sudden jump** (e.g. `+50`) — likely someone created Projections or ClusterProjections targeting many distinct Kinds at once. Confirm against `kubectl get projections -A -o json` plus `kubectl get clusterprojections -o json` and decide whether that's expected.
- **Gauge drops to 0 with active CRs** — suggests a watch-handle leak (the watches went away but the controller didn't notice). The controller treats this as a reconcile-bug-class issue; please file a bug.
- **`projection_watched_dest_gvks` consistently larger than `projection_watched_gvks`** — possible if the same source GVK projects into multiple distinct destination Kinds via overlay (rare), or if cleanup of stale destination watches lags. Worth investigating but not necessarily wrong.

Plus the usual controller-runtime metrics (`controller_runtime_reconcile_total`, `workqueue_depth`, etc.). Both reconcilers register against the shared controller-runtime registry.

### Scraping

The metrics endpoint is **secure by default** — authn/authz-filtered, TLS-wrapped, on `:8443/metrics`. If you're running the supplied Helm chart or install manifest, a `ClusterRole` named `projection-metrics-reader` is provisioned for you to bind to your Prometheus service account:

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: projection-metrics-reader-prom
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: projection-metrics-reader
subjects:
  - kind: ServiceAccount
    name: prometheus-k8s           # or your scraper's SA
    namespace: monitoring
```

If you use the `PrometheusOperator`, the chart ships an opt-in `ServiceMonitor` (set `serviceMonitor.enabled=true`) that scrapes `:8443/metrics` over HTTPS with the operator's serving cert.

Pass `--metrics-bind-address=0` at startup to disable the endpoint entirely. Pass `--metrics-secure=false` to downgrade to plain HTTP on `:8080` (not recommended).

### Sample alerts

```yaml
# Any reconcile-side error in the last 5 min, broken out by kind so you can
# tell namespaced vs cluster reconciler problems apart at-a-glance.
- alert: ProjectionReconcileErrors
  expr: |
    sum by (kind, result) (rate(projection_reconcile_total{result=~".*_error"}[5m])) > 0
  for: 5m
  labels: { severity: warning }
  annotations:
    summary: "{{ $labels.kind }} controller reporting {{ $labels.result }} at {{ $value | humanize }}/s"

# Destination conflicts — probably someone else owns the destination.
# Conflicts on ClusterProjection are noisier (they fan out across namespaces),
# so split by kind to avoid one ClusterProjection drowning the namespaced signal.
- alert: ProjectionConflicts
  expr: |
    sum by (kind) (rate(projection_reconcile_total{result="conflict"}[15m])) > 0
  for: 15m
  labels: { severity: warning }
  annotations:
    summary: "Persistent DestinationConflict on {{ $labels.kind }} — a CR is being blocked by an unowned destination"

# Success should dominate — ratio alert per kind. Aggregating both kinds together
# can hide a chronic ClusterProjection failure behind healthy namespaced traffic.
- alert: ProjectionSuccessRateLow
  expr: |
    sum by (kind) (rate(projection_reconcile_total{result="success"}[15m]))
      /
    sum by (kind) (rate(projection_reconcile_total[15m])) < 0.95
  for: 15m
  labels: { severity: warning }
  annotations:
    summary: "{{ $labels.kind }} reconcile success rate below 95% (15m)"
```

#### Partial failures on ClusterProjection

`projection_reconcile_total` only tells you that a reconcile failed — it doesn't tell you *how many* of a ClusterProjection's destination namespaces failed. The canonical signal for partial failure is the `status.namespacesFailed` field on the CR, which is exposed in the controller's status but not in metrics. The recommended pattern is to alert off the *condition* (via `kubectl` automation, an admission policy that emits a metric, or a kube-state-metrics-style exporter) rather than off `projection_reconcile_total`.

A pragmatic rule of thumb: a single failed reconcile is noise (transient apiserver hiccup, namespace just being created), but a ClusterProjection sitting at `DestinationWritten=False` for more than five to fifteen minutes is worth a page. If you have `kube-state-metrics` configured to export CR status, the query shape is:

```promql
# Pseudo-PromQL — replace with your kube-state-metrics CR-status conventions.
max by (name) (kube_clusterprojection_status_namespacesfailed) > 0
```

The per-namespace causes for a partial failure live in `events.events.k8s.io` (`DestinationCreateFailed`, `DestinationUpdateFailed`, `DestinationConflict` — one per affected namespace). When the alert fires, drill in with the [event query](#querying) above.

## 4. Logs

The controller's structured logs distinguish the two reconcilers via the standard controller-runtime `controller=` field on every log line:

- `controller=projection` — the namespaced reconciler.
- `controller=clusterprojection` — the cluster-scoped reconciler.

A `kubectl logs` filter shape that's useful in practice:

```bash
# Just ClusterProjection reconcile activity, with object coordinates.
kubectl -n projection-system logs deploy/projection-controller-manager \
  | grep -E 'controller=clusterprojection'
```

Both reconcilers share the same shared-source-watch and shared-destination-watch infrastructure, so source-watch / dest-watch log lines (e.g. `ensureDestWatch registering watch on <gvk>`) come from the controller-manager rather than either reconciler specifically.

## 5. Operational tuning

Two CLI flags (and their Helm chart equivalents) control reconciliation and leader-election timing. Defaults are conservative cluster-scale values; tune only when you've observed a specific pain point.

### `--requeue-interval` / `requeueInterval` (default: `30s`)

How long the controller waits between reconciles of the same Projection or ClusterProjection. Tune when:

- **Longer (e.g. `2m`)** if your cluster has hundreds of CRs that flap on a flaky upstream API and you're seeing apiserver load from repeated reconciles. The trade-off is slower recovery when a transient failure clears.
- **Shorter (e.g. `5s`)** in dev clusters where you want fast feedback as you iterate on source objects. Don't go below the controller-runtime minimum (~1s) — the reconciler will busy-loop.

### `--leader-election-lease-duration` / `leaderElection.leaseDuration` (default: `15s`)

Only relevant when `replicaCount > 1`. How long the leader holds the lease before a standby may take over on crash.

- **Longer (e.g. `30s`)** reduces lease-renewal traffic against the apiserver — useful on large fleets where many operators each renew their own leases.
- **Shorter (e.g. `10s`)** speeds up failover at the cost of more apiserver churn. Must remain strictly greater than controller-runtime's 10s renew-deadline default — go below and leader election misbehaves.

### `--selector-write-concurrency` / `selectorWriteConcurrency` (default: `16`)

Selector-based ClusterProjections write destinations across matching namespaces in parallel, capped per ClusterProjection at this value. Each worker issues a Get plus optionally a Create or Update against the apiserver; HTTP/2 multiplexing in client-go shares a single connection across the workers, so the flag caps parallelism rather than connections. The cap exists so a ClusterProjection matching thousands of namespaces can't DoS the apiserver or blow out controller memory with goroutines. (The flag does not apply to namespaced `Projection`, which is single-target only.)

- **Raise it (e.g. `64`, `128`)** when a single ClusterProjection matches many hundreds or thousands of namespaces and reconcile latency on that CR is the constraint. The ceiling is set by your kube-apiserver's APF priority-level budget for the controller's identity, not by the controller — values above ~256 are rare enough that the controller logs a warning so you can confirm the choice was deliberate.
- **Lower it (e.g. `4`, `8`)** on apiserver-constrained clusters or when sharing a priority-level budget with other heavy controllers. The trade-off is slower per-ClusterProjection reconcile time at large fan-out.

Must be strictly greater than zero — the controller refuses to start with `--selector-write-concurrency=0` (the value would produce a zero-capacity worker semaphore that deadlocks on the first send).

## One-shot snapshot

The repo ships [`hack/observe.sh`](https://github.com/projection-operator/projection/blob/main/hack/observe.sh) as a copy-paste debugging helper. It dumps:

- Cluster info and nodes.
- Operator pod and the last 80 lines of controller logs.
- `kubectl get projections -A` and `kubectl get clusterprojections`.
- Per-CR condition summary (both kinds).
- Recent events in `projection-system`.
- Optionally, full YAML of one Projection or ClusterProjection plus its source and destination(s).

```bash
# Overall snapshot
./hack/observe.sh

# Deep dive on a single namespaced Projection
./hack/observe.sh app-config-to-tenant-a default

# Deep dive on a single ClusterProjection (omit the namespace argument)
./hack/observe.sh shared-app-config-fanout
```

It honors `KUBECTL_CONTEXT` (default `kind-projection-dev`) so you can point it at any kubeconfig context.
