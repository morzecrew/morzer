# desired-state-rig · RFC 0057 §6, the cases left

On branch `desired-state-rig` from `main` at `47cdfe5c`, building the tests
[RFC 0057](../rfcs/0057-desired-state-an-installation-pulls-and-applies-to-itself.md)
§6 lists that P2–P5 left unbuilt: the acceptance-rig cases that need two
products, a rebuilt machine or facts (row 53), and the leak scan of
notifications and attestations. The RFC is complete and its decisions are
ruled. Tests only: no product code changes unless a case finds a defect, and
then it gets its own entry.

**Drift count: 0.**

| Class | Test | Meaning |
|---|---|---|
| `discovery` | Could not have been known before code existed | Healthy — the spec was right to be silent |
| `spec-gap` | Could have been known; the spec was silent or at the wrong altitude | The design process missed something |
| `drift` | The spec covered it and it was built otherwise anyway | **A defect** |
| `irreducible` | No amount of design settles it | Stop and spike |

## Plan

### What §6's rig list already has

Each case below is covered by a suite test on main, so it is not built again:

- Tamper with a document, refused as `bad-signature`:
  `TestADocumentThatCannotBeUsedIsRefusedWithItsClass`,
  `TestAPairThatIsNotTheAuthoritysLeavesTheWatermarkAlone`.
- Replay an older document: `TestAnOlderDocumentIsAReplay`,
  `TestNothingAtOrBelowTheLastRefusedIsConsidered`.
- An expired document, `doctor` warns: `TestDoctorWarnsWhenTheAuthorityGoesQuiet`.
- A change that fails a health check, unwound, refused with the reason and
  shown in the row: `TestADocumentThatFailsItsHealthChecksIsUndoneAndRefused`,
  `TestARowReportsSyncWithoutTheRefusalsText`.
- One machine through a real object store with a prefix-scoped credential:
  `TestAnAuthorityReachesAMachineThroughAnObjectStore`,
  `TestAReadOnlyPrefixScopedCredentialIsEnoughToSync` (docker-tagged).
- The row's `sync` block carries no value, a refusal that quoted one
  included: `TestARowReportsSyncWithoutTheRefusalsText`.

### What this PR adds

All in `test/suite`, through the operations with the suite's fakes, and
against one shared target the way §6's rig shares one bucket:

1. **Two products, one authority, one bucket** (docker-tagged, RustFS, in
   `sync_docker_test.go`). Two installations of different products, each
   with a credential scoped to its own `state/<product>/<id>` prefix. The
   authority publishes to each through one roster; both apply on their next
   cycle; `state ls` reports each one's applied serial; neither credential
   reads the other's documents.
2. **A rebuilt machine** (`sync_rebuild_test.go`). A machine follows the
   authority, is exported, and is imported on a fresh root, which mints a new
   age identity. Its next cycle refuses the old document as `undecryptable`.
   The authority reads the new `age_recipient` from the row, publishes again,
   and the rebuilt machine applies. Uses `newMachine` and so real sops, which
   the container suites job has not got: not docker-tagged.
3. **The sync and an unattended update contend for the lock**
   (`sync_test.go`). A sync cycle finds the deployment lock held by an
   update and gives way; the update completes; the next cycle applies. Then
   the other way round: an unattended update's tick finds a sync holding the
   lock, and the next tick installs. What the update's tick does on a held
   lock is read from the code during the build, and an entry is written if
   it is anything but "gives way and the next tick installs".
4. **A machine joined at `init`** (`facts_test.go`). A third installation is
   initialised with `InitOptions` carrying the sync target, authority and
   recipient (`init --sync-*`), applies, and publishes its row. `state ls`
   lists it as unexpected; once the roster names it, `state facts` prints its
   facts; a document built from them for another installation is published
   and applied there.
5. **A row for an installation the roster does not list**
   (`state_test.go`). A second key-holder with write access posts a signed
   row for an unknown id: `state publish` refuses it, and publishes once the
   roster lists that id with that key.
6. **Leaks** (`sync_test.go`). A document that sets a managed secret and a
   managed parameter, and one refused with an error quoting a value: neither
   value appears in any notification the cycle sent, nor in any statement it
   filed, scanned with `withoutDigests` as `TestAConfigChangeFilesAStatement`
   scans a statement.

Harness: `newHarness` names its product `demo`; case 1 needs a second
product, so the harness gains a variant that copies the bundle under another
product name. Nothing else changes in it.

### Decisions the plan needs that the RFC does not settle

- Where the cases run. Row 53 keeps them for the acceptance rig, and they are
  built as suite tests instead. Entry below.
- What an unattended update's tick does on a held lock (case 3). Read from
  the code during the build, and logged if it is not the obvious answer.

```divergence
decision: 53
grade: ASSUMED
class: discovery
at: 2026-11-11T13:16:45Z
attempt: 1
claim: row 53 keeps the cases that need two products, a rebuilt machine or facts for the acceptance rig, a bash scenario that runs the demo product under real Docker on one port. None of these cases depends on what the runtime does: each is about the object store, the lock, the age identity or the roster, which the suite reaches with the same operations and can put in states the script cannot, such as a lock held at a chosen moment. The suite runs in every CI test job; the script runs once, on the port the demo uses.
evidence: .github/scripts/acceptance.sh:22-45
action: departed
proposal: ASSUMED — §6's remaining rig cases run as suite tests, the two-product case against a real object store; the acceptance rig keeps the one-installation scenario it has
```

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-11T13:27:12Z
attempt: 1
claim: §6's rebuild case has the old document refused as undecryptable and applied once re-encrypted to the new recipient, and says nothing of the roster. A rebuilt machine also mints a new signing key (RFC 0028 §5.3), so its next row does not verify against the key the roster names, and `state publish` refuses it until the roster names the new key, which has to arrive out of band (decision 13). Built as that: the test updates the roster from the rebuilt row's signing key, as an operator would from `fleet publish --dry-run`, and the how-to gains the step.
evidence: rfcs/0028-the-machines-signing-identity.md:174
action: decided
proposal: UNLISTED — a rebuilt machine is followed again once the roster names its new signing key; the next publish then encrypts to the recipient its new row names
```
