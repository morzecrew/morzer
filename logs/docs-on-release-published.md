# Docs on release published · a release's docs wait for its assets

On branch `docs-on-release-published` from `e6fe1cb`. Found on 0.8.0's release
prep (#167): `docs-release.yaml` runs on the `v*` tag push, while `release.yaml`
runs the full CI on the same tag and then leaves a draft release for a
maintainer to publish. Between the two, `latest` and the site root's
`install.sh` told readers to download `--version 0.8.0` assets that were not
public yet.

**Drift count: 0.**

| Class | Test | Meaning |
|---|---|---|
| `discovery` | Could not have been known before code existed | Healthy — the spec was right to be silent |
| `spec-gap` | Could have been known; the spec was silent or at the wrong altitude | The design process missed something |
| `drift` | The spec covered it and it was built otherwise anyway | **A defect** |
| `irreducible` | No amount of design settles it | Stop and spike |

RFC 0006 carries no grades, so its rows are treated as `LOCKED`. Its decision 3
and §5's table say a pushed tag publishes a version, which is the trigger at
fault, so this halted for a ruling.

```divergence
decision: RFC 0006 D-3
grade: LOCKED
class: spec-gap
at: 2026-10-29T12:30:00Z
attempt: 1
claim: deploying a release's docs on the tag push publishes them before release.yaml has run its CI and a maintainer has published the draft, so the docs and the site root name assets that are not downloadable yet
evidence: .github/workflows/release.yaml:15-26
action: halted
proposal: LOCKED — refines D-3: a release's docs deploy on the release's `published` event; the version, the `latest` alias and the site root are decided as before
```

## Ruled 2026-10-29

The author asked for the docs to deploy when the release is published. Recorded
as RFC 0006 decision 20. §5's table and decision 3 are left as written, as the
design that shipped then.

## As built

- `docs-release.yaml` runs on `release: types: [published]` and keeps
  `workflow_dispatch`. A release event's `GITHUB_REF_NAME` is its tag, so
  `resolve-docs-version.sh` is unchanged.
- The release a maintainer publishes from the draft raises the event. One a
  workflow published with `GITHUB_TOKEN` would not; `release.yaml` leaves a
  draft, so nothing here depends on that.
- The nightly installer check already compares the site with GitHub's latest
  *published* release (`installer-drift.sh`), so the root and the release it is
  compared with now change at the same moment, where the tag trigger moved the
  root first.

Not run: the workflow itself, which only a published release can trigger. A
prerelease raises the same event, as a prerelease tag raised the push; that is
unchanged.

## Review, 2026-10-29

`resolve-docs-version.sh` took the newest minor from every git tag, so a newer
tag whose release was still a draft kept a published older line's release from
moving `latest` and the root. RFC 0006 decision 18 and the script's own comment
say `latest` follows the newest *released* minor, and under the published
trigger a draft is not released. The script now asks the API for the published
releases and adds the one being deployed. No decision changes, so no entry: the
code now does what decision 18 says.

Checked with a stand-in `gh` against three cases: 0.9.1 published while 0.10.0
is a draft moves `latest`; 0.10.0 published moves it; a 0.8.5 backport with
0.9.0 published does not.

**Drift count: 0.**

A manual run (`workflow_dispatch`) from a tag whose release is still a draft
also counted that tag, so it could move `latest` ahead of the release. The
deployed tag is now counted only when a `published` event runs the workflow, in
case the API has not caught up with the release that event just published; a
manual run counts it only if its release is published. Checked with the
stand-in `gh`: a manual run from a draft v0.10.0 leaves `latest`, one from the
published v0.9.0 moves it, and a `published` event moves it even when the API
does not list the release yet.

**Drift count: 0.**
