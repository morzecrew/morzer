# RFC 0008 — Testing the claims: a coverage programme to 95%

- **Status:** 🚧 In progress — P1 shipped 2026-08-04, measured 70.0%. P2–P5 open.
- **Scope:** Raises statement coverage from a measured 70% to 95%, and — the
  actual point — makes every security property this project advertises name the
  test that enforces it. Covers counting the coverage the acceptance suite
  already produces, a harness that drives every CLI command, fault injection at
  the I/O boundary, real-service contract suites against public container
  images, and a claims-to-tests inventory. Explicitly **not** in scope: chasing
  the last few percent through assertion-free tests, testing third-party
  behaviour, or a per-package floor regime.
- **Related:** [`.github/scripts/coverage-floor.sh`](../.github/scripts/coverage-floor.sh),
  [`test/contract/`](../test/contract/),
  [`test/suite/`](../test/suite/),
  [`.github/scripts/acceptance.sh`](../.github/scripts/acceptance.sh),
  [`internal/infra/logging/logging.go`](../internal/infra/logging/logging.go),
  RFC [0005](0005-continuous-integration-and-release.md) for the CI topology this extends

---

## 1. Summary

Coverage is 70%. This project refuses a bundle whose image is not digest-pinned,
renders secrets `0400` into tmpfs, confines archive extraction with `os.Root`,
scrubs secret values out of every log line, and has no flag that skips TLS
verification. Those are security claims, and a claim whose enforcement path is
70% covered is a claim with an untested third.

The programme has five phases. The first is mechanical and buys 10 points. The
rest buy the remainder by driving the CLI, injecting I/O failures, and running
adapters against real services instead of fakes. The last one is the reason for
the other four: an inventory mapping each advertised property to the test that
would fail if it stopped holding.

## 2. Motivation

### 2.1 The claims are ahead of the tests

Every number below was measured on the tree at `be86d75`, from the union of the
`go test` profile and a `-cover`-instrumented acceptance run.

The clearest example is the boldest claim. *Secrets never reach a log* is
enforced by one type — `redactingHandler` in
[`internal/infra/logging`](../internal/infra/logging/logging.go) — and its
enforcement methods are the least-covered code in the package:

| Function | Coverage |
| --- | --- |
| `redactAttr` — scrubs a log attribute | **21.4%** |
| `WithAttrs` — scrubs attributes captured by `logger.With` | **0.0%** |
| `WithGroup` | **0.0%** |
| `Apply` — scrubs a string | 62.5% |

`WithAttrs` redacts **eagerly**, at the moment `With` is called. A value
captured before the secret is registered is therefore written in the clear.
Demonstrated:

```go
child := logger.With("token", "s3cr3t-value")
redactor.Register("s3cr3t-value")   // operations register secrets when they load them
child.Info("using the token")
// level=INFO msg="using the token" token=s3cr3t-value
```

**This is not currently reachable.** The only two `.With` call sites in the
codebase pass `operation_id`, `operation_type` and `step_id`. It is a latent
hazard that today is correct by accident of what those two call sites happen to
carry, and nothing — no test, no type, no lint — prevents a third call site from
introducing it. That is exactly the shape of defect coverage exists to surface,
and 21% is why nobody has.

Other enforcement paths in the same position:

| Property | Enforced in | Union coverage |
| --- | --- | --- |
| Extraction cannot escape its root | `internal/infra/atomicfs` | 70.3% |
| Secret state stays decryptable, recipients correct | `internal/adapters/secrets/sopsage` | 74.5% |
| A release is refused unless it verifies | `internal/adapters/verify/*` | 76–83% |
| Preflight refuses an unsafe machine | `internal/lifecycle/preflight` | 67.3% |
| Health is what the manager says it is | `internal/adapters/health` | 58.5% |

### 2.2 The number understates the testing, and that hides where the real gaps are

`go test` measures 59.5%. The acceptance suite runs the *built binary*, so
nothing it exercises is counted. Instrumenting it with `go build -cover` and
merging the profiles:

| Measured | Coverage |
| --- | --- |
| Unit and integration tests (`go test`) | 59.5% |
| Acceptance run alone | 47.6% |
| **Union** | **70.0%** |

Ten points of existing, genuine testing are invisible. Until they are counted,
every judgement about where to write tests next is made against the wrong map.

### 2.3 What the remaining gap actually is

