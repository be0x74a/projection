# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project overview

`projection` is a Kubernetes operator scaffolded with Kubebuilder v4 (`go.kubebuilder.io/v4`, domain `sh`, group `projection`). It defines two CRDs at `projection.sh/v1`:

- `Projection` — namespaced, single-target. Mirrors one Kubernetes object identified by `(group, version, kind, namespace, name)` into the Projection's own `metadata.namespace`. `spec.destination.name` is optional (defaults to `source.name`); there is no `destination.namespace` field. Use this for "tenant in namespace X needs a copy of object Y".
- `ClusterProjection` — cluster-scoped, fan-out. Mirrors one source object into either an explicit list `destination.namespaces: [a, b, c]` (with `minItems=1`) OR a `destination.namespaceSelector` matching namespaces by label. CEL admission enforces XOR + at-least-one. Use this for "every namespace matching X needs a copy of Y".

Both CRDs share the same `SourceRef{Group, Version, Kind, Namespace, Name}` (all required, all pattern-validated at admission). CEL on SourceRef requires `version` when `group` is empty (i.e. core kinds must specify v1). Non-core groups can omit `version` for preferred-version lookup via the RESTMapper. Both CRDs accept an optional `spec.overlay{Labels, Annotations}` that merges on top of the source with overlay winning on conflicts.

The controller fully implements the write side: it fetches the source via the dynamic client, strips server-owned metadata and `.status`, applies the overlay, stamps a per-CRD-scope ownership annotation, and creates or updates the destination. ClusterProjection iterates target namespaces in parallel (cap `--selector-write-concurrency`, default 16), tracks per-namespace success/failure into `status.namespacesWritten` / `status.namespacesFailed`, rolls up `DestinationWritten`, and cleans up stale destinations when namespaces stop matching the selector or get removed from the list. Distinct finalizers (`projection.sh/finalizer` for Projection, `projection.sh/cluster-finalizer` for ClusterProjection) clean up every owned destination on CR deletion. Source edits propagate via dynamic watches (`ensureSourceWatch`); manual `kubectl delete` of a destination triggers immediate re-creation via a label-filtered watch on the destination GVK (`ensureDestWatch`).

Positioning: aims to be the de-facto CRDs for resource mirroring, competing with emberstack/Reflector (ConfigMap/Secret only) and Kyverno `generate` (policy-shaped, not per-resource).

## Common commands

Build, test, and lint run through the Makefile. Most targets depend on generated code, so prefer `make` over raw `go` invocations.

- `make build` — regenerates manifests + DeepCopy code, formats, vets, and builds `bin/manager`.
- `make run` — runs the controller against the cluster in `~/.kube/config`.
- `make test` — runs unit/integration tests with envtest (downloads Kubernetes control-plane binaries for version `1.34.1` into `./bin`). Excludes `test/e2e`. Produces `cover.out`.
- `make test-e2e` — runs Ginkgo e2e tests under `test/e2e/` against a Kind cluster (must be provisioned separately).
- `make lint` / `make lint-fix` — runs `golangci-lint` (config in `.golangci.yml`).
- Run a single test: `go test ./internal/controller/ -run TestName -v` (requires `KUBEBUILDER_ASSETS` to point at envtest binaries — easiest via `make test`, or export `KUBEBUILDER_ASSETS="$(./bin/setup-envtest use 1.34.1 --bin-dir ./bin -p path)"` first).

Code generation (run after editing `api/v1/*_types.go` or any `+kubebuilder:*` marker):

- `make manifests generate docs-ref sync-chart-crds` — the four-target chain that keeps CRDs, RBAC, DeepCopy, the rendered API reference under `docs/api-reference.md`, and the chart-shipped CRDs under `charts/projection/crds/` all in sync. Two CI jobs (CRD reference drift, Chart CRD drift) fail loudly if any of these drift, so always run the full chain after touching API types or markers.

Cluster lifecycle:

- `make install` / `make uninstall` — apply/remove CRDs only.
- `make deploy IMG=<registry>/projection:tag` / `make undeploy` — apply/remove the full controller deployment via Kustomize.
- `make docker-build docker-push IMG=...` — build and push the manager image.
- `make build-installer IMG=...` — emits a single `dist/install.yaml` bundle.

