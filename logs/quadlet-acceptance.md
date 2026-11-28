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