2331 statements are uncovered in the union. Classified by what guards them:

| | Statements | Share |
| --- | --- | --- |
| Behind an I/O or subprocess error check | 687 | 29% |
| Everything else — reachable behaviour | 1644 | 71% |

And 128 functions are at 0%. The largest single concentration is not error
handling at all:

| Package | Uncovered | Union |
| --- | --- | --- |
| `internal/cli` | **604** | 47.6% |
| `internal/lifecycle/ops` | 390 | 77.0% |
| `internal/domain` | 139 | 82.5% |
| `internal/infra/atomicfs` | 113 | 70.3% |
| `internal/adapters/runtime/compose` | 108 | 48.1% |
| `internal/adapters/secrets/sopsage` | 96 | 74.5% |

Inside `internal/cli`, the uncovered statements are whole `RunE` closures:
`release.go` (174), `secret.go` (154), `commands.go` (94). Nothing drives
`morzer release prune`, `morzer secret recipients add`, or
`morzer installation export` as commands — only the operations beneath them.
The flag parsing, the argument validation, the confirmation prompts and the
error rendering are untested surface, and they are the surface an operator
touches.

### 2.4 70% is not a defensible number for this project

A tool whose entire value proposition is careful, reversible, verified state
transitions is a tool whose failure modes are the interesting part. The step
engine's compensation paths, the refusals, the "this would destroy data so I am
stopping" branches — those are the code that justifies the design, and they are
disproportionately represented in the 30%.

## 3. Current state

- One floor: 59% total statements, `go test` only
  ([`coverage-floor.sh`](../.github/scripts/coverage-floor.sh)).
- Contract suites run each port against the fake **and** the real adapter, and
  `just contract-strict` fails when a real-adapter suite *skips* — the mechanism
  this RFC extends rather than replaces.
- Fake-backed integration suites cover the operations well (`ops` at 77%).
- The acceptance run exercises real Docker, real Compose, real sops — and is not
  measured.
- Codecov receives the profile and reports the trend; it does not gate.

The existing skip pattern is worth quoting because phase 4 depends on it:

```go
if _, err := exec.LookPath("sops"); err != nil {
    t.Skip("sops is not installed; skipping the real SecretStore contract suite")
}
```

## 4. Goals / Non-goals

**Goals**

- 95% statement coverage of `./internal/...`, measured over the union of every
  suite the project runs.
- Every advertised security property names the test that enforces it, and that
  test fails when the property is removed — verified by removing it.
- I/O failure paths are reachable from a test, because compensation is the
  behaviour this design rests on.
- Adapters are tested against the real services they wrap, not only against
  fakes that agree with them.

**Non-goals**

- **Coverage theatre.** A test that executes a line without asserting anything
  about it raises the number and lowers the signal. Decision 6 makes this a
  review rule with teeth.
- **Testing other people's software.** That Postgres stores rows or that Redis
  expires keys is not this project's concern; that the *adapter* reports their
  state correctly is.
- **100%.** The last few percent are `String()` methods, unreachable defaults
  and `panic("unreachable")`. Chasing them costs more than it finds.
- **Per-package floors.** They let the domain sit at 98% and the adapters at 40%
  while the headline looks respectable — the reasoning already recorded in
  `coverage-floor.sh`.

## 5. Design

### 5.1 P1 — Count what is already tested

Build the binary with coverage instrumentation, point the acceptance run at it,
and merge the profiles.

```sh
go build -cover -coverpkg=./internal/...,./cmd/... -o morzer-cov ./cmd/morzer
MORZER=./morzer-cov GOCOVERDIR="$dir" .github/scripts/acceptance.sh
go tool covdata textfmt -i="$dir" -o acceptance.profile
```

Verified end to end while writing this RFC: 36 profiles, 47.6% from the
acceptance run alone, 70.0% unioned. Two details found doing it:

- `-coverpkg=./internal/...` alone produces **no output**. The main package must
  be instrumented too or nothing registers the meta-data.
- The profiles use different counter modes (`atomic` from `go test`, `set` from
  the binary), so they are unioned block-by-block rather than concatenated.

The union becomes the gated number, and CI's `coverage` job takes the acceptance
artefact. When acceptance is skipped — it is change-gated — the gate falls back
to the unit floor rather than failing, so a docs-only pull request does not have
to run Docker to be mergeable.

