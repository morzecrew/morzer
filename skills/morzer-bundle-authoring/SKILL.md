---
name: morzer-bundle-authoring
description: Use when writing or changing a morzer release bundle — manifest.yaml, its Compose files, configuration templates, the secret schema, hooks, tasks or backup declarations — or when `morzer release verify` refuses one. Not for operating an installed product, and not for changing morzer itself.
---

# Authoring a morzer release bundle

A release bundle is one version of a product, described completely enough that
morzer can install, configure, update, back up and roll it back on a machine
the vendor never sees. The manager owns ordering, locking, journaling, backups
and secret storage; the bundle declares what the product is and supplies the
product-specific logic. Nothing product-specific belongs in the manager, and
nothing the manager already does belongs in a hook.

The documentation is the authority, and this skill routes to it rather than
restating it. Read the page a step points at before writing that part.

## The loop

Start from a skeleton that already verifies, and keep it verifying:

```sh
morzer release new ./my-product --vendor example
morzer release verify ./my-product
morzer release verify ./my-product --render-check
```

`release verify` reports every violation in one pass, with the line and column,
and needs no installation. `--render-check` renders each template against
invented values, which catches a misspelt secret or parameter name that parsing
cannot. **Run both after every change, and do not call a bundle finished until
both pass.** A bundle that verifies can still be wrong in ways only a deployment
shows (below), so a passing verify is the floor, not the proof.

The manifest's first line, the `# yaml-language-server: $schema=…` modeline the
skeleton writes, gives an editor completion and validation against the schema.
Keep it.

When it is ready to ship: `morzer release build` writes `SHA256SUMS` and
re-verifies, `minisign -Sm ./my-product/SHA256SUMS` signs it, and
`morzer release archive` packs it. A bundle that carries its own images runs
`morzer release pack` before `build`.

## Decisions that are easy to get wrong

Each of these passes verify when done wrong, or fails it with a message that
does not say why the rule exists.

- **`metadata.name` is permanent.** It becomes `/etc/<name>`, `/var/lib/<name>`
  and the `<NAME>_` prefix of every variable a hook and a Compose file read —
  `DEMO_` for the product `demo` the examples below use. Lowercase, starting
  with a letter.
- **`runtimes.compose.options.project` never changes after a release ships.**
  Every container, network and volume of a deployment lives under it; a new
  value orphans the old ones.
- **Images are pinned by digest**, and the Compose file reads them from the
  manager: `image: ${DEMO_IMAGE_APP:-…}` for an `images:` key `app`. Writing the
  digest only in Compose leaves the manifest, which the manager verifies,
  describing something else.
- **Secrets are files, never environment variables.** Declare each in the secret
  schema; give it a `generator` whenever the manager may create the value. A
  service reads it from the Compose `secrets:` file under
  `${DEMO_SECRETS_DIR}`, and a template refers to its path with
  `{{ secretFile .Secrets "name" }}`, never its value. An image that does not run
  as root needs the secret's `owner`.
- **Parameters are not secrets.** They are visible in `docker inspect`, the
  journal and `installation.yaml`. They reach Compose and hooks as
  `<NAME>_PARAM_<PARAM>` and templates as `.Parameters.<param>`; list the
  `services` a change must restart.
- **Hooks are executables in the bundle**, resolved against the release root.
  Read every variable with `${DEMO_DATA_DIR:?}` so an empty one fails loudly.
  Exit `0` for work done, `2` for nothing to do, anything else for failure, and
  write the structured result to file descriptor 3, not stdout. Make every hook
  idempotent: `apply` re-runs them.
- **A migration reports the schema it leaves.** A migrate hook writes
  `schema_version` to its result; a migration run as a `runtime-service`
  container cannot, so the manifest declares
  `compatibility.database_schema_produces`. Without either, rollback and
  update skip their schema checks. Unattended updates install only a release
  that declares `database_schema_produces`.
- **Compatibility is a promise the manager enforces.** `upgrade_from` says what
  this release may replace, `database_schema_min`/`max` what it can read,
  `rollback_safe` whether going back is safe — `false` for one-way migrations.
  `min_manager_version` is the oldest manager that may install the release;
  for tasks, several migrate steps and a secret's `owner`, verify refuses one
  that is too old and names the version to declare.
- **The smoke test checks behaviour, not liveness**: a missing rendered config
  or an unrendered secret, not that a process started. It fails the operation.
- **Volumes are backed up cold unless declared otherwise.** Declare `hot` only
  for data a running copy cannot corrupt, and `exclude` for a database volume
  the backup hook already dumps. A bind mount is never backed up.
- **A task an operator may only run in a sandbox lists `modes: [dev]`**; that is
  enforced, not advisory. Its arguments are on a world-readable command line, so
  a task never takes a credential as one.

## What a passing verify cannot tell you

- Whether an `http` health check can reach the service: it runs from the host,
  so the port must be published.
- Whether a migration is idempotent, or a smoke test meaningful.
- Whether the declared requirements are honest. Too loose is a support ticket,
  too tight an installation that refuses for no reason.

The example bundle in the morzer repository, `testdata/bundle`, is installed,
updated, backed up, restored and rolled back against real Docker on every CI
run. When in doubt about a shape, copy it from there.

## Where the details are

| Writing | Read |
| --- | --- |
| The first manifest, templates, secret schema | [Your first bundle](https://morzecrew.github.io/morzer/latest/authoring/your-first-bundle/) |
| Several services, parameters, profiles | [A three-tier bundle](https://morzecrew.github.io/morzer/latest/authoring/a-three-tier-bundle/) |
| Hooks | [Writing hooks](https://morzecrew.github.io/morzer/latest/authoring/hooks/) and the [hook ABI](https://morzecrew.github.io/morzer/latest/reference/hooks/) |
| Volume backup declarations | [Declaring volume consistency](https://morzecrew.github.io/morzer/latest/authoring/backing-up-volumes/) |
| Images inside the bundle | [Bundled images](https://morzecrew.github.io/morzer/latest/authoring/bundled-images/) |
| Building, signing, publishing | [Signing and publishing](https://morzecrew.github.io/morzer/latest/authoring/publishing/) |
| Any field | [Release manifest](https://morzecrew.github.io/morzer/latest/reference/manifest/), including [tasks](https://morzecrew.github.io/morzer/latest/reference/manifest/#tasks) and [the secret schema](https://morzecrew.github.io/morzer/latest/reference/manifest/#the-secret-schema) |
| What verify checks | [`morzer release`](https://morzecrew.github.io/morzer/latest/reference/release-commands/#release-verify) |
