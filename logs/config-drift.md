# config-drift · RFC 0044 P1, configuration drift an operator can see

On branch `config-drift` from `8ebd593`, building
[RFC 0044](../rfcs/0044-configuration-drift-an-operator-can-see.md) P1 as
ruled by the author on 2026-10-31: §5.1, §5.2, §5.3 and §5.5. That covers:

- `morzer config diff`, read-only, its diffs scrubbed on both sides, and `--json`;
- the `doctor` check `config.drift`, which warns;
- the unattended tick leaving a release staged while files differ;
- `apply --dry-run`'s plan detail moved onto `configComparison`, which gains the profile;
- the tests of §6 that P1 reaches, and the docs of §7.

**Drift count: 0.**

| Class | Test | Meaning |
|---|---|---|
| `discovery` | Could not have been known before code existed | Healthy — the spec was right to be silent |
| `spec-gap` | Could have been known; the spec was silent or at the wrong altitude | The design process missed something |
| `drift` | The spec covered it and it was built otherwise anyway | **A defect** |
| `irreducible` | No amount of design settles it | Stop and spike |

Four things the RFC leaves to the build, decided while planning and before any
code.

Decision 10 scrubs "every diff before it is shown", and §5.5 moves the plan's
diff onto the same comparison. The plan's diff printed the on-disk side
unscrubbed, so a value pasted into a file showed in `apply --dry-run`.

```divergence
decision: 10
grade: ASSUMED
class: spec-gap
at: 2026-10-31T21:12:00Z
attempt: 1
claim: the RFC scrubs config diff's output but does not say whether the plan's diff, now built on the same comparison, is scrubbed
evidence: internal/lifecycle/ops/apply.go:533
action: departed
proposal: ASSUMED — `apply --dry-run`'s configuration diff is scrubbed as `config diff`'s is, and names the targets without their lines when the secret values cannot be loaded.
```

§5.1 sets `redacted: false` when the redactor cannot be armed, and arming
decrypts the secret state. Arming with nothing to show would decrypt for no
reader, and warn on a machine whose files all match.

```divergence
decision: 10
grade: ASSUMED
class: spec-gap
at: 2026-10-31T21:13:00Z
attempt: 1
claim: what `redacted` says when no file differs, and whether the redactor is armed then, is not settled
evidence: rfcs/0044-configuration-drift-an-operator-can-see.md:146
action: departed
proposal: ASSUMED — the redactor is armed only when a file differs, and `redacted` is false only when a file differs and its lines could not be scrubbed.
```

§5.2 names unreadable targets in the warning, and says nothing of a run where
some are unreadable and none differ.

```divergence
decision: 1
grade: ASSUMED
class: spec-gap
at: 2026-10-31T21:14:00Z
attempt: 1
claim: config.drift's status when targets could not be read and none of the rest differ is not settled
evidence: rfcs/0044-configuration-drift-an-operator-can-see.md:155
action: departed
proposal: ASSUMED — with no differing target, `config.drift` is ok and names the unreadable ones; an unreadable target never makes it warn, as it never counts as drift.
```

Decision 4 holds an unattended update while files differ, and does not say what
a tick does when it cannot compare them: the installed release's templates fail
to render, or the release cannot be read.

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-10-31T21:15:00Z
attempt: 1
claim: an unattended tick that cannot compare the files does not know whether a hand edit would be replaced
evidence: internal/lifecycle/ops/channel.go:497
action: decided
proposal: ASSUMED — a tick that cannot compare the configuration files leaves the release staged, with the reason, as it does for drift; unreadable targets do not hold it, as they are not drift.
```

## Found while building

`TestAnUnattendedTickTakesThePreUpdateBackup` found it: an installation `init`
created and nothing has applied has no configuration files, each absent target
is drift by decision 1, and the gate then held its first unattended update
forever. An absent file holds no edit, so replacing it loses nothing, which is
the whole of decision 4's reason to hold.

```divergence
decision: 1
grade: ASSUMED
class: discovery
at: 2026-10-31T21:48:00Z
attempt: 1
claim: counting an absent target as drift makes the unattended gate hold an installation that was never applied, where no edit could be lost
evidence: test/clitest/unattended_test.go:67
action: departed
proposal: ASSUMED — the unattended gate counts only the differing targets that exist on disk; `config diff` and `config.drift` still count an absent one, as decision 1 says.
```

The `LOCKED` row this branch touches, honored:

```divergence
decision: 4
grade: LOCKED
kind: resolved
at: 2026-10-31T21:16:00Z
attempt: 1
claim: drift holds an unattended update, and the release stays staged with the reason
evidence: internal/lifecycle/ops/channel.go:497
action: decided
```

**Drift count: 0.**

Review found that `config diff` loads the installed release without the digest
check `apply`, `doctor` and the unattended gate make, through the loader every
`config` command shares; `config set` renders from that tree and writes what it
renders. Found after the fix was written, in the same sitting.

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-01T07:47:00Z
attempt: 1
claim: `config diff` compares against, and `config set` renders from, a release directory changed after install
evidence: internal/lifecycle/ops/config.go:509
action: decided
proposal: ASSUMED — every `config` command that loads the installed release verifies it against the digest recorded at install, as `apply` does, and refuses a release directory changed since.
```

**Drift count: 0.**
