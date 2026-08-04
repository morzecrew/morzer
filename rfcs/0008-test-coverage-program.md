# RFC 0008 — Testing the claims: a coverage programme to 95%

- **Status:** 🚧 In progress — P1 through P5 all complete as designed.
  Measured **81.6%**, from 70.0% when this was written. Every row of §5.4's
  real-service table has a real service behind it and all three of §5.3's
  mechanisms are in use; §15 records where execution diverged. 95% remains
  unmet: §16 measures the 1438 statements between here and there, and §17
  plans P6–P11 against them — 1048 have to be covered, the phases target 1155,
  and the 107-statement margin is published rather than hidden.
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
| **P3** | I/O fault injection: real filesystem conditions, port fakes, scripted subprocesses | est. ~86%, measured **81.6% with P4** |
| **P4** | Real-service contract suites: Caddy, Postgres, Redis | est. ~92%, see above |
| **P5** | Claims inventory, the gate for it, and the remaining named gaps | est. 95%, **not met — §16** |
| **P6** | The interactive surface: the `init` wizard, the editor, the prompts | +100 stmts (§17.5) |
| **P7** | The rest of the CLI, against a populated fixture machine | +215 (§17.6) |
| **P8** | Adapters and infrastructure: scripted sops, corrupt archives, the OCI source | +320 (§17.7) |
| **P9** | Operation steps, injected at the three ports the harness does not yet fail | +265 (§17.8) |
| **P10** | The renderer's program lifecycle under `teatest` | +65 (§17.9) |
| **P11** | Domain, ports and the remainder | +190 (§17.9) |

P1 is worth landing on its own and changes no test. P5 is worth landing even if
the number stops short, because it is the part that answers the question the
percentage is only a proxy for.

Every projection after P1 is an estimate from the per-package figures in §2.3,
not a measurement. Each phase reports its actual number and amends this table.

## 12. Amendments

### P1 — the union pipeline (shipped, 70.0%)

Landed as designed and measured exactly the predicted number. Three things the
design did not say:

- `-coverpkg=./internal/...` alone produces **no profile at all**. The main
  package has to be instrumented too, or nothing registers the coverage
  meta-data and `GOCOVERDIR` stays empty. This cost an hour the first time.
- The profiles cannot be concatenated. `go test` writes `atomic` counts and the
  instrumented binary writes `set`, `go tool cover` refuses a file with two mode
  lines, and the same block appears in both. `tools/covmerge` unions them
  block-by-block and writes `set`, because "how many times" stops meaning
  anything once two counting schemes are combined.
- CI needs **two floors**, not one. The acceptance job is change-gated, so a
  pull request can legitimately produce only the `go test` profile. The gate
  picks its floor from what actually arrived rather than from what was hoped
  for; a missing artefact is the unit floor, not a failure.

### P2 — the CLI harness (shipped, 72.9%)

`internal/cli` went from 17.3% to 60.4%, and `go test` alone from 59.5% to
69.0%.

**The union only moved 70.0% → 72.9%.** Most of what the new command tests
execute was already being executed by the instrumented acceptance run; what they
add is *assertions* on paths that were previously run without being checked.

That is worth recording because it is the strongest argument for P1 having gone
first. Judged by `go test` alone this phase looks like +9.5 points; judged
honestly it is +2.9, and the rest of its value is in what it pins rather than
what it reaches. A programme that had skipped P1 would have congratulated itself
for the wrong reason and aimed the next phase at the wrong packages.

Two test-design notes:

- Four of the first twenty tests failed on **my** expectations, not on defects:
  the envelope field is `supported_api_versions`, `release verify` prints its
  verdict to stderr because summaries go there, and both `secret rotate` without
  a generator and an unsafe `installation export` are usage errors rather than
  domain-specific ones. Each was checked against the code before the test was
  changed.
- The first version of the restore-guard test asserted only the exit code. Every
  path through `restore` exits 2, so it passed with the confirmation check
  removed — the failure had simply moved to the next step. It now asserts *which*
  refusal fires, and that a correct confirmation gets past the guard to fail on
  its merits. Verified by perturbation both ways.

### P3 and P4 — failure paths and real listeners (partial)

