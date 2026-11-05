# RFC 0048 — A configuration target that lands on an existing file

- **Status:** ✅ Complete — proposed 2026-11-04, from
  [#237](https://github.com/morzecrew/morzer/issues/237), and ruled by the
  author 2026-11-04: §11's `OPEN` rows as recommended. **Built 2026-11-05** in
  #273; the entries its build and review wrote in
  [logs/preexisting-target.md](../logs/preexisting-target.md) are decisions 9–15.
- **Scope:** What happens to a file that is already at a path the release
  about to be installed renders for the first time. That covers a release
  that adds a configuration target, and a first install onto a machine that
  already has a file at a target. It covers what `update` and `apply` do with
  such a file when someone is watching and when nobody is, what `--dry-run`
  shows, and where a copy is kept. Not covered: a target the installed release
  already renders, which is drift and RFC 0044's; files a newer release stops
  rendering; and `rollback` removing the targets the release it returns to
  does not declare. *(2026-11-05: a rollback returning to a release that
  renders a target the release it leaves does not is covered, and keeps a
  copy as an update does; see decision 15.)*
- **Related:** [0044](0044-configuration-drift-an-operator-can-see.md) (drift, `config diff` and
  the unattended hold, decisions 4 and 15);
  [0034](0034-removing-an-installation.md) (removal leaves targets outside the
  configuration directory behind);
  [`internal/lifecycle/ops/apply.go`](../internal/lifecycle/ops/apply.go)
  (`stepRenderConfiguration`);
  [`internal/lifecycle/ops/config_diff.go`](../internal/lifecycle/ops/config_diff.go)
  (`heldForDrift`, `driftedFiles`);
  [`internal/lifecycle/ops/channel.go`](../internal/lifecycle/ops/channel.go)
  (the unattended tick's gates);
  [`pages/docs/reference/manifest.md`](../pages/docs/reference/manifest.md)
  (`configuration[].target`).

---

## 1. Summary

A release can declare a configuration target at any absolute path on the
host. When a release declares one the installed release did not, and a file
is already there, the update replaces it. Nothing says so, and nothing keeps
it. RFC 0044's drift checks cannot see the file, because they compare against
what the *installed* release renders, and this path is not one of its targets.
The unattended hold exists so that a hand edit is not replaced with nobody
watching, and this is the one case it misses.

This RFC proposes that the plan names such a file, and that the update keeps
a copy under the installation's data directory and says where. An unattended
update holds, as it does for drift. The author ruled §11's decisions 3 and 4
as recommended: an attended update keeps a copy, and the tick holds.

## 2. Motivation

The paths are not confined to the installation's own directories. The
manifest reference says only "Absolute destination on the host"
(`pages/docs/reference/manifest.md:378`). A vendor adding
`/etc/nginx/conf.d/demo.conf` in 1.3.0 overwrites whatever the operator kept
at that name. That might be the reverse-proxy block they wrote by hand
because 1.2.0 shipped none.

The update keeps the old bytes, but only while it runs. `stepRenderConfiguration`
reads each existing file before writing it, and its compensator puts them back
if the operation fails (`internal/lifecycle/ops/apply.go:572`, compensated at `:618`). An update
that succeeds discards them with the operation's state. The operator learns
the file is gone when the service that read it misbehaves.

A first install is the same case. `installation remove` empties the
configuration directory but cannot know about a target elsewhere
(`internal/lifecycle/ops/remove.go:446-449`). A reinstall finds the file the
earlier installation rendered, or one the operator wrote in between.

## 3. Current state

- **`update` and `apply` render every target** in `stepRenderConfiguration`.
  The step's `Check` compares bytes, and its `Execute` keeps an in-memory
  backup per target for the compensator (`apply.go:476-620`).
- **The plan's detail for that step** is `configComparison` against the
  release being installed (`apply.go:553-559`). A file at a new target shows
  as a difference, the same as any other changed file. Nothing marks it as a
  file the installed release never rendered.
- **The unattended tick** holds a candidate while `heldForDrift` counts
  differing targets of the *installed* release that exist on disk
  (`config_diff.go:283-316`, `channel.go:553`). RFC 0044 decision 4 is
  `LOCKED` on the reason: "A hand edit replaced with nobody watching cannot be
  recovered". The candidate's manifest is loaded later, by `assessUnattended`
  (`channel.go:665`).
- **`config diff` and `doctor`'s `config.drift`** compare against the
  installed release only, and that is right for them: between updates, a file
  at a path no release renders is not the manager's business.

## 4. Goals / Non-goals

**Goals**

- An operator reading `update --dry-run` sees, apart from ordinary changes,
  each file the update will replace that the installed release never rendered.
- No such file is lost: either it is kept, or the update does not run.
- An unattended update does not replace one with nobody watching.

**Non-goals**

- Detecting the file between updates. `doctor` has no release to compare it
  with until one is staged.
- Merging the operator's file with the vendor's.
- Undoing an update's new targets on `rollback`. That is a wider gap (§8).

## 5. Design

### 5.1 Which files count

A file counts when all of these hold:

- the release about to be installed declares a target at its path, after
  `configTarget`;
- the installed release declares no target at that path, or no release is
  installed;
- something exists at the path (`Lstat` succeeds), and its bytes differ from
  what the new release renders.

An identical file counts for nothing, since nothing in it is lost. A file
that exists and cannot be read is listed apart, as `config diff` lists one.
The render step already refuses to replace it (`apply.go:600-604`).

"The installed release" is the one recorded as current, but only once an
`apply`, `update`, `rollback` or `restore` has succeeded on the machine.
`init --release` records a release before anything renders it, so until then
no release is installed for this purpose, and the first `apply` treats every
target as new. Paths are compared after `filepath.Clean`. *(2026-11-04: read
when the render runs, and a current release that cannot be loaded declares
nothing; see decision 9. 2026-11-05: a rollback counts against the release it
leaves; see decision 15.)*

One function computes the list. Its inputs are the installation, the profile
the render uses, the installed release (or none), and the release about to be
installed. The plan, the render step and the tick each call it, so none of
them can disagree about a file. *(2026-11-04: the render step decides each
file in its write loop instead, from the same inputs; see decision 12.)*

### 5.2 What `--dry-run` shows

The render step's plan detail names each counted file on a line of its own,
ahead of the diff it already prints:

```text
render configuration    2 file(s)
  /etc/nginx/conf.d/demo.conf exists, and 1.2.0 does not render it: 1.3.0 replaces it
```

The diff below that line is scrubbed as every plan diff is (RFC 0044
decision 10), since the operator's file may hold a credential.

### 5.3 An attended `update` or `apply`

This is §11's decision 3. The recommendation: the render step copies each
counted file before replacing it, to
`<VarDir>/replaced/<operation id>/<target path>`. The copy is written mode
0600 in a 0700 directory, because the file may hold a credential. The step's
journal detail records the target, the copy and the original mode. The
command's summary says, for example: "replaced /etc/nginx/conf.d/demo.conf,
which 1.2.0 did not render; the old file is at
/var/lib/demo/replaced/op_…/etc/nginx/conf.d/demo.conf". *(2026-11-05: a
release that declares one file under two spellings is refused before anything
is written; see decision 14.)*

A copy next to the file was considered and rejected. A `conf.d/*` include
would load `demo.conf.orig` as configuration, which changes a third-party
service's behaviour in exactly the directory this happens in.

### 5.4 The unattended tick

This is §11's decision 4. The recommendation: it holds, as for drift. The
gate runs beside `heldForDrift`, after the candidate is loaded, and leaves the
release staged with this reason:

> /etc/nginx/conf.d/demo.conf exists, and 1.3.0 would replace it although 1.2.0
> does not render it, so the release is staged and not installed;
> `morzer update --dry-run` shows it

The reason is that the copy keeps the bytes, not the behaviour. From the next
tick on, the product reads the vendor's file instead of the operator's, and
nobody is there to notice. RFC 0044 decision 4 ruled the same way for an
edited file.

A file at such a path that cannot be read holds the tick too. The update would
refuse at the render, and a candidate that failed is not retried until the
channel moves, so letting the tick try it would cost the release.
*(2026-11-04: a candidate the tick cannot render is not held by this gate, and
a file that appears after the gate is refused at the render; see decisions 11
and 13.)*

### 5.5 Alternatives considered

- **Refuse an attended update until the file is moved.** This is safe, but
  it makes every such update a two-step for a file the operator may not care
  about. It also needs a flag to get past the refusal. Decision 3's
  alternative.
- **Warn in the plan only.** This costs the least, but a plan is only read
  under `--dry-run`. An `update` run without one loses the file as it does
  now.
- **Fold this into `config.drift`.** Drift is defined against the installed
  release (RFC 0044 decision 1), and between updates there is no other
  release to compare with.

## 6. Tests

- `update --dry-run` to a release that adds a target over an existing file:
  the plan names it, and nothing changes on disk.
- The attended update: the copy holds the old bytes at the documented path,
  mode 0600, and the summary names it. An identical file is not copied. A
  failed update that puts the file back through the compensator removes its
  copy. One that cannot put it back, or that failed past its point of no
  return, keeps the copy, since it may be the only one.
- A first install over an existing file: the same. So is `init --release`
  followed by the first `apply`.
- The tick: a candidate adding such a target is held with the reason, and
  installs once the file is moved or matches.
- Sabotage: dropping the installed-release filter makes every changed target
  count, and the drift tests must notice.

## 7. Docs

- The operating page on updates: what happens to a file at a new target.
- `pages/docs/reference/manifest.md`, `configuration[].target`: a vendor
  adding a target over a common path is now visible to the operator.
- The changelog.

## 8. Out of scope

- **`rollback` and targets the release it returns to does not declare.**
  Today those stay as the newer release rendered them, whether or not a file
  was there before. Putting the copy back is decision 6's alternative. Deleting
  the files a newer release created is a separate issue.
- **A release that stops rendering a target.** The file stays, and no release
  owns it after that.
- **Pruning `replaced/`.** Copies are made once per target per release that
  first declares it, and `installation remove --purge` deletes them.

## 9. Risks

- **A vendor who adds a target at a path every machine has** turns each
  machine's next unattended update into a hold, if decision 4 holds. That is
  the intended effect, and the hold's reason says what to do.
- **The copy holds whatever the operator's file held.** It lives in the data
  directory with owner-only permissions, and this RFC adds it to neither
  backups nor the support bundle.

## 10. Unresolved questions

- Whether `release diff` (#249) should list added targets, so a vendor sees
  them before shipping. That is useful, but it is a separate surface.

## 11. Decisions

| # | Grade | Decision |
| --- | --- | --- |
| 1 | `ASSUMED` | A file counts when the release about to be installed declares a target at its path, the installed release does not (or none is installed), something exists there, and its bytes differ from the render. A release recorded but never converged, as `init --release` leaves one, is not installed for this purpose. An identical file is not counted. An unreadable one is listed apart, the render step's existing refusal applies to it, and it holds the tick. |
| 2 | `ASSUMED` | One function computes the list, from the installation, the profile the render uses, the installed release and the release about to be installed. The plan, the render step and the tick all call it. |
| 3 | `ASSUMED` | **What an attended `update` or `apply` does with a counted file.** Recommended: copy it to `<VarDir>/replaced/<operation id>/<target path>`, mode 0600, before replacing it, and name the copy in the summary. Nothing is lost, and nothing new is asked of the operator. Alternative: refuse before any step, naming the files, until they are moved or `--replace-existing` is passed. Was `OPEN`; ruled by the author 2026-11-04: as recommended. |
| 4 | `ASSUMED` | **Whether a counted file holds an unattended update.** Recommended: yes, as drift does (RFC 0044 decision 4), with a reason that names the files. The copy keeps the bytes, not the behaviour, and nobody is watching the product switch to the vendor's file. Alternative: no; the tick keeps a copy as an attended update does, and the notify target reports it. Was `OPEN`; ruled by the author 2026-11-04: as recommended. |
| 5 | `ASSUMED` | The copy lives under the installation's data directory, not beside the file. A `conf.d/*` include would load a copy kept beside it. The journal records the target, the copy and the original mode. |
| 6 | `ASSUMED` | **What `rollback` does with a copy.** Recommended: nothing; its summary names the copy when the release it returns to does not declare the target. Rollback cannot tell whether the operator has since changed the file, and the wider gap (§8) needs its own design. Alternative: rollback puts the copy back when the release it returns to does not declare the target. Was `OPEN`; ruled by the author 2026-11-04: as recommended. |
| 7 | `ASSUMED` | The plan names each counted file on its own line, ahead of the scrubbed diff, saying which release does not render it. |
| 8 | `ASSUMED` | `config diff` and `config.drift` are unchanged. They compare against the installed release, and between updates no other release exists to compare with. |
| 9 | `ASSUMED` | *(2026-11-04, from the build; accepted 2026-11-05)* **Refines decision 1.** The installed release is read when the render step runs. A resume after the new release became current finds its files already rendered, so nothing counts. When the current release cannot be loaded, as an update with a fallback release can find, no target counts as declared, so every differing file is copied rather than none. Decision 15 refines this for `rollback`. See [logs/preexisting-target.md](../logs/preexisting-target.md) (decision 1, attempt 1, 21:52). |
| 10 | `ASSUMED` | *(2026-11-04, from the build; accepted 2026-11-05)* **Refines decision 6.** `rollback` finds the copies by walking `<VarDir>/replaced/`, where a copy's path is its target's, rather than in the journal of the update it undoes, which it does not know. It names every copy whose target the release it returns to does not render, an earlier update's copy of the same target included. See [logs/preexisting-target.md](../logs/preexisting-target.md) (decision 6, attempt 1, 21:52). |
| 11 | `ASSUMED` | *(2026-11-04, from the build; accepted 2026-11-05)* **Refines decision 4.** A candidate the tick cannot render is not held by this gate. Its update fails at the render step, before the first write, and says why, as any failed candidate does. See [logs/preexisting-target.md](../logs/preexisting-target.md) (decision 4, attempt 1, 21:58). |
| 12 | `ASSUMED` | *(2026-11-04, from the review; accepted 2026-11-05)* **Refines decision 2.** The plan and the tick call the list. The render step decides each file inside its write loop, from the installed release's targets and the bytes it is about to replace, so the copy is of what is there at the write, a file written after the plan included. See [logs/preexisting-target.md](../logs/preexisting-target.md) (decision 2, attempt 2, 22:33). |
| 13 | `ASSUMED` | *(2026-11-04, from the review; accepted 2026-11-05)* **Refines decision 4.** The tick marks its update unattended. Its render refuses a counted file that appeared after the gate as a failed preflight, before replacing it, rather than keep a copy. The update fails and is compensated, and the candidate is not retried until `morzer update --to` or the channel moves. See [logs/preexisting-target.md](../logs/preexisting-target.md) (decision 4, attempt 2, 23:04). |
| 14 | `ASSUMED` | *(2026-11-05, from the review; accepted 2026-11-05)* The render refuses a release whose configuration targets clean to the same path, as a validation error naming both, before it writes anything. Written twice, the file's second backup and copy held the first render, and a failed update could put back the wrong bytes and then remove the copy. The manifest's own validation is unchanged, so an installed release that declares one still loads. See [logs/preexisting-target.md](../logs/preexisting-target.md) (unlisted, attempt 3, 08:02). |
| 15 | `ASSUMED` | *(2026-11-05, from the review; accepted 2026-11-05)* **Refines decisions 3 and 9.** The installed release is the one the operation started from. `rollback` moves the pointer before it renders, so it passes the release it leaves to the render, which counts files against it; a resume, which `rollback` does not have, still reads the current release. A rollback keeps a copy of a counted file as an attended update does, and names it in its summary. Before this, its plan named the file and its render replaced it with no copy. See [logs/preexisting-target.md](../logs/preexisting-target.md) (decision 2, attempt 3, 08:24). |

## 12. Phasing

One phase, one PR. The function from decision 2, the plan line, the copy or
refusal from decision 3, and the tick's gate from decision 4, with tests and
docs. *(2026-11-05: built in #273.)*
