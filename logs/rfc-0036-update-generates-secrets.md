# RFC 0036 · generating a secret a new release declares

On branch `update-generates-secrets` from `7e54654`, building
[RFC 0036](../rfcs/0036-generating-a-secret-a-new-release-declares.md), which was
written in the same change: `update` generates a secret the release it installs
adds, in a step of its own, and `--generate-secrets=false` turns that off.
Closes #163.

**Drift count: 0.**

| Class | Test | Meaning |
|---|---|---|
| `discovery` | Could not have been known before code existed | Healthy — the spec was right to be silent |
| `spec-gap` | Could have been known; the spec was silent or at the wrong altitude | The design process missed something |
| `drift` | The spec covered it and it was built otherwise anyway | **A defect** |
| `irreducible` | No amount of design settles it | Stop and spike |

Every decision in the RFC is `ASSUMED` and was written before any code. The
issue proposed generating at `apply` too, and the RFC departs from that before
the build (decision 4, and §5 *Alternatives considered*). That is a design
choice made in the RFC, not a departure from it, so it has no entry here.

## Self-audit, 2026-10-29

Scope: the whole branch before its first commit. That is the RFC, the step, the
flag, two test files and four doc pages.

The step reported what it generated in its detail, as `init` reports a count.
The plain presenter prints a step's detail only under `--verbose`. An
unattended tick runs in plain mode, so the line that says which secrets it
generated reached nobody. The value is a new credential on the machine, and an
operator should learn it exists without asking for verbose output.

```divergence
decision: 8
grade: ASSUMED
class: spec-gap
at: 2026-10-29T07:09:22Z
attempt: 1
claim: a step's detail is printed in plain mode only under --verbose, so the names of the secrets an unattended update generated were not printed at all
evidence: internal/ui/plain/plain.go:90-96
action: departed
proposal: ASSUMED — the step says what it generated as an informational message, printed in every mode, and never the value.
```

Also checked, with nothing found:

- **Sabotage.** Dropping the clause that skips a secret the replaced release
  declared, ignoring `NoGenerateSecrets`, and unwiring the flag in the CLI each
  turn one test red: the running-release test, the opt-out test and the clitest
  plan test.
- **Resume across this change.** An update interrupted under a manager without
  the step is refused by `--resume`, because its journaled step list differs.
  That is the engine's existing rule for any step list change
  (`resumePoint`), and it asks for a fresh update, which then generates.
- **The value.** No path prints it. The step reports names, the journal holds
  step statuses and the `generate_secrets` flag, and the suite asserts the
  generated value is absent from the journal and the messages.

Not run: anything against the real sops-age adapter. The fake store's
`Generate` refuses to overwrite as the port requires, and the step relies on
that only as a second guard behind its own check for an existing value.

**Drift count: 0.**