Not the mechanisms the RFC designed. §5.3 proposed three, cheapest first, and
the cheapest turned out to be enough for everything attempted: real filesystem
conditions (an unwritable directory, a file where a directory belongs, a mode
nobody can read) and real network listeners (`httptest`, a bound-then-closed
port, a socket that accepts and drops). No port-level `FailOn`, no scripted
subprocess runner, no containers. Those remain for the packages named in §13.

**What covering the enforcement point actually found.** The RFC predicted that
`redactAttr` at 21% was where a defect would be, and it was. The `KindAny`
branch carried this comment:

> Anything that stringifies could carry a secret through its String method, so
> it is rendered and scrubbed rather than passed through untouched.

It handled `string` and `error`. A value implementing only `fmt.Stringer`, or a
plain struct, was passed through and rendered by the handler **unscrubbed** — so
a secret in either printed in full. The comment described the intent; the code
did not implement it. Fixed, with the `KindAny` branch now ending in a scrub of
the `%v` rendering, and both the `Stringer` case and the fallback shown to be
independently load-bearing by removing each and watching a different test fail.

That is the whole argument for this RFC in one defect: a claim, a comment
asserting it, and a third of the enforcing function never executed.

### P5 — the claims inventory (shipped)

[`explanation/what-is-tested.md`](../pages/docs/explanation/what-is-tested.md)
maps 31 advertised properties to the tests that enforce them, across secrets,
verification, filesystem containment, refusals, the runtime boundary, and health
reporting. `docs-check` fails when a row names a test that does not exist —
verified by renaming a row and by renaming a test, each of which breaks the
build.

It also has a *what is not claimed* section, which is the part worth keeping
honest: eager redaction on `.With`, `services` not being checked against the
topology, and overwriting-before-deletion being meaningful on tmpfs and very
little else.

## 13. What remains, measured

| | At the RFC | Now |
| --- | --- | --- |
| `go test` alone | 59.5% | **75.4%** |
| Union with the acceptance run | 70.0% | **77.4%** |
| Uncovered statements (union) | 2331 | **1770** |

Floors are 75 (unit) and 77 (union), ratcheted in the same changes that raised
them. Nothing is below 51% any more; at the RFC, four packages were under 40%.

1379 statements stand between here and 95%, and **604 of them — 44% — are in
`internal/cli`**, which is 65% covered. What is left there is the interactive
`init` wizard, and the `update`/`rollback`/`backup`/`restore` command paths,
which need a running deployment. The acceptance run drives those as a *binary*,
so they are counted; what is uncovered is the branches it does not take.

| Package | Union | Uncovered | Needs |
| --- | --- | --- | --- |
| `internal/cli` | 65% | 404 | a wizard harness, and command paths behind Docker |
| `internal/lifecycle/ops` | ~79% | ~330 | more fault injection through the fakes' `Fail` switch |
| `internal/domain` | ~84% | ~130 | error-formatting branches |
| `internal/adapters/backup/hookbackup` | 67% | 68 | a real Postgres, per §5.4 |
| `internal/lifecycle/preflight` | 68% | 65 | a machine that fails its checks |

**95% remains unproven.** Decision 7 stands: if the last statements are
unreachable defensive code, the floor stops honestly below 95% with the measured
reason. The evidence so far is encouraging — every mechanism tried has paid —
but 1379 statements is not a rounding error and this RFC will not claim the
target before it is met.

## 14. Amendments from the P3 and P4 pass

### Two of the three mechanisms in §5.3 were never needed

§5.3 listed three, cheapest first. The cheapest — provoking real conditions —
covered `atomicfs`, the redaction routes and the health probers on its own.
Mechanism 2, a `FailOn` switch on the fakes, **already existed**: `fakes.Runtime`
has had a `Fail map[string]error` since the engine's fault-injection suite was
written. The RFC proposed building something the repository already had, which
is what happens when a design is written from the coverage numbers rather than
from the test code.

Only mechanism 3 was new. `exec.Scripted` answers on a substring of the command
line and records what it was asked — no expectation ordering, no verify phase,
nothing that turns a failed test into a puzzle about the mock. It took two
packages from 37% to comfortably above the floor, and it is what makes
`systemd` testable at all: that adapter needs a real init system and root, so
the acceptance run cannot reach it either.

### The scripted runner tests the reading, not the running

