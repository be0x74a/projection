# Observability

`projection` exposes three signals. They are complementary, and you will want to use all three in production.

The condition types, reasons, event reasons, and metric names listed below are part of the [v1 API stability promise](api-stability.md) — they will not be renamed or repurposed.

## 1. Status conditions

Every `Projection` carries three conditions. They are the primary source of truth — they're what `kubectl wait` and `kubectl get` surface.

| Type                 | Meaning                                                                  |
| -------------------- | ------------------------------------------------------------------------ |
| `SourceResolved`     | Did the controller find the source object?                               |
| `DestinationWritten` | Was the destination created or updated (or already in sync)?             |
| `Ready`              | Aggregate; `True` iff both of the above are `True`.                      |

### Querying

Minimal — all conditions on one Projection:

```bash
kubectl -n <ns> get projection <name> \
  -o jsonpath='{range .status.conditions[*]}{.type}={.status} reason={.reason} msg={.message}{"\n"}{end}'
```

Just `Ready`:

```bash
kubectl -n <ns> get projection <name> \
  -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}'
```

Cluster-wide, tabular:

```bash
kubectl get projections -A -o json \
  | jq -r '.items[] | [
      .metadata.namespace,
      .metadata.name,
      (.status.conditions[]? | select(.type=="Ready") | .status + " (" + .reason + ")")
    ] | @tsv'
```

CI-friendly wait (returns 0 once the mirror has taken):

```bash
kubectl -n <ns> wait --for=condition=Ready projection/<name> --timeout=60s
```

### Reasons you'll see

Each failure-mode reason below links to its entry in the [troubleshooting guide](troubleshooting.md).