Tool binaries (kustomize, controller-gen, setup-envtest, golangci-lint, crd-ref-docs) are installed on-demand into `./bin/` pinned by version in the Makefile.

## Architecture

**CRDs (`api/v1/`)** — three files:

- `source_types.go` defines the shared `SourceRef{Group, Version, Kind, Namespace, Name}` (DNS-1123 namespace, PascalCase Kind, regex-validated group/version/name). Both `group` and `version` are optional; empty `version` resolves to the RESTMapper's preferred served version (for the core group, currently `v1`).
- `projection_types.go` defines `Projection` (scope: Namespaced) with `Spec{Source, Destination ProjectionDestination{Name string}, Overlay}` and `Status{DestinationName string, Conditions []Condition}` (Ready, SourceResolved, DestinationWritten). Printcolumns: Source, Destination, Ready, Age.
- `clusterprojection_types.go` defines `ClusterProjection` (scope: Cluster) with `Spec{Source, Destination ClusterDestination{Namespaces []string +listType=set +minItems=1, NamespaceSelector LabelSelector}, Overlay}` plus two CEL admission rules on `ClusterProjectionDestination`: `!(has(self.namespaces) && has(self.namespaceSelector))` (mutex) and `has(self.namespaces) || has(self.namespaceSelector)` (at-least-one). `Status{DestinationName string, NamespacesWritten int32, NamespacesFailed int32, Conditions []Condition}`. Printcolumns: Source, Destination, Written, Failed, Ready, Age.

**Controllers (`internal/controller/`)** — split between two reconcilers, with shared infrastructure on `*ControllerDeps` (`deps.go`):

- `ControllerDeps` embeds `client.Client` and holds `DynamicClient`, `RESTMapper`, `Recorder`, `Scheme`. Methods on `*ControllerDeps` cover GVR resolution, destination Build/Create/Update, ownership stamping/check, and `ensureDestWatch` (the label-filtered destination watch that fires on manual delete). Both reconcilers embed `*ControllerDeps`.
- `ProjectionReconciler` (`projection_controller.go`) handles the namespaced CRD. `SetupWithManager` calls `.Build(r)` (not `.Complete(r)`) so the controller + cache are stashed for lazy source-watch registration via `ensureSourceWatch`. Field indexer keyed on a 4-part canonical `sourceKey` (`group/kind/namespace/name`, no version — preferred-version lookup happens at resolve time) maps source events back to every Projection referencing them.
- `ClusterProjectionReconciler` (`cluster_projection_controller.go`) handles the cluster-scoped CRD. Same lazy source-watch registration. Holds `SelectorWriteConcurrency` (the `--selector-write-concurrency` flag, default 16) bounding parallel writes during fan-out. A `mapNamespace` handler re-enqueues only ClusterProjections whose selector matches (or whose explicit list contains) the changed namespace.
- Both reconcilers register a `metadata.uid` field indexer for the destination watch and a label-filtered `PartialObjectMetadata` watch on each unique destination GVK they handle (added lazily in `ensureDestWatch`).

Key invariants to preserve when editing:

- GVR resolution goes through `resolveGVR` (`source.go`), which reads `src.Group`/`src.Version`/`src.Kind` and asks the RESTMapper for the `{GroupKind}` → `GroupVersionResource` mapping. Do not hand-build `GroupVersionResource`s.
- Ownership is per-CRD-scope:
  - Namespaced: annotation `projection.sh/owned-by-projection: <ns>/<name>` + label `projection.sh/owned-by-projection-uid: <uid>` (label is a watch-filter hint; annotation is authoritative).
  - Cluster: annotation `projection.sh/owned-by-cluster-projection: <name>` (no `<ns>/` prefix — cluster-scoped owner has no namespace) + label `projection.sh/owned-by-cluster-projection-uid: <uid>`.
