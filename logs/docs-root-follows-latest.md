# Docs root follows latest · a backport no longer republishes the site root

On branch `docs-root-follows-latest` from `1d8d671`. Found while checking #113:
`publish-site-root.sh` runs on every `v*` tag. A backport tag on an older line,
0.3.5 after 0.4.0, publishes the docs to `0.3` and leaves `latest` alone, but it
also copies its own `install.sh` and `schemas/` over the root's. Everyone
curling the documented installer URL would get 0.3.5's script.

**Drift count: 0.**

| Class | Test | Meaning |
|---|---|---|
| `discovery` | Could not have been known before code existed | Healthy — the spec was right to be silent |
| `spec-gap` | Could have been known; the spec was silent or at the wrong altitude | The design process missed something |
| `drift` | The spec covered it and it was built otherwise anyway | **A defect** |
| `irreducible` | No amount of design settles it | Stop and spike |

RFC 0022 carries no grades, so its rows are treated as `LOCKED`. Its §5 says
the workflow "re-publishes the file on every docs release", which is the
behaviour at fault, so this halted for a ruling.

```divergence
decision: RFC 0022 D-11
grade: LOCKED
class: spec-gap
at: 2026-10-18T20:52:17Z
attempt: 1
claim: re-publishing the root on every docs release lets a backport tag replace the root's install.sh and schemas/ with older ones; RFC 0022 chose the root for its lifetime and did not consider a release older than the newest
evidence: .github/workflows/docs-release.yaml:75
action: halted
proposal: LOCKED — refines D-11: install.sh and schemas/ are published at the root only by a docs release that moves `latest`, and the nightly job compares the site's schemas/ with the newest release as it does install.sh
```

## Ruled 2026-10-18

Two ways were offered: publish the root only when `latest` moves, or have every
docs release publish the newest tag's files. The user chose the first. It is
one condition the workflow already computes, where the second needs code that
runs only on a backport. Its cost is that a backport no longer repairs a root
file mike dropped. The nightly job covers that: it now compares `schemas/` as
well as `install.sh`, and the repair is to re-run the newest tag's docs release.

Recorded as RFC 0022 decision 18. §5's sentence is left as written, as the
design that shipped then.

## As built

- `docs-release.yaml`: the site-root step has
  `if: steps.ver.outputs.move_latest == 'true'`.
- `installer-drift.sh`: for a tag whose tree has `publish-site-root.sh`, each
  file under `schemas/` at the tag is fetched from the site and compared. An
  older tag gets a notice, as one from before `install.sh` does. v0.4.0 is such
  a tag, so the nightly job passes until the next release publishes the schemas.

Checked by hand against a `file://` site with a fake `gh`: matching schemas
report nothing, a changed or a missing schema is an error, and a tag without
`publish-site-root.sh` gets the notice. The workflow condition has no offline
test.

Open: a manual run of this workflow from `main` fails, because
`resolve-docs-version.sh` refuses a ref that is not a release tag. A manual run
from the v0.4.0 tag uses v0.4.0's workflow, which publishes no schemas. So the
schemas reach the site with the next release, not before.
