# RFC 0005 — Continuous integration and release automation

- **Status:** 📝 Draft
- **Scope:** Adds `.github/` — workflows, reusable shell helpers, Dependabot,
  issue templates, CODEOWNERS — plus the governance files a public repository is
  expected to carry (`SECURITY.md`, `CONTRIBUTING.md`, `CODE_OF_CONDUCT.md`).
  Establishes a change-gated CI topology, makes the currently-skipped real-adapter
  contract suite actually run, and automates tagged releases. Covers the workflow
  that *invokes* goreleaser; the goreleaser configuration itself belongs to RFC
  [0004](0004-distribution-and-verification.md). No Go source changes beyond test
  helpers. Explicitly **not** in scope: the documentation site and its deployment
  workflows (RFC [0006](0006-documentation-site.md)), and any hosted
  infrastructure.
- **Related:** [`justfile`](../justfile),
  [`.golangci.yml`](../.golangci.yml),
  [`test/suite/contract_test.go`](../test/suite/contract_test.go),
  RFC [0004](0004-distribution-and-verification.md) (goreleaser config),
  RFC [0006](0006-documentation-site.md) (docs workflows)
- **Origin:** Job topology, the reusable-workflow release pattern and the
  `.github/scripts/` convention are ported from
  [morzecrew/forze](https://github.com/morzecrew/forze). Its CI steps are Python
  (`uv`, `pytest`) and do not transfer; its *shape* does, and several of its
  shell helpers are language-agnostic.

---

## 1. Summary

A `ci.yml` that gates on what actually changed, runs `golangci-lint`, the test
suite with the race detector, and a coverage floor — and, critically, installs
`sops` and Docker so the suites that currently skip themselves run for real. A
`release.yaml` triggered by a tag that calls CI as a reusable workflow before
building anything. Supply-chain workflows (CodeQL, dependency review, Scorecard)
and Dependabot.

## 2. Motivation

**There is no CI.** No `.github/` directory exists. Every check that has ever run
on this code ran on one laptop, and two things have already slipped through in
exactly the way an absent CI predicts:

- `golangci-lint` was never executed while the code was written. When it was
  finally run it reported 29 issues, two of which were real defects — an
  infinite loop in secret generation on any power-of-two alphabet, and a
  `depguard` rule scoped so narrowly it missed three genuine layering violations
  in `lifecycle/ops`. Both had been committed and described as verified.
- `just check` runs `fmt-check`, `vet` and `test`. It does **not** run `lint`.
  So even the local convenience command does not catch what the linter catches.

**The test suite is quietly greener than the code deserves.**
[`test/suite/contract_test.go`](../test/suite/contract_test.go) contains:

```go
if _, err := exec.LookPath("sops"); err != nil {
    t.Skip("sops is not installed; skipping the real SecretStore contract suite")
}
```

That skip is correct for a developer without `sops` — but it means the entire
real-adapter contract suite, the thing that stops the fake from lying, is
optional. On a machine without `sops`, `go test ./...` passes while never
exercising the production secret store. CI is where that skip must not fire.

**Releases are manual.** `just build-all` cross-compiles and writes
`SHA256SUMS` by hand. Nothing tags, signs, or publishes.

## 3. Current state

**Present.**

- [`justfile`](../justfile) with `check` (fmt-check + vet + test), `lint`,
  `test-race`, `test-cover`, `contract`, `build-all`. CI can call these directly
  rather than restating commands, so local and CI cannot drift.
- [`.golangci.yml`](../.golangci.yml) with the `depguard` layering rules, and
  documented exclusions. Currently passes with 0 issues.
- A test suite that runs in ~1s without Docker, plus contract suites that use
  the real `sops` binary when present.
- `go.mod` pinning Go 1.24 as the language version.

**Absent.**

- `.github/` entirely: no workflows, no Dependabot, no issue templates, no
  CODEOWNERS.
- `SECURITY.md`, `CONTRIBUTING.md`, `CODE_OF_CONDUCT.md`.
- Any coverage measurement in anger — `just test-cover` exists and no floor is
  enforced.
- Any use of the E2E acceptance scenario the spec describes (clean VM → init →
  apply → reboot → update → rollback → backup → restore). Nothing runs it,
  because there has never been a clean machine to run it on. **A GitHub runner
  is that clean machine.**

## 4. Goals / Non-goals

**Goals**

- Every push and PR runs lint, tests and the race detector.
- The real-adapter contract suites run in CI, never skipped.
- A tagged push produces signed, reproducible binaries without manual steps.
- Supply-chain scanning proportionate to a tool that runs as root on someone's
  server.
- CI invokes `just` recipes so local and CI cannot diverge.

**Non-goals**

- **A matrix over operating systems.** The manager targets Linux and its
  filesystem code uses `syscall.Statfs` and `Setpgid` directly. macOS and
  Windows are not supported targets, and pretending to test them would be
  theatre.
- **Publishing container images.** The manager is a static binary; it manages
  containers rather than shipping as one.
- **A merge queue or required-review automation.** Repository settings, not
  workflow files, and the user's call.
- **Reimplementing checks in YAML.** Every gate is a `just` recipe.

## 5. Design

### 5.1 Topology

Ported from forze's `changes → quality → test → coverage` chain, which gates
each stage on a path-filter job so a docs-only change does not run the full
suite:

```text
ci.yml (also callable as a reusable workflow)
  changes    → path filter; emits code / docs / force_full outputs
  quality    → just fmt-check, just vet, just lint          [needs: changes]
  test       → matrix: {go: [1.24, stable]}                 [needs: quality]
               just test-race, with sops + Docker present
  coverage   → floor gate                                   [needs: test]
  acceptance → the clean-VM scenario                        [needs: test]
```

`quality` before `test` is deliberate: a formatting or layering violation should
fail in twenty seconds rather than after the suite.

**The Go version matrix is `[1.24, stable]`** — the declared minimum in `go.mod`
and whatever is current. A build that only ever runs on the latest toolchain
silently raises the floor.

### 5.2 The skip that must not fire

The single most important line in the whole workflow:

```yaml
- name: Install sops
  run: |
    curl -fsSL -o /usr/local/bin/sops \
      https://github.com/getsops/sops/releases/download/v${SOPS_VERSION}/sops-v${SOPS_VERSION}.linux.amd64
    chmod +x /usr/local/bin/sops
    sops --version

- name: Assert no contract suite skipped
  run: |
    # A skipped real-adapter suite means CI is greener than the code.
    go test ./test/suite/ -run Contract -v 2>&1 | tee out.txt
    if grep -q -- '--- SKIP' out.txt; then
        echo "::error::a contract suite skipped; the real adapter was not exercised"
        exit 1
    fi
```

The assertion matters more than the install: without it, a future change to how
`sops` is located would silently return the suite to skipping, and nothing would
report it.

### 5.3 Coverage floor

`just test-cover` already emits a profile. The gate is a floor, not a target:

```bash
# .github/scripts/coverage-floor.sh — ported in shape from forze's
# coverage_floors.py, in shell because there is no Python in this toolchain.
total=$(go tool cover -func=coverage.out | awk '/^total:/ {print substr($3, 1, length($3)-1)}')
awk -v t="$total" -v f="$FLOOR" 'BEGIN { exit (t+0 >= f+0) ? 0 : 1 }' || {
    echo "::error::coverage ${total}% is below the floor of ${FLOOR}%"
    exit 1
}
```

The floor starts at the measured value on the first green run, rounded down,
and is raised deliberately. A floor set aspirationally above current coverage is
a permanently red build that everyone learns to ignore.

Per-package floors are **not** used. A single total avoids the failure mode
where `internal/domain` is at 95% and the adapters at 5% while the number looks
respectable.

### 5.4 The acceptance scenario

The spec's §25 acceptance criteria describe a clean-VM run. A GitHub runner is
a clean VM with Docker, so the scenario becomes an actual job:

```text
init → apply → (restart docker) → status → doctor → backup → restore → doctor
```

It runs against a real Compose project built from stub images, using `--root` so
it never touches the runner's `/etc`. Steps involving `update`/`rollback` are
gated on RFC 0001 landing, and the job is written so those stages are added
without restructuring it.

This job is allowed to be slow. It runs on `push` to main and on tags, not on
every PR commit.

### 5.5 Release

Ported directly from forze's pattern — the release workflow **calls CI as a
reusable workflow** rather than trusting that CI already passed on the commit:

```yaml
on:
  push:
    tags: ["v*"]

jobs:
  ci:
    uses: ./.github/workflows/ci.yml
    with:
      force_full: true          # a tag runs everything, path filters ignored
  release:
    needs: ci
    permissions:
      contents: write
      id-token: write           # keyless attestation
    steps:
      - run: .github/scripts/ensure-tag-on-main.sh   # ported verbatim
      - uses: goreleaser/goreleaser-action@...        # config from RFC 0004
```

`ensure-tag-on-main.sh` and `compute-version-from-tag.sh` are lifted from forze
unchanged — they are pure shell and language-agnostic. Tagging a commit not on
`main` is refused: a release built from a side branch is a release nobody can
reconstruct.

Until RFC 0004 lands its goreleaser config, the release job runs
`just build-all`, which already produces both architectures and `SHA256SUMS`.
The workflow is written so swapping in goreleaser is a step replacement.

### 5.6 Supply chain

| Workflow | Purpose |
| --- | --- |
| `codeql.yml` | CodeQL has first-class Go support. Weekly plus on PR. |
| `dependency-review.yml` | Blocks a PR introducing a dependency with a known advisory. |
| `scorecard.yaml` | OpenSSF Scorecard, weekly. |
| `dependabot.yml` | `gomod` and `github-actions` ecosystems, weekly, grouped. |

Actions are pinned **by commit SHA**, not by tag. A moving tag on a third-party
action is arbitrary code execution with write access to the release pipeline.
Dependabot updates the SHAs.

`gosec` already runs inside `golangci-lint`, so it is not duplicated as a
separate workflow.

### 5.7 Governance files

`SECURITY.md` (how to report a vulnerability privately, and what is in and out
of the threat model — pointing at the README's existing section rather than
restating it), `CONTRIBUTING.md` (the `just check` / `just lint` loop, the
commit convention from the gitmoji skill, and the RFC-before-large-changes
expectation), `CODE_OF_CONDUCT.md`, `CODEOWNERS`, and issue templates for bug
and feature.

## 6. Tests

CI is itself largely untestable, so the design leans on assertions inside it:

- **The no-skip assertion** (§5.2) is the test that CI is doing its job.
- **`act` or a scratch branch** for workflow syntax before merge; workflows that
  only run on `main` are otherwise debugged in production.
- **A workflow-lint step** (`actionlint`) catching YAML and expression errors.
- **The acceptance job is the integration test** for the whole tool, and its
  value is precisely that nothing else runs it.

## 7. Docs

- `CONTRIBUTING.md` documents the local loop and states that CI runs exactly the
  `just` recipes, so `just check && just lint` locally means CI will pass.
- The README's badge row: CI status, Go report card, OpenSSF Scorecard.
- RFC 0006 owns where longer documentation lives; this RFC adds only the
  contributor-facing files that conventionally sit at the repository root.

## 8. Out of scope

- **Nightly or scheduled full runs.** Worth adding once the acceptance job has a
  track record; scheduling a job that has never been green is noise.
- **Performance benchmarking gates.** forze has these
  (`perf-benchmark-gate.sh`); this project has no performance-sensitive path
  worth gating. Named as the obvious later addition if one appears.
- **Publishing to a package manager** (Homebrew, AUR, apt). Follows a first
  tagged release, not a prerequisite for one.
- **Signing with a hardware key or Sigstore keyless.** RFC 0004 chose minisign;
  the release workflow consumes a key from repository secrets.

## 9. Risks

- **CI green becoming the only signal anyone reads.** A coverage floor and a
  passing suite say nothing about whether `doctor`'s advice is good. Mitigated by
  the acceptance job asserting behaviour rather than counts — and by nobody
  claiming the floor is a quality measure.
- **The acceptance job being flaky and then disabled.** Docker-in-CI is the most
  likely source of intermittent failure here. Mitigation: it runs on `main` and
  tags rather than every PR commit, so a flake blocks a merge less often; if it
  flakes anyway, the answer is to fix or delete it, not to mark it
  `continue-on-error`.
- **Release automation with `contents: write`.** A compromised third-party
  action could publish arbitrary binaries. Mitigated by SHA-pinning every action
  and by the minimum permissions per job, with `permissions: contents: read` at
  the workflow level and elevation only where needed.
- **The Go 1.24 matrix leg rotting.** When the floor is raised, the matrix and
  `go.mod` must move together. Named in `CONTRIBUTING.md`.

## 10. Decisions

| # | Decision |
| --- | --- |
| 1 | CI invokes `just` recipes rather than restating commands in YAML. Local and CI cannot drift when there is one definition, and a contributor can reproduce a failure exactly. |
| 2 | CI fails if any contract suite **skips**. A skipped real-adapter suite means CI is greener than the code — the fake was exercised and the production store was not. |
| 3 | `quality` gates `test`. A formatting or layering violation should fail in twenty seconds, not after the full suite. |
| 4 | The Go matrix is the `go.mod` minimum plus `stable`. Testing only the latest toolchain silently raises the declared floor. |
| 5 | One total coverage floor, not per-package. Per-package floors let the domain sit at 95% and the adapters at 5% while the headline looks respectable. |
| 6 | The floor starts at the first measured value and is raised deliberately. A floor set above current coverage is a permanently red build everyone learns to ignore. |
| 7 | `release.yaml` calls `ci.yml` as a reusable workflow rather than trusting a prior run. A tag must be built from a commit that passed everything, verified at release time. |
| 8 | A tag not on `main` is refused (`ensure-tag-on-main.sh`, ported from forze). A release built from a side branch cannot be reconstructed. |
| 9 | Every third-party action is pinned by commit SHA. A moving tag is arbitrary code execution with write access to the release pipeline. |
| 10 | No OS matrix. The manager targets Linux and calls `Statfs`/`Setpgid` directly; testing macOS would be theatre. |
| 11 | The acceptance scenario runs on `main` and tags, not every PR commit. It is the slowest and most flake-prone job, and its value is in catching real regressions rather than gating every push. |

## 11. Phasing

- **P1** — `ci.yml` with `changes`, `quality`, `test`, and the no-skip
  assertion; `actionlint`. The highest-value piece: it is what would have caught
  the two defects named in §2.
- **P2** — Coverage floor, measured then set.
- **P3** — Supply chain: Dependabot, CodeQL, dependency review, Scorecard.
- **P4** — Governance files, issue templates, CODEOWNERS.
- **P5** — `release.yaml`, initially over `just build-all`, swapping to
  goreleaser when RFC 0004 P5 lands.
- **P6** — The acceptance job. Last because it is the most work and the most
  likely to need iteration; and once RFC 0001 ships, it grows the
  `update`/`rollback` stages.

P1 is worth landing before any further feature work. Every commit made without
it is a commit whose lint status is a matter of whether someone remembered.
