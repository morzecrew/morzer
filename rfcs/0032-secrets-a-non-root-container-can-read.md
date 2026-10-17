# RFC 0032 — Secrets a non-root container can read

- **Status:** 📝 Draft — written 2026-10-17, **execution-ready, one PR**.
  Nothing is built. Ruled 2026-10-17: one owner per secret (decision 2).
- **Scope:** Who owns a rendered secret file. Today every file under the render
  directory is `0400` and owned by the manager, which is root on every machine
  that runs units, so a container whose image runs as any other user cannot
  read the Compose file secret it was given. This RFC adds an optional numeric
  `owner` to a secret's declaration, applied each time the file is rendered,
  and covers its shape and validation, where the owner is set, what a manager
  that cannot set it does, how the result is verified, and the manager-version
  floor a bundle using it declares. The mode stays `0400` and the directory
  stays `0700` and root's. Deliberately not a list of owners per secret, not
  owners by name, not a change to Compose's own secret handling, and not
  user-namespace remapping or rootless Docker. One optional field in the secret
  schema, so a published-schema change; no manifest change; no installation
  schema change; no port change.
- **Related:** [`internal/domain/secret.go`](../internal/domain/secret.go)
  (`SecretDeclaration`, `SecretSchema.Validate`),
  [`internal/release/load.go`](../internal/release/load.go)
  (`LoadSecretSchema`, `manifestPreamble`),
  [`internal/adapters/secrets/sopsage/sopsage.go`](../internal/adapters/secrets/sopsage/sopsage.go)
  (`Render`), [`internal/infra/atomicfs/atomicfs.go`](../internal/infra/atomicfs/atomicfs.go)
  (`WriteFileIn`), [`internal/lifecycle/ops/apply.go`](../internal/lifecycle/ops/apply.go)
  (`stepRenderSecrets`), [`test/contract/secretstore.go`](../test/contract/secretstore.go),
  [`schemas/selfhost-v1alpha1-secrets.json`](../schemas/selfhost-v1alpha1-secrets.json),
  [0003](0003-secrets-recovery-and-onboarding.md) (the render directory's
  invariants), [0018](0018-the-pre-1-0-manifest-surface.md) (decision 1, the
  floor that lets an older manager say it is too old),
  [0029](0029-macos-as-a-development-host.md) (Docker Desktop maps ownership)
- **Origin:** Item M1 of the requests filed while packaging `erp-standalone`
  on 0.4.0, 2026-10-17, marked as the one that blocks the bundle. Its backend
  runs as uid 65532 (distroless `nonroot`) and its object store as 10001; both
  exit at start because they cannot open their secrets. Postgres, Valkey and
  Flyway work only because they start as root. Until this lands the bundle runs
  four services as root to read one file each.

---

## 1. Problem

A Compose file secret is a bind mount of the host file, as it is. Compose has
`uid`, `gid` and `mode` on a service's secret, and ignores all three for file
secrets — it says so (§2.1). So the only thing deciding whether a container can
read its secret is the file's owner and mode on the host, and the manager
renders every one `0400` and its own. A vendor whose image runs as a non-root
user, which is the default for distroless and for most hardened images, has two
ways out today: run the service as root (`user: "0:0"`), giving up the reason
the image was built that way, or skip file secrets and pass values through the
environment, where `docker inspect` and every child process can see them.
Neither is something a vendor should have to choose to ship on this manager.

## 2. Current state

- **One write path renders every secret.** `Store.Render`
  ([`sopsage.go:379`](../internal/adapters/secrets/sopsage/sopsage.go)) creates
  the directory with `MkdirExact(targetDir, 0o700)` (:385) and writes each
  declared value with `atomicfs.WriteFileIn(root, fileName, value, 0o400)`
  (:413), then removes files no declaration backs. `WriteFileIn`
  ([`atomicfs.go:165`](../internal/infra/atomicfs/atomicfs.go)) writes a temp
  file, fsyncs it, sets the mode explicitly (:194) and renames it into place
  (:202). Nothing sets an owner, so the file belongs to whoever runs the manager.
- **Four callers, all through `Render`.** `stepRenderSecrets` on `apply` and
  every `update` (apply.go:340), the update restore point re-rendering the
  previous release's secrets (update.go:942), `morzer secret render`
  (cli/secret.go:235) and `restartDependents`, which re-renders after `secret
  set`, `generate`, `rotate` and `edit`, before the declared services restart
  (cli/secret.go:455). A property `Render` applies is
  kept by all four without a caller change.
