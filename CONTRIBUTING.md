# Contributing to projection

Thanks for your interest in contributing! This document covers the essentials.
Whether you're fixing a typo or adding a feature, you're welcome here.

## Getting started

Prerequisites:

- Go 1.24+
- Docker (for building images and running Kind)
- [Kind](https://kind.sigs.k8s.io/) (for local clusters and e2e tests)
- `make`

Clone and build:

```bash
git clone https://github.com/be0x74a/projection.git
cd projection
make build
```

`make build` regenerates manifests + DeepCopy code, runs `go fmt`/`go vet`,
and produces `bin/manager`.

## Running tests

Unit and envtest-based integration tests:

```bash
make test
```

This downloads the Kubernetes control-plane binaries for the pinned envtest
version into `./bin/` on first run and produces `cover.out`.

End-to-end tests (Ginkgo) against a Kind cluster you've already deployed
the operator to:

```bash
make test-e2e
```

For debugging a live local cluster, `hack/observe.sh` is a handy helper
that streams reconciler logs, events, and Projection statuses side by side.

Running a single Go test:

```bash
export KUBEBUILDER_ASSETS="$(./bin/setup-envtest use 1.31.0 --bin-dir ./bin -p path)"
go test ./internal/controller/ -run TestName -v
```

## Submitting changes

1. Fork the repo and create a branch off `main`.
2. Make your changes. Keep commits focused; squash fixups before opening a PR.
3. Use [Conventional Commits](https://www.conventionalcommits.org/) for
   commit messages. Common prefixes:
   - `feat:` new user-facing behavior
   - `fix:` bug fix
   - `docs:` documentation only
   - `chore:` tooling, deps, non-functional cleanup
   - `test:` test-only changes
   - `refactor:` no behavior change
   - `ci:` CI/workflow changes
4. Open a pull request against `main`. Fill in the PR template.
5. A maintainer will review. Squash to logical units before merge.

## Code standards

Before opening a PR, make sure these pass locally:

```bash
make lint    # golangci-lint
make test    # unit + envtest
```

New behavior should come with tests. If you change CRD fields or RBAC
markers, run `make manifests generate` (or `make build`, which chains them).

## Local development quirks

A couple of gotchas worth knowing before you lose time to them:

- **`kind load docker-image` with multi-arch manifests on Apple Silicon**:
  Kind's image loader chokes on multi-arch manifest lists with a digest
  mismatch error. Two workarounds: pull the per-platform tag
  (`:X.Y.Z-arm64`) and load that, or skip the load and install the
  operator with an `imagePullSecret` so the cluster pulls from the
  registry directly (see `hack/observe.sh` area of the README for the
  exact secret+SA patch commands).

## Adding a Kind to `droppedSpecFieldsByGVK`

Some Kinds carry apiserver-allocated spec fields that must be stripped before mirroring (see [limitations.md](./docs/limitations.md#some-kinds-need-extra-stripping-rules)). The map at `internal/controller/projection_controller.go` grows case-by-case as users hit gaps. To add an entry:

1. **Confirm the evidence.** There must be a reproducible `spec.FIELD: field is immutable` (or equivalent) error when creating the destination against a real apiserver. Defaulted fields re-apply on the destination and do not belong in this map. Speculation is not enough — the cost of a wrong entry is silently dropping user data.
2. **Write the reproducer in the PR body.** Include the source object YAML, the Projection YAML, and the apiserver error message. This is what the reviewer verifies against.
3. **Add the map entry** at `internal/controller/projection_controller.go` (keep a string-valued path first, for the fuzz seed).
4. **Add a unit subtest** in `internal/controller/build_destination_test.go` mirroring the existing `"strips apiserver-allocated Kind spec fields"` cases: populate the source, assert the fields are stripped, assert user-set fields on the same object survive.
5. **Add a fuzz seed** in `internal/controller/fuzz_test.go`'s `FuzzPreserveAPIServerAllocatedFields`.
6. **Document** — add a row to the limitations.md table and a `### Added` bullet to `CHANGELOG.md` under `[Unreleased]`.

The umbrella issue tracking this work is [#32](https://github.com/be0x74a/projection/issues/32).

## Code of Conduct

This project follows the [Contributor Covenant](./CODE_OF_CONDUCT.md).
By participating, you agree to uphold it. Report issues privately via a
[GitHub Security Advisory](https://github.com/be0x74a/projection/security/advisories/new).

## License

By contributing, you agree that your contributions will be licensed under
the [Apache License, Version 2.0](./LICENSE), matching the per-file
boilerplate already present in the codebase.
