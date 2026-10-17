# Docs 0.4 corrections · pages that described what 0.4.0 does not do

ERP request M9, on branch `docs-0-4-corrections` from `f927b4c` (v0.4.0). Every
correction was checked against the code before it was written:

- `reference/manifest.md`: the `runtime` row said "deprecated, still read". It
  is refused since 0.3.0, as the section further down the same page says.
- `reference/hooks.md`: the phase list named `preflight`, `pre-update` and
  `post-update`. Only `migrate`, `smoke-test`, `backup`, `restore` and
  `health-check` are passed to a hook (`apply.go:767`, `:938`, `:975`;
  `hookbackup/backup.go:335`, `:968`).
- `authoring/publishing.md`: showed `policy.signing_keys` in
  `installation.yaml`, a report whose edits change nothing. It now shows
  `init --signing-key` and `release trust add`.
- `operating/updating.md`: repeated `update.check` as an `installation.yaml`
  block under the `config set` that does it.
- Found by the same search: `operating/backups.md` called `installation.yaml`
  "the file an operator edits"; `reference/parameters.md` said values are
  recorded there; `reference/release-commands.md` said the trusted keys are set
  at `init` with no mention of `release trust`; and
  `operating/several-installations.md` called the file "the truth", where what
  it decides is which installations exist.
- Left alone: `operating/notifications.md` shows `notify.targets` in
  `installation.yaml` because nothing else sets them yet. That is request M4,
  and the page changes with the command that replaces the edit.

**Drift count: 0.**

| Class | Test | Meaning |
|---|---|---|
| `discovery` | Could not have been known before code existed | Healthy — the spec was right to be silent |
| `spec-gap` | Could have been known; the spec was silent or at the wrong altitude | The design process missed something |
| `drift` | The spec covered it and it was built otherwise anyway | **A defect** |
| `irreducible` | No amount of design settles it | Stop and spike |

The phases the manager never runs are documented as reserved rather than
removed. RFC 0001 §8 calls `post-update` reserved by the ABI for a future
release. A hook that already branches on one of these names is not wrong, only
unreached. Dropping a name from a published ABI page would make the release
that first runs it look like a new phase rather than the reserved one arriving.

```divergence
decision: unlisted
grade: UNLISTED
class: spec-gap
at: 2026-10-17T20:55:21Z
attempt: 1
claim: the hook ABI page listed eight phases as values PHASE takes; three of them, preflight, pre-update and post-update, are declared constants no lifecycle path passes, and the page now lists the five that are passed and names the three as reserved
evidence: pages/docs/reference/hooks.md:71-73
action: decided
proposal: ASSUMED — a hook phase the ABI declares and the manager never runs is documented as reserved, never dropped from the published ABI
```

**Drift count: 0.**
