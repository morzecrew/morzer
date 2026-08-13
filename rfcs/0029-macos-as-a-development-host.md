# RFC 0029 — macOS as a development host

- **Status:** 🚧 In progress — **P1 shipped (2026-08-13)**. `GOOS=darwin go
  build ./...` and `go vet ./...` are clean for `amd64` and `arm64`, gated in
  `just ci` as `darwin-check`, and `install.sh`'s advice is true for the first
  time. Three decisions moved during execution and §15 records each: **11**
  departed (the prompt keeps its own ioctl pair rather than adopting
  `x/term.ReadPassword`, which would have reintroduced a race the code exists to
  close), **12** resolved to the fail-safe stub rather than the real
  `SysctlKinfoProc`, and **3** widened from `go build` to `go build` *and* `go
  vet`. That last one has a cause: §3.1's count of six sites was two short
  because it counted what `go build` reaches, and test files are invisible to
  it. P2 remains **demand-gated** on §6.
- **Scope:** Making `GOOS=darwin` compile, and then making it *mean* something
  bounded. P1 is four compile fixes, a fail-safe stub for a fifth site, and one
  honest error message: the binary builds from source on a Mac, and nothing else
  changes. The sixth site and the real fix for the fifth are P2's, and §5.1 says
  which is which. P2 promotes that into a supported tier — a published darwin
  archive, a relocated layout, a `doctor` that tells the truth about secrets on
  a machine with no tmpfs — scoped explicitly to **authoring bundles and
  evaluating the manager**, never to running a production installation. Deliberately not a launchd supervisor, not
  an ephemeral-storage mechanism for macOS, not Windows, and not any change to
  the Linux layout, the manifest contract or the hook ABI.
- **Related:** [`internal/infra/atomicfs`](../internal/infra/atomicfs),
  [`internal/lifecycle/preflight/mounts.go`](../internal/lifecycle/preflight/mounts.go),
  [`internal/infra/lock/lock.go`](../internal/infra/lock/lock.go),
  [`internal/domain/paths.go`](../internal/domain/paths.go),
  [`internal/ports/supervisor.go`](../internal/ports/supervisor.go),
  [`install.sh`](../install.sh), [`.goreleaser.yaml`](../.goreleaser.yaml),
  [0022](0022-bootstrapping-the-manager.md) (the installer and the release
  matrix it refuses outside), [0023](0023-runtimes-beyond-compose.md) (the
  precedent for a port absorbing a second platform),
  [0010](0010-compose-volume-capture.md) (the rule that the *permissive* side is
  the one enumerated, so anything unrecognised lands on the safe side — §3.4 is
  that rule failing from a direction it did not anticipate)
- **Origin:** The refusal in `install.sh` promises "build from source with Go
  1.25 or newer if you need one", and that promise was measured false on
  2026-08-13. §12 has the output.

---

## 1. Problem

`install.sh` refuses on a Mac with this:

```
error: this installs Linux builds only, and there is no macOS build to point
       at: the release matrix is linux/amd64 and linux/arm64. Build from
       source with Go 1.25 or newer if you need one.
```

The last sentence is false. The tree does not compile for darwin, and has never
been asked to:

```
$ GOOS=darwin GOARCH=arm64 go build ./cmd/morzer
internal/infra/atomicfs/space.go:51: invalid operation: int64(stat.Bavail) * stat.Bsize (mismatched types int64 and uint32)
internal/infra/atomicfs/copy.go:291: undefined: syscall.Openat
internal/infra/atomicfs/copy.go:313: undefined: syscall.Openat
```

That is the whole of the immediate problem, and it is small. A refusal that
tells somebody what to do instead has to be right about what they should do,
because they will spend the next hour doing it.

Behind it sits a larger question the refusal implicitly answers "no" to, and
which this RFC exists to answer deliberately: **who is on a Mac, and what do
they want from this?** Not an operator running a production installation — this
manages a self-hosted product on a Linux server, and that is not negotiable.
The person on a Mac is authoring a bundle, or evaluating whether the manager is
worth adopting, and today both are told to find a Linux box first.

The cost of saying nothing is that the answer gets decided by whoever first
needs it, in a hurry, in a PR — which is exactly the shape of decision this
directory exists to prevent.

## 2. Why this is not simply "add darwin to the matrix"

Because two of the manager's load-bearing guarantees are Linux facts, not Go
facts, and shipping a darwin binary without deciding what happens to them would
publish a build that quietly means less than the Linux one.

