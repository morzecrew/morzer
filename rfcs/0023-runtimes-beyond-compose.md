# RFC 0023 — Runtimes beyond Compose

- **Status:** 🚧 In progress — **P1a shipped 2026-08-12**: the leak inventory
  came to 19 mentions in three classes, and `tools/runtimecheck` now enforces the
  boundary depguard cannot see. It reversed §12.2's cost estimate along the way:
  the expensive leaks are a published environment variable and a default, not a
  category. **P1b is partly answered as of 2026-08-16**: a rootless Podman host
  exists, §12 items 5 and 6 are measured against it, and item 4 is restated —
  it wanted a cold boot rather than a host, and the host it has cannot supply
  one. P1b stays open on that item alone, and **item 4 gates P3 rather than P2**:
  the claim that P1b blocked P2 was withdrawn on 2026-08-16 (D-005), so ~~**P2 is
  the next phase available**~~ and the Podman programme is not waiting on
  hardware. **P2 is complete as of 2026-08-17**: `runtime:` then named 0.4.0 as
  the release that stops reading it and warns where somebody can act on it, and
  `release new` writes the current spelling (decisions 18–19). What remains of
  this RFC is P1b item 4 and everything after it. **P1b is complete as of 2026-08-18**: item 4 was measured in a booted venue, and it answered a different question than it asked -- the file never survives a boot and never had to; the ordering decides the outcome and the `-` prefix decides whether a failure is loud or silent (decisions 21 and 22). **What remains of this RFC is P3, and it is no longer gated.** **2026-08-19:** `runtime:` stops being read in **0.3.0**, not 0.4.0 — decision 23 supersedes 18, and it withdraws a compatibility promise rather than moving a date, because no released manager reads `runtimes:` and so no version reads both spellings.
- **Scope:** Grading the `ports.Runtime` seam by writing a second implementation
  of it — rootless Podman with Quadlet — and recording every place the port had
  to change to accommodate one. Covers the manifest's runtime dimension, the
  runtime kind fixed at `init`, parameter delivery under systemd, and the
  adapter itself. Deliberately not a promise that one bundle runs unchanged on
  both runtimes (decision 4), not a translation layer between Compose and
  Quadlet, and not Kubernetes in any form.
- **Related:** [`internal/ports/runtime.go`](../internal/ports/runtime.go),
  [`internal/ports/compose_abi.go`](../internal/ports/compose_abi.go),
  [`internal/domain/manifest.go`](../internal/domain/manifest.go),
  [`internal/adapters/runtime/compose`](../internal/adapters/runtime/compose),
  [0007](0007-operator-parameters.md) (the three bundle-facing ABIs),
  [0010](0010-compose-volume-capture.md) (volume capture),
  [0011](0011-bundled-container-images.md) (in-process registry),
  [0018](0018-the-pre-1-0-manifest-surface.md) (strict decoding)
- **Origin:** Drafted 2026-08-10; adopted 2026-08-12 with §12's measurements
  taken against the code.

---

## 1. Problem

`ports.Runtime` is described in the architecture document as an interface declared
by its consumer: *"`Runtime` exists because the lifecycle layer needs to start
services, not because Compose has an API worth wrapping."* That is a claim about
where the seam sits, and it has never been tested, because the port has exactly
one implementation.

A port with one implementation is not an abstraction. It is a guess about which
of its methods describe *starting services* and which describe *Compose*, and the
guess is graded the first time someone writes the second adapter — which is
precisely the shape this project has caught twice already:
[0015](0015-notifications.md) found `Notifier` fully specified with zero
implementations, [0021](0021-into-the-running-deployment.md) found `Logs`/`Exec`
fully implemented with zero callers. This is the third variant: one
implementation, and no evidence the interface is about the right thing.

The second, weaker reason: Compose is a non-starter in a set of environments that
otherwise match this project's target exactly — RHEL and derivatives, edge
deployments, and shops where a root-owned daemon is a compliance finding. Rootless
Podman with Quadlet is the sanctioned answer there. Those are the customers who
also want signed bundles, offline install and an audit trail.

The ordering matters: **this RFC is an architecture test that produces a feature,
not a feature that stresses the architecture.** If it is run the other way round
the design will be bent to make the adapter easy, which is the outcome that
teaches nothing.

## 2. Current state

Measured 2026-08-12. The draft asserted this section from documentation; what
follows is what the code says, and it differs in one important way.

**What `depguard` actually enforces.** The draft claimed it *"guarantees the
string `docker` appears nowhere above `internal/adapters`"*. It does not, and
cannot: `depguard` is an import linter. [`.golangci.yml`](../.golangci.yml) denies
`internal/lifecycle` importing `internal/adapters`, and denies `internal/domain`
importing anything from this repository. That is a real and valuable rule about
*dependencies*. It says nothing about vocabulary, and the leak this RFC is looking
for is vocabulary.

**P1 found where the draft got that idea**, which matters more than the slip
itself: [`CONTRIBUTING.md`](../CONTRIBUTING.md) stated it as an enforced rule, in
a list introduced by *"`depguard` enforces these mechanically"*. Anybody
reasoning about this boundary would have read that first and believed it. It has
been corrected there — and the sentence is true now, because P1 built the thing
that checks it.

**The leak inventory, measured.** Compose semantics reach above
`internal/adapters` in the type system, not merely in prose:

| Where | What |
| --- | --- |
| [`internal/ports/compose_abi.go`](../internal/ports/compose_abi.go) | An entire file of the ports layer: `ComposeVars`, `ComposeVarPatterns`, `ComposeVarNames(product)`. The Compose interpolation ABI, published as a contract. |
| [`internal/ports/hooks.go`](../internal/ports/hooks.go) | `HookEnv.ComposeProject`, exported to every hook as `COMPOSE_PROJECT`. |
| [`internal/domain/manifest.go`](../internal/domain/manifest.go) | `RuntimeSpec{Project, Files, Profiles}`, the method `ComposeFiles(profile)`, and two validation messages reading *"must list at least one compose file"*. |
| [`internal/domain/release.go`](../internal/domain/release.go) | `Release.ComposeFilePaths(profile)`. |
| `internal/lifecycle/ops` | 31 further mentions, mostly prose but including the builder that produces the interpolation set. |

**One thing the draft missed, and it changes §4.1.** The manifest *already* has a
runtime name: `providers.runtime.name`, which
[`manifest.go:452`](../internal/domain/manifest.go) defaults to `"compose"`. So
the manifest is not runtime-blind; it names a runtime and then describes it in
Compose's vocabulary. The design below has to reconcile with that field rather
than introduce a second way of saying the same thing.

### 2.1 The inventory, finished (P1, 2026-08-12)

The table above was the first draft. P1 walked the AST rather than grepping, and
the result is [`tools/runtimecheck`](../tools/runtimecheck) — `just
runtime-inventory` prints it, `just runtime-check` enforces it, and the list is
not repeated here because a copy of a machine-checked list is a copy that drifts.

**The number, as measured on 2026-08-12: 19 mentions above `internal/adapters`
— 8 port-shaped, 3 Compose-shaped, 8 catalogue — and 0 branches on runtime
kind.**

Dated, because it is a measurement rather than a property: `just
runtime-inventory` is the live count, and the whole point of writing this one
down is that a later reader can see whether it fell.

Three classes, not the two P1 was asked for. The third earned its place: a
runtime named as *data* — a key in the table of tools the manager can probe,
matching what a vendor writes in `requirements.tools` — is not a leak. A second
runtime adds a row. Eight of the nineteen are that, and counting them as leaks
would have made the problem look twice the size it is.

