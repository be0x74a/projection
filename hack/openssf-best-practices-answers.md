# OpenSSF Best Practices — Passing-level answers

Scratch file for submitting `projection` to https://www.bestpractices.dev. Each criterion below maps to a field on the form — paste the **Answer** text into the "Justification / URL" box under that criterion.

Status as of v0.1.0-alpha.1: **56 Met, 0 Unmet, 3 N/A** (of 59 criteria).

> Note: the form may paginate or re-order criteria. This file follows the order the badge PDF prints in. Search by criterion ID if the form layout differs.

---

## Header fields (not pass/fail — just values the form asks for up top)

| Field | Value |
|---|---|
| Human-readable name | `projection` |
| Brief description | The Kubernetes CRD for declarative resource mirroring across namespaces — any Kind, conflict-safe, watch-driven. |
| Description/justification language | English (en) |
| Project URL (homepage) | `https://be0x74a.github.io/projection` |
| Version control repository URL | `https://github.com/be0x74a/projection` |
| License | Apache-2.0 |
| Programming languages | Go, Makefile, Shell, Go Template, Dockerfile |
| CPE name | *(leave blank)* |

---

## Basics

### `description_good` — Met
The README at https://github.com/be0x74a/projection/blob/main/README.md and the docs site at https://be0x74a.github.io/projection open with a one-sentence description of what `projection` does, followed by a comparison table versus emberstack/Reflector and Kyverno `generate`.

### `interact` — Met
The README's Quick start, Contributing, and Security sections link to install commands, the public issue tracker at https://github.com/be0x74a/projection/issues, GitHub Discussions at https://github.com/be0x74a/projection/discussions, and https://github.com/be0x74a/projection/blob/main/CONTRIBUTING.md.

### `contribution` — Met (URL required)
`Non-trivial contribution file in repository: https://github.com/be0x74a/projection/blob/main/CONTRIBUTING.md.`

### `contribution_requirements` — Met
CONTRIBUTING.md specifies Go 1.24+, Conventional Commits, `make lint` (golangci-lint with 19 linters in `.golangci.yml`), `make test`, and "new behavior should come with tests"; the PR template at https://github.com/be0x74a/projection/blob/main/.github/PULL_REQUEST_TEMPLATE.md enumerates the per-PR checklist.

---

## FLOSS license

### `floss_license` — Met
`The Apache-2.0 license is approved by the Open Source Initiative (OSI).` Full text at https://github.com/be0x74a/projection/blob/main/LICENSE.

### `floss_license_osi` — Met (SUGGESTED)
`The Apache-2.0 license is approved by the Open Source Initiative (OSI).` See https://opensource.org/license/apache-2-0.

### `license_location` — Met (URL required)
`Non-trivial license location file in repository: https://github.com/be0x74a/projection/blob/main/LICENSE.`

---

## Documentation