- **The mode is verified, not assumed.** `stepRenderSecrets`'s `Verify` checks
  the directory is `0700` (apply.go:352) and each file `0400` (:363). Nothing
  checks an owner. `doctor` checks only that the directory is memory-backed
  (`checkSecretsOnEphemeralStorage`, doctor.go:678).
- **The schema is decoded strictly.** `LoadSecretSchema`
  ([`load.go:227`](../internal/release/load.go)) decodes `secrets.schema.yaml`
  with `yaml.Strict()` and `DisallowUnknownField` (:251), so a field this build
  does not know is a decode error. An older manager reading a schema with
  `owner` would refuse it as malformed, not as needing a newer manager.
- **The floor is read before anything strict.** `ParseManifest` reads
  `compatibility.min_manager_version` through a lenient preamble
  (`manifestPreamble`, load.go:319) and refuses a bundle needing a newer
  manager before decoding the rest ([0018](0018-the-pre-1-0-manifest-surface.md)
  decision 1). The secret schema is reached only through the manifest, so a
  bundle declaring the right floor is refused by an older manager before its
  schema is read. `MigrateStepsMinManagerVersion` is the precedent: a manifest using a migrate
  list must declare `0.4.0` or more, and the current build refuses one that
  declares less ([`manifest.go:1158`](../internal/domain/manifest.go)).
- **Two implementations of the store.** `sopsage` and the fake in
  [`test/fakes/fakes.go:826`](../test/fakes/fakes.go), held to one battery in
  [`test/contract/secretstore.go`](../test/contract/secretstore.go), which
  already asserts the `0400`-in-`0700` invariant
  ([0003](0003-secrets-recovery-and-onboarding.md) §3).
- **Nothing checks the effective uid.** The manager assumes root on a machine
  with units and never asserts it. The test lanes, including `just acceptance`,
  run as an ordinary user under `--root`, so none of them can give a file to
  another uid today.

### 2.1 Measured: the owner is the only thing that matters

Docker 29.7.2 with Compose 5.5.1, `alpine:3`, a file secret at `./d/s` holding
`hunter2`, with `d` `0700` and owned by uid 1000:

| File | Read as | Result |
| --- | --- | --- |
| `0400`, owned by 1000 | 65532, short-form secret | `Permission denied` |
| `0400`, owned by 1000 | 65532, long form with `uid: "65532"`, `gid`, `mode: 0444` | `Permission denied`, and Compose warns: *secrets `uid`, `gid` and `mode` are not supported, they will be ignored* |
| `0400`, owned by 65532 | 65532 | `hunter2` |
| `0400`, owned by 65532 | 10001 | `Permission denied` |

The third row is the step this design assumes: the parent directory's mode and
owner do not take part, because the container opens the bind-mounted file, not a
path through the directory. So the directory can stay `0700` and root's, and a
host user, who still has to traverse it, is still kept out. The fourth row is
what one owner per secret buys: a container that runs as anybody else is
refused.

## 3. Goals / Non-goals

**Goals**

- A container running as a non-root user reads its Compose file secret with no
  `user: "0:0"` and no change to the vendor's Compose file beyond what it
  already declares.
- The owner survives every render: `apply`, an update and its restore, `secret
  render`, and a rotation.
- A manager that cannot give the file away says so at render, naming the
  secret, instead of rendering a file the container will be refused later.
- An older manager meeting a bundle that uses this says it needs a newer
  manager.

**Non-goals**

- **Several owners for one secret.** Ruled (decision 2): two users means two
  declarations with the same value. A list rendered as copies needs a naming
  rule the vendor's Compose file then has to follow, and can be added later as
  a second accepted shape without breaking this one.
- **Owners by name.** A name resolves through the *host's* `/etc/passwd`, and
  the user that matters is the one in the *image's*. `nonroot` on the host, if
  it exists at all, is not 65532 in a distroless image.
- **Changing Compose.** Compose ignoring `uid`/`gid`/`mode` for file secrets is
  its behaviour, and the manager does not rewrite a vendor's Compose file to
  work around it.
- **User-namespace remapping and rootless Docker.** There the container's 65532
  is a different uid on the host. Named in §7 and in the docs, not solved.

## 4. Design

### 4.1 The field

```yaml
secrets:
  - name: postgres_password
    generator: {kind: password, length: 32}
    owner: "65532:65532"        # uid, or uid:gid; numeric only
    services: [postgres, backend, worker]
```