What `systemd` and `compose` mostly do is decide what a tool's output *meant*.
The interesting answers are the ones a healthy machine never gives: a container
that exited 137, `compose ps` in the JSON-array shape rather than the
newline-delimited one, `systemctl show` reporting a unit that failed. Those are
now pinned, and none of them were reachable before.

### Four test expectations were wrong, and each was checked before changing

`missingkey=error` fires before a `required` helper can run, so `required` is
for a declared-but-empty value rather than an absent one. `checkServices`
reports `warn` rather than `fail` — services being down is a finding, not a
failure of `doctor`. The backup check is `backup.freshness`, not
`backup.recent`. And under `--verbose`, events legitimately appear *inside* the
JSON envelope; the contract is one object on stdout, not the absence of
narration from it.

Each was read out of the code before the test was adjusted, which is the rule
that keeps this exercise from transcribing bugs into assertions.

## 15. Amendments from the P3 and P4 completion pass

P3 and P4 are now complete as §5.3 and §5.4 defined them. Every row of §5.4's
table has a real service behind it, and all three of §5.3's mechanisms are in
use. What follows is where execution diverged, and what it cost.

### The container suites are behind a build tag, not a skip

Decision 4 said the real-service suites would follow the existing contract
pattern exactly: skip when Docker is absent, with `contract-strict` failing on
a skip. **They do not skip at all.** The `docker` build tag is the opt-in, and
once it is set a missing daemon is a `t.Fatal`.

The reason is that the skip-plus-strict-recipe arrangement solves a problem the
tag does not have. A skip is dangerous because it is invisible: a suite that
quietly stopped running looks identical to one that passed, which is why
`contract-strict` exists to grep for `--- SKIP`. A build tag makes the same
guarantee structurally — the suite either compiled and ran, or was never asked
for. There is no third state to police, and `just test-docker` is one recipe
rather than a recipe plus a grep plus a rule about what the grep means.

The cost is that `just test` no longer runs everything, which §5.4 wanted
anyway: a contributor without Docker keeps a fast loop, and CI runs
`test-docker-cover` in its own job alongside acceptance.

### What only a real service could answer

The five things a fake could not have told us:

- **`docker compose config` does not list profiled services.** The plan view is
  therefore correct to omit one-shot migrations, which nobody had written down.
- **`down` really does preserve a named volume, and `down --volumes` really
  does destroy it.** The fake could only report that the flag was passed. This
  is the claim every compensation path rests on, and it is now checked by
  writing a byte and looking for it afterwards.
- **`pg_isready` without `-h` reports the entrypoint's throwaway initialisation
  server as ready**, moments before it shuts down to hand over to the real one.
  Any readiness check that does not probe TCP races it.
- **`RunOneShot` cannot distinguish a misnamed service from a failed
  migration.** Compose reports both by exiting non-zero. Exit 1 rather than the
  ABI's 2 is what keeps a typo from being read as "nothing to do", and that is
  now a test rather than an assumption.
- **`ProbeRegistry` cannot probe a plaintext registry.** `docker manifest
  inspect` speaks HTTPS unless given `--insecure`, which the adapter
  deliberately never passes. The clean-probe path is therefore not covered by
  any test that can run without reconfiguring the daemon; the three failure
  classifications, which carry the real consequences, are.

### Three more expectations of mine were wrong

The same rule as the P3/P4 pass: read the code, then change the test.

- **A compensated operation exits `compensated`, not the cause's own code.**
  What an operator needs first is whether the system was put back; the cause
  travels in the message. A failure before any compensable step keeps its own
  code, which is the distinction the test now pins.
- **Retention failing is `Continue` by design.** The backup has already been
  taken and verified, so a full disk must not turn a good backup into a failed
  operation. The assertion is that the failure survives in the record — an
  operator whose retention has been silently failing for a month finds out when
  the disk fills.
- **`age.ParseIdentities` refuses a file with no key lines** rather than
  returning an empty slice, so `PublicKeyFromIdentityFile`'s "contains no keys"
  branch is unreachable today. Left in as defence against a future parser, and
  recorded as unreachable rather than quietly counted.

### One asymmetry found, and left alone

`config set typo=1` is refused by name. `config unset typo` succeeds quietly,
because the merge treats "not recorded" as "already at its default" without
asking whether the release declares the name at all. An operator who mistypes
an unset is told it worked.

