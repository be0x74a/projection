# Scale

First numbers for `projection` at three operating points on Kind, using the
benchmark harness shipped in
[`test/bench/`](https://github.com/projection-operator/projection/tree/main/test/bench)
(see `make bench`). The goals are **scaling behavior** (does the controller
stay flat under more Projections?) and **user-visible latency** (how fast does
an edit on a source object reach the destination?), not absolute throughput
on production hardware.

## Methodology

| | |
|---|---|
| Tool | `make bench PROFILE=<small\|medium\|selector>` |
| Kubernetes | 1.31.0 via `kindest/node:v1.31.0` (the chart now requires ≥1.32 for the destination CEL rule; numbers below predate the floor bump and have not been re-measured on 1.32 yet — re-bench is on the v1.0 to-do list) |
| Cluster topology | single-node Kind (apiserver, etcd, scheduler, kubelet co-located) |
| Host OS / arch | Ubuntu 24.04, amd64 |
| Host hardware | AMD Ryzen (24-thread) / 32 GiB RAM, Proxmox VM |
| Docker | Docker Engine in the VM |
| Controller | run locally (`go run ./cmd/main.go --metrics-bind-address=:8080 --metrics-secure=false`), connected to the Kind cluster via `/tmp/bench.kubeconfig` |
| Harness → controller | harness scrapes `http://127.0.0.1:8080/metrics`; source-to-destination latency measured by stamping a unix-nano annotation on the source and polling destinations |

All numbers below come from a `main` controller at `v0.1.0-alpha.1` (the first published tag). They will be re-measured at v0.2.0 on a `kindest/node:v1.32.x` cluster; the scaling shape is expected to hold but absolute numbers may shift slightly.
The harness spins up its own bench CRDs (`bench.projection.sh/v1
BenchObject{N}`), source objects, destination namespaces, and Projections,
then tears them all down after the measurement window. Each profile runs a
30-second settle before measuring to let the initial-reconcile backlog drain.

## Operating points

| profile | Projections | GVKs | Namespaces | Selector match | Measurement |
|---|---:|---:|---:|---:|---|
| `small` | 100 | 10 | 10 | — | 100 stamped-source roundtrips across sampled Projections |
| `medium` | 1000 | 20 | 50 | — | same |
| `selector` | 1 | 1 | 1 | 100 matching namespaces | 30 stamps × polling all 100 destinations per stamp |

`small` is the laptop-validation point; `medium` is the headline scale on a
decent single-node host; `selector` exercises the fan-out path where a single
Projection's reconcile has to write many destinations.

## Results

### Profile: `small` (100 Projections, 10 GVKs, 10 namespaces)

```text
projections                   100
gvks                          10
namespaces                    10
watched_gvks                  10
controller_heap_mb            11.9
controller_rss_mb             54.2
controller_cpu_seconds_delta  0.22
reconcile_p50_ms              6.96
reconcile_p95_ms              28.44
reconcile_p99_ms              47.69
e2e_p50                       15.87 ms
e2e_p95                       22.72 ms
e2e_p99                       24.46 ms
duration_seconds              38.0
```

At 100 Projections, the controller's footprint is negligible (~12 MB heap,
~54 MB RSS). User-visible source-to-destination latency sits at about 16 ms
p50 and 25 ms p99 — wall-clock-dominated by the apiserver round-trips on
the source watch event and the destination Get/Update; controller-side
reconcile work itself is single-digit milliseconds (see `reconcile_p50_ms`).

### Profile: `medium` (1000 Projections, 20 GVKs, 50 namespaces)

```text
projections                   1000
gvks                          20
namespaces                    50
watched_gvks                  20
controller_heap_mb            24.2
controller_rss_mb             69.1
controller_cpu_seconds_delta  0.21
reconcile_p50_ms              4.39
reconcile_p95_ms              24.44
reconcile_p99_ms              46.24
e2e_p50                       15.90 ms
e2e_p95                       22.41 ms
e2e_p99                       25.23 ms
duration_seconds              521.9
```

The key result is the **scaling shape**:

| | small (100) | medium (1000) | scaling |
|---|---:|---:|---:|
| heap | 11.9 MB | 24.2 MB | **2.0×** for 10× Projections |
| RSS | 54.2 MB | 69.1 MB | **1.3×** |
| reconcile p99 | 47.69 ms | 46.24 ms | essentially flat |
| e2e p99 | 24.46 ms | 25.23 ms | essentially flat |

Heap scales sub-linearly; RSS barely moves; both latency tails stay within
noise. The controller is watch-driven (no periodic requeue on success), so
idle 1k-Projection cost is dominated by the informer caches, which are
shared-by-GVK rather than per-object. `watched_gvks` matches the input
`gvks` exactly — one dynamic watch per distinct source Kind.

Note that the `medium` profile uses **single-destination** Projections
(1000 Projections × 1 destination each) — it does not exercise selector
fan-out at scale. The `selector` profile below is the only fan-out data
point published today, and it caps at 100 matching namespaces. Larger
fan-out profiles (1k+ matching namespaces per Projection) are on the to-do
list as part of the v1 readiness work.

### Profile: `selector` (1 Projection fanning out to 100 namespaces)

```text
projections                   1
gvks                          1
namespaces                    1
selector_ns                   100
watched_gvks                  1
controller_heap_mb            12.1
controller_rss_mb             55.9
controller_cpu_seconds_delta  2.46
reconcile_p50_ms              74.14
reconcile_p95_ms              98.97
reconcile_p99_ms              134.00
e2e_earliest_p50              35.78 ms
e2e_earliest_p95              39.33 ms
e2e_earliest_p99              40.19 ms
e2e_slowest_p50               75.72 ms
e2e_slowest_p95               87.16 ms
e2e_slowest_p99               133.93 ms
duration_seconds              45.7
```

