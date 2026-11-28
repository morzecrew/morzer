# quadlet-podman-floor · RFC 0023, the quadlet runtime's Podman floor

On branch `quadlet-podman-floor` from `main` at `9f975be3`. Builds the
author's ruling of 2026-11-28 ("Ruling on the halt" in
[logs/quadlet-acceptance.md](quadlet-acceptance.md)): the quadlet runtime
needs Podman 5.3 or later, `init` refuses an older one, and `doctor`
reports the version against the floor.

**Drift count: 0.**

| Class | Test | Meaning |
|---|---|---|
| `discovery` | Could not have been known before code existed | Healthy — the spec was right to be silent |
| `spec-gap` | Could have been known; the spec was silent or at the wrong altitude | The design process missed something |
| `drift` | The spec covered it and it was built otherwise anyway | **A defect** |
| `irreducible` | No amount of design settles it | Stop and spike |

## Measured, 2026-11-28

- Podman's Quadlet reads drop-ins from v5.0.0, and a rootless one reads
  `$XDG_RUNTIME_DIR/containers/systemd` and stops checking `PublishPort=`
  for numbers from v5.3.0 (`cmd/quadlet/main.go` and
  `pkg/systemd/quadlet/quadlet.go` at each tag; the halt entry in
  `logs/quadlet-acceptance.md` cites them).
- The generator logs every file it loads before any reason, on Podman
  6.1.1 here and in v4.9.3's source (`Debugf("Loading source unit file
  %s")`, then `converting %q: %w`). A unit with `ExposeHostPort=${X}`,
  run through `QUADLET_UNIT_DIRS=<dir> podman-user-generator -dryrun
  -user`, exits 1 with six lines: two `Loading source unit file`, two
  warnings, `converting "t-bad.container": invalid port format '${X}'`,
  and `processing encountered some errors`. The adapter reported the
  first.

## Built, 2026-11-28

- `ports.ToolFloorer`, optional beside `ToolRequirer`: a runtime names
  the oldest version of a required tool it can drive. The quadlet adapter
  holds `podman` to `>=5.3.0` (`MinPodman`); Compose names none.
- `refuseUnfitRuntime` (`internal/lifecycle/ops/engine_scope.go`), which
  was `refuseForeignEngine`, refuses a tool older than its runtime's floor
  before it asks about the engine. Its call sites are unchanged: `init`,
  before anything is created, every command that takes the deployment
  lock, and `exec`. A missing tool is not refused there.
- `doctor` holds each tool to its runtime's floor with no installation,
  where an old Podman leaves quadlet out of `runtime.tools`. With an
  installation, `runtime.tool-floor` reports the version and fails while
  the tool is missing or older.
- The generator's refusal names its `converting` lines, for one unit
  alone when one is named, and falls back to the first line. The install
  hint asks for Podman 5.3.
- Docs: "Running on Podman" (before you start, `init`, `doctor`), the
  `doctor` table in the commands reference, and the manifest reference.
  No changelog entry: the runtime is unreleased, and its entry is at the
  house limits.
- Two sabotage runs, each red. Without the refusal, the ops test and the
  suite's `init` test failed; with `toolFit` ignoring the floor, those
  and the `doctor` test failed.

```divergence
decision: UNLISTED
grade: UNLISTED
class: discovery
at: 2026-11-28T13:47:55Z
attempt: 1
claim: a runtime's tool can have a floor below which the runtime cannot run at all, and the port could only name tools; ToolRequirer stays as it is, and an optional ToolFloorer beside it names the floor, which §6 counts as a capability forced by Podman's history rather than by the second adapter
evidence: internal/ports/runtime.go:524
action: decided
proposal: ASSUMED — a runtime names a floor for a tool it requires through the optional ports.ToolFloorer; init, every command that takes the deployment lock, and exec refuse a tool below it, and doctor reports it, as runtime.tool-floor with an installation and in the tool checks without one
```

```divergence
decision: UNLISTED
grade: UNLISTED
class: discovery
at: 2026-11-28T13:47:55Z
attempt: 1
claim: the Quadlet generator logs each file it loads before any reason, so a refusal reported by its first line named a file and no reason, on every Podman version
evidence: `QUADLET_UNIT_DIRS=<dir> /usr/lib/systemd/user-generators/podman-user-generator -dryrun -user` over a unit with `ExposeHostPort=${X}` — exit 1, first line `Loading source unit file …`, reason in `converting "t-bad.container": invalid port format '${X}'`
action: decided
proposal: ASSUMED — a generator refusal is reported by its `converting` lines, for the unit refused when one is named
```

**Drift count: 0.**

## Review of #450, 2026-11-28

- **A refused first `init` left a current release pointing at nothing.**
  A release declaring `quadlet` alone names its runtime once it is
  staged, so the refusal comes in `write-installation`, after
  `stage-release` has made the release current. That step's undo removed
  the release directory and left the link and the record. It did so for
  the engine-scope refusal too, and for any failure of a later step.
  Reproduced red by `TestARefusedInitOfAStagedReleaseLeavesNoCurrentRelease`.
  The undo now also removes the link and the record when they name the
  directory it removes, through a new `StateStore.ClearCurrentRelease`.
  `TestAFailedIngestDuringInitUnwindsTheInstallation` had pinned the
  residue, with a note to assert it gone once the undo learned this; it
  now does. A repair's pointer is kept: the undo touches only a
  directory its own Execute created.
- **The generator's reasons were named twice.** The exit's stderr is an
  excerpt of the run's, and both were read. Only the run's is now, and
  only a line that starts with `quadlet-generator[` has its prefix cut,
  so a reason holding `]: `, as an IPv6 address does, is kept whole.
- **The docs said Ubuntu 24.04 runs on Compose**, as though `init`
  chose it. They now say a release that also declares Compose installs
  with `--runtime compose`, and one declaring only `quadlet` is refused.
  The `doctor` table no longer says a missing tool refuses a command.
- **A version probe that fails does not refuse**, as a failed engine
  probe does not (`refuseUnfitRuntime`): the commands that need Podman
  fail on it, and `runtime.tool-floor` fails in `doctor`. Kept.

**Drift count: 0.**