Not fixed here: this RFC is a testing programme, and changing a refusal is a
behaviour change that belongs in its own pull request. It is recorded in
[what-is-tested](../pages/docs/explanation/what-is-tested.md) under *what is
not claimed*, and pinned by a test that fails if it ever becomes a refusal.

## 16. What remains, measured

§13 superseded. Measured on the same tree as the floors:

| | At the RFC | After P3/P4 (§13) | Now |
| --- | --- | --- | --- |
| `go test` alone | 59.5% | 75.4% | **79.6%** |
| The container suites alone | — | — | **63.0%** |
| The acceptance run alone | 47.6% | 47.6% | **47.3%** |
| Union of all three | 70.0% | 77.4% | **81.6%** |
| Uncovered statements (union) | 2331 | 1770 | **1438** |

Floors are 79 (unit) and 81 (union), ratcheted in the same change that raised
them. Nothing is below 65% any more; at the RFC, four packages were under 40%.

**1438 statements stand between here and 95%, and 404 of them — 28% — are in
`internal/cli`**, which is 65% covered and has not moved since §13. That is now
the single largest block by a wide margin, and it is the one this pass did not
touch.

| Package | Union | Uncovered | Needs |
| --- | --- | --- | --- |
| `internal/cli` | 65% | 404 | the interactive `init` wizard, and the flag-combination paths the acceptance run does not take |
| `internal/lifecycle/ops` | 80% | 331 | the step bodies each operation only reaches under a specific failure |
| `internal/domain` | 87% | 102 | error-formatting branches |
| `internal/adapters/secrets/sopsage` | 78% | 82 | sops failure modes: a wrong key, a truncated file, a binary that exits mid-write |
| `internal/infra/atomicfs` | 82% | 70 | archive extraction failures, which need a corrupt `tar.zst` per branch |
| `internal/ui/tty` | 85% | 57 | terminal resize and interrupt handling under `teatest` |
| `internal/adapters/backup/hookbackup` | 77% | 48 | the manifest-reading branches, reachable with hand-written backup directories |
| `internal/adapters/source/oci` | 71% | 44 | a bundle pushed as an OCI artifact to the registry `dockerlab` already starts |

**95% is not met, and this RFC does not claim it.** Decision 7 said that if the
last statements turn out to be unreachable defensive code, the floor stops
below 95% with the measured reason. That is not yet the finding here: of the
1438, only a handful are genuinely unreachable — the `contains no keys` branch
in §15, `randomSuffix`'s fallback for a `crypto/rand` that cannot fail. The
rest is reachable work that has not been done, and saying so is more useful
than a number.

The honest summary of the programme so far: **70.0% → 81.6%**, every claim in
the inventory backed by a test that was verified by perturbation, and one real
security defect found (§12, `redactAttr`). The percentage is the proxy; the
inventory is the thing; and the remaining 13.4 points are named above rather
than waved at.

## 17. P6–P11: the plan to 95%

### 17.1 The arithmetic

95% of 7815 statements is 7425 covered. 6377 are covered today, so **1048 more
have to be, and 391 may be left**. That is the whole budget, and everything
below is sized against it.

### 17.2 The shape of what remains, which decides the plan

| | |
| --- | --- |
| Uncovered statements | 1438 |
| ...in blocks | 1225 (mean **1.17** statements each) |
| ...across functions | 468 (mean **3.07** each) |

| Uncovered per function | Statements |
| --- | --- |
| 1–3 | 572 |
| 4–9 | 507 |
| 10–19 | 304 |
| 20 or more | **55** |

**There is no large win left.** Fifty-five statements — under 4% of the gap —
live in functions with twenty or more uncovered. The rest is 468 small pieces
of work, and the only thing that makes it tractable is that they cluster by the
*mechanism* each needs rather than by package. That is what the phases below
group on.

By shape, classified from the profile:

| Shape | Statements | Share |
| --- | --- | --- |
| `if err != nil` after a call that has not failed in a test | 538 | 37% |
| A conditional whose other arm nothing takes | 417 | 29% |
| Straight-line code in a function nothing calls | 366 | 26% |
| A `switch` arm never selected | 81 | 6% |
| Loop bodies and explicit refusals | 36 | 2% |

The 366 straight-line statements are the encouraging number: they are whole
functions and whole branches that nothing drives, which is fixture work rather
than fault-injection work. 67 functions are at 0.0%.

