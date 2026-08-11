# RFC 0020 — Several installations on one machine

- **Status:** ✅ Complete — **P2–P4 shipped 2026-08-11**: `morzer ls` (and
  `installation list`) reads every installation's state through one layout per
  product, `--status` adds a bounded runtime query per row, and `doctor` gained
  `machine.installations` and `machine.ports`. §9's open question is settled the
  third way: every command declares its scope where it is registered, and two
  tests over the real command tree carry it — one refuses a command that declares
  nothing, the other runs the refusal against every command there is. That closed
  the hole the risk named, where `release list` on a machine with three
  installations answered `no releases are installed` about a layout nobody has.
  `MORZER_PRODUCT` ships with the precedence recorded below. Divergences from
  this document are recorded in §13. **P1 shipped 2026-08-10**: discovery returns the
  inventory it always computed, `Deps.MachineProducts` carries it, and a machine
  whose installations nobody chose between is refused by name instead of being
  reported as bare. Both flags record the inventory, including `--config`, which
  is the one the systemd units pass. The refusal lives in the lookup rather than
  in path resolution, so `version`, `release verify` and `init` still work on a
  machine with three installations — but it is asked *before* the state is read,
  not after the read fails: the placeholder product is `morzer`, the manager's
  own name and so the likeliest name for a real installation, and where one
  existed the fallback silently selected it. `doctor` asks the same question
  first, since degrading to a reduced check list is what made it the one command
  that swallowed the refusal.
- **Scope:** Making the multi-installation case — which the path layout has
  always supported and no command has ever acknowledged — visible and safe:
  `morzer ls`, a refusal that names the installations it found instead of
  inventing a product that does not exist, and one documented way to say which
  installation a command means. Touches `internal/cli/root.go`'s product
  resolution, adds one lifecycle operation and one command, and changes no path,
  no state file and no port. Deliberately *not* isolation work: two installations
  on one machine already share a Docker daemon, a port space and a filesystem,
  and this RFC does not pretend otherwise — it makes the sharing legible.
- **Related:** [`internal/cli/root.go`](../internal/cli/root.go)
  (`resolvePaths`, `discoverProduct`, `pathsFromConfig`),
  [`internal/domain/paths.go`](../internal/domain/paths.go),
  [`internal/adapters/supervisor/systemd/systemd.go`](../internal/adapters/supervisor/systemd/systemd.go)
  (`UnitNames`),
  [0019](0019-the-command-surface.md) (where `ls` lands in `--help`),
  [0003](0003-secrets-recovery-and-onboarding.md) (`installation export`/`import`, the
  existing `installation` noun)

---

## 1. Summary

A machine can run several installations today: every path is keyed by product
(`/etc/demo`, `/var/lib/demo`, `/opt/demo`), the deployment lock is under the
product's manager directory, and systemd units are named per product. What is
missing is any command that says so.

Three things ship:

**`morzer ls`** lists the installations on the machine — product, release,
schema version, mode, whether units are installed, and the path — from the state
files alone, with no Docker call. `--status` adds what is running, at the cost of
one runtime query per installation.

**A refusal that names them.** With two installations and no `--product`, the
manager today invents the product `morzer`, finds nothing at `/etc/morzer`, and
tells the operator to run `morzer init` — advice that would create a third
installation. It will instead name the installations it found and how to select
one.

**One documented way to select.** `--product`, `--config` and the
`MORZER_PRODUCT` environment variable, with the precedence stated and the
conflicts refused rather than resolved silently — the last of which is already
true and undocumented.

## 2. Motivation

### The manager's answer to "which one?" is to invent a fourth

Reproduced against the current binary, two installations present:

```text
$ ls $ROOT/etc
demo  other
$ morzer --root $ROOT status
error: no installation found at $ROOT/etc/morzer
hint:  run `morzer init` to create one
```