### 5.2 P2 — Drive every command

A CLI harness that runs commands the way an operator does — through
`cli.Execute` with an argv, against a `--root` temporary tree — asserting the
exit code, stdout, stderr and the JSON envelope.

```go
r := clitest.New(t)                       // an initialised installation under t.TempDir()
out := r.Run("release", "prune", "--json")
out.ExitCode(0).JSON(".data.removed | length", 2)
```

This is the largest single win (604 statements) and the one that tests what
operators actually touch: argument validation, refusals, confirmation prompts,
`--json` shape, exit codes. It also gives the exit-code contract in
[`exit-codes.md`](../pages/docs/reference/exit-codes.md) a test per row, which it
does not have today.

Every command gets: the success path, one refusal, and the `--json` envelope.
The parity rule from RFC 0002 decision 3 extends to it — plain, rich and JSON
for the same command must carry the same information.

### 5.3 P3 — Make I/O failures reachable

687 statements are `if err != nil` after a call that does not fail in a test.
Three mechanisms, cheapest first:

1. **Provoke it for real.** A read-only directory, a path that is a file where a
   directory is expected, a file with mode `0000`, a full filesystem via a small
   `tmpfs`, a symlink loop. This reaches most of `atomicfs` and needs no new
   abstraction.
2. **Inject at the port.** The fakes already exist; they gain a
   `FailOn(op string)` so a specific call returns an error. This is how the
   engine's compensation paths get their remaining coverage.
3. **Inject at the subprocess.** `internal/infra/exec` gains a test-only runner
   returning scripted exits and output, so an adapter's handling of a tool that
   fails, hangs or prints garbage is testable without that tool misbehaving.

What this buys is not the percentage: it is that the compensation paths — the
thing that makes a failed update safe — are exercised rather than assumed.

### 5.4 P4 — Real services, not only fakes

The user-facing question this answers: does the health adapter actually report
what a real service is doing?

| Adapter | Today | Against a real service |
| --- | --- | --- |
| `health` HTTP | a stub that always answers | Caddy: slow responses, 500s, connection refused, TLS |
| `health` TCP | **0% — `Check` never runs** | any container with a published port |
| `runtime/compose` | 48% | multi-service projects, dependency ordering, unhealthy containers |
| `backup/hookbackup` | 67% | Postgres: a real dump and a real restore, verified by querying the data back |
| `secrets/sopsage` | 74.5% | already real; extend the failure cases |

Public images, pinned by digest like everything else this project runs:
`caddy` for HTTP behaviour, `postgres` for a backup that means something,
`redis` for a fast TCP listener, `busybox` for the trivial cases.

They follow the existing pattern exactly: a shared contract suite run against
the fake and the real thing, skipping when Docker is absent, with
`contract-strict` failing on a skip so CI cannot go green without them. They are
a separate `just` recipe from `test`, so a contributor without Docker still has
a fast loop.

**Scope discipline.** These test the adapter, never the service. "Postgres
restored the rows" is a fixture assertion; "the backup hook's non-zero exit
became a `BackupError` with the right exit code" is the test.

### 5.5 P5 — The claims inventory

A table in [`explanation/`](../pages/docs/explanation/) mapping every security
property this project advertises to the test that enforces it, and a test that
the table is complete.

| Claim | Enforced by | Test |
| --- | --- | --- |
| Secrets never reach a log | `redactingHandler` | `TestARegisteredSecretNeverReachesAnAttribute` |
| Rendered secrets are `0400` in a `0700` tmpfs directory | `sopsage.Render` | contract suite |
| Extraction cannot escape its root | `atomicfs.ExtractTarZst` | traversal fixtures |
| Images are digest-pinned or refused | `Manifest.Validate` | manifest tests |
| Hooks run only from a verified release | `ops` staging | fault-injection suite |
| No flag skips TLS verification | absence | a test asserting the flag does not exist |

Each row's test must **fail when the property is removed**, verified by removing
it — the perturbation discipline the project already applies, made explicit and
recorded per row.

This is what the user asked for when they said 70% is unprofessional for a
project making security claims. The percentage is the proxy; this table is the
thing.

## 6. Tests

The programme's own verification:

- **Every new test is perturbed.** A test that does not fail when the behaviour
  it names is removed does not count as covering it.
- **The union pipeline is tested** by asserting a statement covered only by the
  acceptance run appears as covered in the merged profile.
