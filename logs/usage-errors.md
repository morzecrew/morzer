# usage-errors · usage errors without the usage dump, examples, docs links in help

On branch `usage-errors` from `6a0c877`, Phase 1 of the gap report: a usage
error printed the command's whole usage, cobra's argument-count messages named
a count rather than the argument, examples were missing on commands that need
them, and no help page pointed at the documentation. Governed by
[RFC 0019](../rfcs/0019-the-command-surface.md), whose decision table is
ungraded, so every row and recorded position is treated as `LOCKED`.

**Drift count: 0.**

| Class | Test | Meaning |
|---|---|---|
| `discovery` | Could not have been known before code existed | Healthy — the spec was right to be silent |
| `spec-gap` | Could have been known; the spec was silent or at the wrong altitude | The design process missed something |
| `drift` | The spec covered it and it was built otherwise anyway | **A defect** |
| `irreducible` | No amount of design settles it | Stop and spike |

The plan asked for examples on a list of commands that includes `apply`,
`backup`, `status`, `doctor` and `config get`. RFC 0019 §5.9 rule 3 and its
recorded divergence put examples "where the invocation is not obvious, and
nowhere else", and say a sweep was deliberately not shipped. The sweep stops at
that position: examples went on `init`, `update`, `rollback`, `restore` and
`release verify`, whose invocations the usage line does not make obvious, and
on none of the others.

```divergence
decision: RFC 0019 §5.9 rule 3
grade: LOCKED
kind: blocked
at: 2026-10-30T10:20:00Z
attempt: 1
claim: an Example on every listed command is the sweep §5.9 rejects; apply, backup, status, doctor and config get read plainly from their usage lines
evidence: rfcs/0019-the-command-surface.md:617-629
action: halted
proposal: none — the rule stands; reopening it is the author's call, not this branch's
```

Three choices the RFC does not cover, made here and owed as rows.

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-10-30T10:25:00Z
attempt: 1
claim: the usage dump on a flag error, cobra's argument-count wording, and links from help to the site were each unsettled by RFC 0019
evidence: internal/cli/root.go:202-213
action: decided
proposal: ASSUMED — a usage error is the error and a hint naming `<command> --help`, never the usage; an argument-count refusal names what the usage line says the command takes; a group's help ends with one `Docs:` link under the site's `latest` alias, which docs-check resolves to a page.
```