**The finding that matters is that §12.2's conclusion was wrong.** It said both
`ports.compose_abi.go` and `HookEnv.ComposeProject` are published ABIs, "so
renaming them is a bundle-breaking change and not a refactor. This is the finding
that decides the RFC's cost, and it says the RFC is not cheap." Measured, the two
are not alike at all:

- **`ComposeVars` is port-shaped and the rename is free.** Its values are
  `DATA_DIR`, `SECRETS_DIR`, `CONFIG_FILE`, `RELEASE_DIR`, `VERSION`, `PROFILE`,
  `DOMAIN`. Not one is a Compose concept — they are the facts about an
  installation that a declarative file refers to, and a Quadlet unit needs the
  same seven. The published ABI is the *environment variable names*, which the
  Go identifier does not appear in. Renaming `ComposeVars` to something
  runtime-neutral changes no bundle anywhere.
- **`HookEnv.ComposeProject` is the expensive one.** A Compose project is
  Compose's grouping primitive; Quadlet has a unit prefix, which is a naming
  convention rather than a handle. It reaches every vendor hook as
  `<PRODUCT>_COMPOSE_PROJECT`, and [the reference
  page](../pages/docs/reference/hooks.md) documents it as being for a hook that
  shells out to `docker compose`. Its *meaning* is absent under a second runtime,
  not merely its name.

- **And the manifest's default**, which review found and the first version of
  the checker did not. `manifest.go:452` sets `providers.runtime.name` to
  `"compose"` when a bundle declares nothing, in the domain layer, on a line
  where every symbol is neutrally named. It is §2's "the manifest is not
  runtime-blind" as one assignment, and it is decision 8's question in
  executable form.

So the cost is two things — a published variable and a default — rather than a
category of bundle-breaking renames. That is a materially different RFC from the
one §12.2 described.

### 2.2 What a vocabulary checker structurally cannot find

The rule catches leaks that are *spelled*. The ones that are not spelled are
worse, and P1 found them by hand:

- **`RuntimeSpec.Project`** is Compose's grouping primitive wearing a neutral
  name. It is read by [`ui/views/release.go`](../internal/ui/views/release.go)
  to print `(project X)`, by [`ops/doctor.go`](../internal/lifecycle/ops/doctor.go)
  to say *"no containers exist for project %q"*, and defaulted in
  [`manifest.go`](../internal/domain/manifest.go) to the product name. No linter
  will ever flag it.
- **[`ops/doctor.go`](../internal/lifecycle/ops/doctor.go) hard-codes
  `tools.Docker`** in the branch that runs when there is no installation yet,
  with the comment that tool availability is *"what `init` will need next"*. That
  is the lifecycle layer stating which runtime this machine will use, in a
  sentence with no runtime's name in it.
- **`RuntimeSpec.Files` validation** says *"must list at least one compose
  file"* — the vocabulary in a string an operator reads, which the checker
  deliberately does not scan (§2.3).

One item left this list during review. The manifest's `"compose"` default was
here as unfindable, and it is now found — by a third rule that flags a string
whose *value* is a runtime's name, which also closes the `const defaultRuntime =
"compose"` indirection that would have walked past the other two. The lesson is
not that the list was wrong but that "a checker cannot see this" is a claim worth
attacking before it is written down: two of the three survived the attack and one
did not.

This is the same shape as the finding about depguard that opened this section,
one level along: **depguard sees imports and not names; a name checker sees names
and not meanings.** Each rule buys exactly its own layer, and the ones that
matter most are still found by reading.

### 2.3 What the checker deliberately does not do

It does not scan prose. 148 string literals above the adapters mention a runtime
and almost all are help text, error hints and comments — `--help` explaining that
this deploys "with Docker Compose on one Linux machine" is documentation of what
the product does today, not an architectural claim. The first draft of the branch
rule flagged nine of them, because Go spells string concatenation with the same
AST node as comparison; a rule that cries wolf on the command's own help text is
a rule somebody switches off. The branch rule now matches a literal that *is* a
runtime's name, not prose containing one.

**The rest of §2's candidates, unchanged and still unverified in detail:**

- **0007 documents three bundle-facing ABIs, and one of them is "Compose
  interpolation".** Quadlet has no interpolation. It has systemd specifiers and
  `EnvironmentFile=`. So parameter delivery is either a fourth ABI or a
  redefinition of the second, and 0007 explicitly drift-gates all three.
- **`depends_on`.** Quadlet expresses ordering as `After=`/`Requires=` between
  units. Compose's `depends_on: condition: service_healthy` has no direct
  translation; systemd's readiness notion is the unit's own, not a healthcheck's.
- **Project name.** Compose has one; Quadlet has a unit prefix and no grouping
  primitive. 0021 already noticed the docs' teardown snippet works only because
  the example's project name happens to equal its product name.
- **0010's volume capture** stops "only the services mounting that volume". In
  Quadlet a service is a unit and a volume is a `.volume`; rootless volumes live
  under the user's storage root, not `/var/lib/docker/volumes`. The storage-root
  half is measured and harmless (§12 item 5) — the capture never names a host
  path. The *stopping* half is untouched by that measurement and still open.
- **`Supervisor` and `Runtime` overlap.** The manager already renders systemd
  units (the backup timer). Under Quadlet, the runtime *is* systemd. Two ports
  will be issuing `systemctl` at the same host.

## 3. Goals

1. A second `Runtime` implementation, passing the existing contract suite, against
   rootless Podman + Quadlet.
2. An honest written account of every place the port had to change to accommodate
   it — that account is the deliverable, more than the adapter.
3. A bundle that declares which runtimes it supports, and refuses fail-closed on a
   machine whose runtime it does not.

Explicitly **not** a goal: that a single bundle run unchanged on both. See
decision 4.

## 4. Design

### 4.1 The manifest grows a runtime dimension

The manifest's `runtime:` block becomes `runtimes:`, a map keyed by runtime name
rather than a Compose-shaped block. The field is called `runtimes:` everywhere
below; decision 8 is about how it relates to `providers.runtime.name`, not about
what it is called:

```yaml
runtimes:
  compose:
    files: [compose.yaml, compose.prod.yaml]
  quadlet:
    units: [app.container, db.container, data.volume]