Both halves are wrong. There is no installation at `/etc/morzer` because there
was never going to be one — `morzer` is the placeholder `resolvePaths` falls back
to when discovery finds anything other than exactly one installation
([`root.go:849`](../internal/cli/root.go)). And the hint tells an operator whose
problem is *two* installations to create a third.

`discoverProduct` already knows the answer. It reads `/etc/*/installation.yaml`,
collects every match, and returns only when there is exactly one:

```go
// Exactly one, or the operator must say which: guessing between two
// installations is how a command acts on the wrong deployment.
if len(found) == 1 {
    return found[0], true
}
return "", false
```

The refusal to guess is right and stays. What is lost is `found` — the list is
computed, discarded, and the operator is left to run `ls /etc` themselves.

### The layout supports it, the docs describe one

Every path is product-keyed by construction (`domain.DefaultPaths(product)`), the
lock lives under the product's own manager directory, `UnitNames(product)`
namespaces all five systemd units, and the Compose project comes from the
release's own manifest. Two installations on one machine is not a scenario this
project has to build support for; it is a scenario it supports and never mentions
— the documentation is written throughout in the singular, and `--product`'s help
string ("product name (inferred from the installation when omitted)") is the only
hint that inference could fail.

### What a fleet-of-one-machine operator actually does

The comparison the request named is Porter, where multiple installations are
first-class and listing them is the entry point to everything else. The gap here
is narrower than Porter's — morzer's installations are already isolated by path —
but the entry point is missing in exactly the same way: there is no command whose
output tells you what this machine is running, and no way to get one without
knowing the layout.

## 3. Current state