### 17.3 What is genuinely out of reach

Measured, not estimated. Statements sitting behind a call that cannot fail at
that call site:

| Statements | Why |
| --- | --- |
| 16 | marshalling a struct the manager itself defines |
| 5 | a write to an in-memory buffer |
| 4 | `filepath.Abs`, which fails only if the working directory has been unlinked |
| 3 | `crypto/rand`, which does not fail on any supported platform |
| 2 | parsing a constant template; `filepath.Rel` on two absolute paths |
| **30** | **total** |

Add roughly 25 more in `systemd.Start` and `systemd.Units`, which need a real
init system and root — the acceptance runner has neither, and giving it both
would mean the test suite could stop and start units on the developer's own
machine.

**So about 55 statements are permanently out, against an allowance of 391.**
The remaining ~336 of slack is what absorbs estimation error. It is not
comfortable, and §17.10 says what happens when it runs out.

### 17.4 The mechanisms, and which already exist

| Mechanism | Exists? | Reaches |
| --- | --- | --- |
| `exec.Scripted` — a runner whose replies are written in advance | **yes**, P3 | sops failure modes, tool probes |
| `fakes.Runtime/Backup/Secrets/Supervisor.Fail` | **yes** | operation step bodies |
| `fakes.Renderer.Err` | **yes** | configuration rendering failures |
| Real hostile filesystems (unwritable, mode 0000, a file where a directory belongs) | **yes**, P3 | state, lock, atomicfs, ops |
| `dockerlab` containers, registry included | **yes**, P4 | the OCI source |
| `teatest` | **yes**, RFC 0002 | the renderer's program lifecycle |
| `clitest` | **yes**, P2 | every command, once its fixtures are richer |
| **huh accessible mode + an injected reader** | **no** | the `init` wizard, the editor, the password prompt |
| **A corrupt-archive fixture builder** | **no** | `ExtractTarZst`'s refusals, one archive per branch |

Two new mechanisms for 1438 statements. Everything else is fixtures and
injection through switches that are already there — which is the same finding
as §14, and the reason the phases below are sized in fixture-days rather than
in design.

### 17.5 P6 — The interactive surface (121 statements)

`runInitWizard`, `resolveRecoveryChoice`, `generateRecoveryKey`,
`defaultRecoveryKeyPath`, `readPassword`, `readSecretValue`, `readAll`,
`editSecrets`, `checkRemovals`, `editorCommand`. All at or near 0%: nothing in
any suite can answer a prompt.

**The one production change in this plan**, and the reason this phase goes
first and alone. `huh.Form` already has `WithAccessible(bool)`, `WithInput` and
`WithOutput`; in accessible mode a form is line-oriented and needs no
pseudo-terminal, because that is what a screen reader needs too. The change is
to thread the `App`'s own streams and an accessible flag into the three forms
the wizard builds, and to give `readPassword`/`readAll` the same reader instead
of `os.Stdin`.

That is a small change with a user-facing justification independent of testing:
today the wizard writes to `os.Stdout` regardless of what `--json` or a
redirect asked for, and honours accessible mode only if the ambient environment
happens to set `ACCESSIBLE`.

What the tests then assert is the wizard's actual contract: that it fills only
what the flags left empty, that a fully-specified command line runs untouched,
that each of the three recovery answers produces the right `InitOptions`, that
cancelling is `CodeInterrupted` and not a half-made installation, and that the
generated recovery key is `0400` with the "move it off this machine" warning on
stderr where a pipe cannot swallow it.

**Target: 100 of 121.** `isInteractive` and the `UserHomeDir` fallback stay
out — both need the process to be attached to a terminal or to have no home.

### 17.6 P7 — The rest of the CLI (255 statements)

`release fetch` (29), `release prune` (19), `secret recipients` (17), `secret
set` (13), `backup verify` (12), `installation import` (11), `secret generate`
(11), `secret remove` (9), `parseComponents` (14), and a long tail of flag
combinations.

No new mechanism: `clitest` drives these already, and what is missing is a
machine for them to act on. The phase builds one fixture family —

- a release store holding three versions, so `prune`, `list` and `show` have
  something to choose between;
- a populated secret set with a recovery recipient, so `recipients list`,
  `remove` and the refusals have a real set to operate on;
