# quadlet-acceptance · RFC 0023 P4, the rootless Podman acceptance stage

On branch `quadlet-acceptance` from `main` at `9f975be3`. Executes
[RFC 0023](../rfcs/0023-runtimes-beyond-compose.md) P4 (§8, §10): a rootless
Podman stage that installs, reconfigures a port (0007's
re-create-not-restart assertion), updates, rolls back and restores.

**Drift count: 0.**

| Class | Test | Meaning |
|---|---|---|
| `discovery` | Could not have been known before code existed | Healthy — the spec was right to be silent |
| `spec-gap` | Could have been known; the spec was silent or at the wrong altitude | The design process missed something |
| `drift` | The spec covered it and it was built otherwise anyway | **A defect** |
| `irreducible` | No amount of design settles it | Stop and spike |

## Plan, 2026-11-28

### Measured for this plan

- The `ubuntu-24.04` runner image ships Podman 4.9.3 and Buildah 1.33.7
  (`images/ubuntu/Ubuntu2404-Readme.md` in `actions/runner-images`).
  Every measurement behind rows 33 to 61 was taken on Podman 6.1.1.
- The fixture hooks under `testdata/bundle/hooks/` call no `docker`.
- The Quadlet adapter has `CaptureVolume`, `RestoreVolume`
  (`internal/adapters/runtime/quadlet/volumes.go`) and `IngestImages`
  (`ingest.go`), so a backup, a restore and a bundle's images reach Podman.
- `rollback` refuses across a schema move (`pages/docs/reference/commands.md`,
  "rollback"). `testdata/bundle-1.3.0` moves the schema from 12 to 14, which
  is why the Compose stage asserts the refusal. A rollback that succeeds
  needs a successor with the same schema.

### Plan

- `testdata/bundle-quadlet/`: release `demo` 1.2.0 declaring `quadlet`
  alone. It keeps `testdata/bundle`'s hooks, templates, secrets schema,
  parameters and health checks. `demo-app.container` publishes
  `${DEMO_PARAM_HTTP_PORT}` and carries `DEMO_VERSION`, so an update and a
  rollback show inside the container. `demo-uploads.volume` is the named
  volume a backup captures. Both images are `from: bundle`.
- `.github/scripts/acceptance-podman.sh`, run by `just acceptance-podman`:
  - Refuses to run as root, without a reachable user manager, or when
    `$XDG_RUNTIME_DIR/containers/systemd/morzer/demo` exists, so a local
    run cannot touch an installation of the user's.
  - Builds the `app` and `db` stubs with `podman build`, pushes them to a
    registry run by Podman on loopback, pins their digests, and packs them
    with `release pack`. `init` and `update` then load them, and no unit
    pulls. The script derives 1.2.1 from the fixture, the version alone.
  - `release verify`, `init`, `apply`: both services active, the app
    answering on the port that was set.
  - `config set http_port`: the app's container has a new ID, is
    published on the new port, and answers there; the old port refuses.
  - A `file://` backup target, a marker written into `uploads`, `backup`.
  - `update` to 1.2.1: `DEMO_VERSION` in the container is 1.2.1, and
    `status` says so. `rollback`: both say 1.2.0.
  - `restore` after the marker is removed: the marker is back.
  - A trap removes the installation, the registry and the work directory.
- `.github/workflows/ci.yml`: a job, "Acceptance (rootless Podman)", on
  `ubuntu-24.04` by name, since the Podman version is what it tests. It
  runs when the Docker acceptance job does. It enables lingering for the
  runner user, sets `XDG_RUNTIME_DIR`, installs sops, logs Podman in to
  Docker Hub when a token is configured, and runs `just acceptance-podman`.
  It is not in the coverage job's `needs`.
- Docs: "Running on Podman" says the acceptance run exercises the runtime,
  on Podman 4.9.3. Its "one data home" bullet names `CONTAINERS_CONF`,
  `CONTAINERS_STORAGE_CONF` and `CONTAINERS_REGISTRIES_CONF` beside
  `XDG_DATA_HOME` ("Rulings after #447" in `logs/quadlet-adapter.md`).
  `testdata/acceptance/README.md` names the second stage.
