# Release 0.3.0 · The notes that never reached the release

> **Migrated from `rfcs/EXECUTION-LOG.md`, verbatim.** These entries predate the
> ```divergence``` block format and are reproduced in the prose form they were
> written in. They are deliberately *not* rewritten to satisfy the current
> schema: this record is append-only, and retrofitting `at` stamps, `attempt`
> numbers and evidence citations that resolve against today's line numbers would
> be editing what was decided to match what a checker wants — the one thing the
> format exists to prevent.
>
> `log_check.py` runs against logs that have a task file in `tasks/`. These have
> none, on purpose.

## Classes

| Class | Test | Meaning |
|---|---|---|
| `discovery` | Could not have been known before code existed | Healthy — the RFC was right to be silent |
| `spec-gap` | Could have been known; the RFC was silent or at the wrong altitude | The design process missed something |
| `drift` | The RFC covered it and it was built otherwise anyway | **A defect** |
| `irreducible` | No amount of design settles it | Stop and spike |

---

Branch `fix/release-notes-never-reached-the-release`. No RFC — a defect found
in the published artefact of the 0.3.0 release, fixed against the release
pipeline rather than against a design.

**Drift count: 0.** One defect, found in production rather than by a wave, and
against a mechanism that had never run before: `--release-notes` and
`changelog.disable: true` were introduced by the same PR (#47), after v0.2.0
was tagged. v0.3.0 is the first release that exercised either, and the two are
mutually exclusive.

## D-070 — `changelog.disable` disables the flag that replaces the changelog

- **Touches:** `.goreleaser.yaml`, `.github/workflows/release.yaml`, PR #47
- **Symptom:** v0.3.0 published with an **empty body**, and every job in the
  release run reported success.
- **Not what it looked like.** The notes were built correctly: the
  `Build the release notes from CHANGELOG.md` step ran `release-notes.sh`,
  emitted 99 lines, and printed all of them into the job log. GoReleaser was
  invoked with `--release-notes=/home/runner/work/_temp/release-notes.md`. Both
  halves were right and the release was still empty.
- **Mechanism**, from GoReleaser v2.17.1's own source rather than inferred —
  `internal/pipe/changelog/changelog.go`:
  - `Skip()` (line 45) returns `ctx.Config.Changelog.Disable`.
  - `Run()` (line 66) is the **only** caller of
    `loadContent(ctx, ctx.ReleaseNotesFile, ...)` and the **only** assignment
    to `ctx.ReleaseNotes` from that file (line 71).
  - So `disable: true` skips the one pipe that reads `--release-notes`. The
    file is never opened, `ctx.ReleaseNotes` stays empty, and nothing errors:
    from GoReleaser's side it was asked to skip a step and it skipped it.
- **Built:** `disable: true` removed. Verified with the real binary
  (`goreleaser v2.17.1`, the version CI used) against the v0.3.0 tag: with
  `disable`, the `generating changelog` pipe is absent from the run; without
  it, the pipe runs.
- **Removing it does not restore the commit-log body #47 killed.** `Run()`
  loads the file, assigns it, and returns at line 73 before the commit walk. A
  run *with* the flag generates nothing from git. Measured both ways: with the
  flag, no `dist/CHANGELOG.md` is written; without it, one is — 2130 bytes of
  subjects and full SHAs, which is v0.1.0's body exactly.
- **Class:** `spec-gap` — knowable before the release, and knowable by exactly
  the reading nobody did: what `disable` disables. The comment beside it named
  the flag it was breaking, three lines above it.
- **Consequence:** the guard added below now fails a release whose body is not
  the notes, so this cannot recur silently for any cause.

## D-071 — A gate that validates its own output stops short of the artefact

- **Touches:** `.github/scripts/verify-release-notes.sh` (new)
- **Found by:** asking why D-070 was not caught, given that a guard for exactly
  this exists and ran.
- `release-notes.sh` opens with *"A missing section is an error, not an empty
  release"* and refuses to emit empty notes. It did its job. It guards **what
  it produces** and says nothing about **what is published**, and the whole
  defect lived in the space between those two.
- **Built:** a post-GoReleaser step that reads the release back and compares
  its body against the notes file. It runs while the release is still a draft,
  so a mismatch costs a re-run rather than a re-release.
- **It compares rather than checking for emptiness, and that is the load-bearing
  choice.** With `disable` gone, the *next* way to lose the notes is a run that
  loses the flag — and that produces a body that is full of the wrong thing
  rather than empty. A non-empty check passes it. Measured: 2130 bytes of
  commit log, which is precisely the failure #47 existed to prevent.
- **Verified red before green**, three ways, with the published release as the
  fixture: a matching body exits 0; an empty body exits 1 naming the changelog
  pipe; a commit-log body exits 1 on the size and diff.
- **Class:** `spec-gap`

## Rules distilled

- **Disabling a pipe disables everything that pipe reads.** `disable: true` on
  a stage reads as "skip this feature" and means "skip this code path" — flags
  the stage parses stop being parsed, and a flag that is never read fails
  silently rather than loudly. (D-070)
- **A guard on your own output is not a guard on the artefact.** Validate the
  thing that ships, not the thing you hand to whatever ships it; the gap
  between them is where a green pipeline publishes a wrong release. (D-071)
- **When the near miss is not emptiness, do not check for emptiness.** Compare
  against what was built. An emptiness check would have passed the commit-log
  body — the exact output the mechanism it guards was built to eliminate.
  (D-071)
- **Green CI is a claim about the steps, not about the result.** Every job in
  the v0.3.0 release run succeeded, and the release was wrong. (D-070)

## Carried into the next unit

- ~~**Cutting 0.3.0**~~ — closed, and checked against the published artefact
  rather than against a locally stamped build. Downloaded
  `morzer_0.3.0_linux_amd64.tar.zst` from the release, verified `SHA256SUMS`
  against `morzer.pub` (*trusted comment: morzer 0.3.0*) and the archive
  against the sums, unpacked it, and ran the binary: `morzer 0.3.0` scaffolds a
  bundle stamped `min_manager_version: 0.3.0` and then plans it — *would create
  an installation for demo*. The floor names a manager that exists, and the
  manager it names is the one an operator downloads.
- **A plan does not validate the bundle it plans against** (D-055, wave 34).
- **`FieldRemovalRelease` is a single-member design with no members** (D-052).
- **`Installation.Providers`** — RFC 0027's question, not gated on P3. Wave 36.
- **`saveInstallation` writes its report before the state store** (wave 31),
  the oldest item in this file.
- **`release.draft: true` means a human publishes.** That human is the last
  reader of the notes, and for v0.3.0 the draft was published without them
  being there to read. The guard above now refuses before that point, but the
  step is still a manual one and is worth naming as such.