```

A bundle declaring one runtime installs only where that runtime is present. A
bundle declaring both carries both sets of files and the vendor owns keeping them
equivalent — the manager asserts nothing about equivalence and must not pretend to
(decision 4).

**Reconciling with `providers.runtime.name`.** That field already exists and
already defaults to `compose` (§2). Two fields naming the runtime is the drift
this project keeps finding. P2 picks one: either `runtimes:`' keys are the
declaration and `providers.runtime` keeps only the *version constraint*, or
`providers.runtime.name` stays the selector and `runtimes:` is keyed by it. The
second reads better and is a smaller change; the choice is P2's and is recorded
in §11 when made.

Strict, recursive decoding ([0018](0018-the-pre-1-0-manifest-surface.md)) makes
this a breaking manifest change, which is why it lands as a *replacement* of the
existing block before the first tag rather than an addition after it. If the tag
is cut before this RFC ships, the two-pass decode from 0018 §5.1 is the only thing
standing between an operator and `unknown field` about a file they did not write —
and 0018 §5.1 already bounds how much that can be oversold.

### 4.2 Runtime kind is fixed at `init` and never transitions

Same argument as [0016](0016-update-checking-and-unattended-updates.md)'s `mode`,
and for a stronger reason: the state directory records volume names, image
references and unit names that are runtime-specific, so a transition is a
migration of everything the manager knows, not a setting. `installation import`
is the second creation path (0016 found this) and must carry the kind.

### 4.3 Parameters reach Quadlet through a rendered `EnvironmentFile`

The manager renders one file per unit into the installation's render directory and
each `.container` references it. This keeps 0007's `<PRODUCT>_PARAM_<NAME>`
naming identical across runtimes — the *names* are the ABI, the delivery mechanism
is not. The render directory's tmpfs requirement
([0003](0003-secrets-recovery-and-onboarding.md)) now covers a file systemd reads
at unit start.

**Amended 2026-08-18, after §12 item 4 was measured.** This paragraph used to say
the file "must survive a reboot before the unit starts", and called that the
single hardest problem in the RFC. A tmpfs is empty at every boot, so it never
survives one and never had to: what decides the outcome is whether the unit is
ordered behind whatever renders it. Ordered, it works. Unordered, the behaviour
is chosen by the prefix -- `EnvironmentFile=` fails the unit loudly, and
`EnvironmentFile=-` starts it with empty parameters and reports success. The
parameter file is therefore referenced **without** the `-` prefix (decision 21), and
what a reader owes is that the file exists before it starts -- an invariant rather
than a mechanism (decision 22). The hard problem is the ordering rather than the
persistence: under Compose the product
unit's `ExecStart` is `apply --startup`, so rendering and starting are one
process, while Quadlet's generated units are started by systemd directly and owe
an explicit dependency on the render. Measurements in §12 item 4.

### 4.4 Health

Compose healthchecks and 0018's `health.checks[].start_period` stay the manifest's
health model. Under Quadlet the manager runs them itself rather than reading the
runtime's opinion — which is arguably what it should have been doing all along,
since 0007 already made health URLs follow parameter values.

### 4.5 Optional capabilities finally do their job

`RegistryProber` and `ImageInspector` exist —
[`runtime.go:377`](../internal/ports/runtime.go) and
[`runtime.go:391`](../internal/ports/runtime.go), confirmed — so an adapter can
decline by name rather than stub. Today nothing declines. Podman answers both; a
future plain-systemd adapter answers neither. This RFC produces the first real
refusal, which is also the first test that the refusal path is reachable.

## 5. Decisions

| # | Decision | Grade | Why |
| --- | --- | --- | --- |
| 1 | Quadlet, not `podman kube play` | LOCKED | `kube play` would make the bundle carry Kubernetes manifests, which reintroduces the vocabulary this project exists to avoid and makes "not a container orchestrator" a sentence nobody believes. |
| 2 | Rootless is the default and the tested configuration | LOCKED | Rootful Podman is a thinner variant of the same adapter; rootless is where the compliance value is and where the volume, network and port-binding differences actually bite. |
| 3 | Runtime kind is immutable per installation | LOCKED | §4.2. The state records runtime-specific names, so a transition is a migration of everything the manager knows. |
| 4 | The manager never asserts that two runtime declarations in one bundle are equivalent | LOCKED | It cannot check it, and a claim it cannot check is the failure 0013 exists to fix. `release verify --render-check` renders both sets; that is the whole of the promise. |
| 5 | An absent declared runtime is a refusal, not a fallback | LOCKED | Same reasoning as 0011 decision 20: a fallback that silently converges on a different substrate than the vendor tested is worse than a stop. |
| 6 | `Supervisor` keeps ownership of manager-generated units; `Runtime` owns product units | ASSUMED | They both call `systemctl` and that is fine — the ports are distinguished by *whose* units, not by which binary they invoke. Graded ASSUMED because §12.3 measured the collision as one-sided today and the Quadlet case is untested. |
| 7 | The port may grow methods; it may not grow `switch kind` | LOCKED | A conditional on runtime kind above `internal/adapters` is the abstraction failing in the one way that looks like progress. |
| 7a | What enforces decision 7 | LOCKED | Resolved by P1: [`tools/runtimecheck`](../tools/runtimecheck), an AST walk rather than `forbidigo`, because the rule needs to tell a comparison from a concatenation and a declared name from a mention in prose — §2.3 — and a pattern matcher cannot. Two rules: an allowlisted vocabulary rule whose allowlist is the inventory, and an un-allowlistable branch rule. `just runtime-check`, in CI beside the linter, with failing fixtures for both. |
| 7b | The vocabulary rule names `podman` and `quadlet` before either exists | LOCKED | The measured Compose leak is inventoried and meant to shrink; the leak worth preventing is the *second* adapter teaching the layers above it a second private language. A rule naming only the incumbent would have permitted exactly that, and would have been added after the first `if kind == "quadlet"` was already load-bearing. |
| 7c | A runtime named as data is not a leak | LOCKED | §2.1. `tools.Docker` is a key in a probe catalogue matching `requirements.tools`; a second runtime adds a row rather than contradicting one. Eight of the nineteen are this, and classing them as leaks would have roughly doubled the apparent problem and sent P2 renaming a lookup table. |
| 8 | How the runtime is named in the manifest | LOCKED | Resolved by P2, 2026-08-16, and not the way §4.1 predicted. `runtimes:`' keys are the declaration; `providers.runtime.name` is derived from them when a release declares one runtime and left empty when it declares two. §4.1 called the other option "smaller and reads better" — measured against the struct, `Providers.Runtime` is a single `Provider` beside `secrets` and `backup`, so it holds one value and cannot express the two-runtime bundle §4.1 itself requires and decision 4 renders both halves of. Consequence: the manifest's `"compose"` default (§2.1's second expensive leak) is gone, replaced by a value derived from what the vendor declared. Added by execution 2026-08-16, accepted the same day — see EXECUTION-LOG.md D-008. |
| 9 | `runtimes:` is added; `runtime:` stays readable and deprecated | LOCKED | §4.1 specified a replacement and argued it on landing "before the first tag". 0.1.0 and 0.2.0 are cut, and under strict decoding a replacement makes `runtime:` an unknown field — so every bundle already built would stop parsing to buy a tidier surface. A manifest declaring both is refused rather than merged, because merging nominates a winner the vendor did not. Consequence: two spellings to maintain until a named removal release, and no `api_version` bump — 0018 decision 1's `min_manager_version` carries the cost, which is what that mechanism is for. Added by execution 2026-08-16, accepted the same day — see EXECUTION-LOG.md D-009. |
| 10 | One `files` key per runtime, not a key named per runtime | LOCKED | §4.1 sketched `units:` for Quadlet beside `files:` for Compose. Validating that means asking which runtime a block belongs to, and a branch on a runtime's name above `internal/adapters` is what decision 7 forbids and `tools/runtimecheck` fails the build over. What the files mean is the adapter's to know. Consequence: a vendor writes `runtimes.quadlet.files: [app.container]`, which are files. Added by execution 2026-08-16, accepted the same day — see EXECUTION-LOG.md D-010. |
| 11 | The runtime is recorded in a new installation field, not `Providers` | LOCKED | `Installation.Providers` is the obvious home and is a field with zero writers and zero readers, carrying two contradictory documented meanings — `describe.go` calls it "declared by the release manifest", a repair test calls it "from the flags". Recording the runtime there would give it a third, real one, and an older manager would find a name it understands and no reason to refuse. Schema 8 → 9 instead, and the bump is for the *read* path, which none of 5–8 were. Added by execution 2026-08-16, accepted the same day — see EXECUTION-LOG.md D-011. |
| 12 | A release declaring several runtimes is refused at `init` | ASSUMED | §4.1 lets a bundle declare two runtimes and never says which one the manager installs with. Choosing needs to know which runtime this manager can drive, and today the only answers are a branch on a runtime's name — decision 7 forbids it — or a name injected at the composition root that every test would set and no test would exercise as production leaves it. Refusing costs a bundle nobody ships yet; either alternative costs the architecture test this RFC exists to run. Graded ASSUMED rather than LOCKED because it expires: P3 brings the second adapter and with it a real basis for choosing. Added by execution 2026-08-16, accepted the same day — see EXECUTION-LOG.md D-012. |
| 13 | `installation import` refuses an export whose runtime this manager does not drive, before anything is created | LOCKED | §4.2 said the second creation path must carry the kind and stopped there. Decision 3 makes the runtime immutable, so an imported record naming a runtime with no adapter here is a machine where every command fails and nothing can correct it. Refusing costs an operator one sentence during a rebuild; proceeding costs them the discovery one command later, in the same incident. Consequence: this is the one thing an import refuses about the *manager* rather than about the document, and it sits beside `ManagerVersion`, which is deliberately never enforced — the difference is that a version mismatch still leaves a working machine. It exits 9, the code the schema-from-the-future refusal on the same path already uses. Added by execution 2026-08-16, accepted the same day — see EXECUTION-LOG.md D-015. |
| 14 | Where the manager would otherwise state which runtime a machine uses, it asks the adapter; an optional capability is the mechanism, so an adapter that cannot answer declines rather than stubs | ASSUMED | Proposed by wave 27 and carried unruled for four waves, which is itself the argument: it was never one decision but the shape three of them took. `tools.Docker` was a required-tool list the core asserted on a runtime's behalf, closed by `ToolRequirer` rather than by renaming the field (D-014). `<PRODUCT>_COMPOSE_PROJECT` was a variable the core hook ABI promised, moved to `HookVarSupplier` (D-020). The project a deployment runs under was a value the manager compared as typed, moved to `OptionResolver` (D-024). Each was reached independently and each landed on the same mechanism, so the row records a practice already followed rather than one being introduced. `ASSUMED` and not `LOCKED`: it is a default for how to answer the next such question, and a runtime dimension nobody has met yet may want a required method instead — §6's counter is what makes that visible rather than free. Consequence: every instance costs an optional capability, and §6 now counts those, so following this row is what moves the escape hatch toward firing. Added by execution 2026-08-17 — see EXECUTION-LOG.md D-014, and D-020, D-024 for the instances. |
| 15 | Per-runtime settings are an opaque `options` map; the adapter validates them | LOCKED | §4.1 gave a runtime a file list and nothing else, and decision 10 removed the per-runtime *key name*. What went with it, unnoticed, was `project` — so `runtimes:` gave a vendor no way to name one while `ApplyDefaults` supplied it from the deprecated block anyway (D-016). A map the manager bounds in shape and never reads is the only form that survives a second runtime: `project` is Compose's grouping primitive and Quadlet's equivalent question has a different name, so a typed field would put one runtime's vocabulary in the shape both share. Consequence: an unknown key can only be refused by the adapter, from `Validate`, and a manifest surface exists that the manager cannot check the meaning of. Added by execution 2026-08-16, accepted the same day — see EXECUTION-LOG.md D-018. |
| 16 | The installation records the options it was created with, and a release that changes them is refused | LOCKED | These name durable things: measured, `--project-name alpha` resolves a volume called `alpha_data` and `beta` resolves `beta_data`. A changed project is not a reconfiguration, it is a deployment pointed at storage nothing has written to, with the operator's data still on the disk and nothing referring to it — and no other check would notice, since the backup that follows captures the new empty volumes and `doctor` reports them covered. Every option is treated as durable because only the adapter knows which are. Consequence: installation schema 9 → 10, a refusal on a path that has never had one, and an installation created before the field adopts what it is running on its next converge rather than being refused for a baseline nobody wrote. Added by execution 2026-08-16, accepted the same day — see EXECUTION-LOG.md D-019. |
| 17 | `<PRODUCT>_COMPOSE_PROJECT` is supplied by the runtime, not promised by the core hook ABI | LOCKED | §2.2 called it the expensive leak and P1's inventory named the shape of the fix: *"the variable stays for Compose installations and is absent under another runtime, which makes it a runtime-supplied variable rather than a core one"*. Renaming it was never available — the name is what every vendor hook already writes. Consequence: the hook ABI is two lists rather than one, `docs-check` gained a gate for the second so it does not become the undocumented surface RFC 0007 §13 found the Compose interpolation set in, and a hook that needs the variable should test for it. Byte-identical for every installation that exists today, since all of them are Compose. Added by execution 2026-08-16, accepted the same day — see EXECUTION-LOG.md D-020. |
| 18 | `runtime:` stops being read in 0.4.0, and the deprecation warns at `release verify`, `init` and `update` only | LOCKED | Decision 9 accepted "two spellings to maintain until a named removal release" and named none, so the cost ran with no clock on it and no signal to a vendor that one was running (D-017). A field cannot be deprecated by the `api_version` mechanism, which is a map keyed by value: a field is deprecated by being written at all, which only the manifest can answer. The three surfaces are the moments somebody can act — a vendor before publishing, an operator while choosing a bundle. Refused: a `doctor` check, because every installation that exists runs a `runtime:` bundle, so it would warn on every machine, permanently, about a file the operator did not write and cannot change. Consequence: an operator who runs neither `init` nor `update` before 0.4.0 is never warned, bounded by 0.4.0 refusing at `update` rather than breaking a running deployment. Added by execution 2026-08-17, accepted the same day — see EXECUTION-LOG.md D-021. |
| 19 | `morzer release new` writes `runtimes:` and stamps the `min_manager_version` it needs | LOCKED | The scaffold emitted `runtime:` until this wave, so every bundle authored from the documented starting point was born on the spelling the manager was about to stop reading — a project that warns about a field its own scaffold writes has deprecated nothing. The floor beside it is not bookkeeping: `runtimes:` is an unknown field to every released manager and strict decoding refuses the whole manifest over one, so without it the vendor's customer is told about a typo rather than an upgrade requirement, which is the conversion 0018 decision 1 exists to perform. Consequence: a bundle scaffolded today cannot be installed by any released manager, because the manager it declares is not released — and the floor exposed that a build between tags understates its own version, which had to be fixed before the binary would accept its own output (EXECUTION-LOG.md D-023). Added by execution 2026-08-17, accepted the same day — see EXECUTION-LOG.md D-022. |
| 20 | The option comparison runs on the values the runtime resolves, not the ones the manifest declares | LOCKED | Decision 16 refuses a release that changes a recorded option, and it compared declared maps. Declared is not what the runtime reads: an installation created with no `project` is already running under its product name, so a release writing that name out in full renames nothing, and the manager refused it and told the vendor to restore a value that was never doing any work (EXECUTION-LOG.md wave 28 R-4). Only the adapter can tell those apart — knowing that `project` falls back to the product is exactly what decision 7 keeps out of these layers — so it is asked, through `ports.OptionResolver`. Refused: recording the *effective* options instead, which pins the namespace against an adapter later changing its defaults but costs installation schema 11, a migration that cannot run where the state package lives because it has no adapter to ask, and a change to what `installation describe` publishes. Consequence: the recorded baseline stays as declared, so a future change to an adapter's default silently moves both sides of the comparison with it; and the port gains its 8th optional capability, which is what prompted §6's amendment. Added by execution 2026-08-17, accepted the same day — see EXECUTION-LOG.md D-024. |
| 21 | The `EnvironmentFile` **carrying parameters** is referenced without the `-` prefix | LOCKED | Measured on the host and again in each of four boots of the venue (§12 item 4). With the file absent, `EnvironmentFile=` fails the unit before the process runs (`Failed to load environment files`, `Result=resources`); `EnvironmentFile=-` **starts it, reports success, and runs with the parameter empty** -- observed as `B_PORT=[]` on a unit systemd marked active. This is systemd's behaviour rather than a design choice, so what is being decided is only which of the two failure modes this project accepts: one that refuses, or one that lies. Scoped to the *parameter* file deliberately: `-` stays legitimate for a genuinely optional file, such as an operator override drop-in, and an unscoped ban would forbid a pattern nothing here objects to. LOCKED because the alternative lets a product run on an empty parameter while systemd reports the unit started successfully -- that unit-level signal is what was measured, and whether anything downstream notices depends on something validating the parameter, which nothing here guarantees -- and because the ordering that exposes it is a race -- a deployment can boot correctly for months before the once it does not, so this can survive every test anybody thought to run. Added by execution 2026-08-18 -- see EXECUTION-LOG.md D-038. |
| 22 | A unit that reads the parameter file must not start before the file exists; **how that is achieved is P3's to settle** | ASSUMED | The invariant the measurement establishes, stated without the mechanism it was observed through. Ordering the reader behind whatever renders the file is one way, and the one the venue tested (`After=`+`Requires=`, which worked); a generator that emits its own ordering, rendering earlier in the boot, or Quadlet inlining values rather than referencing a file at all are others. **Not** `RequiresMountsFor=`, which the first draft of this row listed and review caught: it adds dependencies on the *mount units* covering a path (systemd.unit(5)), so it guarantees `/run` is mounted and says nothing about whether anything has written into it -- the tmpfs is already there when the race runs, and it survives intact. This row deliberately does not choose. A spike that has built no adapter is the worst-placed thing to foreclose the design of one, and the first draft of this row did exactly that -- it locked a mechanism inferred from a fixture rather than a constraint derived from a measurement. ASSUMED rather than LOCKED for the same reason: it is load-bearing for P3 and expected to be satisfied, not to be defended. Consequence: Compose satisfies it for free, because its product unit's `ExecStart` *is* `apply --startup`, so rendering and starting are one process; Quadlet's units are started by systemd directly and satisfy it by construction or not at all. Added by execution 2026-08-19 -- see EXECUTION-LOG.md D-042. |
| 23 | `runtime:` stops being read in **0.3.0**, not 0.4.0, and this withdraws a compatibility promise rather than moving a date | LOCKED | **Supersedes row 18, which stays in this table and stays LOCKED for the window it governed.** Row 18 named 0.4.0 so a vendor had one release in which both spellings worked. Measured 2026-08-19: **no released manager reads `runtimes:`** -- `git show v0.2.0:internal/domain/manifest.go` has no `Runtimes` field, and the spelling exists only in unreleased `main`. So the window row 18 was buying does not exist and never did: 0.1.0-0.2.0 read only the old spelling, 0.3.0 would read only the new, and no version reads both. A vendor cannot write one bundle that works across the upgrade either way. What is being given up is therefore the *promise*, which three released binaries still make at `release verify`, `init` and `update`; the grace period it promised was already void. Priced rather than assumed: 11 amd64 downloads and 0 arm64 across three releases, read from the GitHub release assets on 2026-08-19. Consequence: `runtime:` is refused rather than deleted from the struct -- the manifest is strict-decoded (`yaml.Strict()`, `yaml.DisallowUnknownField()`), so deleting the field would answer a vendor with "unknown field runtime" instead of naming the migration. `DeprecatedFields` keeps its machinery and loses its only member. Added by execution 2026-08-19 -- see EXECUTION-LOG.md D-052. |

## 6. The escape hatch, restated after measurement

The draft wrote: *"If satisfying both runtimes drives `ports.Runtime` past roughly
a dozen methods … the conclusion is that there are two bundle profiles rather than
one abstraction."*

**`ports.Runtime` has twelve methods today** — `Validate`, `Pull`, `Up`, `Down`,
`Restart`, `Stop`, `Start`, `RunOneShot`, `Exec`, `Status`, `Logs`, `Stats`. The
threshold is met before the second adapter exists, which makes the test as written
fire on day one and therefore useless.

Restated, so it can still do its job:

> **The RFC closes as partially wrong if the second adapter forces more than two
> new additions to the port surface — core methods and optional capabilities
> alike — or forces any existing method that the Quadlet adapter can only stub.**

**Amended 2026-08-17** (§13): the original counted methods on `ports.Runtime`
only, and every growth but one has gone into optional capabilities instead. The
surface today is **13 core methods and 8 optional capabilities**, against 12 and
5 when this section was written; that is the baseline P3 is measured against.
The condition is still about what the *second adapter* forces, so the larger
count does not fire the hatch — it makes it able to.

A stub is the signal, not the count: a method one implementation cannot mean is
the proof that the interface describes Compose rather than "starting services".
Writing this down before P3 is the only way it stays available; afterwards, sunk
effort argues for the abstraction.

## 7. Non-goals, and what reopens each

- **Kubernetes / Helm.** Reopened by nothing. It is the negation of the project.
- **containerd/nerdctl, Swarm, LXC.** Reopened by a second real user request after
  Podman ships — the marginal adapter is cheap once the port has been graded once.
- **A runtime with no containers at all (plain systemd services, `.deb`/`.rpm`).**
  Genuinely interesting, genuinely a different bundle format. Reopened by a vendor
  who wants signed, rollback-able delivery of something that is not a container.
- **Migrating an existing installation between runtimes.** Reopened by an operator
  demonstrating that reinstall-and-restore-from-backup does not work — which it
  should, and if it does not, that is a defect in restore, not a missing feature.
- **Automatic translation of Compose files to Quadlet units.** `podman-compose`
  and `podlet` exist; wrapping either would make the manager responsible for a
  translation it cannot verify (decision 4 again).

## 8. Tests

- The existing runtime contract suite, run against the Quadlet adapter unchanged.
  A contract that needs a per-adapter exemption is a finding for §11, not a
  parameter.
- A rootless Podman stage in the acceptance suite (P4).
- The lint rule from decision 7, verified red against a deliberate `switch kind`
  above `internal/adapters`.

## 9. Docs

The runtime dimension is a manifest field, so it joins the generated manifest
schema and the reference page `docs-check` already gates. `doctor` gains a check
for the declared runtime's presence, which is where an operator meets decision 5.

## 10. Phasing

- **P1a — The leak inventory and its enforcement.** ✅ **Shipped 2026-08-12.**
  No adapter. The classification came out as three classes rather than two
  (decision 7c), the number was 19 on the day, and the rule from decision 7 is
  `tools/runtimecheck` in CI. §2.1–2.3.
- **P1b — The Podman host.** ⏳ **Partly answered 2026-08-16.** §12 items 5 and 6
  are measured: rootless volume paths do not break 0010's staging, because 0010
  has no path to break, and 0011's registry is reachable over plain HTTP with
  `--tls-verify=false`. Item 4 — the `EnvironmentFile`-on-tmpfs question — is
  open, and the reason is worth carrying: it was written as needing a host, and
  a host is not what it needs. It needs somewhere that can be booted on demand.

  **"One task, not three" was wrong, and the way it was wrong is instructive.**
  The three were grouped by what they were blocked on, which looked like one
  thing — a Podman host. Two of them were, and closed within an afternoon of one
  existing. The third was blocked on something the grouping never named, so it
  inherited a completion criterion that had nothing to do with it. A phase
  boundary drawn around a shared blocker holds only as long as the blocker was
  identified correctly for every member.
- **P2 — Manifest and state.** 🚧 **Partly shipped 2026-08-16.** `runtimes:` map,
  decision 8 resolved (and decisions 9–11 added), kind fixed at `init` and
  carried through `--repair`, an undeclared runtime refused rather than
  substituted. ~~**Still open in P2:** `installation import` carrying the kind,
  `doctor` reporting it, and §14's two unspelled leaks — `RuntimeSpec.Project`
  and `doctor.go`'s hard-coded `tools.Docker`.~~ **Three of those four shipped
  2026-08-16**: import carries the kind and refuses one this manager cannot
  drive, `doctor` gains `runtime.declared`, and the `tools.Docker` leak is
  closed by an optional capability rather than by a rename. ~~**Still open in
  P2:** `RuntimeSpec.Project`, which is a published hook ABI and its own unit of
  work (EXECUTION-LOG.md D-016), and the deprecation of `runtime:`, which has no
  mechanism.~~ `RuntimeSpec.Project` shipped 2026-08-16 as decisions 15–17, and
  ~~**P2 is complete** but for one thing that is decision 9's cost rather than a
  phase's work: `runtime:` is deprecated and nothing warns, because the only
  deprecation mechanism this project has is keyed by `api_version` and this is a
  field (D-017, carried).~~ **P2 is complete as of 2026-08-17.** The deprecation
  names 0.4.0 as the release that stops reading `runtime:` and says so at
  `release verify`, `init` and `update` (decision 18), and `release new` writes
  the current spelling rather than the deprecated one (decision 19). Building
  the second half found that a manager built between tags understates its own
  version and so refused the bundle its own scaffold had just written — fixed
  against RFC 0018's mechanism, EXECUTION-LOG.md D-023. **No longer gated on P1b** — decided 2026-08-16, see EXECUTION-LOG.md
  D-005. None of §12's three measurements is consumed by anything on this list:
  items 5 and 6 are volume capture and image ingest, both P3, and item 4 is
  §4.3's parameter delivery, also P3. What P2 did need from P1b is the list of
  what "this machine can run the declared runtime" means — decision 5's refusal
  and §9's `doctor` check both rest on it — and that list **arrived alongside
  items 5 and 6 rather than out of them**. §11's two rootless preconditions and
  the finding that a present binary can still be unusable are discoveries of the
  host work itself, recorded in §11 and EXECUTION-LOG.md D-006; none of them is
  an answer to item 5 or item 6. What unblocks P2 is that they are written down,
  not which measurement produced them.
- **P3 — The adapter.** Quadlet against the contract suite. Volume capture (0010)
  and bundled images (0011) are the two places to expect the design to come back —
  0012 already found that `oras-go` demands TLS where `docker pull` does not, and
  Podman's `registries.conf` is a third opinion about the same loopback registry.
- **P4 — Acceptance.** A rootless Podman stage that installs, reconfigures a port
  (0007's re-create-not-restart assertion), updates, rolls back and restores.
- **P5 — The report.** What the port changed, what §6 nearly triggered, and
  whether the architecture claim survived.

## 11. Risks

- **The lowest-common-denominator risk** (§6) is the main one, and §2's measured
  inventory makes it likelier than the draft assumed: an interpolation ABI in the
  ports layer is not a small rename.
- **Rootless port binding below 1024** requires `net.ipv4.ip_unprivileged_port_start`
  or a proxy. 0007 made ports the operator parameter that matters most, so this is
  on the critical path, not an edge case. `doctor` must check it. **Measured
  2026-08-16 on the development host: `1024`, the kernel default**, so the risk
  is live exactly as written rather than mitigated by a distribution that ships
  a lower value.
- **Rootless units do not start at boot unless the account is lingering**, which
  `loginctl enable-linger` sets. That lingering is opt-in is documented rather
  than measured here — `loginctl(1)` describes it as what causes a user manager
  to be *"spawned for the user at boot and kept around after logouts"* — and
  what was measured is that the development host has it on. Found while
  measuring, and it is a second `doctor` check rather than a footnote to the
  first: a machine without it installs, runs, converges and passes every check
  the manager makes, and then does not come back after a reboot. That is the
  worst shape a precondition can have, because the failure is separated from its
  cause by however long the machine stays up.

  It also contaminates §12 item 4, and the venue has to account for it: the
  development host has `Linger=yes` set for unrelated reasons, so a measurement
  taken there answers the boot question *with lingering already on* and cannot
  see the case a fresh machine is in.
- **Quadlet requires `systemctl daemon-reload` after unit changes**, which is a
  step with a failure mode (a partially reloaded generator) that has no Compose
  analogue and therefore no existing fault-injection point.
- **Two ports issuing `systemctl`** (decision 6) is the design's least confident
  line.

## 12. What this draft owed a measurement

Taken 2026-08-12, against the code rather than the documentation.

1. **That the manifest names Compose files in a runtime-specific way.**
   **Confirmed, and worse than stated.** `RuntimeSpec.Files`,
   `RuntimeSpec.ComposeFiles()`, `Release.ComposeFilePaths()`, and validation
   messages naming "compose file". Also **`providers.runtime.name` already
   defaults to `"compose"`**, which the draft did not know and which §4.1 now has
   to reconcile with (decision 8).
2. **The count and location of Compose-semantic leaks above `internal/adapters`.**
   **Answered by P1, and this item's own conclusion was wrong.** It read: *"Both
   are published ABIs, so renaming them is a bundle-breaking change and not a
   refactor. This is the finding that decides the RFC's cost, and it says the RFC
   is not cheap."*

   The count and its breakdown are in §2.1 and are deliberately not repeated
   here: this paragraph carried its own copy and it was stale within a day, which
   is the second time a number restated in prose has drifted in this document.
   What matters is the shape — **most of the mentions are renames that break
   nothing, and the expensive ones are two.**

   `ComposeVars` is not a published ABI: the published thing is the *variable
   names* it produces, and the Go identifier appears in none of them. The
   genuinely expensive items are `HookEnv.ComposeProject`, whose meaning — not
   whose spelling — is absent under Quadlet, and the manifest defaulting
   `providers.runtime.name` to `"compose"`, which decision 8 is already open
   about and which only the literal rule finds (§2.1).

   The correction is worth more than the number. This item was the reason to
   believe the RFC was expensive, and it was reasoning from a grep. **Reasoning
   from a grep is what produced the wrong premise in §2's opening paragraph too**,
   where the draft believed depguard forbade the string `docker`.
3. **Whether `Supervisor` and a Quadlet `Runtime` genuinely collide.**
   **One-sided today.** `Supervisor` has eleven methods and the only caller of
   `systemctl` in the tree is
   [`internal/adapters/supervisor/systemd`](../internal/adapters/supervisor/systemd).
   Nothing else issues it, so the collision is entirely prospective — which is why
   decision 6 is graded ASSUMED rather than LOCKED.
4. **Whether an `EnvironmentFile` on tmpfs can be read by a unit at boot** (§4.3).
   **Measured 2026-08-18. Both halves, and the answer is that the file's
   presence is not the question — the ordering is, and the `-` prefix is the
   hazard.**

   The item asked for a venue that could be booted on demand. It got one: a
   privileged container running systemd as PID 1, booted repeatedly in about a
   second. `systemd-nspawn` was the intended tool and needs root this
   environment does not have; the substitute runs the same systemd, has `/run`
   as a real tmpfs (`rw,nosuid,nodev,noexec,relatime,mode=755`) and executes a
   real boot transaction, which is what the ordering question needs. That tmpfs
   is mounted by the container runtime rather than by systemd as it would be on
   a host — immaterial here, since what matters is that it is a tmpfs and empty
   when the transaction begins, but named so the item does not imply more
   fidelity than it has.
   It is **not** bare metal and does not answer firmware-level questions; it was
   not asked any.

   **The mechanism half**, measured on the host (systemd 261) and confirmed in
   the venue. With the file absent:

   - `EnvironmentFile=` — the unit **fails and the process never runs**:
     `Failed to load environment files: No such file or directory`, then
     `Failed to spawn 'start' task`, `Result=resources`.
   - `EnvironmentFile=-` — the unit **starts, reports success, and runs with the
     parameter empty**.

   A trap worth naming: the failing unit reports `ExecMainStatus=0`. Nothing
   ran, so there is no exit code, and anything reading exit status alone sees a
   zero from a unit that failed.

   **The ordering half**, measured across four independent boots of the venue —
   three before the base image was pinned by digest, and once after, which is
   what makes the pin worth having rather than an assertion about it.
   Four units, all `WantedBy=multi-user.target`, with a render unit standing in
   for `apply --startup`:

   | Unit | Ordering | Prefix | Boot outcome |
   |---|---|---|---|
   | A | none | none | **failed** — `Result=resources` |
   | B | none | `-` | **active, success**, `B_PORT=[]` |
   | C | `After=`+`Requires=` render | none | active, success, `C_PORT=[18080]` |

   The timeline is the finding. B *started and finished before the render unit
   had written the file* — and this is a **race**, not a determinism. Unordered
   units have no guaranteed order; B won four boots out of four, but it could
   lose, in which case it succeeds with the correct value. That makes the hazard
   worse rather than milder: the same deployment can boot correctly for months
   before the once it does not.

   ```
   12:17:46.322936  Starting product B (unordered, dash)...
   12:17:46.323457  Starting render parameters (stands in for apply --startup)...
   12:17:46.328716  Finished product B (unordered, dash).
   12:17:46.331510  Finished render parameters (stands in for apply --startup).
   ```

   **What this settles.** A tmpfs is empty at every boot by definition, so the
   file is always absent when the boot transaction begins; "does it survive"
   was never the question. What decides the outcome is whether the unit is
   ordered behind whatever renders it, and what the missing file does when it
   is not. Ordered, it works. Unordered without the prefix, it fails loudly —
   recoverable, and the operator is told. Unordered *with* the prefix, the
   product comes up misconfigured and systemd reports the unit started
   successfully. That unit-level signal is what was measured; whether anything
   downstream notices depends on something validating the parameter, which
   nothing in this design guarantees.

   **Consequence for §4.3, and it is not what the section feared.** §4.3 called
   this "the single hardest problem in the RFC" on the grounds that the file
   must survive a reboot. It cannot and need not. The hard part is that under
   Compose the ordering is free — the product unit's `ExecStart` *is*
   `apply --startup`, so the same process renders and then starts — while under
   Quadlet systemd starts generated `.container` units directly from a
   generator, with no natural ordering behind the manager. P3 owes that graph the
   file's presence before a reader starts -- by ordering, by a generator that
   emits its own, or by not referencing a file at all -- and owes it without the
   `-` prefix. Neither is a discovery this item can make; both are now measured
   constraints on it.

5. **Whether rootless Podman's volume paths break 0010's staging assumptions.**
   **Measured 2026-08-16: they do not, and the item's premise was wrong.**
   0010 has no host-path assumption to break. `CaptureVolume` runs a helper
   container with the volume mounted read-only and tars to *stdout*, which the
   manager writes itself; the volume's host path is never named, so it is free
   to move. The round trip was run under rootless Podman with the adapter's own
   argv and its own pinned helper image, `DefaultHelperImage`: a dotfile, a
   `0600` mode and a nested directory survived capture, and the restore's
   wipe-then-extract removed a planted file rather than merging around it.

   **What did change is a property, not a path — and it is narrower than it
   first looked.** A rootless volume lives under
   `~/.local/share/containers/storage/volumes/<name>/_data`, where Docker's
   lives under `/var/lib/docker/volumes` and answers `Permission denied` to an
   unprivileged manager. But "readable by the manager" holds only for what
   *container root* wrote. Rootless maps container uid 0 to the invoking user
   and every other container uid to a subordinate one, so measured 2026-08-16:
   a file written by container root is owned by the manager's own user and
   reads; a file written by container uid 1000 is owned by subuid `100999` at
   mode `0600` and answers `Permission denied`. The helper container reads both,
   because it is inside the namespace where those ids mean something.

   **So the helper is not merely still correct under rootless — it is the only
   thing that works.** A capture reading the host path directly would succeed
   against a product running as root and fail against one that drops privileges,
   which is the configuration a security-conscious vendor ships. That is a
   sharper argument for the existing design than the one `CaptureVolume`'s
   comment gives, and it is not the argument the comment gives: it cites a
   root-owned file the manager cannot clean up, which is a Docker concern.
6. **Whether 0011's in-process registry is reachable by Podman over plain HTTP.**
   **Measured 2026-08-16: not by default, and yes with one flag.** 0012's
   prediction holds in direction — Podman sends a TLS ClientHello to
   `127.0.0.1` and refuses with `http: server gave HTTP response to HTTPS
   client`, where Docker falls back to HTTP for loopback unprompted. With
   `--tls-verify=false` Podman falls back too, and a pull from `ociserve`
   itself completes: blobs copied, no digest mismatch, no serve error.

   So the cost is a flag on one command rather than a `registries.conf` the
   manager would have to write into somebody's home directory — the cheap end
   of what §10's P3 warned about. The flag is also the sharp edge: on the
   general `Pull` path it would disable verification against a real registry,
   which is the one place it must never be off.

Item 4 alone still needs something this repository does not have, and it is a
bootable venue rather than a host. Items 5 and 6 needed only the host, which is
why they closed the day one existed.

**Status after P1a (2026-08-12): items 1–3 answered, 4–6 not, and P1 is therefore
not closed.** The inventory and the enforcement shipped; the Podman host did not,
because the development environment has no Podman and this project does not
report a lane it has never run as a lane that works — the rule that already
stopped the acceptance and container suites being described as CI's problem.
Writing a Podman job into CI from here would produce exactly that: a green badge
nobody has watched go red.

**Status after P1b-partial (2026-08-16): items 5 and 6 answered, item 4 open.**
A rootless Podman host now exists on the development machine — not in the test
lanes, and the paragraph above still governs that distinction: nothing here has
been written into CI, because nothing here has been watched go red. The
measurements were taken by hand against that host and are reproducible from what
§12 records rather than from a lane that claims to run them.

One measurement therefore stands between P1b and closure rather than three, and
**it does not stand between here and P2**. That claim was this document's, it was
made before any Podman host existed, and it was re-examined and withdrawn on
2026-08-16 — see EXECUTION-LOG.md D-005 and §10's P2 bullet. Item 4 gates P3,
where §4.3 consumes its answer. P1b stays open on it rather than being closed by
moving it, because a phase held open on one item is a legible statement that the
architecture test is not yet de-risked.

## 13. Amendments

**2026-08-19 -- decision 18's removal release moves to 0.3.0, and the move is a
withdrawal rather than a reschedule.** Row 18 is `LOCKED` and is not edited; row 23
supersedes it and says why. The correction worth recording is the shape of the
mistake row 18 could have made and did not quite avoid: it named a removal release
on the reasoning that a deprecation needs a clock, which is right, and assumed the
release before it would be a window in which both spellings worked, which was never
checked. It was not true when row 18 was written -- `runtimes:` had not shipped in
0.1.0, 0.1.1 or 0.2.0, and has not shipped since. **A grace period is a claim about
what some released binary can read, and it is checkable against the tags.** This one
was assumed from the phasing instead, which is the same failure as §12 item 5's void
premise and D-048's unreachable consumer: a premise stated once, carried by everything
downstream, and never attacked. Added by execution 2026-08-19 -- see EXECUTION-LOG.md D-052.

**2026-08-17 — §6's escape hatch counts the whole port surface, not only
`ports.Runtime`'s methods.** As written it fires when "the second adapter forces
more than two new methods onto `ports.Runtime`", and every growth since it was
restated has gone somewhere else. Measured today: **13 core methods** — one of
the two spare slots spent, on `Name()` in P2 — and **8 optional capabilities**,
against 12 and 5 when §6 was written. A capability only Compose implements is
the same evidence a Compose-shaped method is; it just does not trip a counter
aimed at the other half of the interface. The threshold now reads over both, and
today's numbers are the baseline P3 is measured against. Note the condition is
unchanged and still forward-looking — *what the second adapter forces* — so
recording a larger surface today does not fire the hatch, it makes it capable of
firing. Added by execution 2026-08-17 — see EXECUTION-LOG.md D-024.

**2026-08-12 — P1 split into P1a and P1b.** The phase was written as one task
whose deliverable was "a list and a number", with §12 separately asserting that
standing up a Podman host was "the true first task of P1". Those are not one
phase: the inventory is a static analysis of this repository and the three open
measurements need a machine this project does not have. P1a shipped; P1b is
named and open. Recorded rather than quietly redefined, because the phase's own
text says P1 "may not report complete" without item 4 and that remains true.

**2026-08-12 — decision 7a resolved, and the mechanism is not the one the draft
predicted.** It expected `forbidigo` or an equivalent pattern rule. A pattern
rule cannot distinguish `kind == "compose"` from `"…deployed with Docker Compose
on one Linux machine"`, and the first attempt at the branch rule proved it by
flagging nine help strings. The enforcement is an AST walk.