- No changelog entry: the stage is CI, and the runtime is unreleased.

### Decisions this plan needs

- **D-1, load-bearing. Where the stage runs.** Lean: a CI job on
  `ubuntu-24.04` with the runner's own Podman 4.9.3, and the same script
  locally. A recorded local run, as macOS has, gives no signal per pull
  request. A newer Podman on the runner has no first-party source for
  Ubuntu 24.04.
- **D-2, load-bearing. What happens if Podman 4.9.3 cannot run the
  adapter.** Nothing in rows 33 to 61 states a Podman floor. Lean: a gap
  that a small fix closes on both versions is fixed in this PR, with its
  test. A gap that needs a design, such as a floor `init` and `doctor`
  enforce, stops the work and comes back for a ruling.
- **D-3. The fixture.** Lean: `testdata/bundle-quadlet/` as above, with
  1.2.1 derived by the script. `testdata/bundle` does not gain Quadlet
  files: unit and contract tests read it, and a second runtime there
  changes what they read.
- **D-4. The images.** Lean: bundled and packed, so the units never pull
  from the loopback registry and no `registries.conf` is edited, on the
  runner or on a user's machine.
- **D-5. Coverage.** Lean: the stage runs an uninstrumented binary and
  stays out of the coverage gate. Adding it later is an artifact upload
  and one more profile for `covmerge`.

Whether the new job is a required check is a branch-protection setting,
the user's.

### Rulings, 2026-11-28

The author took every lean on 2026-11-28: D-1 a CI job on `ubuntu-24.04`
with its own Podman 4.9.3, and the script locally; D-2 a small fix that
holds on both versions lands here, and a gap needing a design halts; D-3
`testdata/bundle-quadlet/`, with 1.2.1 derived by the script; D-4 bundled,
packed images; D-5 no coverage instrumentation.

### Built, 2026-11-28

Built as planned, with three changes.

- **The fixture is a whole bundle.** `testdata/bundle-quadlet/` holds
  copies of `testdata/bundle`'s hooks, template, secrets schema and
  release notes, so `release verify` reads it on its own. Its images are
  plain references. The script marks both `from: bundle` before packing,
  as `acceptance.sh` does for its air-gapped bundle.
- **The job is part of the required verdict.** The plan left that to
  branch protection. But "Required Continuous Integration" refuses a job
  missing from its `needs`, and fails when any job fails, so every job in
  `ci.yml` is required, this one included.
- **`podman build` runs with `--http-proxy=false`.** Podman hands a build
  the shell's proxy variables, and a proxy on the host's loopback is not
  on the build's. The first local run failed on it: `apk` could not reach
  its index.

Checked:

- `just acceptance-podman` passes locally, rootless, on Podman 6.1.1 and
  systemd 261.
- One sabotage run, with the definition hash made to ignore values
  (`internal/adapters/runtime/quadlet/install.go`, `definition`). The
  stage failed at the update, with `the container still runs 1.2.0`. The
  port move passed under it, because `config set` forces a re-create of
  the services a parameter names (`stepRecreateServices`,
  `internal/lifecycle/ops/config.go`), whatever the hash says.
- `just ci` passes at 88.0%.

**Drift count: 0.**

## The first run on Podman 4.9.3, 2026-11-28

The job failed at `apply`, step `validate-runtime`: `the Quadlet
generator refused the bundle: quadlet-generator[5931]: Loading source unit
file /tmp/morzer-quadlet-1401314803/demo-app.container`. The message is the
generator's first line, a log line, so it hides the cause. Podman's own
source gives it, and two more gaps behind it:

- **`PublishPort=` takes no variable before Podman 5.3.** Quadlet checks
  each host and container port against `\d+(-\d+)?(/udp|/tcp)?$` and
  refuses `${DEMO_PARAM_HTTP_PORT}` with `invalid port format`
  (`pkg/systemd/quadlet/quadlet.go` at v4.9.3, line 1459). From v5.3.0
  only `ExposeHostPort=` is checked.