- `isOwnedBy` (per-CRD, in `destination.go`) reads the annotation, NOT the label. It is the only thing standing between us and overwriting somebody else's object — `Reconcile` checks it before updating and the cleanup paths check it before deleting.
- Finalizers are distinct: `projection.sh/finalizer` for Projection, `projection.sh/cluster-finalizer` for ClusterProjection. Cluster cleanup scans all owned namespaces; namespaced cleanup is a single-namespace delete.
- `droppedMetadataFields` and `droppedAnnotations` (`destination.go`) keep the destination clean; extending them (e.g. dropping a new server-owned field) is the right place to fix metadata-leak bugs.
- Metrics are part of the v1.0 API contract per `docs/api-stability.md`. `projection_reconcile_total{kind,result}` and `projection_e2e_seconds{kind,event}` use externally-visible label values (`kind ∈ {Projection, ClusterProjection}`, `event="create"` only in v0.3.x with reserved future values). Histogram bucket boundaries are locked at v1.0.
- RBAC markers above each `Reconcile` currently grant `"*"/"*"` because both CRDs can target any Kind. Narrowing this requires a design change.

**Entry point (`cmd/main.go`)** — standard Kubebuilder scaffolding: registers schemes for both CRDs, constructs the manager with metrics (secure by default, filtered with authn/authz when enabled), health probes, and optional leader election (`LeaderElectionID: 92777bdc.projection.sh`). HTTP/2 is disabled by default for the metrics/webhook servers (CVE mitigation); pass `--enable-http2` to re-enable. The dynamic client and RESTMapper are constructed from `mgr.GetConfig()` / `mgr.GetRESTMapper()` and injected into a single `ControllerDeps` value, then both reconcilers embed it. The `--selector-write-concurrency` flag wires only into `ClusterProjectionReconciler.SelectorWriteConcurrency`.

**Tests (`internal/controller/`)** — envtest-based integration tests; `suite_test.go` boots the test environment and scheme. Per-CRD test files:

- `projection_controller_test.go` — namespaced reconciler happy paths, conflict, deletion, source-policy, requeue, source-deletion.
- `cluster_projection_controller_test.go` — cluster reconciler happy paths (explicit list and selector), namespace add/remove via `mapNamespace`, fan-out cleanup, cluster finalizer.
- `rbac_test.go` — SubjectAccessReview matrix asserting the chart-rendered ClusterRoles enforce the namespaced-vs-cluster split. Shells out to `helm template` so it tests the actual rendered RBAC, not a hardcoded slice.

In unit-test paths that call `Reconcile` directly (no running manager), `Controller` and `Cache` are nil and `ensureSourceWatch`/`ensureDestWatch` no-op — the reconcile path must continue to work in that mode. Keep new controller tests in this package so they share the suite.

**Debugging (`hack/observe.sh`)** — a bash script for snapshotting operator + Projection state. Dumps cluster-info, operator pod status and recent logs, operator-namespace events, and (when given a Projection name) that Projection's spec/status/conditions plus its resolved source and destination objects. Usage: `./hack/observe.sh [projection-name] [projection-namespace]`. Reach for this first when a Projection isn't behaving.

**Config (`config/`)** — Kustomize overlays: `crd/` (generated CRDs for both Projection and ClusterProjection), `rbac/` (generated roles from kubebuilder markers), `manager/` (Deployment + ConfigMap), `default/` (top-level overlay wired by `make deploy`), plus `network-policy/`, `prometheus/`, and `samples/` (example CRs). The Helm chart at `charts/projection/` is the supported install path; Kustomize is for development. Do not hand-edit `config/crd/bases/`, files under `config/rbac/`, or anything under `charts/projection/crds/` — they are generated. Edit the source markers and rerun the codegen chain.

## Conventions specific to this project

- Module path is `github.com/projection-operator/projection`. API imports use `projectionv1 "github.com/projection-operator/projection/api/v1"`.
- Go 1.22+ required (see `README.md`). Controller-runtime v0.19 conventions apply (e.g. `log.FromContext(ctx)` for loggers).
- After editing `api/v1/*_types.go` or any `+kubebuilder:*` marker, run `make manifests generate docs-ref sync-chart-crds`. Two CI drift checks (CRD reference drift, Chart CRD drift) fail otherwise.
- CEL XValidation rules in marker strings: prefer `size(self.X) != 0` over adjacent single quotes `''` — gofmt has historically mangled `''` to U+201D (right double quotation mark) in `+kubebuilder:` markers.