**Decrypted secrets are supposed to be pages of RAM.** `RunDir` is documented
as "ephemeral and expected to be tmpfs. Cleared on reboot; decrypted secrets
live here and nowhere else"
([`paths.go:50`](../internal/domain/paths.go#L50)). macOS has no tmpfs. On a
Mac that directory is APFS, the overwrite that `secret edit` performs stops
being meaningful, and reboot stops being a guaranteed cleanup.

**The deployment lock is guarded against PID reuse by `/proc`.**
[`lock.go:254`](../internal/infra/lock/lock.go#L254) reads field 22 of
`/proc/<pid>/stat` — the holder's start time — so a recycled PID cannot
impersonate a live lock holder. There is no `/proc` on macOS. This one
**compiles**, which makes it the most dangerous item in the RFC: a portability
defect that type-checks is one nothing in CI will find.

Neither is a reason to refuse macOS. Both are reasons the answer has to be a
*tier* with a written-down meaning, rather than a matrix entry.

## 3. Current state

Verified against the code on 2026-08-13, not from memory. §12 carries the
commands.

### 3.1 The compile gap is six sites in six files

There is not one `//go:build linux` in the tree. Every coupling below is
incidental rather than designed, which is why the list is this short.

> **Read as of 2026-08-13, before P1.** The line references below are to the
> code as it was; P1 moved four of them into per-OS files (`space_linux.go`,
> `memory_linux.go`, `termios_linux.go`, `pidstart_linux.go`) and each has a
> darwin counterpart beside it. The count was also two short — §15 A4. The table
> is left as it was measured, because a current-state section that gets edited
> to match the code stops being evidence of anything.

| # | Site | What | Compiles on darwin? |
| --- | --- | --- | --- |
| 1 | [`atomicfs/space.go:51`](../internal/infra/atomicfs/space.go#L51) | `Statfs_t.Bsize` is `int64` on Linux, `uint32` on darwin | ❌ |
| 2 | [`atomicfs/copy.go:291,313`](../internal/infra/atomicfs/copy.go#L291) | `syscall.Openat` is Linux-only; `unix.Openat` exists on darwin and the file already imports `x/sys/unix` | ❌ |
| 3 | [`preflight.go:578`](../internal/lifecycle/preflight/preflight.go#L578) | `syscall.Sysinfo` for total memory; `x/sys/unix` defines `Sysinfo` only in `zsyscall_linux.go` | ❌ |
| 4 | [`cli/secret.go:504`](../internal/cli/secret.go#L504) | `unix.TCGETS`/`TCSETS` for the no-echo prompt; darwin has `TIOCGETA`/`TIOCSETA` and no `TCGETS` at all | ❌ |
| 5 | [`lock.go:254`](../internal/infra/lock/lock.go#L254) | reads `/proc/<pid>/stat` for the holder's start time | ✅ **and wrong** |
| 6 | [`preflight/mounts.go:27`](../internal/lifecycle/preflight/mounts.go#L27) | reads `/proc/mounts` for the filesystem name | ✅, degrades — see §3.4 |

Sites 3 and 4 do not appear in the build output above: `preflight` and `cli`
both depend on `atomicfs`, so the compiler stops before reaching them. Fixing
site 1 and 2 will reveal them, and an implementer who expects three errors will
think they have regressed something.

`internal/infra/exec` and `internal/adapters/runtime/compose` — the process
group handling, `Setpgid`, `Kill(-pgid)`, the SIGTERM-then-SIGKILL escalation —
**already compile clean for darwin**. The code that looks least portable is the
portable part.

### 3.2 The architecture already absorbed the expensive half

Three things that would each have been a workstream are already handled, and an
implementer should know before they start that they are not owed:

- **`ports.Supervisor` is nil-tolerant by construction.** `Available()` gates
  every call site — [`init.go:574`](../internal/lifecycle/ops/init.go#L574),
  [`settings.go:354`](../internal/lifecycle/ops/settings.go#L354),
  [`doctor.go:1026`](../internal/lifecycle/ops/doctor.go#L1026) — and the
  systemd adapter reports unavailable when `/run/systemd/system` is absent
  ([`systemd.go:77`](../internal/adapters/supervisor/systemd/systemd.go#L77)).
  On a Mac, units are simply not installed and `init` says so. **No launchd
  adapter is required**, and the port comment already anticipated this: "a
  container-only or non-systemd host is a new adapter rather than a fork of the
  lifecycle layer."
- **The layout is already relocatable.**
  [`PathsUnder`](../internal/domain/paths.go#L72) roots all four directories
  under one prefix, and `Paths.Root()` inverts it.
- **The tool catalogue's only Linux-only entry is already optional.** `docker`,
  `sops`, `age` and `restic` are all available on macOS;
  [`registry.go:51`](../internal/infra/tools/registry.go#L51) lists `systemctl`
  and nothing else that is not.

### 3.3 `--root` exists, and is currently labelled a test hook

`--root` is registered hidden, described as "prefix for all managed paths (for
testing)" ([`root.go:483`](../internal/cli/root.go#L483)). P2 turns this into
the supported way to run on a Mac, which is a documentation and status change
more than a code one.

### 3.4 A check that fails safe on Linux fails *unsafe* on darwin

This is the finding that most shaped the design.
[`checkSecretsOnEphemeralStorage`](../internal/lifecycle/ops/doctor.go#L499)
asks `FilesystemType` for a name, and treats the empty string as "cannot tell":

```go
fstype := preflight.FilesystemType(dir)
if fstype == "" {
        // "Cannot tell" is not "insecure". Reporting a
        // container that mounts /proc elsewhere as a
        // finding would be crying wolf.
        return preflight.OK("cannot determine the filesystem under %s", dir)
}
```

On Linux that is right, and the comment's reasoning is sound. On darwin
`FilesystemType` returns `""` for a different reason — there is no
`/proc/mounts` and never will be — and the same branch then reports **OK** on
the one platform where the answer is knowably "not memory-backed". A `doctor`
run on a Mac would print a reassurance it has not earned.

This is [0010](0010-compose-volume-capture.md)'s rule arriving from a direction
it did not anticipate. There, the permissive side is the enumerated one so that
anything unrecognised lands on the safe side. Here the enumeration is right and
the *input* changed meaning: `""` was "a filesystem I could not name", and on a
second platform it becomes "a filesystem I can name and it is the bad one". A
predicate can be enumerated correctly for the platform it was written on and
still invert when its silence acquires a new cause. P2 fixes it by answering the
question rather than widening the guard (§5.6).

### 3.5 CI cannot run Docker on the macOS anybody in this tier uses

The acceptance lane drives real Docker on every push. It cannot follow the
binary to macOS in any form worth having.

- **Apple silicon runners cannot run Docker at all.** `macos-14`, `macos-15`,
  `macos-26` and `macos-latest` are arm64, and Apple's Virtualization framework
  did not expose nested virtualization on M1. M3 and macOS 15 support it in
  hardware and software; GitHub has not enabled it on the hosted images. This is
  the binding constraint, and it is architectural rather than a scheduling
  matter.
- **Intel runners still exist**, and this draft first claimed otherwise.
  `macos-13` — the image where `colima` demonstrably worked — was retired on
  4 December 2025, and `macos-15-intel` was announced as the last x86_64 image;
  but `macos-26-intel` reached general availability on 26 February 2026. So
  x86_64 macOS did not end, and any argument resting on its retirement is
  unsound. §14 records the correction.

That leaves a narrower and more awkward fact than "impossible". A Docker-capable
macOS lane is *plausible* — `colima` on an Intel image — and it would prove the
`darwin/amd64` binary against a Linux VM, on the one architecture the tier's
audience does not have. Everyone this tier is for is on Apple silicon.

**A lane that runs only where the users are not is worse than no lane**, because
it reports green about a platform it never touched. That is what decides §5.7,
and it holds whatever GitHub does with Intel images next.

## 4. Goals / Non-goals

**Goals**

- The refusal in `install.sh` says something true (P1).
- `GOOS=darwin go build ./...` succeeds and stays succeeding, gated in CI (P1).
- A bundle author on a Mac can run `init`, `apply`, `status`, `secret`, `backup`
  and `doctor` against Docker Desktop, under a relocated prefix, and be told
  precisely which guarantees are weaker there (P2).
- `doctor` on macOS is *more* informative than on Linux about ephemeral storage,
  not less (P2, §5.6).

**Non-goals**

- **A launchd supervisor.** Boot-time convergence and scheduled backups are
  operating a production installation, which macOS is explicitly not for.
  `Available()` returning false is the design, not a gap. Reopened by somebody
  actually wanting a Mac to converge at boot, which would be a different RFC
  with a different premise.
- **An ephemeral-storage mechanism for macOS.** A RAM disk via
  `hdiutil attach ram://` is a real mechanism and the wrong one to own: it needs
  admin, it is a second lifecycle to create, mount, unmount and clean up after a
  crash, and it exists to prop up a tier this RFC defines as non-production.
  P2's answer is to state the downgrade, loudly and every run. Reopened if macOS
  ever gets a memory-backed filesystem, or if the tier changes.
- **Windows.** Nothing here generalizes: the process-group model, the layout and
  the runtime assumptions are all POSIX.
- **Any change to the Linux layout, the manifest contract, or the hook ABI.**
  A darwin build that shifted `selfhost/v1alpha1` would be paying for a
  development convenience with the thing bundles are written against.
- **Production support on macOS**, at any phase of this RFC. §5.5 makes the
  refusal mechanical rather than advisory.

## 5. Design

### 5.1 P1 — the six sites

Each fix, and how it is verified. The rule is that a fix which cannot be
verified on Linux CI must be *structured* so its Linux behaviour is unchanged by
construction — a per-OS file, not a runtime branch.

| Site | Fix | Verified by |
| --- | --- | --- |
| 1 `space.go` | `int64(stat.Bavail) * int64(stat.Bsize)`, or `statfs_linux.go` / `statfs_darwin.go` if the conversion needs a comment on each | existing space tests on Linux; darwin build gate |
| 2 `copy.go` | `syscall.Openat` → `unix.Openat`; `syscall.Close` → `unix.Close` for symmetry. Every flag used (`O_RDONLY`, `O_DIRECTORY`, `O_NOFOLLOW`, `O_CLOEXEC`, `O_NONBLOCK`) exists on darwin | the existing symlink-refusal and FIFO tests, unchanged |
| 3 `preflight.go` | split: `memory_linux.go` (`Sysinfo`) and `memory_darwin.go` (`sysctl hw.memsize`) | Linux path unchanged; darwin path is P2's spike (§10) |
| 4 `secret.go` | `x/term`'s `ReadPassword`, or per-OS ioctl constants. Prefer `x/term` — **it is already a direct dependency** (`go.mod`), so this costs nothing and deletes platform trivia rather than adding a second file of it | existing prompt tests |
| 5 `lock.go` | **P1 does not fix this.** See §5.2 | — |
| 6 `mounts.go` | **P1 does not fix this.** See §5.6 | — |

Sites 5 and 6 are deliberately left to P2. P1's promise is "it compiles and the
error message is true", and a P1 that also rewrote the lock's liveness guard
would be a P1 nobody can review as one thing.

### 5.2 P1 — the lock, refused rather than silently weakened

Site 5 compiles on darwin and stops guarding. P1 must not leave that as a
runtime surprise, and must not fix it either. The cheap correct move is a
per-OS file whose darwin implementation **reports that it cannot determine a
start time**, and a lock that treats "cannot determine" as "assume the holder is
live" — the fail-safe direction, since a stale lock is an operator inconvenience
and a stolen one is a concurrent deployment.

P2 replaces it with the real answer (`SysctlKinfoProc`), at which point macOS
gets the same PID-reuse guarantee Linux has.

### 5.3 P1 — the honest refusal

`install.sh` keeps refusing on Darwin. The sentence that changes is the advice:
after P1, "build from source with Go 1.25 or newer" is true, and the message may
say that and nothing more.

In particular it must **not** promise that the binary drives a deployment. P1
delivers a CLI that builds and runs on a Mac; whether the manager actually works
against Docker Desktop is §11's first unresolved question and P2's first spike.
A refusal that over-promises is the same defect this RFC exists to fix, one
release later — and P1's whole point is that the message can be trusted. What it
gets them is a binary; what that binary can do is P2's to state, once something
has run it.

### 5.4 P2 — the release matrix

`goreleaser` gains `darwin` with `amd64` and `arm64`
([`.goreleaser.yaml:27`](../.goreleaser.yaml#L27)), which flows automatically
into `SHA256SUMS`, the signature and the checksum's `extra_files` coverage of
`install.sh` itself. `just build-all` gains the two targets. `install.sh` gains
`Darwin` to its `uname -s` case, and the archive name it already computes needs
no new shape.

Two things in `install.sh` that are less symmetric than they look, measured
rather than assumed:

- The architecture table needs **nothing**. It already reads
  `aarch64 | arm64) arch=arm64` ([`install.sh:167`](../install.sh#L167)), so
  macOS's spelling is handled by a branch that was written for Linux's two
  spellings.
- The archive name is
  `archive="${BINARY}_${version}_linux_${arch}.tar.zst"`
  ([`install.sh:357`](../install.sh#L357)) — the OS is a **literal**. That line
  is the actual work, and the `uname -s` case must set a variable it consumes
  rather than the two drifting apart.

### 5.5 P2 — the layout, and where the tier is enforced

macOS has no `/run`. `DefaultPaths` is not made platform-conditional, because a
darwin `DefaultPaths` returning `/private/var/run/<product>` would be the
production layout by another name, and this tier is not production.

Instead: **on darwin, an unrooted invocation is refused.** Running without
`--root` (or a `--config` that implies one) fails with a message naming the
tier and showing the rooted form. `--root` stops being hidden on darwin and
becomes the documented way to run there.

That makes the non-goal mechanical. An operator cannot drift into running a
production installation on a Mac by not reading the docs, which is the only way
anybody ever drifts into anything.

### 5.6 P2 — `doctor` tells the truth about secrets

Two changes, and the second is the point.

**`FilesystemType` learns darwin.** `statfs(2)` returns `f_fstypename` — a
string, which is exactly what the function's contract already promises ("the
answer wanted is the *name*"). A `mounts_darwin.go` returns `"apfs"` where the
Linux file reads `/proc/mounts`.

**The empty string stops meaning OK.** Once darwin can answer, `""` narrows
back to genuinely-cannot-tell, and the check reports:

- ephemeral (`tmpfs`, `ramfs`) → OK, as today
- a named non-ephemeral filesystem → Warn, as today — and on macOS this is now
  reached, with `apfs` in the message
- `""` → OK with "cannot determine", as today

The Linux behaviour is unchanged in all three branches. What changes is that
macOS stops falling into the third.

The wording matters more than usual here. On macOS the warning is not
actionable — there is no tmpfs to mount — so the hint cannot be Linux's "mount
a tmpfs at %s". It has to say that this platform has no memory-backed
filesystem, that decrypted secrets therefore touch disk, and that this is one of
the reasons the tier is what it is.

### 5.7 P2 — what CI can prove, and what it cannot

Given §3.5, the darwin lane is:

- `GOOS=darwin go build ./...` on the Linux runner, for **both** `amd64` and
  `arm64` — free, and catches every compile regression. **This is the gate that
  matters**, and it is available in P1. Both architectures because the matrix
  P2 publishes has both, and because site 1 is itself an arch-dependent type
  question: a gate covering one arch would be a gate that agrees with the bug it
  is there to catch.
- `go test ./...` on `macos-latest` for the packages that do not need Docker —
  proves the darwin syscall paths (memory, statfs, kinfo_proc) actually work,
  which cross-compilation cannot. This is arm64, which is what the tier's
  audience runs.
- **No acceptance lane on macOS.** Not "unavailable" — §3.5 corrects that — but
  available only on Intel, against an architecture no one this tier serves is
  using. Declined rather than deferred, and the docs say so rather than carrying
  a TODO.

The consequence is stated rather than hidden: macOS is verified at a lower tier
than Linux, and the docs say which tier and why.

### 5.8 Alternatives considered

**Ship darwin with no tier language, as a normal platform.** Loses because the
two guarantees in §2 would be silently weaker on one platform, and the project's
whole posture is that a guarantee you cannot state precisely is not one.

**Refuse macOS permanently and delete the "build from source" sentence.**
Coherent, cheap, and the honest fallback if P2 is never demanded — P1 alone
leaves the tree in this state, correctly. It loses as a *destination* because
the compile gap is six sites, the architecture already absorbs the hard parts,
and the people it turns away are bundle authors, who are the ones the project
needs most.

**A launchd adapter, for parity.** Loses on the tier: units exist to converge a
production deployment at boot, and this tier has none. Building it would be
implementing the non-goal.

**A RAM disk on macOS via `hdiutil`.** Loses on ownership cost against benefit
— see the non-goal in §4. Named as the escape hatch, not built.

## 6. The gate on P2

P1 is unconditional: the error message is false today and that is a defect.

**P2 is gated on somebody who is not the author asking for it.** The evidence
that would open it is a person saying they want to author or evaluate a bundle
on a Mac. Absent that, P2 is a published artifact, a platform tier and a
documentation surface built for a hypothesis, and the correct outcome is that it
is never built — the same gate [0027](0027-desired-state-in-a-repository.md) §6
puts on its P2, for the same reason.

P1 makes P2 cheap to pick up later. That is P1's second job.

## 7. Non-goals, and what reopens each

| Non-goal | What reopens it |
| --- | --- |
| launchd supervisor | An operator wanting a Mac to converge at boot — a different RFC, with a premise this one rejects |
| RAM-disk secrets on macOS | A memory-backed filesystem on macOS, or the tier changing |
| Windows | Nothing in this RFC; the port model would need re-deriving |
| Production support on macOS | Nothing. §5.5 is mechanical, and removing it is a decision to be argued, not an omission to be fixed |
| Docker-in-CI on macOS | GitHub enabling nested virtualization on Apple silicon images (§3.5). An Intel lane does not reopen it — that is the option decision 9 declines |

## 8. Tests

- **Build gate (P1).** `GOOS=darwin GOARCH=amd64 go build ./...` and
  `GOOS=darwin GOARCH=arm64 go build ./...` in `just ci` — `CGO_ENABLED=0` is
  already exported at the top of the `justfile`, so the recipe adds two lines
  and no configuration. Cheap, runs on the Linux runner, and is the only thing
  standing between this and the same rot returning.
- **Unchanged Linux suites (P1).** Every fix in §5.1 is required to leave the
  existing `atomicfs`, `preflight`, `lock` and `cli` tests passing untouched. A
  fix that needed its Linux test rewritten is a fix that changed Linux
  behaviour, and that is out of scope by §4.
- **Verified-red on the lock (P1).** The darwin "cannot determine a start time"
  path gets a test that drives the fail-safe branch directly — an unknown start
  time must be treated as a live holder. Testable on Linux by injecting the
  unknown, and it is the branch that decides whether two deployments can run at
  once.
- **`FilesystemType` on darwin (P2).** A `macos-latest` unit test asserting it
  returns a non-empty name for a real path — the assertion the design would be
  wrong without, since §5.6's entire correctness rests on `""` no longer being
  reachable there.
- **Not tested: acceptance on macOS.** §3.5. Manual, local, and recorded in the
  RFC when P2 lands rather than claimed in CI.

## 9. Docs

- The installation page gains a **Platforms** section naming the two tiers and
  what differs: no units, secrets not memory-backed, `--root` required,
  acceptance not run in CI. Written as a tier with reasons, never as a caveat
  list — a reader deciding whether to trust this on a Mac needs the shape, not
  the footnotes.
- `install.sh`'s Darwin message changes in both phases (§5.3, then to an actual
  install in P2).
- The README's one-line platform claim, currently implicit in "a single Linux
  machine", stays exactly as it is: the *product* is still Linux-only, and P2
  does not change that sentence.

## 10. Risks

- **The tier is read as "macOS is supported".** The most likely misreading, and
  the reason §5.5 refuses an unrooted run rather than warning about one. A
  warning is a thing people learn to scroll past.
- **The darwin syscall paths are written blind.** Neither the author nor CI has
  a Mac in the loop for P1. Mitigated by the build gate catching compile errors
  and by P2's `macos-latest` unit lane being the first thing P2 lands — but P1's
  `sysctl hw.memsize` and P2's `kinfo_proc` are the two places to distrust until
  something has run them. Named in §11.
- **Docker Desktop's file sharing is assumed, not verified.** The manager
  bind-mounts its own directories into containers, and Docker Desktop shares
  only certain host paths by default. This is believed fine — the defaults
  include `/private`, and a rooted prefix under the user's home is shareable —
  but nobody has run it. It is P2's first spike, and if it is wrong the layout
  in §5.5 is what changes.
- **P1 ships and P2 never does**, leaving a tree that compiles for a platform it
  does not publish. That is an acceptable resting state and §6 says so, but the
  build gate must stay in `ci` or the compile gap silently returns and the
  refusal becomes false a second time.

## 11. Unresolved questions

- **Does the manager actually work against Docker Desktop?** Bind mounts cross
  virtiofs, ownership is mapped rather than real, and the Compose project the
  manager drives has never been run there. Settled by a spike on a Mac before
  any of P2 is written — and the spike must assert the step the design assumes:
  a rendered config and a secrets directory, bind-mounted, readable by the
  container with the modes the release declares.
- **`hw.memsize` vs what preflight means by memory.** `Sysinfo` gives total and
  available; `hw.memsize` gives total only. Whether preflight's check needs
  available memory on darwin, or whether total is enough for what it asserts, is
  for the implementer to settle against the check's actual assertion — logged
  when decided.
- **Is `macos-latest` in CI worth its minutes for P2's unit lane?** Public-repo
  macOS minutes are not free in the way Linux ones are. Settled when P2 is
  scheduled, against how much of §5.6 the lane is actually proving.

## 12. Decisions

| # | Grade | Decision |
| --- | --- | --- |
| 1 | `LOCKED` | macOS is a **development and evaluation** platform, never a production host. Everything else in this RFC follows from this row; reversing it reopens the supervisor, the ephemeral-storage mechanism and the layout together. |
| 2 | `LOCKED` | P1 is unconditional and P2 is demand-gated (§6). P1's deliverable is "it compiles and the refusal is true", nothing more. |
| 3 | `LOCKED` | `GOOS=darwin go build ./...` joins `just ci` in P1, for **both** `darwin/amd64` and `darwin/arm64`. Without it the gap returns and the message becomes false again — which is how it got here; and with only one arch it would miss exactly the class of defect site 1 belongs to. |
| 4 | `LOCKED` | No launchd adapter. `Supervisor.Available()` returning false is the design; the port already handles a nil supervisor at every call site (§3.2). |
| 5 | `LOCKED` | No RAM disk. The downgrade is **stated** rather than mechanised, and `doctor` states it on every run (§5.6). |
| 6 | `LOCKED` | On darwin, an unrooted invocation is refused (§5.5). The tier is enforced mechanically because a documented tier is not one. |
| 7 | `LOCKED` | `""` from `FilesystemType` must stop being reachable on darwin before it keeps meaning OK. Fixing the check by widening the guard instead of answering the question is refused — see [0010](0010-compose-volume-capture.md) and §3.4. |
| 8 | `LOCKED` | Platform differences are per-OS files, not runtime branches, so Linux behaviour is unchanged by construction rather than by test. |
| 9 | `LOCKED` | No acceptance lane on macOS, and the docs say why rather than carrying a TODO (§3.5). It is **declined, not impossible**: Intel runners exist and `colima` works there, but that lane would prove `darwin/amd64` while every user of this tier is on Apple silicon. A green badge for an architecture nobody runs is worse than an acknowledged gap. |
| 10 | `ASSUMED` | Docker Desktop's default file sharing covers a rooted prefix under the user's home. P2's first spike settles it; if wrong, §5.5's layout changes and this row is superseded. |
| 11 | `ASSUMED` | `x/term` for the no-echo prompt beats two files of ioctl constants, and is already a direct dependency so it adds nothing. Execution may depart if `ReadPassword`'s behaviour differs from what the current prompt promises. |
| 12 | `OPEN` | Whether P1 fixes the lock's darwin path (§5.2) with `SysctlKinfoProc` immediately or leaves the fail-safe stub for P2. The stub is specified; an implementer who finds the real call trivial should take it and log that they did. |

## 13. Phasing

**P1 — it compiles, and the refusal is true.** Sites 1–4 from §5.1, the lock's
fail-safe stub (§5.2), the `install.sh` message (§5.3), the build gate
(decision 3). One PR. Unconditional.

**P2 — the tier.** Gated on §6. Release matrix and installer (§5.4), the rooted
refusal (§5.5), `FilesystemType` on darwin and the `doctor` wording (§5.6), the
`macos-latest` unit lane (§5.7), the Platforms documentation (§9). Ordered so
the Docker Desktop spike (§11) runs first, because it is the one that can
invalidate the rest.

## 14. What this draft owed a measurement

Every claim above that could be checked from this machine was, on 2026-08-13.

- **The compile gap.** `GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 go build ./cmd/morzer`
  produced the three errors in §1. `go list -deps` confirmed that `preflight`,
  `cli` and `lock` all depend on `atomicfs` and were therefore never
  type-checked — which is why §3.1 asserts six sites where the compiler names
  three.
- **`x/sys` symbol availability**, read out of the module cache at
  `golang.org/x/sys@v0.47.0`: `Openat` is present in `zsyscall_darwin_{amd64,arm64}.go`;
  `Sysinfo` appears only in `zsyscall_linux.go`; `TCGETS` appears in no darwin
  file while `TIOCGETA` appears in `zerrors_darwin_{amd64,arm64}.go`. Site 2 is
  therefore a one-word change and sites 3 and 4 are not.
- **`internal/infra/exec` and `adapters/runtime/compose` compile for darwin
  already** — built independently, since neither depends on `atomicfs`.
- **The supervisor is nil-tolerant**, read at four call sites rather than
  inferred from the port comment.
- **`install.sh`'s architecture table and archive name**, which the draft
  initially had backwards. The arch case already accepts `arm64`; the OS is a
  literal in the archive name. §5.4 says so because the file was read, and the
  first draft of that paragraph was wrong in both directions.
- **`golang.org/x/term` is already a direct dependency** (`go.mod:30`), which
  turns site 4's fix from "add a dependency" into "delete platform trivia".
- **The CI ceiling (§3.5)**, which this draft got half wrong and which review
  corrected. Researched: Apple silicon hosted runners cannot run Docker for want
  of nested virtualization, and `macos-13` — where `colima` demonstrably worked
  — was retired on 4 December 2025. **Wrong on first writing:** the draft
  repeated the September 2025 announcement that `macos-15-intel` would be the
  final x86_64 image and inferred that Intel macOS was ending. It is not.
  `macos-26-intel` reached general availability on 26 February 2026, so Intel
  runners are still being shipped and an argument from their retirement is
  unsound.

  The correction moves §5.7 from "impossible" to "declined", which is a
  different and better claim: the lane could exist on Intel and would report on
  an architecture this tier's audience does not use. Recorded here rather than
  quietly edited, because a reader picking this up later needs to know which
  facts were dated and which were reasoned — this is the one most worth
  re-checking, and it was already stale within six months of the announcement it
  came from.

What this draft could **not** measure, and does not claim: anything that
requires a Mac. Docker Desktop's behaviour, `hw.memsize`, `kinfo_proc`, and
`statfs`'s `f_fstypename` are all reasoned from documentation. §11 names them
and P2 spikes them first.

## 15. Amendments

**A1 — decision 11 departed: the prompt keeps its own ioctls (2026-08-13, P1).**

Decision 11 is graded `ASSUMED` and says `x/term`'s `ReadPassword` beats two
files of ioctl constants, with the escape "execution may depart if
`ReadPassword`'s behaviour differs from what the current prompt promises". It
does, in the one way that matters, and the answer was already written in the
code being ported:

> The echo flip is performed *here*, not in the reader goroutine: […]
> `x/term.ReadPassword` does its flip inside the reading goroutine, which is
> exactly the ordering race this replaces.

`readPassword` flips the terminal before the reader starts and restores it where
the reader can no longer contradict it; `prompt_pty_linux_test.go` synchronises on the
flip being observable, precisely so that ordering is pinned. Adopting
`ReadPassword` for portability would have reintroduced a defect somebody had
already fixed, in the one prompt that handles a secret.

So §5.1's other option was taken: `termios_linux.go` and `termios_darwin.go`,
two constants each — darwin spells the requests `TIOCGETA`/`TIOCSETA` and has no
`TCGETS`. Everything else the prompt touches is identical on both. **The lesson
generalises past this row: a portability assumption made about a function's
*signature* can be wrong about its *concurrency*, and the code being replaced is
where that shows.**

**A2 — decision 12 resolved: the fail-safe stub, not `SysctlKinfoProc`
(2026-08-13, P1).**

Decision 12 was `OPEN` and invited whoever executed to take the real call if
they found it trivial, and to log that they did. It is trivial —
`unix.SysctlKinfoProc` is available and the implementation is about five lines.
It was still not taken, and the reason is not effort.

Decision 9 declines a macOS lane, so anything written for darwin ships
unexecuted, and the two options fail in opposite directions. The stub returns
zero, the caller reads zero as "unknown", and an unmatched PID is treated as a
**live holder**: wrong, and an operator waits for a lock that was free. A
`SysctlKinfoProc` implementation that is subtly wrong — wrong field, wrong
units, a `Timeval` compared against Linux's clock ticks — makes every lock look
stale, and the next operation **steals** it: a concurrent deployment against one
installation, which is the single thing the guard exists to prevent.

An untested guard that fails unsafe is worse than an acknowledged absence that
fails safe. P2 takes the real call, with a Mac to run it on.

**A3 — §8's lock test was owed by P1 and nearly missed (2026-08-13, P1).**

§8's third bullet requires P1 to drive the "cannot determine a start time" path
directly, and the first pass of P1 shipped without it: the stub was written, its
reasoning was written, and the branch that reads it had no test. The self-audit
found it by walking §8 rather than the diff, which is the only order that finds
an obligation nobody wrote code for.

Driving it needed a seam. `ownerAlive` called `pidStart` inline, and provoking a
`/proc` read that fails while `kill(pid, 0)` succeeds means racing a process
exit — so the comparison is now `startTimeContradicts(recorded, live)`, a pure
function whose truth table is asserted exhaustively. Either side zero is
silence, and silence is not evidence.

**A4 — §3.1 counted six sites; there were eight (2026-08-13, P1).**

The measurement in §14 is sound and its method has a blind spot it names without
following: `go build ./...` does not compile test files. Two more sites were
waiting in them — `internal/cli/prompt_test.go`'s pty harness (`/dev/ptmx`,
`TIOCGPTN`, `TIOCSPTLCK`) and one pty-dependent assertion in
`root_internal_test.go`. Neither blocks the binary, and both would have met a
developer on the second command they ran, which is worse than meeting them on
the first.

Both files were split rather than gated wholesale, so the portable tests in them
keep running everywhere and only the pty plumbing is Linux-only.

**A5 — decision 3 widened: the gate is `go build` *and* `go vet` (2026-08-13,
P1).**

Decision 3 is graded `LOCKED` and specifies `GOOS=darwin go build ./...` in `just
ci` for both architectures. `darwin-check` runs `go vet` for both as well, which
is more than the row says and therefore gets its own entry: a `LOCKED` row is
graded that way because reopening it was meant to cost a second reader, and a
widening folded into the tail of an amendment about something else is not a
second reader seeing it.

The reason is A4's. Two of the eight sites are in test files, and `go build` does
not compile those, so the gate decision 3 specifies cannot observe the defect
class A4 found — it would have gone green on a tree where `go test` did not
compile. `go vet` type-checks test files, which is the cheapest thing that closes
that gap and needs no macOS runner (decision 9). The direction is the safe one:
this only ever refuses more than the row promised, never less.

---

For the record, because §14 is a claim about rigour and would be worth less if it
were quietly maintained: this draft was corrected in review, before adoption. The
Intel-runner facts in §3.5 were stale, which moved decision 9's reason from
"impossible" to "declined"; the Scope paragraph claimed P1 covered six sites when
§5.1 gives it four and a stub; §5.3 promised a P1 binary that drives Docker
Desktop while §11 lists that as unverified; and the build gate covered one
architecture for a matrix with two. All four are folded into the sections above
rather than tracked here, since the draft never shipped in the state that had
them.