- two backups with different reasons, so `verify` and retention have inputs;
- an export from a second installation, so `import` has something to refuse.

— and drives every command against it in all three output modes, asserting the
exit code, the refusal that fires, and the `--json` envelope. The parity rule
from RFC 0002 decision 3 applies to each.

**Target: 215 of 255.**

### 17.7 P8 — Adapters and infrastructure (420 statements)

The largest block, and four independent pieces:

**sops failure modes (82).** `sopsage` takes an `exec.Runner`, so
`exec.Scripted` reaches all of it with no new machinery: a binary that is not
installed, one that exits 1 with `no matching keys`, one that writes half a
file and dies, one that prints a deprecation warning to stderr and exits zero.
`decryptError`'s classification is the point — "wrong key" and "corrupt file"
send an operator to different places.

**A corrupt-archive fixture builder (70).** `ExtractTarZst` refuses a path that
escapes, a symlink, a device node, an entry over the size limit, one over the
count limit, a truncated stream, and a zstd frame that is not one. Each needs
an archive built to trip exactly that branch, which is a `tar.Writer` and a
helper — the second new mechanism in this plan, and about eighty lines of it.

**The OCI source (44).** `dockerlab` already starts a registry and `oras-go` is
already a dependency, so the test can push a bundle as an OCI artifact and
resolve it back. This closes the last transport with no real-service test.

**The remainder (~224):** hand-written backup directories for `hookbackup`
(48), the HTTPS retry and limit paths (18), `checksum` and `minisign` (~25),
the source registry and its cache (~30), and the leftovers in `exec`,
`logging`, `lock` and `preflight`.

**Target: 320 of 420**, with systemd's privileged half excluded by name.

### 17.8 P9 — Operation steps (331 statements)

Spread over about eighty functions, mean four each: `stepInstallUnits` (14),
`stepRenderConfiguration` (12), `stepMigrate` (11), `stepInitSecrets` (10),
`recovery.Export` (10), `doctor.checkUnits` (9), `stepStageRelease` (9),
`findResumable` (7), and a long tail.

Injection at the three ports the harness does not yet fail — `Supervisor.Fail`
and `Renderer.Err` both already exist, and the state store fails for real when
its directory is made read-only — plus three fixtures the suite has never
built: a journal with an unfinished operation for `--resume`, an installation
export/import round trip on a rebuilt machine, and a host with a supervisor
present so the unit-installation steps run at all.

**Target: 265 of 331.**

### 17.9 P10 and P11 — the renderer (85) and the remainder (226)

**P10.** `teatest` already drives the models; what is uncovered is the program
*lifecycle* — `tty.Run`'s handover on a display failure (17), `Watch` (5 plus
its update loop), `Model.Subscribe` and the three accessors — and
`cli/render.go`'s `runPlan`, `watchStatus` and `runLive`, which are the three
places the CLI hands control to it. **Target: 65 of 85.**

**P11.** `Manifest.MarshalJSON`/`UnmarshalJSON`, `ParameterSpec.Require` and
`ValidateAgainst`, `Secret.MarshalYAML` and `RevealAll`, `Paths.LockFile` and
`PreviousLink`, the engine and step accessors, `release.Load`'s last refusals,
and the leftovers in `ui/plain`, `ui/theme` and `events`. Almost all of it is
one to three statements per function, and almost all of it is a serialisation
contract somebody will eventually depend on. **Target: 190 of 226.**

### 17.10 Whether this actually reaches 95%

| Phase | Statements available | Target |
| --- | --- | --- |
| P6 interactive surface | 121 | 100 |
| P7 the rest of the CLI | 255 | 215 |
| P8 adapters and infrastructure | 420 | 320 |
| P9 operation steps | 331 | 265 |
| P10 the renderer | 85 | 65 |
| P11 domain, ports, remainder | 226 | 190 |
| **Total** | **1438** | **1155** |

1048 are needed. **The margin is 107 statements — about 1.4 points.**

That is thin, and stating it plainly is the point: if two phases each come in
15% under target, 95% is not reached. The estimates are honest but they are
estimates, made by reading 468 functions and judging what a test could drive.

If it comes to that, decision 7 governs and the floor stops where the testing
actually is, with this table amended to say which phase fell short and why. A
number met by writing tests that execute lines without asserting on them would
be worse than an honest 93%, and decision 6 forbids it regardless.