```go
type SecretDeclaration struct {
	// ...existing fields...

	// Owner is the numeric uid, optionally :gid, the rendered file is given
	// to. Absent means the manager's own, which is root wherever units run.
	Owner SecretOwner `yaml:"owner" json:"owner,omitempty"`
}

type SecretOwner struct{ UID, GID uint32; HasGID, Set bool }
```

Validated in `SecretSchema.Validate`: one or two decimal integers separated by
`:`, each from 0 to 4294967294. 4294967295 is `chown(2)`'s "leave unchanged",
which as an owner means nothing. A name is refused, with a hint saying why
(§3). `"0"` and `"0:0"` are accepted and mean root, as absent does. Without a
gid the group is left as the manager's, as `chown(1)` leaves it. The file is
`0400`, so its group can read nothing either way.

### 4.2 Where the owner is set

On the temp file, after the mode and before the rename, inside
`WriteFileIn`'s existing sequence (atomicfs.go:194-202), so the file never
exists at its final name with the wrong owner. `Render` passes the declaration's
owner on every write, which is what makes the four callers of §2 keep it: a
rotation writes a new temp file and gives it away again, and a release that
drops `owner` renders the file back to the manager's own uid.

### 4.3 A manager that cannot give the file away refuses

`chown` to another uid needs `CAP_CHOWN`. A manager without it gets `EPERM`, and
`Render` fails with a secrets error naming the secret and the owner, and a hint
that the manager has to run as root to apply it. It never falls back to a file
it owns. That file would render cleanly, verify cleanly, and surface minutes
later as `Permission denied` inside a container the operator did not write.
Giving a file to one's own uid and group needs no privilege, which is what the
non-root test lanes use (§5).

### 4.4 Verified like the mode

`stepRenderSecrets`'s `Verify` gains the owner beside the mode (apply.go:363):
each rendered file is owned by its declared uid (and gid, when declared), or by
the manager's uid when none is declared. `RenderedFile` carries the owner, so
`secret render` and its JSON say who can read each file. The field is additive.

### 4.5 The floor

A schema in which any secret declares `owner` requires the manifest's
`compatibility.min_manager_version` to be at least
`SecretOwnerMinManagerVersion`, the release that ships this. `LoadSecretSchema`
has the release, and so the manifest, and refuses a schema whose bundle
declares less, as `Validate` does for a migrate list. An older manager then
refuses the bundle at the manifest preamble as needing a newer manager, and
never reaches the strict decode that would call `owner` an unknown field.

### 4.6 Alternatives considered

- **Run the service as root** (`user: "0:0"`). Works today and is what the
  bundle does until this lands. It gives up the non-root image's whole point
  for the sake of reading one file.
- **A world-readable file (`0444`) in the `0700` directory.** §2.1 shows the
  directory does not take part in the container's read, so this needs no new
  field at all, and host users are still kept out by the directory. Rejected
  because the directory's mode becomes the only barrier on the host, and
  inside the container every user can read the secret, including the
  unprivileged processes a non-root image exists to separate. An owner keeps
  the file readable by exactly the user the vendor named.
- **Group-readable `0440` with a shared gid.** Compose ignores `gid` as it
  ignores `uid`, so this still needs the manager to set the group, and it widens
  read access from one user to every member of the group. Nothing is gained
  over an owner.
- **Derive the owner from the image's `USER`.** Automatic, but it couples
  rendering to image inspection, and it is wrong whenever Compose overrides
  `user:`, which the manager would then have to parse too. The vendor knows the
  uid and can write it down.
- **A list of owners, rendered as copies.** Ruled out for now (decision 2): the
  copies need names, and the vendor's Compose file has to know them.

## 5. Tests

- **Domain:** the field's grammar, as table cases. These are `65532`,
  `65532:65532` and `0`; refused are `nonroot`, `-1`, `4294967295`, `1:2:3`,
  ` 65532` and an empty string. A schema with an owner whose manifest declares
  a floor below `SecretOwnerMinManagerVersion` is refused, naming both.
- **Contract battery, both stores:** render with the owner set to the test
  process's own uid and gid, which needs no privilege. Assert the stat owner,
  then that a re-render after `Set` keeps it and that dropping `owner` returns
  the file to the process's uid. The fake honours the field, or the battery
  would pass against one implementation only.
- **Refusal:** with an owner that is not the process's uid, `Render` fails
  naming the secret, and no file is left at the final name. Skipped when the
  test runs as root, where the chown would succeed.
- **Docker lane, the requester's done-when:** the lane's one root is a
  container, so the render runs there. The built binary renders into a
  bind-mounted directory as root, then §2.1 runs as a test. A Compose file
  secret owned by 65532 is read by a service running as 65532, and refused to
  one running as 10001. A rotation re-renders, and 65532 still reads the new
  value.
