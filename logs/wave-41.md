# Wave 41 · The path the operator typed

Executed against branch `wave-41-the-path-the-operator-typed`. Carried out of
wave 40's scoping pass, which measured four carried items and found that two
were ready, one was blocked on an RFC nobody has written, and one was smaller
than it looked.

**Drift count: 0.** Nothing a document settled was built otherwise. The one
departure below is from this wave's own execution plan, announced before any
code was written against it, and RFC 0030's destroyed grades were already
counted as drift by wave 37.

Where building this disagreed with the plan for it, written at the moment it
happened. Nothing here is revised afterwards to agree with what was later
settled, and nothing here has been folded back into any RFC's own text.

| Class | Test | Meaning |
|---|---|---|
| `discovery` | Could not have been known before code existed | Healthy — the spec was right to be silent |
| `spec-gap` | Could have been known; the spec was silent or at the wrong altitude | The design process missed something |
| `drift` | The spec covered it and it was built otherwise anyway | **A defect** |
| `irreducible` | No amount of design settles it | Stop and spike |

```divergence
decision: unlisted
grade: UNLISTED
class: spec-gap
at: 2026-08-27T10:05:00Z
attempt: 1
claim: a plan reads the bundle from a copy it stages under /tmp, and ParseManifest prefixes whatever file it was handed, so a refusal named a morzer-plan- directory the operator never chose and which is removed before they can look at it
evidence: internal/lifecycle/ops/ops.go:305
action: decided
proposal: ASSUMED — a refusal names what the operator passed. `release.LoadManifestAs` names a source it did not read from, and the plan passes `--release`. The real path already names the source without trying, because Resolve reads a local bundle in place.
```

The prefix is not decoration. `ParseManifest` puts the source in front of every
validation failure so "an author with several bundles open knows which file is
being complained about" — and a path under `/tmp` answers that question with one
they cannot place, which is worse than no prefix at all.

`--product` is what kept this out of review. Without it the CLI reads the
manifest at the source to learn the product name, refuses there, and names the
real path by accident; the temp path is reachable only when the operator
supplies the name themselves.

```divergence
decision: unlisted
grade: UNLISTED
class: discovery
at: 2026-08-27T10:20:00Z
attempt: 1
claim: this wave's own plan proposed moving the plan's copy under the managed staging dir, and that directory does not exist while a plan runs — creating it would be a change, which is the one thing a plan may not make
evidence: `find $ROOT -mindepth 1 | wc -l` prints 0 after `morzer --root $ROOT init --product demo --release <bundle> … --dry-run` exits 0 printing `this is a plan; nothing was changed`
action: decided
proposal: the system temp directory is correct here and the plan item is withdrawn. `update.go:187` has to `MkdirAll(StagingDir)` before using it, which is exactly the change a plan is forbidden.
```

**The plan asserted a location without checking it exists at the moment it would
be used.** Escaping `--root` is a real property of the copy and it is the price
of a plan that creates nothing; the alternative buys tidiness by breaking the
guarantee the flag is for.

```divergence
decision: unlisted
grade: UNLISTED
class: discovery
at: 2026-08-27T10:55:00Z
attempt: 1
claim: nothing in the tree asserted that a manifest refusal names any file at all — replacing LoadManifest's source with an empty string degrades every refusal to `error: : manifest is invalid:` and passes the whole suite
evidence: `go test ./internal/release/ ./test/clitest/ ./internal/lifecycle/ops/` printed `ok` for all three with `LoadManifestAs(path, "")` substituted, and the built binary then printed `error: : manifest is invalid:` for a legacy bundle
action: decided
proposal: ASSUMED — `TestAFirstInstallRefusesADeprecatedBundle` asserts the bundle path alongside the field name. The claim the prefix exists to serve is now guarded on the path an operator hits most often.
```

**The fix and the gap are the same claim at two altitudes.** This wave made the
plan name the right path; the sweep then asked whether anything pinned that a
path was named, and nothing did. Found by sabotage, after the fix was already
green — which is the order that finds it, since a passing suite is exactly when
the question stops being asked.

## Found by the audit, after the entries above — 2026-08-27

```divergence
decision: unlisted
grade: UNLISTED
class: discovery
at: 2026-08-27T12:40:00Z
attempt: 2
claim: the fix above covered a directory bundle and left the archive — the shape a vendor publishes — still naming a temp path, on the real install as well as the plan, because Resolve unpacks into morzer-resolve-* and Fetch loads what it just extracted before the plan's own check runs
evidence: `morzer --root $R init --product demo --release demo-1.2.0.tar.zst …` printed `failed: /tmp/morzer-resolve-2896715448/manifest.yaml: manifest is invalid:`, and the same invocation with `--dry-run` printed `error: /tmp/morzer-plan-45132326/manifest.yaml: manifest is invalid:`
action: decided
proposal: ASSUMED — `release.LoadAs` carries a name the way `LoadManifestAs` does, `materialise` returns what the operator named, and `Fetch` names the archive it unpacked. Three call sites, one rule: the reader of a copy names the original.
```

**The wave fixed the case it was reported against and stopped.** That is the
mistake wave 39 distilled a rule about and wave 40 was written to undo, arriving
here in its own turn — and it was found by auditing the fix, not by any gate.
`TestAnInstallFromAnArchiveIsRefusedToo` was passing throughout; it asserted the
refusal happened and never what it named.