- **A rootless Quadlet reads `$XDG_RUNTIME_DIR/containers/systemd` only
  from Podman 5.3.** At v5.2.0, `getUnitDirs` (`cmd/quadlet/main.go`)
  reads `$XDG_CONFIG_HOME/containers/systemd` and
  `/etc/containers/systemd/users`; v5.3.0 adds the runtime directory.
  Row 45 installs every unit there.
- **Drop-ins arrive in Podman 5.0.** v4.9.3's `cmd/quadlet/main.go` reads
  no `.d` directory, and row 27 delivers parameters, the definition
  label, `SuccessExitStatus=143` and `UnsetEnvironment=` through one.

So the adapter needs Podman 5.3 or later, and nothing says so:
`findGenerator`'s hint asks for 4.4. `ubuntu-24.04` cannot run it.
`ubuntu-latest` is now `ubuntu-26.04`, whose image ships Podman 5.7.0
(`images/ubuntu/Ubuntu2604-Readme.md` in `actions/runner-images`).

```divergence
decision: D-2
grade: LOCKED
class: discovery
at: 2026-11-28T12:52:55Z
attempt: 1
claim: the quadlet adapter cannot run on Podman 4.9.3, the version D-1 put the stage on; rows 27 and 45 need drop-ins (Podman 5.0) and the runtime directory in Quadlet's search path (Podman 5.3), and a port parameter needs PublishPort without Quadlet's number check (Podman 5.3); serving 4.9.3 means reversing rows 27 and 45, and declaring a floor means a design that init and doctor enforce, so D-2's ruling halts the work
evidence: `gh api 'repos/containers/podman/contents/cmd/quadlet/main.go?ref=v5.3.0'` — `getRootlessDirs` appends `$XDG_RUNTIME_DIR/containers/systemd`; the same file at `ref=v5.2.0` names no runtime directory; `pkg/systemd/quadlet/quadlet.go` at `ref=v4.9.3` holds `invalid port format` for a `PublishPort=` host port
action: halted
proposal: ASSUMED — the quadlet runtime needs Podman 5.3 or later; `init` refuses it on an older Podman and `doctor` reports the version against the floor, and the acceptance stage runs on `ubuntu-26.04`
```

Also found, for whichever change comes next: a refusal from the
generator is reported by its first line, which on 4.9.3 is a log line
rather than the error.

**Drift count: 0.**

### Ruling on the halt, 2026-11-28

The author ruled on 2026-11-28: the quadlet runtime needs Podman 5.3 or
later. The halt's proposal stands as the row to add at the next
reconciliation. D-1 is superseded: the stage runs on `ubuntu-26.04`,
whose Podman is 5.7.0. The floor itself, `init` refusing an older Podman
and `doctor` reporting the version, is a separate change, with the
generator's refusal reported by its error rather than its first line.

## Review of #449, 2026-11-28

- **The stage could remove a real installation's data.** It refused only
  an existing unit directory, which a reboot clears while the volumes
  stay, and it counted itself the owner before `init`. It now refuses
  existing `demo` containers and volumes too, owns them only once `init`
  succeeds, and its cleanup names its units rather than `demo-*.service`
  (`c3376e9c`). The registry is named for the run.
- **The volume helper override was ignored** (`235fb346`).
- **The db unit kept no data.** It now mounts `demo-database.volume`,
  which a backup captures cold, and the stage checks a marker in it
  survives the restore (`73b747fe`). The local run took one volume cold
  and one hot, and left no unit failed.
- **The backup hook's empty archive** is refuted: the restore step fails
  the run unless the data directory's marker comes back.

**Drift count: 0.**

### Round 2, 2026-11-28

- **The stub tags were shared between runs.** A second run, failing at
  its registry because the first held the port, removed the first's
  stubs on exit. They are tagged `acceptance-<pid>` now.
- **"Lint workflows" failed on `ubuntu-26.04`**: the pinned actionlint
  does not know the label. The job runs on `ubuntu-latest`, which is
  26.04, with Podman 5.7.0.
- The first review's two notes without a thread are fixed: each service
  is waited for on its own (`ab5b1d69`), since `systemctl is-active`
  succeeds when any listed unit is active, and a run that fails before
  packing removes its stub tags (`73d7aef1`).

**Drift count: 0.**