**2026-08-12 — §12.2's conclusion reversed.** The item concluded the RFC "is not
cheap" on the strength of two symbols it called published ABIs. One of them is
not (§2.1). The RFC's cost estimate moves from *a category of bundle-breaking
renames* to *one published environment variable*.

**2026-08-16 — P1b answered two of its three measurements, and item 4's blocker
was misidentified.** §12 items 5 and 6 are measured against a rootless Podman
host; item 4 is restated, because it was never blocked on the host it named. The
consequences are not written into §5 — they are put to the author as proposed
rows in EXECUTION-LOG.md: **D-001, D-002, D-003 and D-006, all outstanding.**
D-005 is not among them and proposes no row; it records the accepted phase-gate
ruling, which is a different kind of entry and is listed as such. The log is also
where the two findings belonging to other documents live: Podman's native `oci:`
transport bears on RFC 0011's decision 19, and the rootless volume's readability
on RFC 0010. Neither is this RFC's row to write.

**2026-08-16 — P1b no longer gates P2.** The document asserted in three places
that P1b was "the only thing between here and P2", and execution found that P2
consumes none of the three measurements — items 5 and 6 belong to P3's volume
capture and image ingest, item 4 to §4.3, also P3. What P2 needed was the host
precondition list behind decision 5's refusal, and that arrived. Put to the
author as EXECUTION-LOG.md D-005 and accepted; the phasing prose in §10 and §12
carries it, and no decision row was added, because a phase gate is not a design
decision. The declined alternative — closing P1b by folding item 4 into P3 — is
recorded in the log beside the ruling, since a refusal is written down nowhere
else.