```divergence
decision: unlisted
grade: UNLISTED
class: discovery
at: 2026-08-27T13:05:00Z
attempt: 2
claim: `git checkout <file>` to revert a sabotage ate an uncommitted doc comment in the same file, because the fix had been committed and the comment written afterwards
evidence: `git diff --stat` printed nothing after `git checkout internal/release/load.go`, on a file carrying an uncommitted paragraph documenting LoadManifestAs's contract
action: decided
proposal: the rule already exists — commit before you sabotage — and it is the *second* commit that this wave needed, not the first. A sabotage sweep that starts after new work has accumulated on top of the last commit eats that work, and the sweep is exactly when nobody is looking at the diff.
```

## Ruling — 2026-08-27, RFC 0030's grades

The author graded all five rows. Rows 1, 3 and 4 `LOCKED`, rows 2 and 5
`ASSUMED`; the answers move to a column of their own and the Grade column
carries grades again.

| RFC | row | outcome | grade | decision | from |
|---|---|---|---|---|---|
| 0030 | 1 | accepted | LOCKED | Reopening silently re-enables units an operator disabled | wave 37, this wave |
| 0030 | 2 | accepted | ASSUMED | Diagnostic; the row argues its own live risk is worth revisiting | wave 37, this wave |
| 0030 | 3 | accepted | LOCKED | Moving them undoes every `systemctl disable`; the path is pinned by a test | wave 37, this wave |
| 0030 | 4 | accepted | LOCKED | State-schema field that bumped the version to 8; removal needs a migration | wave 37, this wave |
| 0030 | 5 | accepted | ASSUMED | `doctor`'s behaviour; depart-if-wrong is proportionate | wave 37, this wave |

Wave 37 carried the open question inside this as *what a grade means on a
decision in an RFC that has already shipped*, since `LOCKED`, `ASSUMED` and
`OPEN` all describe what an executor does on conflict. The last three waves
answered it by doing it: wave 38 conflicted with RFC 0001 row 12 and wave 39
with RFC 0023 decision 23, both in RFCs marked Complete. **A Complete RFC is
what later units collide with**, so the vocabulary needs nothing added and no
shared skill has to change.

`rfc-index` goes from 27 problems to 22. What is left is the 22 RFCs whose
decision tables carry no Grade column at all.

## Rules distilled

- **A measurement from an earlier turn is a memory, not a measurement.** The
  scratchpad was cleaned between turns, and two probes re-run against paths that
  no longer existed returned answers that looked like findings: an empty `find`
  read as "a plan creates nothing" and a missing fixture read as a sabotage
  killing a message. Both were re-taken before anything was concluded from them.
- **Naming the file you read is right until you read a copy.** Every path in an
  error is a claim about where the reader should go and look, and a stage-then-
  read helper breaks it silently, because the read still succeeds.
- **A flag that changes which layer refuses changes the whole error contract.**
  `--product` decides whether `init` refuses before the operation or inside it,
  and with it the exit code, the category, and whether the cause is in the
  top-level error at all.
- **Sabotage after green, not instead of it.** The missing path assertion was
  invisible while the new test passed; the sweep found it by asking what the
  suite would still accept.
- **A plan item can assert a location that does not exist yet.** Checking the
  premise cost one command and withdrew half the change.
- **A path in a message is a claim about where to go and look**, so every
  stage-then-read helper owes the original name. The rule generalises past this
  wave: `Resolve`, `Fetch`, the plan's own check and the https download cache
  are four readers of a copy, and three of them were leaking.
- **Correcting the success path is where the failure path gets forgotten.**
  `https.Resolve` fixes the reference it returns and not the error it returns,
  with a comment naming exactly the problem it does not solve.
- **Commit again before the *second* sweep.** The rule is "commit before you
  sabotage", and the case it misses is a sweep that starts after new work has
  landed on top of the commit it is sweeping.

## Carried into the next unit

- **What an exit code reports when a cause-code and an outcome-code both apply.**
  `domain.ExitCode` answers "the outcome" for every compensated operation, in a
  switch nothing documents, no RFC row covers, `docscheck` does not check, and no
  test in `internal/domain` exercises. Needs an RFC; see wave 39's correction.
- **The 354 ungraded decision rows across 22 RFCs.** All 22 are Complete. The
  vocabulary question that blocked them is answered above, so what remains is
  volume and judgment, not a missing word. Proposed as a standing rule rather
  than a unit: a wave that conflicts with an ungraded table grades that table.
- **The remote sources name their download cache, not the URL.** The same defect
  this wave fixed for `file`, one layer out and worse: measured 2026-08-27, an
  `https` bundle whose manifest does not validate is refused with
  `/tmp/morzer-download-2453913850/bundle-0.tar.zst is not a valid manifest:`,
  and the URL the operator typed appears nowhere. `https.Resolve` already
  corrects this on the *success* path — `resolved.Ref = ref`, with a comment
  saying "not the temp path we resolved through" — and leaves the failure path
  carrying it. **Not fixed here because it is a port change:** `https` delegates
  by constructing `ports.Ref{Scheme: local.Scheme, Location: <temp>}`, so there
  is nowhere to put the operator's name without adding one to the reference, and
  a change to `ports.Ref` re-opens the shared `ReleaseSource` conformance battery
  for all three sources. `oci` is presumed to have it too and was not measured.
- **Whether a plan should verify signatures** (wave 39), and it is cheaper than
  wave 40 assessed. A plan already fetches a local bundle into a temporary
  directory to read its manifest, so verification there costs a hash over bytes
  already on disk; it declines to look only for non-`file` schemes.
- **`rfc-index` is not wired into `ci`**, now failing on 22 problems.
- **`FieldRemovalRelease` is a single-member design with no members** (D-052).
- **`release.draft: true` means a human publishes**, and that human is the last
  reader of the notes (release 0.3.0).
- ~~**RFC 0030's four destroyed grades**~~ (waves 37–38) — closed by this wave.