- **Sabotage:** dropping the chown fails the Docker lane, and dropping the
  owner from `Verify` fails the battery.

## 6. Docs

- The secret schema reference gains `owner`: numeric only and why, the floor it
  needs, and that two users need two declarations with the same value.
- The authoring guide gains a short section for non-root images, using the
  requester's case, and says outright that Compose ignores `uid`, `gid` and
  `mode` on file secrets, quoting its warning, so nobody tries those first.
- A caveat for user-namespace remapping and rootless Docker (§7).
- The published `selfhost-v1alpha1-secrets.json` is regenerated from the type.

## 7. Risks

- **The vendor's uid does not match the image.** The container is refused,
  exactly as it is today, and nothing on the host is wrong. Mitigated in the
  docs by naming where to read the image's user (`docker image inspect
  --format '{{.Config.User}}'`). The manager cannot check it, for the reason in
  §4.6.
- **User-namespace remapping, rootless Docker.** The owner is a host uid, and
  with remapping the container's 65532 is 165532 or similar on the host, so the
  read is refused. Accepted and documented. Mapping uids through the daemon's
  configuration would make the manager depend on reading it.
- **macOS.** Docker Desktop maps ownership across virtiofs rather than carrying
  it, and the manager there runs as a user, so §4.3 refuses. Left `OPEN`
  (decision 10) for [0029](0029-macos-as-a-development-host.md)'s spike, which
  already has to assert that a bind-mounted secrets directory is readable with
  the modes the release declares.
- **Misread as weakening the directory.** The directory keeps its mode and
  owner. What changes is which single uid can open one file through a mount
  the vendor declared.

## 8. Decisions

| # | Decision | Grade | Why |
| --- | --- | --- | --- |
| 1 | A secret's declaration may carry `owner`, a numeric uid or `uid:gid`, applied to its rendered file; absent means the manager's own uid, as today | LOCKED | It is a published schema field that bundles will write. Changing its meaning later changes who can read credentials on machines already running. |
| 2 | One owner per secret; two users need two declarations with the same value | ASSUMED | The smallest shape, and a list rendered as copies can still be added later as a second accepted shape without breaking this one. Ruled by the author 2026-10-17. |
| 3 | Numeric only; a name is refused with a hint | ASSUMED | A name resolves through the host's user database, and the user that matters is the image's. |
| 4 | The file stays `0400` and the directory `0700` and root's | ASSUMED | §2.1: the directory does not take part in the container's read and still keeps host users out. The owner changes who reads the file, not how many. |
| 5 | The owner is set on the temp file before the rename, in `WriteFileIn`, on every render | ASSUMED | The file never exists at its final name with the wrong owner, and all four `Render` callers keep the owner without a caller change. |
| 6 | A manager that cannot give the file away (`EPERM`) refuses to render that secret; it never falls back to owning it | ASSUMED | The fallback renders and verifies cleanly and fails later, inside a container, as `Permission denied`. |
| 7 | `Verify` checks the owner beside the mode, and `RenderedFile` carries it | ASSUMED | The mode is verified rather than assumed for the same reason, and the owner is now part of the same security property. |
| 8 | A schema using `owner` needs `compatibility.min_manager_version` ≥ `SecretOwnerMinManagerVersion`, checked in `LoadSecretSchema` | ASSUMED | The mechanism of [0018](0018-the-pre-1-0-manifest-surface.md) decision 1, used the way the migrate list uses it: an older manager says it is too old instead of calling `owner` an unknown field. |
| 9 | `SecretOwnerMinManagerVersion` is the release that ships this, expected `0.5.0` | OPEN | Whatever the next tag is. The executor sets it and the CHANGELOG names it. |
| 10 | What a darwin manager does with `owner` | OPEN | Settled by [0029](0029-macos-as-a-development-host.md)'s Docker Desktop spike. Until then decision 6 applies, and a darwin manager running as a user refuses. |
| 11 | User-namespace remapping and rootless Docker are documented, not handled | ASSUMED | Handling them means reading the daemon's uid map, a dependency this manager does not have. |

## 9. Phasing

- **P1, one PR:** the field, its validation and the floor; the owner in
  `WriteFileIn` and `Render`; the refusal; `Verify` and `RenderedFile`; the fake
  and the battery; the Docker-lane test of §5; the regenerated schema; the
  docs. P1 is everything; there is no P2.
- **Demand-gated:** a list of owners (decision 2), and darwin (decision 10)
  once 0029's spike has measured Docker Desktop.
