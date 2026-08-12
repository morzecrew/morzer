# RFC 0023 — Runtimes beyond Compose

- **Status:** 📝 Draft — the architecture test in §1 is the deliverable; P1 is the
  phase that decides whether the rest is cheap or is closed as partially wrong.
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
| 7a | What enforces decision 7 | **OPEN** | The draft said "the same mechanism that already forbids the string `docker`" — §2 measured that no such mechanism exists: `depguard` restricts imports and cannot see a `switch`. Enforcing this needs a `go/analysis` pass or a `forbidigo`-style pattern rule, and P1 names one and lands it with a deliberately failing fixture. A decision with no enforcement is a comment. |
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

- **P1 — The leak inventory.** No adapter. §2 above is its first draft, measured
  rather than assumed; P1 finishes it by classifying each leak as *port-shaped*
  (belongs in `ports`, rename it) or *Compose-shaped* (must move below the
  adapter boundary), and lands the lint rule from decision 7. Deliverable is a
  list and a number.
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
   **Measured: not short.** The table in §2. The load-bearing one is
   `internal/ports/compose_abi.go` — an entire ports file whose exported API is
   the Compose interpolation contract — plus `HookEnv.ComposeProject`, which
   reaches every vendor hook as `COMPOSE_PROJECT`. Both are *published ABIs*, so
   renaming them is a bundle-breaking change and not a refactor. **This is the
   finding that decides the RFC's cost, and it says the RFC is not cheap.**
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

## 13. Amendments

*(Empty. Implementation contradicting the design is recorded here, dated, in the
manner of 0009 §12 and 0011 decisions 18–21.)*
