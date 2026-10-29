# Version from git describes the bundle · a commit elsewhere leaves the version alone

On branch `version-from-git-bundle` from `6ff5dc5`, for issue #182.
`release build --version-from-git` ran `git describe --tags --long --dirty` on
HEAD, while the dirty check beside it was scoped to the bundle directory. In a
repository holding more than the bundle, every commit outside it minted a new
dev version for the same bundle, so a pipeline's "already published" check
never fired. Reproduced: a README commit moved `1.4.1-dev.3.g677b471` to
`1.4.1-dev.4.geab1868`, and an edit to a tracked README refused the build as
dirty.

**Drift count: 0.**

| Class | Test | Meaning |
|---|---|---|
| `discovery` | Could not have been known before code existed | Healthy — the spec was right to be silent |
| `spec-gap` | Could have been known; the spec was silent or at the wrong altitude | The design process missed something |
| `drift` | The spec covered it and it was built otherwise anyway | **A defect** |
| `irreducible` | No amount of design settles it | Stop and spike |

RFC 0014 carries no grades, so its rows are treated as `LOCKED`. Its §5.2 says
`--version-from-git` runs `git describe --tags --long --dirty`, which describes
HEAD, so this halted for a ruling.

```divergence
decision: RFC 0014 D-5
grade: LOCKED
class: spec-gap
at: 2026-10-29T18:29:13Z
attempt: 1
claim: describing HEAD gives the same bundle a new version for every commit outside its directory, and `--dirty` refuses a build over a tracked edit outside it, while the status check beside it is scoped to the directory
evidence: internal/release/version.go:51
action: halted
proposal: LOCKED — refines D-5: `--version-from-git` describes the last commit that changed the bundle directory, and only that directory's uncommitted changes make a build dirty
```

## Ruled 2026-10-29

Issue #182, opened by the author, asks for this: describe the last commit that
changed the bundle directory, keep the dirty check as it is. It offers a
separate spelling if the change should be opt-in. Two choices the issue left
open, decided here:

```divergence
decision: unlisted
grade: UNLISTED
class: spec-gap
at: 2026-10-29T18:29:13Z
attempt: 1
claim: the issue leaves open whether the change replaces what --version-from-git prints or arrives under a new spelling; it replaces it, because a version that moves for the same bundle is what the flag's never-republish advice cannot survive; in a repository holding only the bundle the version moves only when HEAD changed no file, such as a merge whose result matches one parent, and then names the commit that did
evidence: internal/release/version.go:124-127
action: decided
proposal: ASSUMED — `--version-from-git` keeps its name and describes the bundle; no opt-in spelling
```

```divergence
decision: unlisted
grade: UNLISTED
class: discovery
at: 2026-10-29T18:29:13Z
attempt: 1
claim: describing the last change on its own names the tag nearest to it, so a release tagged on a commit that did not touch the bundle builds as the previous release's dev version, and a change merged from a branch cut before the newest tag sorts below that tag; the distance is counted from HEAD's nearest tag to the change instead, so a change the tag already holds is the tag verbatim
evidence: internal/release/version.go:68-78
action: decided
proposal: ASSUMED — the distance is the commits the bundle's last change holds past HEAD's nearest tag; zero renders the tag verbatim
```

```divergence
decision: unlisted
grade: UNLISTED
class: spec-gap
at: 2026-10-29T18:29:13Z
attempt: 1
claim: decision 14's commit date was HEAD's, so the same bundle archived at two commits got two timestamps; it is now the date of the bundle's last change, the commit the version names
evidence: internal/release/version.go:102-114
action: decided
proposal: ASSUMED — refines D-14: the commit date in the archive's timestamp precedence is that of the last commit that changed the bundle directory
```

Recorded as RFC 0014 decision 20. §5.2 is left as written, as the design that
shipped then.

## As built

- `DescribeRepository` runs `git describe --tags --long` on HEAD for the tag,
  `git log -1 -- .` in the bundle directory for the last change, and
  `git rev-list --count refs/tags/<tag>..<change>` for the distance. A
  directory no commit has touched falls back to HEAD; it is all untracked, so
  the dirty check refuses it anyway.
- `--dirty` is gone from the describe call. `workTreeDirty` was already the
  check that mattered; it is now the only one.
- `CommitTime`, which `release archive` reads, uses the same last change.
- A shallow clone still fails at the describe call, with the existing hint.

Tests: `TestTheVersionDescribesTheBundleNotTheRepository` (a commit and an edit
outside the bundle, a tag on such a commit, a commit past that tag) and
`TestAChangeMergedPastATagSortsAfterIt`. Three sabotages each fail at least one
of them: dropping the pathspec, counting from the tag nearest the change, and
describing the change on its own.
