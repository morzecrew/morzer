# archive-inputs · `release verify` and `release show` take an archive

On branch `archive-inputs` from `ac45dd6`, building Phase 2's third item of the
gap report: the two commands that inspect a bundle accept the `tar.zst` archive
an author ships, not only its unpacked directory. No RFC covers it; the area is
[RFC 0014](../rfcs/0014-building-a-release-bundle.md)'s and
[RFC 0013](../rfcs/0013-bundle-authoring-experience.md)'s, whose rows settle
no input format for these commands. That covers:

- `release verify <archive>`, with every check it runs on a directory, `--signing-key` and `--digest` included;
- `release show <archive>`;
- the help and the reference saying so.

**Drift count: 0.**

| Class | Test | Meaning |
|---|---|---|
| `discovery` | Could not have been known before code existed | Healthy — the spec was right to be silent |
| `spec-gap` | Could have been known; the spec was silent or at the wrong altitude | The design process missed something |
| `drift` | The spec covered it and it was built otherwise anyway | **A defect** |
| `irreducible` | No amount of design settles it | Stop and spike |

An archive has to be unpacked before anything in it can be read, and `init`
and `update` already unpack one.

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-01T23:10:00Z
attempt: 1
claim: how `verify` and `show` unpack an archive is not settled, and unpacking is where a hostile archive is stopped
evidence: internal/adapters/source/local/local.go:94
action: decided
proposal: ASSUMED — both unpack through the local source's `Fetch`, into a temporary directory removed when the command ends, so the same limits apply as at `init` and `update` and errors name the archive. `release show` reports the archive as the release's root, since the directory it was read from is gone.
```

`release archive` refuses any suffix but `tar.zst`, and the gap report's
`.tar.gz` is a file no command reads.

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-01T23:11:00Z
attempt: 1
claim: a file that is not a tar.zst archive is refused as "not a directory", which does not say what is accepted
evidence: internal/release/load.go:89
action: decided
proposal: ASSUMED — a regular file that is not a tar.zst archive is refused saying that a bundle is a directory or a tar.zst archive, and that `release archive` writes one.
```

**Drift count: 0.**