- **The claims table is gated** — `docs-check` learns to fail when a row names a
  test that does not exist, the same way it already reads error codes and hook
  variables out of the source.
- **A coverage ratchet**: the floor rises with each phase, in the same pull
  request, so the number cannot drift back down quietly.

## 7. Docs

- `explanation/what-is-tested.md` — the claims table, and the testing levels it
  sits on top of.
- `CONTRIBUTING.md` — the union measurement, the container suites, the
  no-assertion-no-credit rule.
- The existing testing-levels table gains the two new levels.

## 8. Out of scope

- **Mutation testing.** The right answer to "are these tests any good", and a
  much larger commitment. Worth its own RFC once the coverage is there to mutate.
- **Fuzzing.** The manifest decoder and the archive extractor are the obvious
  targets. Deferred for the same reason.
- **Windows and macOS.** The tool manages a Linux VM.
- **Benchmarks.** Nothing here is performance-sensitive; a slow `apply` is
  dominated by Docker.

## 9. Risks

- **Goodhart's law, and it is the main risk.** A 95% target invites tests that
  execute code without asserting anything. Decision 6 and the perturbation rule
  in §6 are the mitigation, and they are only as good as review. The claims
  table in P5 exists partly so there is a second metric that cannot be gamed by
  volume.
- **CI time.** Container suites and an instrumented acceptance run cost minutes.
  Mitigation: they are change-gated like the existing acceptance job, run in
  parallel, and the container images are pinned and cached.
- **Flakiness.** Tests against real services are the flakiest kind, and a flaky
  gate teaches people to re-run rather than read. Mitigation: no timing
  assumptions, explicit readiness waits, and a flaky test is deleted or fixed
  within one working day — never retried in a loop.
- **The last five points are the expensive ones.** 70→90 is steady work;
  90→95 is where unreachable branches live. Phasing puts the value first, and
  decision 7 says what happens if 95 turns out to cost more than it returns.
- **Instrumented binaries are not the shipped binaries.** `-cover` changes what
  runs. The acceptance run must also execute against an uninstrumented build, or
  the thing being verified is not the thing being released.

## 10. Decisions

| # | Decision |
| --- | --- |
| 1 | Coverage is measured over the **union** of every suite, including the acceptance run against an instrumented binary. Measuring `go test` alone understates the testing by 10 points and points the effort at the wrong packages. |
| 2 | One total floor, not per-package floors. Per-package numbers let the adapters rot behind a respectable headline — the reasoning already in `coverage-floor.sh`. |
| 3 | The floor ratchets up in the same pull request that raises coverage. A floor that lags is a floor that permits a silent slide back. |
| 4 | Real-service suites extend the existing contract pattern: same suite against fake and real, skip without Docker, `contract-strict` fails on a skip. A second testing mechanism would be a second thing to keep honest. |
| 5 | Container images are pinned by digest, exactly as the manifest requires of a release. A test fixture that silently changes under you is worse than no fixture. |
| 6 | **A test that executes a line without asserting on it does not count.** Every new test names the behaviour it pins, and is verified by removing that behaviour and watching it fail. |
| 7 | If a phase's last statements turn out to be unreachable defensive code, the floor stops below 95% and this RFC is amended with the measured reason. A number met by lowering the bar is worse than an honest 92%. |
| 8 | The claims inventory is the deliverable; the percentage is how progress is measured. If the two ever conflict, the inventory wins. |

## 11. Phasing

| Phase | What | Expected |
| --- | --- | --- |
| **P1** | Union pipeline: instrumented acceptance, merged profiles, CI wiring | 59% → **70%** (measured, mechanical) |
| **P2** | CLI harness driving every command, exit codes, `--json` envelopes | → **~78%** |
| **P3** | I/O fault injection: real filesystem conditions, port fakes, scripted subprocesses | → **~86%** |
| **P4** | Real-service contract suites: Caddy, Postgres, Redis | → **~92%** |
| **P5** | Claims inventory, the gate for it, and the remaining named gaps | → **95%** |

P1 is worth landing on its own and changes no test. P5 is worth landing even if
the number stops short, because it is the part that answers the question the
percentage is only a proxy for.

Every projection after P1 is an estimate from the per-package figures in §2.3,
not a measurement. Each phase reports its actual number and amends this table.