### `documentation_basics` — Met
Basic documentation is provided via the README (https://github.com/be0x74a/projection/blob/main/README.md) and a published mkdocs-material site at https://be0x74a.github.io/projection covering Getting Started, Concepts, Use Cases, Comparison, Observability, Security, and Limitations.

### `documentation_interface` — Met
The CRD reference at https://be0x74a.github.io/projection/crd-reference/ (source `docs/crd-reference.md`) documents every field of the `Projection` spec/status. The observability doc at https://be0x74a.github.io/projection/observability/ documents the `projection_reconcile_total{result}` Prometheus metric, the three status conditions (`SourceResolved`, `DestinationWritten`, `Ready`), and emitted Kubernetes Events — i.e. the complete external input + output surface.

---

## Other

### `sites_https` — Met
`Given only https: URLs.` Project site, repository, and release downloads are all HTTPS-only.

### `discussion` — Met
`GitHub supports discussions on issues and pull requests.` The project also has GitHub Discussions enabled at https://github.com/be0x74a/projection/discussions; issues at https://github.com/be0x74a/projection/issues.

### `english` — Met
All documentation, code comments, README, CONTRIBUTING, issue templates, and PR template are in English.

### `maintained` — Met
Active commits on the default branch within the last week; https://github.com/be0x74a/projection/blob/main/CHANGELOG.md shows a 2026-04-13 release; Dependabot at https://github.com/be0x74a/projection/blob/main/.github/dependabot.yml runs weekly grouped updates; SECURITY.md commits to a 5-business-day triage SLA.

---

## Change control

### `repo_public` — Met (URL required)
`Repository on GitHub, which provides public git repositories with URLs.` https://github.com/be0x74a/projection.

### `repo_track` — Met
`Repository on GitHub, which uses git. git tracks the changes, who made them, and when they were made.`

### `repo_interim` — Met
Every change lands via pull request; per-commit history is retained on the default branch at https://github.com/be0x74a/projection/commits/main; PRs are reviewable before any tagged release at https://github.com/be0x74a/projection/pulls.

### `repo_distributed` — Met (SUGGESTED)
`Repository on GitHub, which uses git. git is distributed.`

### `version_unique` — Met
Releases use unique SemVer tags (e.g. `v0.1.0-alpha.1`) visible at https://github.com/be0x74a/projection/releases and https://github.com/be0x74a/projection/tags.

### `version_semver` — Met (SUGGESTED)
The project follows Semantic Versioning 2.0.0 as declared in https://github.com/be0x74a/projection/blob/main/CHANGELOG.md ("this project adheres to Semantic Versioning"); current version `v0.1.0-alpha.1`.

### `version_tags` — Met (SUGGESTED)
Each release is an annotated git tag; see https://github.com/be0x74a/projection/tags and https://github.com/be0x74a/projection/releases/tag/v0.1.0-alpha.1. The `release.yml` workflow triggers on `v*` tags.

### `release_notes` — Met (URL required)
`Non-trivial release notes file in repository: https://github.com/be0x74a/projection/blob/main/CHANGELOG.md.` Keep-a-Changelog 1.1.0 format; the GitHub Release at https://github.com/be0x74a/projection/releases/tag/v0.1.0-alpha.1 mirrors the notes.

### `release_notes_vulns` — N/A
`N/A — this is the initial release (v0.1.0-alpha.1); no prior publicly known CVEs applied to project results, so there are no fixed CVEs to list.`

---

## Reporting

### `report_process` — Met (URL required)
Issues are enabled with two structured issue forms at https://github.com/be0x74a/projection/blob/main/.github/ISSUE_TEMPLATE/bug_report.yml and https://github.com/be0x74a/projection/blob/main/.github/ISSUE_TEMPLATE/feature_request.yml; the public tracker is at https://github.com/be0x74a/projection/issues; security reports are routed via https://github.com/be0x74a/projection/blob/main/SECURITY.md.

### `report_tracker` — Met (SHOULD)
GitHub Issues is the issue tracker: https://github.com/be0x74a/projection/issues.

### `report_responses` — Met
All bug reports opened to date against https://github.com/be0x74a/projection/issues have received a maintainer response; SECURITY.md (https://github.com/be0x74a/projection/blob/main/SECURITY.md) documents a 5-business-day acknowledgement SLA that also applies to bug triage.

### `enhancement_responses` — Met (SHOULD)
Enhancement requests filed via the feature-request form at https://github.com/be0x74a/projection/issues have all received maintainer triage to date.

### `report_archive` — Met (URL required)
All issues and their threads are publicly archived and URL-addressable at https://github.com/be0x74a/projection/issues?q=is%3Aissue; PR discussions at https://github.com/be0x74a/projection/pulls?q=is%3Apr.

---

## Vulnerability report process

### `vulnerability_report_process` — Met (URL required)
The vulnerability reporting process is published at https://github.com/be0x74a/projection/blob/main/SECURITY.md and mirrored at https://be0x74a.github.io/projection/security/; the README Security section links to it.

### `vulnerability_report_private` — Met
SECURITY.md directs reporters to submit a private GitHub Security Advisory at https://github.com/be0x74a/projection/security/advisories/new (transport-encrypted, visible only to maintainers), with explicit instructions not to file public issues for vulnerabilities.

### `vulnerability_report_response` — Met
https://github.com/be0x74a/projection/blob/main/SECURITY.md commits to responding within 5 business days and providing a fix/mitigation timeline within 14 days. No vulnerability reports have been received to date, so all received-report response times are within the stated SLA.

---

## Quality

### `build` — Met (URL required)
`Non-trivial build file in repository: https://github.com/be0x74a/projection/blob/main/Makefile.` `make build` regenerates manifests + deepcopy code, runs `go fmt`/`go vet`, and produces `bin/manager`. Multi-arch container images are built by https://github.com/be0x74a/projection/blob/main/.github/workflows/release.yml.

### `build_common_tools` — Met (SUGGESTED)
`Non-trivial build file in repository: https://github.com/be0x74a/projection/blob/main/Makefile.` Uses `make` + `go build` (standard Go toolchain) + `docker build`; container release uses GoReleaser — all FLOSS.

### `build_floss_tools` — Met (SHOULD)
The entire build chain — Go, make, controller-gen, kustomize, golangci-lint, Docker/BuildKit, GoReleaser, cosign, syft — is FLOSS; see tool pinning in https://github.com/be0x74a/projection/blob/main/Makefile.

### `test` — Met
Unit tests (`internal/controller/build_destination_test.go`, `destination_diff_test.go`), Ginkgo envtest integration tests (5 specs in `internal/controller/projection_controller_test.go`), and Ginkgo e2e tests (6 specs in `test/e2e/e2e_test.go` against a live Kind cluster) — all invoked via `make test` / `make test-e2e` documented in https://github.com/be0x74a/projection/blob/main/CONTRIBUTING.md and executed by CI at https://github.com/be0x74a/projection/blob/main/.github/workflows/ci.yml.

### `test_invocation` — Met (SHOULD)
Tests run via the standard `go test` toolchain; see https://github.com/be0x74a/projection/blob/main/Makefile and https://github.com/be0x74a/projection/blob/main/CONTRIBUTING.md.

### `test_most` — Met (SUGGESTED)
Coverage is approximately 65% of statements in `internal/controller/` (reported via `cover.out` from `make test`); coverage spans unit tests, envtest integration tests, e2e tests, plus three native Go fuzz targets at https://github.com/be0x74a/projection/blob/main/internal/controller/fuzz_test.go.

### `test_continuous_integration` — Met (SUGGESTED)
GitHub Actions CI at https://github.com/be0x74a/projection/blob/main/.github/workflows/ci.yml runs lint, unit/envtest tests, build, and e2e (Kind) on every push and PR; CodeQL runs on push/PR/weekly at https://github.com/be0x74a/projection/blob/main/.github/workflows/codeql.yml.

### `test_policy` — Met
https://github.com/be0x74a/projection/blob/main/CONTRIBUTING.md explicitly states "New behavior should come with tests"; the PR template at https://github.com/be0x74a/projection/blob/main/.github/PULL_REQUEST_TEMPLATE.md includes a test-plan section.

### `tests_are_added` — Met
Every feature commit in the `0.1.0-alpha` release ships with matching tests — e.g. `build_destination_test.go` and `destination_diff_test.go` accompany the builder/diff logic in `projection_controller.go`, and the envtest suite `projection_controller_test.go` exercises the end-to-end reconcile path. See https://github.com/be0x74a/projection/tree/main/internal/controller.

### `tests_documented_added` — Met (SUGGESTED)
Documented in https://github.com/be0x74a/projection/blob/main/CONTRIBUTING.md ("New behavior should come with tests") and reinforced in https://github.com/be0x74a/projection/blob/main/.github/PULL_REQUEST_TEMPLATE.md.

### `warnings` — Met
golangci-lint v1.64.8 runs on every CI job with 19 linters configured in https://github.com/be0x74a/projection/blob/main/.golangci.yml (including `govet`, `staticcheck`, `errcheck`, `gosec`, `unused`, `ineffassign`); `go vet` runs on every `make build` via https://github.com/be0x74a/projection/blob/main/Makefile.

### `warnings_fixed` — Met
CI blocks merges on `make lint` failures (the `Lint` job is a required status check in the main ruleset), so any lint warning must be addressed before a PR can merge.

### `warnings_strict` — Met (SUGGESTED)
golangci-lint config at https://github.com/be0x74a/projection/blob/main/.golangci.yml enables a maximal practical set (19 linters including `gosec`, `staticcheck`, `revive`, `gocritic`, `unparam`); CodeQL at https://github.com/be0x74a/projection/blob/main/.github/workflows/codeql.yml adds security-focused static analysis.

---

## Security

### `know_secure_design` — Met
The primary developer designed the system to follow least-privilege Kubernetes controller conventions: non-root distroless runtime image, scoped RBAC generated from per-type `+kubebuilder:rbac:` markers, HTTP/2 disabled by default on the metrics endpoint (CVE-2023-44487/39325 mitigation, see `cmd/main.go`), TLS-protected metrics endpoint with authn/authz filter, stateless operator (no secrets stored), and no custom cryptography. The security model is documented at https://be0x74a.github.io/projection/security/.

### `know_common_errors` — Met
The codebase reflects awareness of OWASP/CWE classes relevant to controllers: input validation via CRD schema patterns (DNS-1123 names, PascalCase Kinds) to prevent injection of arbitrary GVRs, ownership-annotation checks before mutating destinations to prevent privilege escalation via overwrite (CWE-269), finalizer-guarded deletion to prevent orphaned state; CI-enforced `gosec` analysis in https://github.com/be0x74a/projection/blob/main/.golangci.yml plus CodeQL `security-extended` queries in https://github.com/be0x74a/projection/blob/main/.github/workflows/codeql.yml.

---

## Cryptography

### `crypto_published` — Met
The project uses only Go standard library `crypto/tls` for apiserver and metrics TLS, and Sigstore cosign (ECDSA P-256 via Fulcio, SHA-256) for keyless release signing. No custom cryptographic primitives.

### `crypto_call` — Met (SHOULD)
All crypto is via the Go standard library (`crypto/tls`) and Sigstore tooling (cosign keyless, syft for SBOM). No re-implementation of crypto primitives.

### `crypto_floss` — Met
All cryptographic functionality relies on FLOSS: Go standard library `crypto/tls`, cosign (Apache-2.0), syft (Apache-2.0), all available in any standard Go/container build environment.

### `crypto_keylength` — Met
TLS keys for apiserver communication are managed by Kubernetes itself (RSA 2048+ / ECDSA P-256); cosign keyless signing uses ECDSA P-256 certificates issued by Fulcio (meets NIST ≥2030). No user-configurable keys produced by the project itself.

### `crypto_working` — Met
No use of MD4, MD5, SHA-1, single-DES, RC4, or Dual_EC_DRBG. Release checksums use SHA-256 (`checksums.txt` on https://github.com/be0x74a/projection/releases/tag/v0.1.0-alpha.1); cosign signing uses ECDSA P-256 + SHA-256.

### `crypto_weaknesses` — Met (SHOULD NOT)
No use of SHA-1 or other weakened primitives. TLS defaults are the Go standard library's, which disables RC4, 3DES, and SHA-1 for TLS 1.2+.

### `crypto_pfs` — Met (SHOULD)
TLS 1.3 enforces PFS via ephemeral key exchange; Go 1.25's `crypto/tls` defaults preserve this.

### `crypto_password_storage` — N/A
`N/A — projection is a stateless Kubernetes controller; it does not authenticate or store passwords for external users. Authentication is delegated to the Kubernetes apiserver (ServiceAccount tokens / mTLS).`

### `crypto_random` — N/A
`N/A — the controller does not generate cryptographic keys or nonces itself; all TLS and signing key generation is delegated to the Go standard library / Kubernetes / Sigstore Fulcio.`

---

## Secured delivery against MITM

### `delivery_mitm` — Met
`Distribution channels use HTTPS exclusively.` All artifacts are distributed over HTTPS (GitHub Releases, `ghcr.io` OCI registry for images and Helm charts); container images and release archives are additionally keyless-signed with cosign (Sigstore Fulcio + Rekor transparency log) via https://github.com/be0x74a/projection/blob/main/.github/workflows/release.yml.

### `delivery_unsigned` — Met
`checksums.txt`, `.sig`, `.pem`, and `.sbom.json` artifacts are served over HTTPS from https://github.com/be0x74a/projection/releases/tag/v0.1.0-alpha.1; cosign verifies release archives and images against Rekor transparency log entries rather than a bare SHA over HTTP.

---

## Publicly known vulnerabilities fixed

### `vulnerabilities_fixed_60_days` — Met
No medium-or-higher public vulnerabilities are known against the project. CodeQL (https://github.com/be0x74a/projection/security/code-scanning) and OpenSSF Scorecard (https://scorecard.dev/viewer/?uri=github.com/be0x74a/projection) are currently clean; Dependabot (https://github.com/be0x74a/projection/blob/main/.github/dependabot.yml) opens weekly PRs for transitive-dep advisories.

### `vulnerabilities_critical_fixed` — Met (SHOULD)
https://github.com/be0x74a/projection/blob/main/SECURITY.md commits to a 14-day fix/mitigation timeline; no critical vulnerabilities have been reported to date.

---

## Other security issues

### `no_leaked_credentials` — Met
The repository contains no credentials. CodeQL, Scorecard's `Token-Permissions` / `Dangerous-Workflow` / `Secrets` checks (https://scorecard.dev/viewer/?uri=github.com/be0x74a/projection) pass; GitHub secret scanning is enabled on the public repo and has no findings. Sample files under `config/samples/` contain only placeholder values.

---

## Analysis

### `static_analysis` — Met
golangci-lint (19 linters including `gosec`, `staticcheck`, `govet` — see https://github.com/be0x74a/projection/blob/main/.golangci.yml) runs on every push and PR via https://github.com/be0x74a/projection/blob/main/.github/workflows/ci.yml; GitHub CodeQL (Go, `security-extended` queries) runs on push/PR/weekly via https://github.com/be0x74a/projection/blob/main/.github/workflows/codeql.yml with SARIF uploaded to the Code Scanning tab. Both ran on the `v0.1.0-alpha.1` commit.

### `static_analysis_common_vulnerabilities` — Met (SUGGESTED)
`gosec` (inside golangci-lint per https://github.com/be0x74a/projection/blob/main/.golangci.yml) targets Go-specific vulnerability patterns; CodeQL uses the `security-extended` query suite (see `.github/workflows/codeql.yml`) which includes CWE-based vulnerability rules.

### `static_analysis_fixed` — Met
Neither CodeQL (https://github.com/be0x74a/projection/security/code-scanning) nor golangci-lint currently reports any medium-or-higher issues on `main`; CI blocks merges on lint failures.

### `static_analysis_often` — Met (SUGGESTED)
golangci-lint runs on every push/PR; CodeQL runs on every push, every PR, and weekly on a schedule.

### `dynamic_analysis` — Met (SUGGESTED)
Ginkgo envtest integration tests (5 specs, `internal/controller/projection_controller_test.go`) exercise the reconciler against a real Kubernetes control-plane binary; Ginkgo e2e tests (6 specs, `test/e2e/e2e_test.go`) run against a live Kind cluster via `make test-e2e` in https://github.com/be0x74a/projection/blob/main/.github/workflows/ci.yml; three native Go fuzz targets in https://github.com/be0x74a/projection/blob/main/internal/controller/fuzz_test.go (`FuzzBuildDestination`, `FuzzNeedsUpdate`, `FuzzPreserveAPIServerAllocatedFields`) provide random-input dynamic analysis.

### `dynamic_analysis_unsafe` — N/A
`N/A — projection is written entirely in Go, a memory-safe language (see go.mod and language breakdown). Bounds checks, garbage collection, and -race detection cover memory-safety concerns; a -race build is exercised via go test.`

### `dynamic_analysis_enable_assertions` — Met (SUGGESTED)
Envtest and e2e suites use Gomega matchers (https://github.com/be0x74a/projection/blob/main/internal/controller/projection_controller_test.go, https://github.com/be0x74a/projection/blob/main/test/e2e/e2e_test.go) producing rich assertions on every reconcile step; fuzz targets in https://github.com/be0x74a/projection/blob/main/internal/controller/fuzz_test.go use `t.Fatalf`/`require`-style assertions on every iteration.

### `dynamic_analysis_fixed` — Met
No medium-or-higher exploitable issues have been discovered via envtest/e2e/fuzz runs to date; CI blocks merges on test failure so regressions cannot land silently (https://github.com/be0x74a/projection/blob/main/.github/workflows/ci.yml).

---

## Summary

| Bucket | Count |
|---|---|
| Met | 56 |
| Unmet | 0 |
| Unmet (justified) | 0 |
| N/A | 3 |
| **Total** | **59** |

**N/A items** (all defensibly N/A for a stateless Kubernetes controller):

- `release_notes_vulns` — first release, no prior CVEs
- `crypto_password_storage` — no external-user password storage
- `crypto_random` — no self-generated crypto keys/nonces
- `dynamic_analysis_unsafe` — Go is memory-safe

---

## After submission

1. The form issues a numeric project ID (e.g. `9876`).
2. Add the badge to the README header:

   ```markdown
   [![OpenSSF Best Practices](https://www.bestpractices.dev/projects/<ID>/badge)](https://www.bestpractices.dev/projects/<ID>)
   ```
3. On the next main-branch push, Scorecard's `CII-Best-Practices` check flips **0 → 5** (passing badge; 7 for silver, 10 for gold).

## Optional silver/gold-tier strengthening (later)

- Require PR approvals in the main ruleset (raises `Branch-Protection` and, over time, `Code-Review`).
- Adopt DCO sign-off (`Signed-off-by:` trailer required on commits) or GPG-signed commits.
- Add a second maintainer to `CODEOWNERS` once one is onboarded.
- Record a primary-developer security-training URL for the silver-level `know_secure_design` specificity requirement.

## Reusable evidence URLs (for quick paste)

- Repo: https://github.com/be0x74a/projection
- Site: https://be0x74a.github.io/projection
- Release: https://github.com/be0x74a/projection/releases/tag/v0.1.0-alpha.1
- SECURITY: https://github.com/be0x74a/projection/blob/main/SECURITY.md
- CONTRIBUTING: https://github.com/be0x74a/projection/blob/main/CONTRIBUTING.md
- LICENSE: https://github.com/be0x74a/projection/blob/main/LICENSE
- CHANGELOG: https://github.com/be0x74a/projection/blob/main/CHANGELOG.md
- Scorecard: https://scorecard.dev/viewer/?uri=github.com/be0x74a/projection
- Private advisory: https://github.com/be0x74a/projection/security/advisories/new
