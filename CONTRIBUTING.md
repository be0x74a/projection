# Contributing to projection

Thanks for your interest in contributing! This document covers the essentials.
Whether you're fixing a typo or adding a feature, you're welcome here.

## Getting started

Prerequisites:

- Go 1.22+
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

## Code of Conduct

This project follows the [Contributor Covenant](./CODE_OF_CONDUCT.md).
By participating, you agree to uphold it. Report issues privately via a
[GitHub Security Advisory](https://github.com/be0x74a/projection/security/advisories/new).

## License

By contributing, you agree that your contributions will be licensed under
the [Apache License, Version 2.0](./LICENSE), matching the per-file
boilerplate already present in the codebase.
