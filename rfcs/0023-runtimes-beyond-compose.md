# RFC 0023 — Runtimes beyond Compose

- **Status:** 🚧 In progress — **P1a shipped 2026-08-12**: the leak inventory
  came to 15 mentions in three classes, and `tools/runtimecheck` now enforces the
  boundary depguard cannot see. It reversed §12.2's cost estimate along the way:
  the expensive leak is one published environment variable, not a category. P1b
  — a rootless Podman host and the three measurements behind it — is open, and is
  the only thing between here and P2.
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

**The number, as measured on 2026-08-12: 15 mentions above `internal/adapters`
— 8 port-shaped, 2 Compose-shaped, 5 catalogue — and 0 branches on runtime
kind.**

Dated, because it is a measurement rather than a property: `just
runtime-inventory` is the live count, and the whole point of writing this one
down is that a later reader can see whether it fell.

Three classes, not the two P1 was asked for. The third earned its place: a
runtime named as *data* — a key in the table of tools the manager can probe,
matching what a vendor writes in `requirements.tools` — is not a leak. A second
runtime adds a row. Five of the fifteen are that, and counting them as leaks
would have made the problem look a third bigger than it is.

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
- **`HookEnv.ComposeProject` is the expensive one, alone.** A Compose project is
  Compose's grouping primitive; Quadlet has a unit prefix, which is a naming
  convention rather than a handle. It reaches every vendor hook as
  `<PRODUCT>_COMPOSE_PROJECT`, and [the reference
  page](../pages/docs/reference/hooks.md) documents it as being for a hook that
  shells out to `docker compose`. Its *meaning* is absent under a second runtime,
  not merely its name.

So the cost is one published variable, not a category. That is a materially
different RFC from the one §12.2 described.

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

This is the same shape as the finding about depguard that opened this section,
one level along: **depguard sees imports and not names; a name checker sees names
and not meanings.** Each rule buys exactly its own layer, and the ones that
matter most are found by reading.

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
  under the user's storage root, not `/var/lib/docker/volumes`.
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
at unit start, which means it must survive a reboot before the unit starts or the
unit fails on a cold boot. **This is the single hardest problem in the RFC** and
§12 owes it a measurement.

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
| 7c | A runtime named as data is not a leak | LOCKED | §2.1. `tools.Docker` is a key in a probe catalogue matching `requirements.tools`; a second runtime adds a row rather than contradicting one. Five of fourteen mentions are this, and classing them as leaks would have inflated the problem by more than half and sent P2 renaming a lookup table. |
| 8 | How the runtime is named in the manifest | OPEN | §4.1. `providers.runtime.name` already exists; P2 chooses and records here. |

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
> new methods onto `ports.Runtime`, or forces any existing method that the Quadlet
> adapter can only stub.**

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
  (decision 7c), the number was 15 on the day, and the rule from decision 7 is
  `tools/runtimecheck` in CI. §2.1–2.3.
- **P1b — The Podman host.** ⏳ §12 items 4–6: the `EnvironmentFile`-on-tmpfs
  question, rootless volume paths against 0010's staging, and whether 0011's
  in-process registry is reachable over plain HTTP. One task, not three, and the
  only thing between here and P2. Split out because P1a turned out to be
  answerable from a laptop and P1b is not — keeping them one phase would have
  meant either blocking a finished deliverable or reporting a phase complete with
  half of its questions open.
- **P2 — Manifest and state.** `runtimes:` map, decision 8 resolved, kind fixed at
  `init`, carried by `installation import`, refused on mismatch, reported by
  `doctor`.
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
  on the critical path, not an edge case. `doctor` must check it.
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

   The count is **14, of which 7 are renames that break nothing, 5 are catalogue
   data, and 2 are Compose's own concepts** (§2.1). `ComposeVars` is not a
   published ABI: the published thing is the *variable names* it produces, and
   the Go identifier appears in none of them. The one genuinely expensive item is
   `HookEnv.ComposeProject`, whose meaning — not whose spelling — is absent under
   Quadlet.

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
   **Not measured — needs a machine with Podman, which this repository's test
   lanes do not have.** It stays the item most likely to change the design, and
   P1 may not report complete without it.
5. **Whether rootless Podman's volume paths break 0010's staging assumptions.**
   Not measured; needs the same machine.
6. **Whether 0011's in-process registry is reachable by Podman over plain HTTP.**
   Not measured; 0012's TLS finding predicts it is not.

Items 4–6 all need one thing: a rootless Podman host in the test lanes. Standing
that up is the true first task of P1, ahead of the inventory, because three of the
six unknowns are behind it.

**Status after P1 (2026-08-12): items 1–3 answered, 4–6 not, and P1 is therefore
not closed.** The inventory and the enforcement shipped; the Podman host did not,
because the development environment has no Podman and this project does not
report a lane it has never run as a lane that works — the rule that already
stopped the acceptance and container suites being described as CI's problem.
Writing a Podman job into CI from here would produce exactly that: a green badge
nobody has watched go red.

So the remaining three measurements are all that stands between P1 and P2, and
they are one task rather than three. §14 records the split.

## 13. Amendments

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

## 14. What P1 leaves for whoever picks this up

- **P1b, above.** Three measurements behind one Podman host.
- **The two unspelled leaks** in §2.2 — `RuntimeSpec.Project` and `doctor.go`'s
  hard-coded `tools.Docker` — are not in the inventory, because the inventory is
  what a checker can see and these are not. They are P2's, and they are the ones
  most likely to be forgotten precisely because nothing fails when they are.
- **The renames themselves are not P1's.** P1 classified; it changed no name. A
  rename sweep that lands before the manifest work of P2 would churn the same
  files twice.