**2026-08-16 — §12 item 5's premise was void, and it was void before any host
existed.** The item asked whether rootless volume *paths* break 0010's staging.
`CaptureVolume` names no path — it mounts the volume into a helper container and
takes the tar off stdout — so the question could have been answered by reading
the adapter on the day §12 was written, four days before a Podman host existed
to answer it against. Recorded because the failure is not a wrong answer but a
wrong question sitting on the list that defines what is unknown, where it read
as blocked on hardware. §2.1 already warns that "a checker cannot see this" is a
claim worth attacking before writing it down; "a host is needed for this" is the
same claim about a different obstacle, and it went unattacked.

**2026-08-16 — §2.2's second unspelled leak is closed, and the mechanism is an
optional capability rather than a rename.** `doctor`'s no-installation branch
named `tools.Docker` directly; it now asks the wired adapter through
`ports.ToolRequirer`. The leak was the lifecycle layer deciding which runtime
this machine will use, so a rename could never have fixed it — the sentence had
no runtime's name in it to rename, which is why the inventory could not hold it.
§4.5's optional-capability pattern is what the fix is built on, and the port
grows no method, so §6's count is unchanged at thirteen. The one leak §2.2 still
lists is `RuntimeSpec.Project`, and execution found more of it than §2.2
described — see EXECUTION-LOG.md D-016, outstanding.

