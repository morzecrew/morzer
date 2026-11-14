# pending-values · RFC 0058, values a document gives the release an installation updates to

On branch `rfc-0058-build` from `main` at `60c9d74d`, executing
[RFC 0058](../rfcs/0058-values-a-document-gives-the-release-an-installation-updates-to.md)
in full, as its §12 phases it: one PR. Decisions 1–4, 6 and 12 were ruled by
the author 2026-11-12, as recommended.

**Drift count: 0.**

| Class | Test | Meaning |
|---|---|---|
| `discovery` | Could not have been known before code existed | Healthy — the spec was right to be silent |
| `spec-gap` | Could have been known; the spec was silent or at the wrong altitude | The design process missed something |
| `drift` | The spec covered it and it was built otherwise anyway | **A defect** |
| `irreducible` | No amount of design settles it | Stop and spike |

## Plan-time decisions

The plan named one gap the RFC leaves load-bearing, and the author ruled it
2026-11-13, in the session that built it, as recommended. The others are
choices §10 leaves to implementation, or follow from the code, and are
recorded here before they are built.

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-13T20:44:00Z
attempt: 1
claim: The RFC adds `staged` and `sync.pending` to the fleet row and names a schema bump only for installation state. The fleet row is a versioned contract, and FleetRow.Validate refuses a row from a newer schema whole (RFC 0026 decision 4), which is the rule 0.16.0 followed for schema 2. §9 says an older authority sees the refusals it sees today, which a bump contradicts for the rows themselves. Ruled by the author 2026-11-13: FleetSchemaVersion goes from 2 to 3. An older `fleet ls` reports an upgraded machine's row as written by a newer manager, so the authority's manager is upgraded first, and the changelog says so.
evidence: internal/domain/fleet.go:32
action: decided
proposal: UNLISTED — the fleet row's `staged` and `sync.pending` are schema 3; an authority reading rows is upgraded before the machines that write them
```

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-13T20:44:00Z
attempt: 1
claim: Decision 9 gives the row's sync block `pending`, "the names". §5.2 keeps a secret the staged release manages in the secret store, never in `pending`. The row lists the parameter names pending holds and no secret name: a stored secret is not waiting for anything, and the applied serial already tells the authority the document landed.
evidence: rfcs/0058-values-a-document-gives-the-release-an-installation-updates-to.md:199-206
action: decided
proposal: UNLISTED — the fleet row's `sync.pending` names the pending parameters only; a secret a document gives the staged release is stored, and is not pending
```

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-13T20:44:00Z
attempt: 1
claim: An update that waited for the deployment lock is refused when a parameter it renders changed meanwhile (#348, refuseChangedInstallation). A sync that wrote pending while an update waited changes what that update takes as given, so the same refusal covers pending: the update planned with the values it read, and would record those under a pending section that says otherwise.
evidence: internal/lifecycle/ops/apply.go:1495-1530
action: decided
proposal: UNLISTED — an update that waited for the lock is refused when the pending values for its release changed meanwhile, naming them
```

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-13T20:44:00Z
attempt: 1
claim: Decision 6 refuses `update --set` of a managed or pending name without `--force`. `--force` is the global flag, and on `update` it already authorises `--skip-backup`, so one `--force` given for that also lets `--set` override the authority. `config set` has the same single flag under RFC 0057 decision 29, so `update` follows it rather than adding a second one.
evidence: internal/cli/root.go:555
action: decided
proposal: UNLISTED — `update`'s `--force` is the one global flag, so it lets `--skip-backup` and a `--set` of a managed or pending name through together
```

## Building it

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-13T20:57:55Z
attempt: 1
claim: §5.1 makes a name pending when the installed release declares it without managing it and the staged release manages it, and says the value is for the staged release and not applied now. For a parameter, pending keeps it apart. A secret has no such place: decision 4 stores it now, and the store is the one the installed release reads, so storing a secret the installed release declares changes what it runs on, which its managed list does not allow. Such a secret is refused, whole, as the installed release refuses it today. A secret the installed release does not declare is stored, as decision 4 says.
evidence: internal/lifecycle/ops/config_apply.go:418-433
action: decided
proposal: UNLISTED — a secret the staged release manages is stored for it only when the installed release does not declare it; one the installed release declares and does not manage refuses the document, as before
```

```divergence
decision: 13
grade: ASSUMED
class: spec-gap
at: 2026-11-13T21:03:39Z
attempt: 1
claim: Decision 13 refuses a resume after a sync changed a pending value, and says a fresh update then takes the new value, because an interrupted update does not block a new operation (§5.3). A run that `--resume` finds is journaled as running, which is not terminal, and gateUnfinished refuses every new operation while such a record exists, naming `--resume`. The refusal therefore names the way out: `morzer status --clear-intervention` writes the interrupted run off, as it does any run abandoned mid-way, and the next `update` takes the values pending then. The resume itself is refused as decision 13 says.
evidence: internal/lifecycle/preflight/preflight.go:586-631
action: departed
proposal: ASSUMED — a resume after a sync changed a pending value is refused naming it; the next update takes the new value once `morzer status --clear-intervention` has written off the interrupted run, as any new operation needs
```

```divergence
decision: 13
grade: ASSUMED
class: spec-gap
at: 2026-11-13T21:18:17Z
attempt: 2
claim: Correcting attempt 1, whose evidence was read too narrowly. `--resume` finds an update journaled as running, interrupted or failed (apply.go:1590-1592). Interrupted and failed are terminal, and gateUnfinished lets a new operation start over them, so for those decision 13 holds as written: the resume is refused naming the value, and a fresh `update` takes the new one. Only a run its process left journaled as running blocks a fresh update, and `morzer status --clear-intervention` writes that off. The refusal's hint says both, and the departure is that second clause alone.
evidence: internal/lifecycle/ops/apply.go:1581-1597
action: departed
proposal: ASSUMED — a resume after a sync changed a pending value is refused naming it, and a fresh update takes the new value; one whose run was left journaled as running is first written off with `morzer status --clear-intervention`, as any new operation needs
```

```divergence
decision: UNLISTED
grade: UNLISTED
class: discovery
at: 2026-11-13T21:21:03Z
attempt: 1
claim: An update that converged on its release and stopped in its record step leaves that release installed with pending kept, as decision 7 says. A sync then applies the authority's newer value for a pending name directly, because the installed release now manages it, and pending still held the older value, which the resume and every later update would record over the newer one. Found by the suite's resume case. A sync's value for a name it applies now, set or unset, removes that name from pending: it is the authority's later word for it.
evidence: test/suite/pending_test.go:330-350
action: decided
proposal: UNLISTED — a document that applies a value for a name now removes what an earlier document left pending for that name
```