Two e2e distributions because the harness polls *all* matched destinations
per stamp and records when the first one (`e2e_earliest`) and the last one
(`e2e_slowest`) catch up. The spread between the two is the true per-stamp
fan-out cost: for 100 matching namespaces, a source edit propagates to the
first destination in about 36 ms (p50) and to all of them in about 76 ms.
Roughly 40 ms of spread across 100 destinations.

## What scales, what to watch

**Scales well today:**

- Reconcile latency at 10× Projection count (small → medium).
- Heap and RSS at 10× Projection count.
- Selector fan-out at 100 matching namespaces: worst-case ~134 ms p99.

**Watch at scale beyond this profile:**

- Selector with >500 matching namespaces hasn't been measured yet. Reconcile
  time for the selector profile is dominated by the worker pool issuing
  destination writes in parallel (default 16, configurable via the
  `--selector-write-concurrency` flag / `selectorWriteConcurrency` Helm
  value — see [observability.md](observability.md#-selector-write-concurrency-selectorwriteconcurrency-default-16));
  throughput past that depends on kube-apiserver APF priority-level
  budgets at the worker count you've configured.
- Create/delete churn across unique GVKs: the controller's `watched` map
  grows monotonically as new GVKs are projected. Bounded in practice by the
  set of distinct Kinds installed on the cluster, so not a leak at realistic
  scale, but an unusual workload (e.g., rapid churn of scratch CRDs) could
  surface this.
- **Steady-state apiserver load is governed by `--requeue-interval`** (Helm
  value `requeueInterval`, default 30s). Dynamic source watches are the
  authoritative path for source-edit propagation, so the requeue is mostly
  a safety-net resync. Lowering it (e.g. 5s for dev clusters) increases
  apiserver Get traffic linearly with Projection count; raising it (e.g.
  2m for clusters with many flapping upstreams) cuts traffic at the cost
  of slower transient-error recovery.
- **Source-mode `allowlist` (the v0.2 default) adds no measurable overhead
  per reconcile** — the projectability check is a single annotation lookup
  on the already-fetched source object. Not benchmarked separately for that
  reason.
- **Leader election adds one apiserver Lease renewal per
  `leaderElection.leaseDuration`** (default 15s) per controller pod, only
  when `replicaCount > 1`. Negligible at single-replica scale; on large
  fleets running many operator instances, raise the lease duration to cut
  renewal traffic.

## Caveats

- **Kind is not a production cluster.** The apiserver, etcd, scheduler, and
  kubelet share one container. Absolute latency numbers will look different
  on a real multi-node cluster with a dedicated etcd — in particular, etcd
  write latency on a production-grade disk can be higher than Kind's
  in-memory-ish behavior, which would push reconcile p99 up.
- **Single-node = no load from other workloads.** On a real cluster sharing
  apiserver with other operators and controllers, APF priority-level budgets
  may serialize parallel destination writes and increase selector fan-out
  tails.
- **arm64 vs amd64.** Published numbers are amd64 (Ryzen). Running on Apple
  Silicon (arm64) via Docker Desktop produces comparable shapes but higher
  absolute latency due to the virtualization layer.
- **30-second settle** is a conservative default to avoid measuring
  cold-cache effects. Shorter settles produce comparable numbers with
  slightly wider tails.

## Reproduce

```bash
cd <projection-repo>

# Terminal 1 — Kind + CRDs
# Use a 1.32+ node to match the chart's kubeVersion floor. Published
# numbers above were captured on 1.31; expect comparable shapes on 1.32.
kind create cluster --name bench --image kindest/node:v1.32.0
kind get kubeconfig --name bench > /tmp/bench.kubeconfig
KUBECONFIG=/tmp/bench.kubeconfig make install

# Terminal 2 — controller
KUBECONFIG=/tmp/bench.kubeconfig go run ./cmd/main.go \
  --metrics-bind-address=:8080 --metrics-secure=false

# Terminal 3 — bench
make bench PROFILE=small KUBECONFIG_BENCH=/tmp/bench.kubeconfig
make bench PROFILE=medium KUBECONFIG_BENCH=/tmp/bench.kubeconfig
make bench PROFILE=selector KUBECONFIG_BENCH=/tmp/bench.kubeconfig

# Teardown
kind delete cluster --name bench
```

See [`test/bench/`](https://github.com/projection-operator/projection/tree/main/test/bench)
for the harness source. Profile shapes and thresholds are in
`test/bench/profile.go`.

## Known limitations in the harness

- **CRD teardown is noisy in controller logs.** When the bench tears down
  its `bench.projection.sh/v1` CRDs, the controller's dynamic
  watches on those GVKs fail with `NoKindMatchError` and retry indefinitely.
  This is cosmetic (the watches aren't doing anything useful post-teardown)
  and documented in [#28](https://github.com/projection-operator/projection/issues/28).
  Restart the controller between bench runs to clear the log.
- **Reconcile histogram p99 can be quantized.** The controller-runtime
  reconcile-duration histogram uses Prometheus' default buckets, so at low
  sample counts the p99 can land on a bucket boundary and appear higher
  than the e2e observation. The `e2e_p99` (harness-owned, nanosecond
  samples) is the user-facing SLI and tracks the underlying reality more
  tightly than `reconcile_p99_ms`.