### 17.11 Decisions

Appended to §10; the existing eight stand.

| # | Decision |
| --- | --- |
| 9 | 95% is measured against the union of all three profiles, on the tree the phases were sized against. If the program grows, the target grows with it — these are phases, not a budget to spend down. |
| 10 | Test scaffolding does not belong in the denominator. `internal/infra/exec/scripted.go` moves under `test/` during P8. Removing 47 statements of which 39 were covered moves the ratio by nothing, and it is not reported as progress. |
| 11 | Each phase ships its own floor ratchet, measured, in the same change. A phase that lands without moving the floor has not been measured, and an unmeasured phase is a claim. |
| 12 | P6 is the only phase permitted to change shipping code, and it does so for a reason that stands without the tests: the wizard should honour the streams and the accessibility mode the rest of the CLI already does. Any other testability refactor is out of scope — the plan is to test what is there, not to reshape it until it is easy. |

### 17.12 Risks

**The interactive refactor is the only real one.** Everything else is fixtures
against mechanisms that already exist and have already paid. P6 touches the
first-run path, which is the one path an operator meets before they trust the
tool, and a regression there is expensive. It goes first, alone, and its
acceptance criterion is that the wizard's output is byte-identical at a
terminal before and after.

**The long tail is the slow one.** 468 functions at three statements each is
not hard, it is long, and the temptation at statement 1100 will be to write
something that executes a line and asserts nothing. Decision 6 exists for that
moment, and §17.10's margin is deliberately published so that giving in shows
up as a number rather than as a feeling.

**Coverage still is not proof.** The claims inventory is at 73 rows and it is
the deliverable; 95% is the proxy. If the two ever conflict — and P11 is where
they might, since a `MarshalJSON` with a round-trip test raises the number
without protecting anything an operator cares about — decision 8 already says
which one wins.

## 18. Amendments from the P6–P11 pass

All six phases ran. What follows is where each landed, what it found, and the
one phase that did not finish.

### 18.1 P6 — the interactive surface

Shipped as designed. `huh.Form` gained the App's own streams and huh's
accessible renderer, and the wizard is now driven line by line with no
pseudo-terminal. `readSecretValue`, `readAll` and `readPassword` take the App's
reader instead of `os.Stdin`.

**Three findings from huh's accessible renderer**, all upstream and all
recorded rather than worked around:

- **It builds a fresh `bufio.Scanner` per field.** The first field's scanner
  buffers the whole reader, so every later field sees EOF. A test fixture that
  hands back one line per `Read` is the faithful shape — that is what a
  terminal does — and a single `strings.Reader` silently answers only the first
  question.
- **It ignores the context and discards each field's error.** huh's own source
  says "no way to bubble up errors or signal cancellation". So ctrl-D during
  the wizard completes the form with defaults rather than aborting, and the
  three `Interrupted` branches are reachable only at a real terminal. What
  makes it survivable is what the defaults are, which is now asserted: walking
  away from the recovery question **generates** a key rather than waiving one.
- **It prints titles and drops descriptions.** A screen-reader user never heard
  "Without one, losing this machine loses its secrets permanently." Fixed for
  that one question by moving the consequence into the title; the general
  limitation is recorded, because rewriting every description into a title
  would be working around huh in our own text.

### 18.2 P7 — the rest of the CLI

Shipped. One fixture family — a release store with three versions, a populated
secret set, an offline recipient, an export from a second machine — and the
commands driven against it.

Four expectations of mine were wrong, and each is a decision worth having
written down:

- **`secret generate` on an existing secret is refused.** `rotate` is the
  deliberate replacement; generating again would silently replace a live
  credential.
- **`generate-recovery-key` does not add the key as a recipient.** Two
  decisions — make me a key, let it read this state — and running them together
  would mean a key written to the wrong path silently became a recipient.
- **`secret list` on a machine with no installation succeeds**, answering
  "nothing". Refusing would make it unusable as the first thing anybody types.
- **`secret remove` of something absent is a no-op**, because delete is
  idempotent and refusing would break a clean-up script's second run.

### 18.3 P8 — adapters and infrastructure (partial)

**Two of three pieces shipped.** sops failure modes through `exec.Scripted`,
and one archive per refusal in `ExtractTarZst`.