| Thing | State |
| --- | --- |
| Path layout | `/etc/<product>`, `/var/lib/<product>`, `/run/<product>`, `/opt/<product>`; `PathsUnder(root, product)` for tests and `--root` |
| Discovery | `discoverProduct(root)` scans `<root>/etc/*/installation.yaml`, returns a product only when exactly one matches |
| Fallback | product `"morzer"`, so `init`, `version` and `doctor` work on a bare machine |
| Selection | `--product`, `--config <path>/installation.yaml` (the layout is derived from the path), `--root` (hidden, for tests) |
| Conflicts | `--product` vs `--config`, and `--root` vs `--config`, are refused with "name different installations" — undocumented |
| Lock | `<var>/morzer/locks/deployment` — per product, so two installations never block each other |
| Units | Five per product, all name-spaced: `<product>.service`, `<product>-backup.{service,timer}`, `<product>-update.{service,timer}` |
| Compose project | `manifest.runtime.project`, defaulting to `metadata.name` |
| `installation` command | `export` and `import` only |
| Environment | No `MORZER_*` variable selects an installation. One exists for an unrelated purpose (`MORZER_VOLUME_HELPER_IMAGE`, the volume helper's image), so the prefix is established; the hook ABI's `<PRODUCT>_*` variables run the other way, from the manager *to* hooks |

The load-bearing fact: **nothing needs to be re-keyed.** This RFC adds a reader
over a layout that is already correct.

## 4. Goals / Non-goals

**Goals**

- One command that answers "what is installed on this machine".
- A refusal, when the answer is ambiguous, that names the candidates and the flag
  that resolves them.
- Documented selection precedence, including the conflicts that are already
  refused.
- `ls` works on a machine with zero installations, and says so.

**Non-goals**

- **Isolation between installations.** They share a Docker daemon, a port space,
  a `/run` tmpfs and a disk. Making them not share those is a container-per-
  installation design that changes what this product is. *What would change
  this:* a customer running two installations that collide on a published port,
  which is a `doctor` check (below), not an isolation feature.
- **A machine-level registry file.** The filesystem is the registry:
  `/etc/*/installation.yaml` is the truth, and a second index would be a second
  thing to keep in sync, wrong exactly when a machine was rebuilt by hand.
- **Cross-installation operations.** No `morzer update --all`. Each installation
  has its own release, its own compatibility gate and its own downtime; a command
  that updated three at once would be a loop with one exit code and no way to say
  which one failed. *What would change this:* nothing in the near term; the
  loop belongs in the operator's shell where its failure handling is theirs.
- **Renaming an installation.** The product name is the path, the Compose
  project, the unit names and the secret recipients' comment; renaming is a
  migration, not a flag.

## 5. Design

### 5.1 `morzer ls`

```text
$ morzer ls
PRODUCT   RELEASE   MODE         UNITS  PATH
demo      1.4.0     production   5      /etc/demo
sandbox   1.5.0-rc1 dev          0      /etc/sandbox
```

Read entirely from state files: the installation (product, schema version, mode)
and the current-release record (version). No Docker call, no lock taken, no
network — so it answers on a machine whose daemon is down, which is exactly when
an operator is trying to find out what is on the box.

```go
// InstallationEntry is one installation as `ls` reports it.
type InstallationEntry struct {
    Product       string         `json:"product"`
    Path          string         `json:"path"`
    SchemaVersion int            `json:"schema_version"`
    Mode          domain.Mode    `json:"mode,omitempty"`
    Release       domain.Version `json:"release,omitzero"`
    Units         int            `json:"units"`

    // Problem is why this row is incomplete, when it is. An installation
    // whose state will not parse is still an installation, and the one
    // thing an operator must not be told is that it is absent. A
    // schema_version this manager is too old to read is one of these:
    // refusing to interpret it is the same rule LoadInstallation already
    // applies, and a partially-read future installation reported as fact
    // would be worse than a row that says "I cannot read this".
    Problem string `json:"problem,omitempty"`

    // Services is what --status found, and nil when it was not asked for.
    // ServicesProblem is per row rather than per command: one wedged
    // daemon must not blank the column for every other installation.
    Services        *ServiceCounts `json:"services,omitempty"`
    ServicesProblem string         `json:"services_problem,omitempty"`
}

// ServiceCounts is the summary --status adds, not the full service list:
// `morzer status --product X` is where the detail lives, and a listing that
// reprinted it would be a worse version of that command.
type ServiceCounts struct {
    Running int `json:"running"`
    Total   int `json:"total"`
}

// ListOptions is one flag today and a struct anyway: `--status` is the
// difference between a listing that reads files and one that talks to a
// daemon, and a bool parameter at the call site would not say which.
type ListOptions struct {
    Status        bool
    StatusTimeout time.Duration // per installation; 5s when zero
}

func ListInstallations(ctx context.Context, d *Deps, opts ListOptions) ([]InstallationEntry, error)
```

`SchemaVersion` is JSON-only: it is what a support engineer reads out of
`--json`, and a `SCHEMA` column in the human table would spend width on a number
that matters on one day in a hundred. The example above is the human contract and
has no such column, deliberately.

Failure semantics, which are most of the design:

- **A state file that will not parse is a row with a `Problem`, not a skipped
  directory.** The alternative — dropping it — makes `ls` say the installation is
  gone at the moment its state is broken, which is the moment `ls` is being run.
- **An unreadable `/etc` is an error**, not an empty list. "I cannot look" and
  "there is nothing there" are different answers and this project refuses to
  conflate them elsewhere (RFC 0010's rule about predicates that fail safe).
- **Zero installations prints one line saying so**, and exits 0. A bare machine
  is a normal state, not a failure.

`--status` adds a `SERVICES` column by asking the runtime per installation.
Separate flag rather than default because it costs a Docker call per row and can
hang on a wedged daemon — the cheap answer must stay cheap.

Each query is bounded: **5 seconds per installation**, and the whole command by
`--timeout` as every other command already is. A row whose query times out prints
the timeout in its services column and the rest of the row is unaffected — one
wedged daemon must not turn a machine listing into a hang, which is the failure
mode that makes people stop using a listing command.

**Naming.** `ls` at the top level, with `installation list` as an alias. The
short name is what an operator types when they have just logged into an unfamiliar
machine; the long name is where it belongs in the noun hierarchy, and RFC
0019's grouping puts both in "Machine".

### 5.2 The ambiguous refusal

`resolvePaths` currently discards the candidate list. Instead:

```text
$ morzer status
error: this machine has 2 installations, so --product is required
hint:  demo, sandbox — pass `--product demo`, or `--config /etc/demo/installation.yaml`;
       `morzer ls` lists them
```

The fallback to the placeholder `morzer` product stays for the zero-installation
case, because `init`, `version` and `doctor` must work on a bare machine. What
changes is that "more than one" stops taking the same branch as "none": today
they are indistinguishable to `resolvePaths`, and that is the whole defect.

**The placeholder is a real name, which is why the count is asked before the
path is used.** `morzer` is the manager's own name and so the likeliest name for
an installation somebody creates; on a machine holding `morzer` and `demo` the
fallback stopped being a placeholder and became a silent choice, with `status`
loading whichever installation happened to be called `morzer` and reporting on
it. A refusal that fires only when the read *fails* cannot catch that, because
the read succeeds — so the question "did anybody choose?" is asked at the top of
the lookup, and the questions that need a failed read stay below it.

Commands that do not act on an installation — `version`, `release verify`,
`release new`, `release pack`, `completion` — must not be refused by this. They
are already the commands that work on a bare machine, so the rule is: the refusal
fires when a command resolves paths in order to *use* them, which is the same
point at which "no installation found" fires today.

**`doctor` is the interesting case, because it is both.** Its machine-scope
checks (§5.4) are about the machine and need no selection; every other check is
about one installation and needs one. So `morzer doctor` on an ambiguous machine
runs the machine-scope checks, reports them, and then refuses the rest by name —
rather than refusing wholesale, which would take away the diagnostic exactly when
the diagnosis is "you have two installations". `morzer doctor --product demo`
runs everything.

### 5.3 Selection, with a precedence nobody has to guess

| Source | Precedence | Notes |
| --- | --- | --- |
| `--config <path>` | 1 — **mutually exclusive with `--product`** | Selects the layout the file sits in; what generated systemd units pass |
| `--product <name>` | 1 — **mutually exclusive with `--config`** | With `--root` when relocating the layout |
| `MORZER_PRODUCT` | 2 | New. For a shell session pinned to one installation |
| Discovery | 3 | Only when exactly one installation exists |

The two flags share a rank because there is no rank between them: **passing both
is refused**, never resolved by precedence. That is already the behaviour
(`pathsFromConfig` and `confirmProductMatchesConfig`) and is written down here for
the first time — with the exception the code also already makes, which a table
reading "`--config` wins" would lose: when both are given and they name the *same*
product, the command proceeds. An operator who spelled one deployment two ways
has said one thing twice.

`MORZER_PRODUCT` is not in the exclusion. An environment variable a flag
overrides is the ordinary shape, and refusing it would make the variable useless
in exactly the case it exists for: a shell session pinned to one installation
where a single command needs another.

**Alternatives considered.** *A `morzer use <product>` that writes a current
selection somewhere.* Rejected: it is `kubectl`'s context, and its failure mode is
the reason operators wrap `kubectl` in scripts that print the context in the
prompt — a mutable global that decides which machine a destructive command hits.
An environment variable is scoped to the shell that set it and dies with it.

### 5.4 `doctor` learns the machine has neighbours

Two checks, both read-only and both about the sharing this RFC declines to
prevent:

- **`machine.installations`** — informational: how many installations exist, and
  a warning when two are in a state that will confuse the operator (the same
  product name under two roots, an installation whose units are installed but
  whose state will not load).
- **`machine.ports`** — a warning when two installations publish the same host
  endpoint. They cannot both be running, and today the second one's `apply` fails
  inside Compose with a message about the port, not about the neighbour. The
  collision key is the **full binding — host IP, published port, protocol** — not
  the port number: `127.0.0.1:8080` and `192.168.1.5:8080` coexist, TCP and UDP
  on one port coexist, and a check that warned about either would be one an
  operator learns to ignore. An unspecified host IP collides with every specific
  one, which is the case that actually bites.

Warnings, never failures: a machine with two installations is not broken, and
`doctor` refusing to pass on a supported configuration would train operators to
ignore it.

## 6. Tests

- **Two installations, no selector** — the refusal names both, and the exit code
  is the usage one rather than "not found".
- **One installation** — discovery still works, unchanged. This is the regression
  that matters most: every existing test runs in this shape.
- **Zero installations** — `ls` prints the empty line and exits 0; `init` still
  works; `status` still says no installation found.
- **A corrupt state file** — `ls` reports the row with its `Problem` and the
  other rows are complete. Verified-red, because "skipped" and "reported" look
  identical in a one-installation fixture. The file to corrupt is
  `<var>/<product>/manager/installation.json`, the state store — *not*
  `/etc/<product>/installation.yaml`, which is a report nothing reads back
  (`root.go`, `doctor.go` and `ops.go` each say so) and whose only role here is
  making the installation discoverable. A test that corrupted the yaml would
  assert nothing.
- **A schema version from the future** — one row, `Problem` naming the version,
  and no interpreted fields beside it.
- **`--status` on a machine with no runtime** — rows still render, services
  column reports the failure per row rather than failing the command.
- **Precedence table, as a table test** — every pair of sources, asserting which
  wins and which is refused.
- **Units count** — an installation with units installed and one without, so the
  column cannot be a constant.

## 7. Docs

- `pages/docs/operating/several-installations.md` (new) — what is shared, what is
  not, how to select, and the port collision to expect.
- `pages/docs/reference/installation-commands.md` — `ls` and the precedence
  table.
- `pages/docs/get-started/installation.md` — one line: a machine may hold more
  than one.

## 8. Out of scope

- **`--all` over installations.** See non-goals; each has its own downtime.
- **Per-installation resource limits.** A Compose concern the manifest already
  exposes.
- **A `morzer top`-style live view across installations.** [0021](0021-into-the-running-deployment.md)
  scopes what live inspection means for one installation first; doing it for
  several before doing it for one is how the one gets designed badly.

## 9. Risks

- **`ls` implies management the product does not offer.** An operator who sees a
  list expects `morzer update --all` next. Mitigated by saying so in the page:
  the list is a map of the machine, and each installation is operated on its own
  terms. The non-goal is recorded here so the answer to the first request is a
  decision rather than a scramble.
- **The refusal changes an exit code.** A script relying on "no installation
  found" (exit 5) on an ambiguous machine gets a usage error instead. This is a
  script that was acting on the wrong installation or none; the change is the
  point, and it is called out in the changelog.
- **It covers exactly the commands that already refused, which is not all of
  them.** The refusal fires where the installation is read, so `status`,
  `doctor`, `update`, `backup` and the rest are covered. `release list` and
  `secret list` read the store and the secret state without loading the
  installation, so on an ambiguous machine they answer about the placeholder
  layout — `no releases are installed` — exactly as they do on a bare machine.
  Measured rather than assumed: that is what
  `morzer --root <two-installation-root> release list` prints today.

  Pre-existing behaviour rather than a regression, and P1 deliberately did not
  widen it: the alternatives are a maintained list of commands that need an
  installation, or refusing during path resolution, which would refuse
  `morzer version` on a machine with two installations. **P2 is where this
  should be settled**, because `ls` is what makes the inventory visible and the
  natural place to decide what every other listing does when the machine is
  ambiguous.

  **Settled in P2, by a third way.** Both alternatives above were rejected for
  the reasons given; what shipped is a *declared* scope per command
  (`machineScope` / `installationScope`, applied where the command is
  registered), asked once in the persistent pre-run. What makes that a rule
  rather than another maintained list is the pair of tests over the real cobra
  tree: `TestEveryCommandDeclaresItsScope` fails a runnable command that declares
  nothing, so a new command cannot inherit the safe default silently, and
  `TestAnAmbiguousMachineRefusesEveryInstallationCommand` runs the actual refusal
  against every command in the tree. Undeclared still *resolves* to installation
  scope — the refusal is the safe direction — but nothing reaches production
  undeclared. Cobra returns before the pre-run for a non-runnable parent and for
  `--help`, so `morzer secret` and `morzer status --help` still print their help
  on an ambiguous machine.
- **`MORZER_PRODUCT` in a systemd unit.** The generated units pass `--config`,
  which outranks it. Stated in the docs because an operator who sets the variable
  globally will otherwise expect it to reach their timers.

## 10. Unresolved questions

- **Does `ls` read the release version from the state record or from the release
  root's manifest?** The record is cheaper and is what `status` trusts; the
  manifest is the truth if the record is stale. Probably the record, with the
  disagreement reported as a `Problem` — but that is a second read per row, and
  whether it is worth it is an implementation call.

  **Settled: the record alone.** A listing is a map of the machine, and reading
  every installation's manifest to check the map against the territory is
  `doctor`'s job — it already has `runtime.release`, which reports a release root
  that disagrees with what was recorded, for the one installation being
  diagnosed. Doing it per row here would make the cheap answer expensive to buy
  a check that exists.
- **Should `--status` take the lock?** It must not — a read that blocked behind a
  running update would make `ls` useless exactly when an operator is watching one
  — but "not taking the lock" means the services column can be a moment stale.
  That is acceptable and should be said in the output's own legend, not just in
  the docs.

  **Settled: it does not, and the legend says so.** A line under the table, drawn
  only when `--status` was asked for: "services are sampled without the
  deployment lock, so a row may be a moment out of date".

## 11. Decisions

| # | Decision | Why |
| --- | --- | --- |
| 1 | The filesystem is the registry; no machine-level index file | `/etc/*/installation.yaml` is already the truth and is rebuilt correctly by `import`. An index would be a second source that is wrong exactly when a machine was recovered by hand. |
| 2 | `ls` reads state files only; `--status` opts into the runtime | The cheap answer must work when the daemon is down, which is when an operator most needs to know what is on the box. |
| 3 | An installation whose state will not parse is a row with a problem, never a skipped row | Dropping it reports the installation as absent at the exact moment its state is broken. Same rule [0010](0010-compose-volume-capture.md) reached with `OccupiesVolume`: a predicate that decides what is safe enumerates the negative. |
| 4 | Ambiguous discovery is a distinct refusal from no-installation | Today they take one branch, which is why two installations produce "run `morzer init`" — advice to create a third. |
| 5 | `--config` and `--product` are mutually exclusive and refused together unless they name the same product; `MORZER_PRODUCT` sits below both; discovery last | The refusals already exist and are undocumented. Ranking the two flags against each other would license "`--config` wins", which is the one reading the code does not implement. |
| 5a | `--status` is bounded at 5s per installation, with the timeout reported in that row | One wedged daemon must not hang a machine listing. A per-row failure keeps the other rows, which is the whole reason the column is opt-in. |
| 5b | `SchemaVersion` is JSON-only; the human table has no `SCHEMA` column | It matters on one day in a hundred, and the human table's width is the scarce thing. |
| 5c | A `schema_version` this manager cannot read is a `Problem` row, never partially interpreted fields | The same rule `LoadInstallation` already applies. Reporting a future installation's fields as fact is worse than saying it cannot be read. |
| 5d | `doctor` on an ambiguous machine runs its machine-scope checks and refuses the rest by name | Refusing wholesale removes the diagnostic exactly when the diagnosis is "you have two installations". |
| 5e | The port-collision key is host IP, published port and protocol — not the port number | `127.0.0.1:8080` and `192.168.1.5:8080` coexist, as do TCP and UDP on one port. A check that warned about those is one operators learn to ignore. |
| 6 | No `morzer use` / no persisted current context | A mutable global that decides which machine a destructive command hits. The variable dies with the shell that set it; a context file does not. |
| 7 | No cross-installation operations | Each installation has its own release, gate and downtime. A loop with one exit code cannot report which of three failed, and the operator's shell does this better. |
| 8 | Port collisions between installations are a `doctor` warning, not a refusal | Two installations on one machine is supported. Failing `doctor` on a supported configuration teaches operators to ignore `doctor`. |
| 9 | `ls` at the top level, `installation list` as the alias | The short name is what someone types on an unfamiliar machine; the long one is where the noun hierarchy puts it. Both, because the cost is one line. |

## 12. Phasing

- **P1 — The refusal.** Return the candidate list from discovery, refuse with it,
  document the precedence. Small, self-contained, and it is the actual bug: an
  operator today is given advice that would make their machine worse.
- **P2 — `ls`.** ✅ Shipped 2026-08-11. The lifecycle operation, the command, the
  JSON shape, the docs page — and §9's open question, which is what made this the
  substantial half.
- **P3 — `--status`.** ✅ Shipped 2026-08-11. Runtime query per row, with per-row
  failure and the per-row bound.
- **P4 — `doctor` checks.** ✅ Shipped 2026-08-11. `machine.installations` and
  `machine.ports`, in a new `machine` category — the only category whose subject
  is not the installation being diagnosed.

## 13. Divergences recorded during P2–P4

- **The collision key is the declared port number, not the full binding.**
  Decision 5e names host IP, published port and protocol. The manager cannot
  observe that: `requirements.ports` is a list of port numbers resolved from
  parameters, and there is no interface or protocol anywhere in what an
  installation declares. Reading the real bindings would mean a Compose render
  per installation — a subprocess per row, in a check that must work when the
  daemon is down and when a neighbour's release directory is unreadable. What
  ships compares declared numbers, which is strictly narrower and cannot produce
  the false positives 5e exists to prevent: two installations declaring 8080 do
  collide, because a declared port is a wildcard TCP binding. The full key
  becomes reachable if `requirements.ports` ever grows an address, and 5e is the
  decision to implement then.
- **A cell holds a state; a note holds the sentence.** §5.1 says a row whose
  query times out "prints the timeout in its services column". It prints
  `unknown` there and the reason under the table, with the failed rows. The
  reason is a sentence — a Docker error, a path, a schema mismatch — and a
  sentence in a column makes every row as tall as the worst line on the machine,
  which is the alignment a table exists for spent on its least useful row. The
  claim §5.1 is actually making — that the rest of the row is unaffected and one
  wedged daemon costs one row — is what the tests pin.
- **`schema_version` is `omitempty`.** §5.1's struct has it unconditional.
  Decision 5c requires a row that could not be read to carry no interpreted
  field, and `"schema_version": 0` is a claim about the schema. Absent is the
  only honest encoding, so the tag carries `omitempty` and the JSON test asserts
  the unreadable row's whole object.
- **`Deps` grew a `StateFor` seam rather than reading state directly.** The
  lifecycle layer may import `internal/infra`, so `ls` could have constructed a
  store per product itself. It takes an injected constructor instead, so the
  adapter is still named in exactly one place and a test can answer for an
  installation with no files at all. A `Deps` pointed at another installation
  drops the lock, the secret store, the backup engine, the targets and the
  notifier rather than carrying them: any of those, held for one installation and
  used against another, would act on the wrong deployment with every path in the
  log saying otherwise.
- **`--status` has no timeout flag.** Decision 5a's five seconds is a constant;
  `ListOptions.StatusTimeout` exists for the test that would otherwise take five
  seconds to prove the bound. A flag would be one more documented surface for a
  number nobody has asked to change.
- **One state-reader message was corrected on the way past.** A state file from
  a newer manager reported "installation state at … is invalid" and dropped both
  the version and the hint the inner error carried — so it read as corruption and
  sent an operator looking for a backup. Decision 5c asks the row to name the
  version, and this is where the version was being lost.