| Condition            | Status  | Reason                                                                                                  |
| -------------------- | ------- | ------------------------------------------------------------------------------------------------------- |
| `SourceResolved`     | True    | `Resolved`                                                                                              |
| `SourceResolved`     | False   | [`SourceResolutionFailed`](troubleshooting.md#sourceresolutionfailed) (RESTMapper can't find Kind), [`SourceFetchFailed`](troubleshooting.md#sourcefetchfailed) (object not found / RBAC), [`SourceDeleted`](troubleshooting.md#sourcedeleted), [`SourceOptedOut`](troubleshooting.md#sourceoptedout-sourcenotprojectable) / [`SourceNotProjectable`](troubleshooting.md#sourceoptedout-sourcenotprojectable) |
| `DestinationWritten` | True    | `Projected`                                                                                             |
| `DestinationWritten` | False   | [`InvalidSpec`](troubleshooting.md#invalidspec), [`NamespaceResolutionFailed`](troubleshooting.md#namespaceresolutionfailed), [`DestinationCreateFailed`](troubleshooting.md#destinationcreatefailed), [`DestinationUpdateFailed`](troubleshooting.md#destinationupdatefailed), [`DestinationFetchFailed`](troubleshooting.md#destinationfetchfailed), [`DestinationConflict`](troubleshooting.md#destinationconflict), [`DestinationWriteFailed`](troubleshooting.md#destinationwritefailed) |
| `DestinationWritten` | Unknown | [`SourceNotResolved`](troubleshooting.md#sourcenotresolved) (never attempted because source step failed)                                        |
| `Ready`              | True    | `Projected`                                                                                             |
| `Ready`              | False   | Mirrors whichever upstream condition failed (same reason & message)                                     |

## 2. Kubernetes Events

The controller emits Events on every state transition. They are the best way to see *history* — a status condition only shows the current state, but Events show what happened and when. Events are written through the `events.k8s.io/v1` API and carry both a `reason` (categorical tag) and an `action` (the controller verb that produced the event).

| Event reason              | Type    | Action     | Trigger                                                                           |
| ------------------------- | ------- | ---------- | --------------------------------------------------------------------------------- |
| `Projected`               | Normal  | `Create`   | Destination was created.                                                          |
| `Updated`                 | Normal  | `Update`   | Destination existed and was updated to match source (after `needsUpdate` diff).   |
| `DestinationDeleted`      | Normal  | `Delete`   | Destination was deleted as part of `Projection` deletion or a stale-namespace cleanup. |
| `DestinationLeftAlone`    | Normal  | `Delete`   | `Projection` was deleted but destination no longer had the ownership annotation.  |
| `StaleDestinationDeleted` | Normal  | `Delete`   | A selector-matched destination was removed after its namespace stopped matching.  |
| `DestinationConflict`     | Warning | `Validate` | Destination existed and was not owned by this `Projection`.                       |
| `DestinationCreateFailed` | Warning | `Create`   | Create on destination failed.                                                     |
| `DestinationUpdateFailed` | Warning | `Update`   | Update on destination failed.                                                     |
| `DestinationFetchFailed`  | Warning | `Get`      | Get on an existing destination failed during reconcile.                           |
| `DestinationWriteFailed`  | Warning | `Write`    | Rollup reason for selector-based Projections where multiple namespaces failed with different reasons. |
| `NamespaceResolutionFailed` | Warning | `Resolve` | `destination.namespaceSelector` failed to resolve to a namespace list.           |
| `SourceFetchFailed`       | Warning | `Get`      | Dynamic client `Get` on the source returned an error.                             |
| `SourceResolutionFailed`  | Warning | `Resolve`  | RESTMapper couldn't resolve `apiVersion`/`kind`.                                  |
| `SourceOptedOut` / `SourceNotProjectable` | Warning | `Validate` | Source is missing the `projection.sh/projectable=true` annotation (allowlist mode) or explicitly sets it to `false`. |
| `InvalidSpec`             | Warning | `Validate` | `destination.namespace` and `destination.namespaceSelector` both set.             |

### Querying

All events for one Projection (note: the legacy `kubectl get events` reads the `core/v1` Event API and will *not* surface these — `projection` writes through `events.k8s.io/v1`, so the resource selector matters):

```bash
kubectl -n <ns> get events.events.k8s.io \
  --field-selector regarding.name=<projection-name>,regarding.kind=Projection \
  --sort-by=.lastTimestamp
```

All Warnings cluster-wide (use this in an on-call runbook):

```bash
kubectl get events.events.k8s.io -A --field-selector type=Warning \
  --sort-by=.lastTimestamp \
  | grep Projection
```

The `action` is visible via `-o wide` or the full YAML (`-o yaml`), and is a stable verb you can switch on from automation.

## 3. Prometheus metrics

The controller registers two projection-specific metrics on top of the standard controller-runtime ones.

`projection_reconcile_total` — `CounterVec` labeled by `result`:

```
projection_reconcile_total{result="success"}
projection_reconcile_total{result="conflict"}
projection_reconcile_total{result="source_error"}
projection_reconcile_total{result="destination_error"}
```

`projection_watched_gvks` — `Gauge` (no labels). Tracks the number of distinct source GVKs the controller currently has dynamic watches registered for. Incremented when a Projection references a previously-unseen source GVK. Useful for capacity planning at scale: each watch has a memory and apiserver cost, so a steady-state value much higher than the number of distinct source Kinds your Projections actually reference can indicate watch leakage.

```
projection_watched_gvks
```

Plus the usual controller-runtime metrics (`controller_runtime_reconcile_total`, `workqueue_depth`, etc.).

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
# Any projection-side error in the last 5 min
- alert: ProjectionReconcileErrors
  expr: sum by (result) (rate(projection_reconcile_total{result=~".*_error"}[5m])) > 0
  for: 5m
  labels: { severity: warning }
  annotations:
    summary: "projection controller reporting {{ $labels.result }} at {{ $value | humanize }}/s"

# Destination conflicts — probably someone else owns the destination
- alert: ProjectionConflicts
  expr: rate(projection_reconcile_total{result="conflict"}[15m]) > 0
  for: 15m
  labels: { severity: warning }
  annotations:
    summary: "Persistent DestinationConflict — a Projection is being blocked by an unowned destination"

# Success should dominate — ratio alert
- alert: ProjectionSuccessRateLow
  expr: |
    sum(rate(projection_reconcile_total{result="success"}[15m]))
      /
    sum(rate(projection_reconcile_total[15m])) < 0.95
  for: 15m
  labels: { severity: warning }
  annotations:
    summary: "projection reconcile success rate below 95% (15m)"
```

## 4. Operational tuning

Two CLI flags (and their Helm chart equivalents) control reconciliation and leader-election timing. Defaults are conservative cluster-scale values; tune only when you've observed a specific pain point.

### `--requeue-interval` / `requeueInterval` (default: `30s`)

How long the controller waits between reconciles of the same Projection. Tune when:

- **Longer (e.g. `2m`)** if your cluster has hundreds of Projections that flap on a flaky upstream API and you're seeing apiserver load from repeated reconciles. The trade-off is slower recovery when a transient failure clears.
- **Shorter (e.g. `5s`)** in dev clusters where you want fast feedback as you iterate on source objects. Don't go below the controller-runtime minimum (~1s) — the reconciler will busy-loop.

### `--leader-election-lease-duration` / `leaderElection.leaseDuration` (default: `15s`)

Only relevant when `replicaCount > 1`. How long the leader holds the lease before a standby may take over on crash.

- **Longer (e.g. `30s`)** reduces lease-renewal traffic against the apiserver — useful on large fleets where many operators each renew their own leases.
- **Shorter (e.g. `10s`)** speeds up failover at the cost of more apiserver churn. Must remain strictly greater than controller-runtime's 10s renew-deadline default — go below and leader election misbehaves.

### `--selector-write-concurrency` / `selectorWriteConcurrency` (default: `16`)

Selector-based Projections write destinations across matching namespaces in parallel, capped per Projection at this value. Each worker issues a Get plus optionally a Create or Update against the apiserver; HTTP/2 multiplexing in client-go shares a single connection across the workers, so the flag caps parallelism rather than connections. The cap exists so a Projection matching thousands of namespaces can't DoS the apiserver or blow out controller memory with goroutines.

- **Raise it (e.g. `64`, `128`)** when a single Projection matches many hundreds or thousands of namespaces and reconcile latency on that Projection is the constraint. The ceiling is set by your kube-apiserver's APF priority-level budget for the controller's identity, not by the controller — values above ~256 are rare enough that the controller logs a warning so you can confirm the choice was deliberate.
- **Lower it (e.g. `4`, `8`)** on apiserver-constrained clusters or when sharing a priority-level budget with other heavy controllers. The trade-off is slower per-Projection reconcile time at large fan-out.

Must be strictly greater than zero — the controller refuses to start with `--selector-write-concurrency=0` (the value would produce a zero-capacity worker semaphore that deadlocks on the first send).

## One-shot snapshot

The repo ships [`hack/observe.sh`](https://github.com/projection-operator/projection/blob/main/hack/observe.sh) as a copy-paste debugging helper. It dumps:

- Cluster info and nodes.
- Operator pod and the last 80 lines of controller logs.
- `kubectl get projections -A`.
- Per-Projection condition summary.
- Recent events in `projection-system`.
- Optionally, full YAML of one `Projection` plus its source and destination.

```bash
# Overall snapshot
./hack/observe.sh

# Deep dive on a single Projection
./hack/observe.sh app-config-to-tenant-a default
```

It honors `KUBECTL_CONTEXT` (default `kind-projection-dev`) so you can point it at any kubeconfig context.