**The OCI source was not done.** 44 statements, and it is the last transport
with no real-service test. `dockerlab` already starts a registry and `oras-go`
is already a dependency, so nothing blocks it but the work.

Two findings:

- **`exec.Scripted.OnExit` was unfaithful.** It set an exit code but returned
  no error, while the real runner returns an `*ExitError`. Every adapter that
  classifies a failure does it with `errors.As`, so the first test written
  against it passed while the adapter treated the failure as a *success*. Fixed
  in the fake, and the comment now says why. This is the fake-that-lies hazard
  the contract-suite discipline exists to catch, found in the fake this
  programme itself added.
- **`archive/tar` will not write a lying archive.** `extractFile` bounds the
  copy as well as checking the declared size, because a header can claim one
  byte and the stream carry a gigabyte — and the fixture for it cannot be built
  without hand-rolling tar records. That bound is asserted by reading, not by
  running, and says so in the test file.

### 18.4 P9 — operation steps

Shipped. Supervisor and renderer failures, a state directory made read-only for
real, and a host with a supervisor present — which is what made
`stepInstallUnits` reachable at all.

**One real gap found.** `preflight.NoUnfinishedOperation` exists, is documented
as refusing to start while a previous operation is flagged, and explains why:
"proceeding over an unfinished operation would layer new changes on a state
nobody has confirmed, which is exactly how a recoverable failure becomes an
unrecoverable one."

**Nothing calls it.** No operation's preflight includes it, so `apply` runs
straight over an operation that asked for a human. Not fixed here — wiring a
new refusal into every mutating operation is a behaviour change and belongs in
its own pull request — and pinned by a test that fails the day it is wired.

That is the second claim-without-enforcement this programme has found, after
`redactAttr` in §12.

### 18.5 P10 and P11

**P10** covered `tty.Run`'s lifecycle and `Watch`, and found two things about
driving Bubble Tea from a test:

- Its cancel reader registers the input with epoll, and `/dev/null` cannot be
  registered. A pipe is the input a test must hand it, or the program fails to
  start for a reason unrelated to what is under test.
- **Tearing down a program that was given an input races inside the library**:
  `cancelreader.epollCancelReader.Close` closes the file while its own wait
  goroutine calls `File.Fd()` on it. The two `Watch` lifecycle tests are
  therefore behind `//go:build !race` with the reason written above them. The
  watch model's own behaviour is covered without a program and runs under
  `-race` with everything else.

**P11** covered the serialisation contracts. One finding: **`ByteSize` does not
round-trip decimal units.** `5GB` marshals as `4.7GiB` and parses back to
5046586572 rather than 5000000000. Harmless today because sizes are read from a
manifest and never written back, and recorded as a test that fails if that
stops being true.

### 18.6 Measured

| | At the RFC | Before P6 | After P11 |
| --- | --- | --- | --- |
| `go test` alone | 59.5% | 79.6% | **84.6%** |
| The container suites alone | — | 63.0% | **66.4%** |
| The acceptance run alone | 47.6% | 47.3% | 47.2% |
| Union of all three | 70.0% | 81.6% | **86.1%** |
| Uncovered statements (union) | 2331 | 1438 | **1090** |

Per phase, `go test` alone: P6 80.8%, P7 82.5%, P8 83.7%, P9 84.3%, P10/P11
84.6%. Floors ratcheted to 84 (unit) and 86 (union), and `just ci` is green
at both.

**95% was not reached.** 1090 statements are still uncovered against an
allowance of 391, so the target is 699 short.

§17.10 said the margin was 107 statements over the 1048 needed, and that two
phases underdelivering by 15% would sink it. What happened was worse and
simpler: the phases covered 348 of the 1155 they targeted. The estimates were
not 15% optimistic, they were roughly three times optimistic — made by reading
468 functions and judging what a test *could* drive, without weighing how many
of them share a single hard prerequisite. `internal/cli` and `lifecycle/ops`
between them still hold most of the gap, and most of that needs a running
deployment rather than another fixture.

That is the correction worth carrying forward: sizing a coverage phase by
counting statements underestimates it whenever the statements cluster behind
one thing that is hard to arrange.

Decision 7 governs and the floor stops where the testing is. What remains is
still named rather than waved at: the OCI source, the CLI paths that need a
running deployment, and the long tail of one-to-three-statement error branches
that P9 and P11 thinned without clearing.
