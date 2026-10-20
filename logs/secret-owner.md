# Secret owner · a non-root container reads the secret it was given

On branch `secret-owner` from `35742c3`, building
[RFC 0032](../rfcs/0032-secrets-a-non-root-container-can-read.md) P1: the
numeric `owner` on a secret's declaration, its grammar and the floor it needs,
the owner set on the temp file before the rename, the refusal, the owner in
`Verify` and in `RenderedFile`, both stores and the battery, the acceptance
lane's run as root, the regenerated schema, and the docs.

**Drift count: 0.**

| Class | Test | Meaning |
|---|---|---|
| `discovery` | Could not have been known before code existed | Healthy — the spec was right to be silent |
| `spec-gap` | Could have been known; the spec was silent or at the wrong altitude | The design process missed something |
| `drift` | The spec covered it and it was built otherwise anyway | **A defect** |
| `irreducible` | No amount of design settles it | Stop and spike |

An unquoted owner is refused. §4.1's example quotes it, and the published
schema describes it as a string, which is what a vendor validates against in
their own CI. A manager that also took a YAML integer would accept a schema
that the published one refuses. The refusal's hint says to quote it.

```divergence
decision: unlisted
grade: UNLISTED
class: spec-gap
at: 2026-10-20T04:05:15Z
attempt: 1
claim: §4.1 gives the grammar of the text but not whether an unquoted YAML integer is accepted; the published schema types the field as a string
evidence: rfcs/0032-secrets-a-non-root-container-can-read.md:173
action: decided
proposal: ASSUMED — owner is a quoted string; an unquoted integer is refused with a hint to quote it, so the manager and the published schema accept the same documents
```

§4.1 puts the grammar in `SecretSchema.Validate`, but the `SecretOwner` it
types holds the parsed numbers and no text, so by the time `Validate` runs
there is nothing left to check. The grammar is checked where the text is
decoded, as `FileMode`'s and `Duration`'s are, with the same message and hint.
`Validate` keeps the checks that need the whole schema.

```divergence
decision: unlisted
grade: UNLISTED
class: spec-gap
at: 2026-10-20T04:05:15Z
attempt: 1
claim: §4.1 validates the owner's grammar in SecretSchema.Validate, but SecretOwner as §4.1 types it keeps no text for Validate to read
evidence: rfcs/0032-secrets-a-non-root-container-can-read.md:173
action: decided
proposal: ASSUMED — the owner's grammar is checked when the text is decoded, as FileMode's and Duration's are
```

A declared `"0"` is applied as written. §4.1 says it means root "as absent
does", which holds wherever units run. On a manager that is not root the two
differ: absent leaves the file with the manager, and `"0"` asks for a chown to
root, which is refused like any other uid. `Verify` compares the declared uid,
so reading `"0"` as absent would have the check expect root and find the
manager.

```divergence
decision: unlisted
grade: UNLISTED
class: spec-gap
at: 2026-10-20T04:05:15Z
attempt: 1
claim: §4.1 equates a declared "0" with an absent owner, which is true only where the manager is root
evidence: rfcs/0032-secrets-a-non-root-container-can-read.md:176
action: decided
proposal: ASSUMED — a declared owner is applied as written, "0" included; a manager that is not root refuses it as it refuses any other uid
```

`RenderedFile` carries the owner as `uid` and `gid`, read from the file after
the write, on every file. What the file has is what a container is refused
against, which is not always what was declared: without a gid, the group is
whatever the manager's is.

```divergence
decision: unlisted
grade: UNLISTED
class: spec-gap
at: 2026-10-20T04:05:15Z
attempt: 1
claim: §4.4 says RenderedFile carries the owner but not in what shape, and its JSON is the output of secret render --json
evidence: rfcs/0032-secrets-a-non-root-container-can-read.md:211
action: decided
proposal: ASSUMED — RenderedFile gains uid and gid, numbers read from the rendered file, present on every file
```

Two places where §5's text and the build differ, neither a decision:

- The requester's done-when runs in `just acceptance`, not a Go test. The
  acceptance script already has the built static binary and Compose, and no Go
  test builds the binary today.
- §5's sabotage says dropping the owner from `Verify` fails the battery. The
  battery holds the stores to their contract and never runs `Verify`, so a
  test of the render step's `Verify` is what goes red.

## As built

The one `LOCKED` decision this touches is built as written.

```divergence
decision: D-1
grade: LOCKED
kind: resolved
at: 2026-10-20T04:21:49Z
attempt: 1
claim: a declaration may carry owner, a numeric uid or uid:gid, applied to its rendered file on every render; absent leaves the file with the manager, as before
evidence: internal/domain/secret.go:157
action: decided
```

Sabotage, each run against its test and restored:

| Sabotage | Red |
|---|---|
| No chown in `WriteFileInAs` | both stores' "render gives each file to its declared owner" and "render refuses an owner it cannot give the file to"; the acceptance step, at the unprivileged render's exit 6 |
| The fake ignores `owner` | the fake's two owner cases in the battery |
| No owner in the render step's `Verify` | `TestTheRenderedOwnerIsVerifiedNotAssumed` |
| macOS let through | `TestADarwinManagerRefusesEveryOwner` |
| No floor in `LoadSecretSchema` | `TestASchemaDeclaringAnOwnerNeedsTheFloor` |
| EPERM not named as giving the file away | `TestAnUnprivilegedChownIsNamedForWhatItIs` |
| An unquoted owner accepted | `TestSecretOwnerIsNumeric/only_a_quoted_owner_decodes` |
| 4294967295 accepted | `TestSecretOwnerIsNumeric` |

What no test sees: each store calls `OwnerRefusal(runtime.GOOS)`, and no lane
runs on macOS, so a store that dropped the call would pass on Linux. The
function it calls is tested with darwin's name; the call is one unconditional
line in each store.

The acceptance step mounts the host's `sops` into a busybox container to render
as root. CI installs the static release binary, so the step runs there and a
binary that does not run in the container fails it. A distribution's `sops` is
usually linked against the host's libc; there, outside CI, the step says it is
skipped and `ACCEPTANCE_SOPS` names a static one. It was run here with the
checksum-verified 3.13.3 release binary.

## Review, 2026-10-20

- `WriteFileInAs` fsynced the temp file before setting its mode and owner,
  so neither was covered by the fsync before the rename published the file.
  The mode had been set after the fsync since before this change. The fsync
  now follows both. No test can crash between the two, and in production the
  render directory is a tmpfs that a crash empties anyway, so this rests on
  what fsync promises rather than on a red run.
- The fake refused a macOS owner only after reading the secrets and checking
  the required ones, so a missing secret came first where the real store
  refuses the owner first. It now refuses before reading, as `sopsage` does.
- `TestSecretRenderWritesToTmpfsAtFourHundred` read a missing `uid` as 0, which
  would pass as root. It now fails when the key is absent.