**2026-08-16 — `installation import` refuses a runtime this manager cannot
drive.** §4.2 said the second creation path "must carry the kind" and stopped
there. It carries it, because an export holds the installation whole; what was
unspecified is what an import does when the kind names a runtime this binary has
no adapter for. Put to the author as EXECUTION-LOG.md D-015 and accepted:
refused before anything is created. The tension is worth recording — an import
is a disaster-recovery path that deliberately refuses almost nothing, and
`ManagerVersion` sits in the same document explicitly unenforced for that
reason. The difference is that a version mismatch still leaves a working
machine and this does not.

**2026-08-16 — §2.2's list is empty, and the last item on it was bigger than
the entry said.** `RuntimeSpec.Project` was described as a Compose primitive
wearing a neutral name, with three readers. Execution found a fourth fact the
entry did not carry: `runtimes:` had no way to express a project at all, while
`ApplyDefaults` supplied one from the deprecated block to every manifest —
including those that never wrote it. The documented migration ("move these files
under `runtimes.compose` and delete the old block") therefore renamed every
volume, network and container of any deployment whose project was not its
product name. Measured on the development host: `--project-name alpha` resolves
a volume named `alpha_data`, `beta` resolves `beta_data`. Rows 15, 16 and 17
carry the fix; the leak inventory falls **18 → 17**, and its compose-shaped
class **3 → 2**.

**2026-08-16 — the hook ABI is two lists.** Everything §4.3 and RFC 0007 say
about it assumed one. A runtime now contributes variables through
`ports.HookVarSupplier`, and `docs-check` gained a gate for that half —
otherwise the fix would have created exactly the ungated ABI RFC 0007 §13 built
these gates to end, one layer along.

## 14. What P1 leaves for whoever picks this up

- **P1b, above.** ~~Three measurements behind one Podman host.~~ One
  measurement — item 4 — behind a bootable venue, as of 2026-08-16. Struck
  rather than rewritten, because the sentence was the reason the phase was
  scoped the way it was and a reader tracing that scope needs to see it.
- ~~**The two unspelled leaks** in §2.2 — `RuntimeSpec.Project` and `doctor.go`'s
  hard-coded `tools.Docker`~~ — are not in the inventory, because the inventory is
  what a checker can see and these are not. They are P2's, and they are the ones
  most likely to be forgotten precisely because nothing fails when they are.
  **Both are closed** (§13, 2026-08-16), and the sentence above proved true of
  the second one twice over: nothing failed when it was wrong, and what nothing
  failed on was a documented migration that renamed an operator's volumes.
- **The renames themselves are not P1's.** P1 classified; it changed no name. A
  rename sweep that lands before the manifest work of P2 would churn the same
  files twice.
