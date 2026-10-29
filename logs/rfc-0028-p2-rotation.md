# RFC 0028 P2 · rotating the machine's signing key

On branch `rotate-signing-key` from `8b3c37e`, building
[RFC 0028](../rfcs/0028-the-machines-signing-identity.md) P2:
`morzer installation rotate-signing-key` mints a new signing key, pushes the
old public key onto `signing.previous_keys` with reason `rotation`, and keeps
every old signature verifiable as "signed by a predecessor". P1 shipped the
key, the succession record and the predecessor outcome; P2 is the command.

**Drift count: 0.**

| Class | Test | Meaning |
|---|---|---|
| `discovery` | Could not have been known before code existed | Healthy — the spec was right to be silent |
| `spec-gap` | Could have been known; the spec was silent or at the wrong altitude | The design process missed something |
| `drift` | The spec covered it and it was built otherwise anyway | **A defect** |
| `irreducible` | No amount of design settles it | Stop and spike |

The RFC settles what rotation is (§9 P2), what it records (§5.4, decision 5's
succession shape with `reason: rotation`), and what it cannot do (§5.3,
decisions 10 and 11). It does not say how a rotation is made safe against a
crash, what happens to the old private key, what a machine with no key or a
disagreeing key does, or how a rotation is confirmed. None of those reaches a
`LOCKED` row, so they are decided here, each written before the code it
governs.

A rotation changes two things that must agree, the key file and the public key
state records (§5.4 refuses their disagreement), and no write changes both at
once. The order decides what a crash leaves. Replacing the file first leaves a
machine whose old public key is in neither place, and `init --repair`, the
remedy `doctor` names for a disagreement, would then record the new key with no
predecessor, so everything the old key signed reads as an unknown signer.

```divergence
decision: unlisted
grade: UNLISTED
class: spec-gap
at: 2026-10-29T10:26:00Z
attempt: 1
claim: P2 names the three things a rotation does and not their order, and the key file and installation state cannot be changed in one write
evidence: rfcs/0028-the-machines-signing-identity.md:373-375
action: decided
proposal: ASSUMED — a rotation is three journaled steps: stage the new key beside the current one, record the old public key as retired and the new one as current (compensable), and only then move the staged key over the old one. A crash leaves the old public key recorded in every case, and `--resume` finishes the move.
```

The old private key is not kept. Old signatures verify against the public half,
which state keeps (decision 7's reasoning, applied to a key retired on purpose),
and the manager never signs with a retired key again. A retired private key
left on the disk would be one more thing for whoever took the machine to take,
for no use.

```divergence
decision: unlisted
grade: UNLISTED
class: spec-gap
at: 2026-10-29T10:26:00Z
attempt: 1
claim: P2 keeps old signatures verifiable and does not say whether the old private key survives the rotation
evidence: internal/domain/signing.go:34-55
action: decided
proposal: ASSUMED — the staged key replaces the old one in place, so the old private key is gone once the rotation completes; only its public half is kept, in `previous_keys`.
```

A machine with no signing key on disk has nothing to rotate, and one whose key
file disagrees with state would retire a key state never recorded. Both are
refused, with the remedy each needs. A machine whose state has no key while the
file has one (a schema-5 machine that signed after migrating, before anything
recorded it) is the case the RFC's §5.6 expects, and that key is the one
retired.

```divergence
decision: unlisted
grade: UNLISTED
class: spec-gap
at: 2026-10-29T10:26:00Z
attempt: 1
claim: P2 does not say what a rotation does on a machine with no signing key, or with a key file that disagrees with the recorded one
evidence: rfcs/0028-the-machines-signing-identity.md:253-258
action: decided
proposal: ASSUMED — no key on disk is refused ("nothing to rotate"); a key file whose public half is not the recorded one is refused with `init --repair` as the remedy; an unrecorded key on disk with nothing in state is retired like a recorded one.
```

A rotation asks for no confirmation beyond its name, as `secret rotate` does.
It destroys nothing an operator can still use: old signatures stay verifiable,
and the key it retires is one the operator has decided not to trust. `--dry-run`
plans it like every operation. The rotation's own statement is signed by the
new key and names the old one through `previous_keys`. It is not countersigned
by the old key, because a key rotated out for a suspected compromise vouching
for its successor proves nothing (decision 11).

```divergence
decision: unlisted
grade: UNLISTED
class: spec-gap
at: 2026-10-29T10:26:00Z
attempt: 1
claim: P2 does not say how a rotation is confirmed or which key signs the statement it leaves
evidence: rfcs/0028-the-machines-signing-identity.md:324-325
action: decided
proposal: ASSUMED — no `--force` or typed confirmation; the operation is journaled, attested and planned by `--dry-run` like any other; its statement is signed by the new key only.
```

The order above makes every crash resumable, but only if a resume knows which
key it is retiring. The engine re-runs completed idempotent steps on resume, and
the first build took the key to retire from the disk. After a run killed between
its move and the journal write, the disk holds the *new* key, and the resume
retired that one too: a second rotation, with the new key in `previous_keys`.
A test forging that record caught it before anything shipped.

```divergence
decision: unlisted
grade: UNLISTED
class: discovery
at: 2026-10-29T10:31:00Z
attempt: 2
claim: a resumed rotation took the key to retire from the disk, which after an interrupted move holds the new key, so the resume rotated a second time
evidence: internal/lifecycle/ops/rotate.go:69-75
action: decided
proposal: ASSUMED — a rotation journals the public key it retires in its flags, and a resume retires that key; the stage step is skipped once the key on disk is no longer it.
```

**Drift count: 0.**

## Self-audit, 2026-10-29

Scope: the whole branch against `origin/main`. That is the port capability, the
adapter's staging, the operation, the command, five suite tests and one
clitest, the reference page, SECURITY.md, and the RFC's status.

**Fixed:**

- **A resume rotated a second time** (the entry above). Caught by a test
  forging a record whose move had finished; removing the stage step's `Check`,
  or the journaled key, turns it red again.
- **The first version of the suite tests overwrote an existing file**,
  `test/suite/rotation_test.go`, which holds the secret-rotation doctor tests.
  Restored from `main`, and the new tests moved to `signing_rotation_test.go`.
- **`installation --help` grew past eighty columns.** The new command is the
  longest name in the group and widened the column; two other `Short` lines
  were trimmed to fit, which `TestHelpLinesFitEightyColumns` enforces.
- **SECURITY.md's closed-gap note described `secret edit` as one encrypted
  round trip.** It sets each changed value separately; the note now says what
  the reference says.
- **RFC §7's decision-11 test had no counterpart:** a signature by the retired
  key made *after* the rotation, dated after `retired_at`, must still read as a
  predecessor's and not be rejected. Added, against a real rotation.

**Checked, nothing found:**

- The staged key cannot leave the machine: backups copy named files from
  `/etc/<product>` and not the signing directory, the support bundle refuses
  the whole directory, and `installation remove` deletes it.
- A rotation with no signer, with no key on disk, and with a key file state
  does not record is refused before anything is staged or journaled, and a
  plan stages nothing.
- A failure at the move compensates. State is put back and the staged key is
  removed, and the old key keeps signing.

**Residue:** interoperation of a rotated key with the real `minisign` binary
was not run here. The new key comes from the same generator P1's interop tests
cover in the container lane, and only the file's name changes.

**Drift count: 0.**
