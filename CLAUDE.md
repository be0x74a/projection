# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project overview

`projection` is a Kubernetes operator scaffolded with Kubebuilder v4 (`go.kubebuilder.io/v4`, domain `be0x74a.io`, group `projection`). It defines a single namespaced CRD, `Projection`, which mirrors one Kubernetes object from a source `(apiVersion, kind, namespace, name)` into either a single destination `(namespace, name)` or — via `destination.namespaceSelector` — into every namespace matching a label selector. All destination fields are optional (defaults: the Projection's own namespace and `source.name`). An optional `spec.overlay` merges labels/annotations on top of the source with overlay winning on conflicts. `namespace` and `namespaceSelector` are mutually exclusive; the reconciler enforces this (not CEL, for cross-apiserver-version compatibility).

The controller fully implements the write side: it fetches the source via the dynamic client, strips server-owned metadata and `.status`, applies the overlay, stamps an ownership annotation, and creates or updates the destination. For selector-based Projections it iterates matching namespaces, tracks per-namespace success/failure, rolls up `DestinationWritten`, and cleans up stale destinations when namespaces stop matching. A finalizer cleans up every owned destination on Projection deletion (scanning all namespaces for selector-based ones). Source edits propagate via dynamic watches; namespace events re-enqueue selector-based Projections via a `selectorIndex` field indexer.

Positioning: aims to be the de-facto CRD for resource mirroring, competing with emberstack/Reflector (ConfigMap/Secret only) and Kyverno `generate` (policy-shaped, not per-resource).

## Common commands

Build, test, and lint run through the Makefile. Most targets depend on generated code, so prefer `make` over raw `go` invocations.

- `make build` — regenerates manifests + DeepCopy code, formats, vets, and builds `bin/manager`.
- `make run` — runs the controller against the cluster in `~/.kube/config`.
- `make test` — runs unit/integration tests with envtest (downloads Kubernetes control-plane binaries for version `1.31.0` into `./bin`). Excludes `test/e2e`. Produces `cover.out`.
- `make test-e2e` — runs Ginkgo e2e tests under `test/e2e/` against a Kind cluster (must be provisioned separately).
- `make lint` / `make lint-fix` — runs `golangci-lint` (config in `.golangci.yml`).
- Run a single test: `go test ./internal/controller/ -run TestName -v` (requires `KUBEBUILDER_ASSETS` to point at envtest binaries — easiest via `make test`, or export `KUBEBUILDER_ASSETS="$(./bin/setup-envtest use 1.31.0 --bin-dir ./bin -p path)"` first).

Code generation (run after editing `api/v1/*_types.go`):

- `make manifests` — regenerates CRDs, RBAC, webhook config under `config/`.
- `make generate` — regenerates `zz_generated.deepcopy.go`.

Cluster lifecycle:

- `make install` / `make uninstall` — apply/remove CRDs only.
- `make deploy IMG=<registry>/projection:tag` / `make undeploy` — apply/remove the full controller deployment via Kustomize.
- `make docker-build docker-push IMG=...` — build and push the manager image.
- `make build-installer IMG=...` — emits a single `dist/install.yaml` bundle.

Tool binaries (kustomize, controller-gen, setup-envtest, golangci-lint) are installed on-demand into `./bin/` pinned by version in the Makefile.

## Architecture

**CRD (`api/v1/projection_types.go`)** — `Projection` has `Spec.Source{APIVersion, Kind, Name, Namespace}` (all required, all pattern-validated at admission: APIVersion must match a core-or-group/version pattern, Kind is PascalCase, names and namespaces are DNS-1123), `Spec.Destination{Namespace, Name}` (both optional — defaults applied in the reconciler), and `Spec.Overlay{Labels, Annotations}`. `Status.Conditions` carries a single `Ready` condition. Printcolumns surface Kind, Source-Namespace, Source-Name, Destination, Ready, Age.

**Controller (`internal/controller/projection_controller.go`)** — `ProjectionReconciler` embeds `client.Client`, holds a `dynamic.Interface` and a `meta.RESTMapper`, and additionally stashes the `controller.Controller` and `cache.Cache` that `SetupWithManager` returns. Those two are used to register source watches lazily at reconcile time — which is why `SetupWithManager` calls `.Build(r)` rather than `.Complete(r)`. `SetupWithManager` also registers a field indexer keyed on a canonical `sourceKey` so a source-object event can be mapped back to every Projection referencing it via a single cached `List`.

Key invariants to preserve when editing:

- GVR resolution goes through `resolveGVR`, which parses `Source.APIVersion` and asks the RESTMapper for the `{Group, Kind}` mapping. Do not hand-build `GroupVersionResource`s.
- Ownership is established via the `projection.be0x74a.io/owned-by: <ns>/<name>` annotation stamped in `buildDestination`. `isOwnedBy` is the only thing standing between us and overwriting somebody else's object — `Reconcile` checks it before updating and `deleteDestination` checks it before deleting.
- `droppedMetadataFields` and `droppedAnnotations` exist to keep the destination clean; extending them (e.g. dropping a new server-owned field) is the right place to fix metadata-leak bugs.
- RBAC markers above `Reconcile` currently grant `"*"/"*"` because a Projection can target any Kind. Narrowing this requires a design change.

**Entry point (`cmd/main.go`)** — standard Kubebuilder scaffolding: registers schemes, constructs the manager with metrics (secure by default, filtered with authn/authz when enabled), health probes, and optional leader election (`LeaderElectionID: 92777bdc.be0x74a.io`). HTTP/2 is disabled by default for the metrics/webhook servers (CVE mitigation); pass `--enable-http2` to re-enable. The dynamic client and RESTMapper are constructed from `mgr.GetConfig()` / `mgr.GetRESTMapper()` and injected into the reconciler.

**Tests (`internal/controller/suite_test.go`, `projection_controller_test.go`)** — envtest-based integration tests; `suite_test.go` boots the test environment and scheme. In unit-test paths that call `Reconcile` directly (no running manager), `Controller` and `Cache` are nil and `ensureWatch` no-ops — the reconcile path must continue to work in that mode. Keep new controller tests in this package so they share the suite.

**Debugging (`hack/observe.sh`)** — a bash script for snapshotting operator + Projection state. Dumps cluster-info, operator pod status and recent logs, operator-namespace events, and (when given a Projection name) that Projection's spec/status/conditions plus its resolved source and destination objects. Usage: `./hack/observe.sh [projection-name] [projection-namespace]`. Reach for this first when a Projection isn't behaving.

**Config (`config/`)** — Kustomize overlays: `crd/` (generated CRDs), `rbac/` (generated roles from kubebuilder markers), `manager/` (Deployment + ConfigMap), `default/` (top-level overlay wired by `make deploy`), plus `network-policy/`, `prometheus/`, and `samples/` (example CRs). Do not hand-edit `config/crd/bases/` or files under `config/rbac/` that are generated — edit the source markers and rerun `make manifests`.

## Conventions specific to this project

- Module path is `github.com/be0x74a/projection`. API imports use `projectionv1 "github.com/be0x74a/projection/api/v1"`.
- Go 1.22+ required (see `README.md`). Controller-runtime v0.19 conventions apply (e.g. `log.FromContext(ctx)` for loggers).
- After editing `api/v1/*_types.go` or any `+kubebuilder:*` marker, always run `make manifests generate` (or simply `make build`/`make test`, which chain them).
