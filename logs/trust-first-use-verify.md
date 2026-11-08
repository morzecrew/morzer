# trust-first-use-verify · RFC 0052 P2, the vendor side

On branch `trust-first-use-verify` from `main` at `1e783c98`, building
[RFC 0052](../rfcs/0052-trusting-a-releases-key-on-first-use.md) P2: §5.6 and
decision 14, with `publishing.md`. P1, the manager side, follows separately
and reuses the key reader this adds.

**Drift count: 0.**

| Class | Test | Meaning |
|---|---|---|
| `discovery` | Could not have been known before code existed | Healthy — the spec was right to be silent |
| `spec-gap` | Could have been known; the spec was silent or at the wrong altitude | The design process missed something |
| `drift` | The spec covered it and it was built otherwise anyway | **A defect** |
| `irreducible` | No amount of design settles it | Stop and spike |

## Plan

- `internal/ports/source.go`: `PublicKeyFileName = "minisign.pub"` beside
  `SignatureFileName` (decision 2). A `ShippedKeyReader` capability, optional
  like `KeyChecker`, returning a `ShippedKey` (the key line and its id), zero
  when the bundle ships none.
- `internal/adapters/verify/minisign/minisign.go`: `ShippedKey` reads the file
  `minisign -G` writes, with the parser verification uses, and checks the
  bundle's signature against it. It refuses a key file that does not parse, a
  bundle that ships one with no signature, and a signature that does not
  verify against it (decision 3's rule, applied where the vendor checks). The
  signature-checking loop is shared with `VerifySigned`.
- `internal/adapters/verify/chain.go`: forwards `ShippedKey` to the member
  that can read one, as `CheckKey` is forwarded.
- `internal/cli/release.go`: `release verify` runs the check after the
  `--signing-key` one. Without `--signing-key` it says that a shipped key
  matching the signature proves only that the two agree (decision 14).
- `internal/cli/release_archive.go`, `internal/cli/release_push.go`: the same
  check (entry below).
- `internal/ui/views/release.go`: `Verified` gains `shipped_key_id`,
  omitted when the bundle ships no key.
- Tests: the adapter in `test/suite`; `release verify` against a matching, a
  stale and a missing `minisign.pub`, with and without `--signing-key`, and a
  shipped key with no signature, in `test/clitest`; `archive` and `push` one
  refusal each.
- Docs: `pages/docs/authoring/publishing.md` (ship the key, keep publishing
  it, what `verify` checks, rotation); `pages/docs/reference/release-commands.md`
  where it describes the three commands. The changelog.

Decisions the plan needs that the RFC does not settle: three, one of them
load-bearing (the first), below.

```divergence
decision: 14
grade: UNLISTED
class: spec-gap
at: 2026-11-08T15:20:28Z
attempt: 1
claim: §5.6 and decision 14 say what `release verify` does with a shipped key and are silent on one it ships without a signature. §5.1 and decision 3 refuse that bundle at `init`, "because a release that ships a key declares that it is signed", so every first-use install of it would be refused. `verify` refuses it too, so the vendor finds out first.
evidence: rfcs/0052-trusting-a-releases-key-on-first-use.md:151-157
action: decided
proposal: ASSUMED — `release verify` refuses a bundle that ships `minisign.pub` with no `SHA256SUMS.minisig`, with or without `--signing-key`, for the reason decision 3 gives.
```

```divergence
decision: 14
grade: UNLISTED
class: spec-gap
at: 2026-11-08T15:20:28Z
attempt: 1
claim: `release archive` and `release push` both say they check a bundle "the same way `release verify` does", and `push` is the step after which a stale `minisign.pub` is in a registry. Checking the shipped key in `verify` alone would make both statements false, and would let `push` publish what decision 14 exists to stop. `release build` is left out: it runs before signing, when a key with no signature is the expected state.
evidence: internal/cli/release_push.go:34-35
action: decided
proposal: ASSUMED — `release archive` and `release push` check a shipped key as `release verify` does. `release build`, which runs before the bundle is signed, does not.
```

```divergence
decision: 14
grade: ASSUMED
class: spec-gap
at: 2026-11-08T15:20:28Z
attempt: 1
claim: decision 14 speaks of "the" given key, and `--signing-key` repeats. With several, the shipped key has to be the one that signed the bundle, not merely one of those given: a vendor mid-rotation who passes both keys, signs with the new one and still ships the old is refused, because a first-use `init` would refuse that release (decision 3). Checking the signature against the shipped key gives that rule and needs no comparison of key strings: a minisign signature names the id of the key that made it.
evidence: internal/adapters/verify/minisign/minisign.go:130-135
action: departed
proposal: ASSUMED — refines decision 14. With `--signing-key`, the bundle's signature must verify against a given key and against the shipped key, so the shipped key is the one that signed it.
```

## Build, 2026-11-08

Built as planned, with the three decisions above. The key reader is
`ShippedKey` on the minisign adapter, behind `ports.ShippedKeyReader`. It
shares the signature loop with `VerifySigned` (`signedBy`), and the chain
forwards it. `release verify`, `archive` and `push` call it through one
helper, `checkShippedKey` (`internal/cli/release.go`). Its refusals name the
key's id and never the key, and their wording is meant for either audience,
so P1 can use them at `init`.

## Self-audit, 2026-11-08

Scope: the branch against `1e783c98`: code in the adapter, chain, port, three
commands and the view, tests in `test/suite` and `test/clitest`, two doc
pages, the changelog and this log.

- **Sabotage:** nine mutations, each run against the tests written for it.
  Each went red:
  - the shipped key's signature check skipped;
  - a key with no signature accepted;
  - the check dropped from `verify`;
  - the check dropped from `archive`'s unsigned path;
  - the check dropped from `archive`'s signed path;
  - the check dropped from `push`;
  - a key file accepted without its comment line, or with two keys;
  - the "proves only" note dropped;
  - the view's shipped-key field dropped.
- **Against real minisign:**
  - Keys made by `minisign -G -W` and a signature by `minisign -S` verify.
  - The id printed matches the one minisign writes in the key's comment.
  - A stale `minisign.pub` is refused under `--signing-key`.
  - `minisign -Vm SHA256SUMS -p minisign.pub` accepts the same files.
- **Residue:**
  - Before this branch, `release verify --signing-key` with a malformed key
    reports `policy.signing_keys[0] is not a valid minisign public key`,
    which is the installation's wording on a build machine. `push` checks the
    keys first and avoids it. Left as it is: out of scope here.
  - Nothing on a deployment host reads the key yet; that is P1.
