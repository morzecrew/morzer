# morzer

[![CI](https://github.com/morzecrew/morzer/actions/workflows/ci.yml/badge.svg)](https://github.com/morzecrew/morzer/actions/workflows/ci.yml)

A CLI that manages the lifecycle of a self-hosted product on a single Linux
machine running Docker Compose: install, configure, update, roll back, back up,
restore, diagnose.

The unit of delivery is a **release bundle** — an immutable archive containing a
manifest, Compose files, configuration templates and lifecycle hooks. The unit
of management is an **installation** — the state of one deployment on one
machine.

**Status:** `init`, `apply`, `status`, `doctor`, `backup`, `restore`, `secret`
and `release` work against a real bundle. `update` and `rollback` do not exist
yet — see [`rfcs/`](rfcs/) for what is designed but not built.

## What it is not

Not a container orchestrator, an infrastructure provisioner, a secret manager, a
backup engine, a workflow engine, or a CI system. It coordinates Docker
Compose, SOPS, age, systemd and the release's own hooks; it owns ordering,
atomicity, verification and reporting.

## Install

```sh
just build          # ./morzer
just build-all      # dist/morzer-linux-{amd64,arm64} + SHA256SUMS
```

`just --list` shows every recipe.

Requires Go 1.25+ to build. At runtime it needs `docker`, `docker compose` and
`sops` on the target machine — the versions a release demands are checked in
preflight and reported by `doctor`.

## Try it without touching /etc

Every managed path derives from a single root, so the whole thing can be
exercised against a throwaway directory:

```sh
just demo
```

That runs `init`, `status`, `doctor` and `secret list` against the example
bundle in `testdata/bundle/`, writing everything under `tmp/demo/`. Two
variants go further: `just demo-plan` shows what `apply` would do as a step
list with a configuration diff, and `just demo-json` shows the machine-readable
output contract.

The `--root` flag relocates *every* managed path -- including the absolute
configuration targets a manifest declares -- so nothing touches the real
`/etc`. It is hidden from `--help` because it exists for testing.

By hand:

```sh
# Generate an offline recovery key. Keep the private half off this machine:
# a recovery key stored on the machine it is meant to recover protects nothing.
RECOVERY=$(./morzer secret recipients generate-recovery-key ~/recovery.key)

./morzer init \
    --release ./testdata/bundle \
    --profile embedded \
    --domain demo.example \
    --recovery-recipient "$RECOVERY"

./morzer apply
./morzer status
./morzer doctor
```

## Commands

| Command | What it does |
| --- | --- |
| `init` | Creates the layout, the machine age identity, the installation config and the secret state. Never overwrites an existing installation. Does not start the product. |
| `apply` | Converges the system to the installed release: secrets, configuration, images, migrations, services, health, smoke test. Idempotent. |
| `status` | What is deployed, which services are up, last backup, last operation, anything needing attention. |
| `doctor` | Read-only diagnostics with a suggested remedy for every non-ok result. Exits 3 on failure. |
| `backup` / `restore` | Coordinates the release's backup and restore hooks, wraps the result in a self-describing manifest, verifies checksums. |
| `secret` | `list`, `set`, `generate`, `rotate`, `remove`, `render`, `recipients`. Values are never printed, never in argv, never journaled. |
| `release` | `list`, `show`, `verify`, `fetch`, `prune`. |

Global flags: `--json`, `--dry-run`, `--yes`, `--force`, `--timeout`,
`--verbose`, `--quiet`, `--log-format`, `--no-color`, `--plain`, `--resume`,
`--wait`.

### Exit codes

Stable, and depended on by systemd units and CI:

| Code | Meaning |
| --- | --- |
| 0 | success |
| 1 | internal or unexpected error |
| 2 | usage or input validation |
| 3 | preflight / precondition failed |
| 4 | lock held by another operation |
| 5 | installation missing or corrupted |
| 6 | secrets error |
| 7 | runtime error (docker / compose) |
| 8 | health check or smoke test failed |
| 9 | incompatible release |
| 10 | backup or restore failure |
| 11 | operation failed, compensation succeeded |
| 12 | manual intervention required |
| 130 | interrupted by signal |

Code 12 is the one that matters: it means an operation mutated something it
could not undo, and a human has to look. The systemd unit sets
`RestartPreventExitStatus=12` so it does not spin.

## Architecture

```text
cmd/morzer          main: signals, exit code
internal/cli        cobra commands, flag parsing, dependency wiring
internal/ui         plain and json presenters — subscribers, never participants
internal/lifecycle  operations as step sequences; preflight; the step engine
internal/domain     pure types and rules: manifest, release, installation, errors
internal/ports      interfaces, declared by the consumer
internal/adapters   compose · sops-age · hooks · dir · checksum · systemd · gotemplate
internal/infra      exec runner, atomicfs, lock, state, logging, tool registry
```

Dependencies point downward only. `internal/domain` imports nothing from this
repository and nothing beyond stdlib and semver. The lifecycle layer speaks only
to ports — the string `docker` appears nowhere above `internal/adapters`.
`internal/cli` is the single place adapters are named.

These rules are enforced mechanically by `depguard` in `.golangci.yml`, not by
discipline. Run `just lint`.

That enforcement is load-bearing rather than decorative: when the rule was first
widened to cover the whole lifecycle layer it immediately found three real
violations in `lifecycle/ops`, which was reaching for the hooks, sops-age and
systemd adapters directly. The hook ABI, the machine identity operations and
unit rendering now sit behind `HookRunner`, `SecretStore` and `Supervisor`.

Two deliberate departures from the spec's package sketch, both documented at the
top of the packages concerned:

- **`internal/events` sits beside `domain`, not under `lifecycle`.**
  `ports.Notifier` takes an `Event`; if the type lived inside the lifecycle
  layer, `ports` would have to import the layer that consumes it and the
  dependency arrows would stop pointing downward.
- **`internal/release` owns manifest parsing.** `domain` is restricted to stdlib
  and semver, so the YAML decoder cannot live there. Domain keeps the types and
  the validation rules; this package turns bytes into them.

### The step engine

Every mutating command is an Operation: an ID, an ordered list of steps, a
journal record. Each step has up to four functions:

```go
Check      func(ctx, *State) (done bool, err error)  // is it already done?
Execute    func(ctx, *State) error                   // do it
Verify     func(ctx, *State) error                   // did it take effect?
Compensate func(ctx, *State) error                   // undo it
```

`Check` is what makes `apply` idempotent — a satisfied postcondition marks the
step skipped. `Verify` is separate from `Execute` because a tool exiting zero is
not the same claim as the system being in the desired state. Every transition is
journaled *before and after* the work, so a crash mid-step is recoverable to a
known position.

On failure the engine compensates completed steps newest-first, including the
step that failed — it may have mutated before failing. A step that declares
`RequiresInterventionOnFailure` (migrations, restore) moves the operation to
`requires-manual-intervention` instead, which keeps surfacing in `status` and
`doctor` until an operator clears it explicitly.

That flag is explicit rather than inferred from a missing compensator: most
steps without one are simply read-only. Inferring it would flag every failed
health check for a human acknowledgement, which trains people to clear the flag
without looking — destroying the value of the one signal meant to stop them.

## Secrets

```text
/etc/<product>/secrets.sops.yaml     SOPS + age, encrypted at rest
        ↓
/run/<product>/secrets/*             tmpfs, 0700 dir, 0400 files
        ↓
/run/secrets/*                       inside the container
```

- Values never reach argv, logs, the journal, or `--json` output. The
  `domain.Secret` type's `String` and `LogValue` return `[redacted]`; a
  redacting `slog` handler and the exec runner's output scrubber are the second
  and third lines of defence.
- Rendered configuration in `/etc` contains *paths* to secrets, never values.
- The state is encrypted for at least two recipients: the machine and an offline
  recovery key. Removing the last recipient, or the machine's own, is refused.
- Rotation restarts only the services the release declares as depending on the
  secret — the difference between a blip and a full outage.

`sops` is subprocessed rather than imported: the library drags in the AWS, GCP
and Azure KMS SDKs for a deployment that only ever uses age. The port makes the
decision reversible.

## Release bundles

```text
release/
├── manifest.yaml       api_version: selfhost/v1alpha1
├── compose/            service topology
├── templates/          configuration + the secret schema
├── hooks/              migrate · smoke-test · backup · restore · check-db
└── VERSION
```

Rules the loader enforces:

- Unknown manifest fields are an error. A typo must not silently fall back to a
  default. Vendor keys live under `extensions.<namespace>`.
- Images are pinned by digest. A bare tag is rejected — an unpinned image makes
  a release mutable, and a mutable release makes rollback meaningless.
- Paths are release-relative and may not escape the root.
- A declared-but-missing hook is a validation error, and so is a hook without
  the executable bit. Both are knowable before the lock is taken.
- Errors report line and column from the source YAML.

A release is identified by `(name, version)` **plus** its content digest. The
same version appearing with a different digest is an error, not a warning.

See [`testdata/bundle/`](testdata/bundle/) for a complete, valid example — it is
what the test suite runs against.

## Hook ABI

Hooks are executables inside a release: the only way to add product-specific
logic without changing the manager.

- Working directory is the release root; hooks run only from a verified release.
- Environment is namespaced per product: `DEMO_OPERATION_ID`, `DEMO_DATA_DIR`,
  `DEMO_BACKUP_DIR`, `DEMO_SECRETS_DIR`, `DEMO_RESULT_FD`, and so on.
- Structured results are written as JSON to `DEMO_RESULT_FD` (3), keeping them
  out of stdout — which goes to the log and the live view.
- Exit `0` success, `2` nothing to do, anything else failure.
- Timeouts come from the manifest; expiry sends `SIGTERM` to the process
  *group*, then `SIGKILL`.

## Testing

```sh
just test        # everything
just contract    # the shared port contract suites
just test-race   # the bus and the engine under -race
just lint        # including the depguard layering rules
just check       # what CI runs: fmt-check, vet, test
```

| Level | What it covers |
| --- | --- |
| Unit | manifest validation, version and compatibility rules, scalars, redaction |
| Contract | one shared suite per port, run against **both** the fake and the real adapter |
| Fake-adapter integration | full `apply`, `backup`, `restore`, `doctor` runs — no Docker, no root, milliseconds |
| Fault injection | every step of an operation failed in turn; compensation order, journal state and exit codes asserted |

The contract suites are the load-bearing ones. `TestSecretStoreContract_SOPSAge`
runs the *same* tests as the fake against the real sops+age adapter, so a fake
that passes tests the real thing would fail cannot exist — which is what keeps
the fast integration tests honest.

The fault-injection suite is the one that matters most: the step engine's value
is entirely in what happens when something breaks.

## Design proposals

Work that has not shipped is designed in [`rfcs/`](rfcs/) before it is built,
one numbered document per piece with its decisions recorded and its exclusions
reasoned. [`rfcs/INDEX.md`](rfcs/INDEX.md) is the table of contents.

Currently proposed: update and rollback, the rich terminal renderer, secrets
recovery and onboarding, distribution with signature verification, continuous
integration, and a documentation site to replace most of this file.

## Changelog

See [CHANGELOG.md](CHANGELOG.md).

## Licence

MIT — see [LICENSE](LICENSE).
