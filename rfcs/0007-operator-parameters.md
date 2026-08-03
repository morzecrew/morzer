# RFC 0007 — Operator parameters

- **Status:** 📝 Draft — analysis complete, scope decisions open. Not scheduled.
- **Scope:** Gives a release a declared set of knobs an operator may set — ports
  above all — and gives the operator a supported way to set them. Covers the
  manifest's `parameters` block, typed validation, delivery to Compose,
  templates and hooks, a `morzer config` command group, and making
  `requirements.ports` and health-check URLs follow the values rather than
  contradict them. Also covers the second worked example (frontend, backend,
  database) that proves the mechanism, and the documentation and drift gate that
  follow from it. Explicitly **not** in scope: secrets, which already have their
  own declared, typed, audited path and must not gain a second one; per-service
  resource limits; anything that changes the Compose topology, which stays the
  vendor's.
- **Related:** [`internal/domain/installation.go`](../internal/domain/installation.go),
  [`internal/domain/manifest.go`](../internal/domain/manifest.go),
  [`internal/lifecycle/ops/ops.go`](../internal/lifecycle/ops/ops.go),
  [`internal/lifecycle/preflight/preflight.go`](../internal/lifecycle/preflight/preflight.go),
  [`internal/ports/render.go`](../internal/ports/render.go),
  [`testdata/bundle/`](../testdata/bundle/),
  RFC [0006](0006-documentation-site.md) for the docs gate this extends

---

## 1. Summary

A release declares the parameters it accepts; the operator sets them; the values
reach Compose, configuration templates and hooks; and the manifest's own port
and health-check declarations follow the values instead of contradicting them.

The request that prompted this was "let me change which ports are exposed".
Investigating it found that an operator cannot change **anything** after `init`,
that the file which says otherwise is never read, and that the one variable the
example bundle already parameterises breaks `apply` when you use it. This RFC is
therefore less "add a feature" than "finish three half-built ones and remove a
contradiction".

## 2. Motivation

Every finding below was verified against the code at `334ef29`, not recalled.

### 2.1 `installation.yaml` is written, backed up, and never read

`init` writes it ([`ops/init.go:279`](../internal/lifecycle/ops/init.go)),
`hookbackup` copies it into every backup, and its header says:

```yaml
# Managed by morzer. Edit with care.
# Values here override release defaults; see `morzer status --json` for effective values.
```

Nothing reads it back. `Paths.InstallationFile()` returns
`/etc/<product>/installation.yaml` and appears only at write sites;
`LoadInstallation` reads `Paths.InstallationState()`, which is
`<var>/manager/installation.json`. Demonstrated on a live installation:

```console
$ ./morzer --root tmp/demo status | grep -E 'profile|url'
  profile        embedded
  url            https://demo.example

$ sed -i 's/^profile: embedded/profile: external-db/; s/demo.example/edited.example/' \
      tmp/demo/etc/demo/installation.yaml

$ ./morzer --root tmp/demo status | grep -E 'profile|url'
  profile        embedded          # unchanged
  url            https://demo.example
```

The file is a report that claims to be a control. That is worse than not having
it: an operator who edits it and sees no effect has no way to tell whether the
edit was wrong, the file was wrong, or the manager is broken.

### 2.2 `Installation.Settings` is wired at one end only

[`installation.go:37`](../internal/domain/installation.go) declares
`Settings map[string]any`. It flows to `ports.TemplateData.Settings` and is
listed in the renderer's own error message as an available top-level field. But:

- no flag sets it — `ops.InitOptions.Settings` has no writer anywhere in
  `internal/cli`;
- it appears nowhere in `pages/docs/` or in `schemas/`;
- no test references it;
- it never reaches Compose, only templates.

So the only way to populate it is to hand-edit a file the manager does not read
(§2.1). It has never been usable.

### 2.3 Changing a port breaks `apply`, and the bundle already invites you to try

[`testdata/bundle/compose/compose.yaml`](../testdata/bundle/compose/compose.yaml)
publishes `"${DEMO_HTTP_PORT:-18080}:18080"`. Nothing in the manager sets
`DEMO_HTTP_PORT` — `runtimeConfig` builds a fixed six-variable map plus the
images ([`ops.go:268`](../internal/lifecycle/ops/ops.go)) — but the Compose
adapter passes `exec.BaseEnv(cfg.Env)`, which is `os.Environ()` plus those
overrides. So a value from the operator's shell reaches Compose by inheritance:

```console
$ DEMO_HTTP_PORT=19999 docker compose -f testdata/bundle/compose/compose.yaml config
    ports:
      - mode: ingress
        target: 18080
        published: "19999"
```

The manifest, meanwhile, is unmoved:

```yaml
requirements:
  ports: [18080]
health:
  checks:
    - {name: api, type: http, url: "http://127.0.0.1:18080/health/ready", timeout: 30s}
```

`HealthCheck.URL` is used verbatim by the health adapter; it is never templated.
So the published port becomes 19999, preflight checks that **18080** is free,
and the health check probes **18080** — and `apply` fails at step 9 of 11,
"wait for health checks", on a deployment that is working.

The port-conflict remedy compounds it:

```go
return Fail(
    "stop whatever is listening (`ss -tlnp`), or change the release's port mapping",
    "port %s already in use", ...)
```

There is no mechanism to change the release's port mapping. This is the same
shape as the cross-installation restore bug RFC 0003 fixed: a hint naming an
action the tool does not offer.

### 2.4 Compose interpolation is a second ABI, and it is ungated

Two variable sets exist. The hook ABI is generated by `ports.HookEnvVars` and
**is** gated — `tools/docscheck` calls that function and fails the build when a
variable is undocumented (RFC 0006 §5.5). The Compose interpolation set is built
separately in `runtimeConfig` and nothing reads it:

| Variable | Documented |
| --- | --- |
| `<PRODUCT>_DATA_DIR`, `_SECRETS_DIR`, `_CONFIG_FILE`, `_RELEASE_DIR` | yes, as hook variables |
| `<PRODUCT>_IMAGE_<NAME>` | yes, in `authoring/your-first-bundle.md` |
| `<PRODUCT>_VERSION` | **no** |
| `<PRODUCT>_PROFILE` | **no** |
| `<PRODUCT>_DOMAIN` | **no** |

A bundle author writing a Compose file has no complete list of what they may
interpolate, and adding a variable cannot fail the build.

### 2.5 One example, two tiers

`testdata/bundle/` and `testdata/bundle-1.3.0/` are the same product: `app` plus
`db`, in `embedded` and `external-db` profiles. Every code sample on the
documentation site is extracted from it, which is RFC 0006's rule and a good
one. But it means the site has never shown a bundle with a separate frontend, a
second port, per-tier credentials, or a service that depends on two others —
which is what the question "how do I expose ports for frontend and backend"
actually asks about.

## 3. Current state

What an operator can influence today, in full:

| Thing | When | How |
| --- | --- | --- |
| Product, profile, domains, recovery recipient, signing keys | `init` only | flags |
| Profile, for one run | `apply`, `update` | `--profile` |
| Retention, signature policy, backup-before-update | never after `init` | — |
| Ports, log levels, feature flags, anything else | never | — |

`--root` relocates every managed path, but it is hidden and exists for the demo
and the tests.

There is no `morzer config`. `init` refuses to run over an existing
installation, so re-running it is not an escape hatch. `installation import`
rebuilds from an export, not from edited intent.

## 4. Goals / Non-goals

**Goals**

- A release declares which parameters it accepts, with types and defaults, so an
  operator setting one gets a typed error rather than a broken deployment.
- Values reach Compose, templates and hooks by one documented mechanism.
- `requirements.ports` and health-check URLs follow the values.
- Setting a parameter is an operation: planned, validated, journaled, and it
  restarts exactly the services the release says depend on it.
- One worked example with three tiers and per-tier credentials, exercised by the
  acceptance run so it cannot rot.

**Non-goals**

- **Parameters are not secrets.** They are visible in `docker inspect`, in
  `status --json`, and in the journal. Secrets keep their own declared, typed,
  audited, tmpfs-rendered path, and nothing here may become a second way to
  deliver one.
- **Not a Compose editor.** Topology is the vendor's. A parameter substitutes a
  value into a file the vendor wrote; it never adds a service, a volume or a
  network.
- **Not arbitrary key-value.** A free-form map that reaches Compose is a typo
  surface with no failure mode: `htpp_port: 9000` would be accepted and ignored.
- **No expression language.** Restricted Go templates over a fixed context, or
  nothing.

## 5. Design

### 5.1 The manifest declares the knobs

```yaml
parameters:
  http_port:
    type: port
    default: 18080
    description: Port the web interface is published on
    services: [frontend]
  api_port:
    type: port
    default: 18081
    description: Port the API is published on
    services: [backend]
  log_level:
    type: enum
    values: [debug, info, warn, error]
    default: info
    services: [backend]
  max_upload:
    type: bytes
    default: 25MiB
    services: [backend]
```

`type` is one of `port`, `int`, `bool`, `string`, `enum`, `duration`, `bytes`.
`duration` and `bytes` are `domain.Duration` and `domain.ByteSize` from
[`domain/scalars.go`](../internal/domain/scalars.go), which already parse and
validate these for the manifest — so `25MiB` means here exactly what it means in
`requirements.disk`. The other five are new, and each is a few lines; the point
of listing them is that the set is closed, not that it is free.

`services` is what to restart when the value changes, and is the same field
`secrets` already uses for rotation. Empty means the change needs a full
`apply`, which is stated rather than guessed.

The manifest decoder is strict (`yaml.DisallowUnknownField`), so a bundle using
`parameters` is refused outright by an older manager. That is the correct
failure and the reason `compatibility.min_manager_version` exists; the authoring
docs must say to set it.

### 5.2 The installation records the operator's choices

```yaml
parameters:
  http_port: "9000"
  log_level: debug
```

Stored as strings and parsed against the release's declaration on load, so the
recorded value is exactly what the operator typed and a type change in a later
release surfaces as a validation error rather than a silent reinterpretation.

This replaces `Installation.Settings`, which is removed. It has never been
settable, documented, schema'd or tested (§2.2), so nothing can depend on it.
`installation.json` is decoded with `encoding/json`, which ignores unknown
fields, so an older manager reading a newer state file does not fail — it
silently uses defaults. That is a real hazard and is why the schema version is
bumped: see decision 9.

### 5.3 Delivery

One namespace, reserved:

| Consumer | Form |
| --- | --- |
| Compose | `<PRODUCT>_PARAM_<NAME>` |
| Templates | `.Parameters.<name>` |
| Hooks | `<PRODUCT>_PARAM_<NAME>` |

`PARAM_` rather than bare `<PRODUCT>_<NAME>` — which is what the fixture's
`${DEMO_HTTP_PORT}` currently implies — because the flat form lets a parameter
named `data_dir` silently repoint `<PRODUCT>_DATA_DIR` and take the deployment's
storage with it. The fixture changes to `${DEMO_PARAM_HTTP_PORT:-18080}`.

Every declared parameter is exported, defaulted when unset, so a Compose file's
`:-` fallback is belt-and-braces rather than the actual source of the value.

`runtimeConfig` must also stop passing the parent environment through. Today
`exec.BaseEnv` merges `os.Environ()`, which is how §2.3's shell variable reaches
Compose at all. Once parameters are a declared surface, an undeclared
`DEMO_ANYTHING` reaching Compose is an unaudited back door — and the one that
currently produces a deployment the health check cannot see.

### 5.4 Making the manifest follow the values

The two fields that contradict a changed port (§2.3) gain access to a restricted
template context — `.Parameters` and nothing else:

```yaml
requirements:
  ports: ["{{ .Parameters.http_port }}", "{{ .Parameters.api_port }}"]

health:
  checks:
    - name: api
      type: http
      url: "http://127.0.0.1:{{ .Parameters.api_port }}/health/ready"
```

`ports` becomes `[]string` and is templated then coerced to int, so one
mechanism covers both fields. A literal `18080` still parses, so every existing
manifest keeps working.

The context is `.Parameters` only — not paths, not secrets, not the
installation. A manifest is read before an installation is necessarily
resolvable, and a health URL that could interpolate a secret path is a health
URL that can leak one into a log line.

### 5.5 `morzer config`

```sh
morzer config list                 # declared parameters, defaults, effective values, source
morzer config get http_port
morzer config set http_port=9000   # validates, plans, journals, restarts `services`
morzer config unset http_port      # back to the release default
```

`set` is an engine operation, not a file edit: it takes the lock, validates
against the release, shows a plan under `--dry-run`, journals before and after,
and restarts the declared services through the machinery `secret rotate`
already uses. A value the release does not declare is refused by name, with the
declared set in the hint.

`init --set http_port=9000` fills the same store, so a provisioning script sets
everything in one command and the wizard can print it in its equivalent-command
line.

### 5.6 Fixing `installation.yaml`

`config set` writes both the state and `installation.yaml`, so the file stays
accurate. That leaves §2.1's real defect — the file claims to be a control —
answered two ways, and this RFC picks the second:

1. **Make it authoritative.** `LoadInstallation` reads the YAML; the JSON
   becomes a cache. Matches what the header promises and what operators expect.
   Costs: two decoders for one type, a strictness decision, and a hand-editable
   file that is also restored from backups — an operator editing it during a
   restore gets a conflict the manager has no way to resolve.
2. **Make it honest.** The header changes to say it is a report, `config` is the
   supported editor, and `doctor` gains a check that fails when the file and the
   state disagree — which is exactly the "I edited it and nothing happened"
   case, caught and named.

Option 2, because the value of hand-editing is convenience and the cost is a
second source of truth for a tool whose entire job is careful state
transitions. Recorded as decision 8 with option 1 rejected rather than
unconsidered.

### 5.7 The three-tier example

A new `testdata/bundle-web/`: `frontend` (publishes `http_port`), `backend`
(publishes `api_port`, holds `db_password` and `session_key`), `postgres`
(holds `db_password`). Profiles `embedded` and `external-db`. Parameters
`http_port`, `api_port`, `log_level`.

It earns its keep only if it runs: the acceptance script gains a stage that
installs it, sets `http_port` to something other than the default, applies, and
asserts the service is reachable **on the port that was set**. That single
assertion is what §2.3 could not pass, and it is the reason this example exists
rather than being prose.

Cost: roughly +20s of acceptance time and a third fixture to keep valid. The
alternative — an example bundle nothing runs — is the thing RFC 0006 decided
against for exactly this reason.

## 6. Tests

- **Unit**: parameter type validation per type, including the coercions that
  should fail (`port: 70000`, `enum` outside `values`, `bytes: "lots"`).
- **Unit**: an undeclared parameter is refused, naming the declared set.
- **Unit**: a parameter named `data_dir` cannot collide with `<PRODUCT>_DATA_DIR`
  — the namespacing decision, asserted rather than assumed.
- **Contract**: the Compose adapter receives exactly the declared parameters and
  no inherited environment, which is the §5.3 back door closed.
- **Integration**: `config set` restarts the declared services and only those.
- **Integration**: a changed `http_port` moves `requirements.ports` and the
  health URL together — the §2.3 incoherence, as a regression test.
- **Acceptance**: the three-tier bundle applied on a non-default port and probed
  on it.
- **Parity**: `config list` output in plain, rich and JSON, per RFC 0002
  decision 3.

## 7. Docs

- `reference/parameters.md` — the declaration syntax, the types, the delivery
  namespaces, and the statement that parameters are not secrets.
- `reference/compose-variables.md` — the full interpolation set (§2.4), gated:
  `tools/docscheck` learns to read `runtimeConfig`'s key set the way it already
  reads `ports.HookEnvVars`, so an undocumented variable fails the build.
- `reference/commands.md` — the `config` group.
- `authoring/parameters.md` — choosing what to expose, and why a port is a
  parameter while a topology is not.
- `operating/changing-configuration.md` — the operator's task page.
- The three-tier bundle becomes the source for a second worked example.

## 8. Out of scope

- **Per-service CPU and memory limits.** Expressible as parameters by any vendor
  who wants them; giving them dedicated manifest fields would mean the manager
  models resource management, which is Compose's job.
- **Parameters that change between profiles.** A parameter is one value per
  installation. Per-profile defaults are a second dimension for a case nobody
  has raised; a vendor needing it ships two profiles today.
- **Reading parameters from a remote source.** Consul, etcd, an HTTP endpoint —
  all make `apply` depend on a network service that can be down, in a tool whose
  point is that a machine comes back up unattended.
- **Migrating parameter values across releases.** If 2.0 renames `http_port` to
  `listen_port`, the operator is told the old name is undeclared. Automatic
  renaming would need a mapping in the manifest and a migration story for it;
  worth revisiting when a vendor actually renames one.

## 9. Risks

- **The Compose environment change is a breaking change for someone.** Closing
  the inherited-environment path (§5.3) is correct and is also the only thing
  making §2.3's workaround function. Anyone relying on it — undocumented, but
  discoverable — loses it. Mitigation: `doctor` warns when a `<PRODUCT>_*`
  variable is set in the environment and is not a declared parameter, so the
  reliance is named rather than silently broken.
- **Parameters become a secret channel anyway.** A vendor will declare
  `db_password` as a `string` parameter, because it is easier than the secret
  schema. Nothing detects this today: the redactor scrubs registered *values*
  and has no notion of a sensitive *name*. Mitigation is therefore weak and
  should be stated as such — the docs say plainly where a parameter's value ends
  up (`docker inspect`, `status --json`, the journal), and `release verify`
  warns when a parameter name looks like a credential. A name check is a
  heuristic and will both miss and misfire; it makes the mistake visible, not
  impossible.
- **Templating manifest fields is a slope.** Once `ports` and `url` interpolate,
  the next request is images, then commands, then hook arguments — and a
  manifest that is a program cannot be validated statically. Mitigation:
  decision 5 names the two fields; adding a third is an RFC.
- **Scope.** This is five workstreams. P1 alone is useful and P5 is the
  expensive one; the phasing is ordered so stopping early leaves something
  coherent rather than half a mechanism.

## 10. Decisions

| # | Decision |
| --- | --- |
| 1 | A release declares its parameters; an operator cannot set one that is not declared. A free-form map has no failure mode for a typo, and `htpp_port: 9000` would be accepted and ignored. |
| 2 | Parameters are typed, from a closed set. `duration` and `bytes` reuse `domain.Duration` and `domain.ByteSize`, so a size means the same thing in a parameter as in `requirements.disk`; the rest are new and small. |
| 3 | Parameters are **not** secrets and the docs say so in those words. Secrets keep their declared, audited, tmpfs-rendered path; nothing here becomes a second way to deliver one. |
| 4 | Values reach Compose and hooks as `<PRODUCT>_PARAM_<NAME>`, namespaced. The flat form lets a parameter named `data_dir` repoint the deployment's storage. |
| 5 | Exactly two manifest fields interpolate parameters: `requirements.ports` and `health.checks[].url`. They are the two that contradict a changed port. A third is a new RFC. |
| 6 | The interpolation context is `.Parameters` and nothing else. A health URL that could reach a secret path is one that can leak it into a log line. |
| 7 | `config set` is an engine operation — locked, planned, journaled, restarting only the declared services — not a file edit. |
| 8 | `installation.yaml` stays a report; `config` is the supported editor and `doctor` fails when the two disagree. Making the YAML authoritative was considered and rejected: it gives a tool whose job is careful state transitions two sources of truth, and an operator editing it mid-restore a conflict nothing can resolve. |
| 9 | `Installation.Settings` is removed rather than deprecated — it has never been settable, documented, schema'd or tested — and `InstallationSchemaVersion` is bumped, because `encoding/json` would otherwise let an older manager read a newer state file and silently apply defaults. |
| 10 | The Compose subprocess stops inheriting the parent environment. Once parameters are a declared surface, an undeclared `<PRODUCT>_*` reaching Compose is an unaudited back door — and it is the one that currently produces a deployment the health check cannot see. |
| 11 | The three-tier example is exercised by the acceptance run or it is not added. An example nothing runs is prose that rots, which RFC 0006 already decided. |

## 11. Phasing

- **P1** — `parameters` in the manifest and the installation, typed and
  validated, delivered to templates and Compose. `init --set`. Removes
  `Settings`. *Useful alone: a vendor can parameterise a Compose file and an
  operator can set it at install time.*
- **P2** — `requirements.ports` and health URLs follow the values; the Compose
  environment stops being inherited. *This is the phase that closes §2.3, and
  the one with the regression test that matters most.*
- **P3** — `morzer config list/get/set/unset`, and the `doctor` check for
  `installation.yaml` drift.
- **P4** — the three-tier `testdata/bundle-web/` and its acceptance stage.
- **P5** — documentation: the two reference pages, the authoring and operating
  pages, and extending `docs-check` to gate the Compose variable set.

P1 and P2 are one pull request in practice: shipping P1 without P2 means an
operator can set a port and still break `apply`, which is the current state with
extra steps.

## 12. Open questions

1. **Does `--set` belong on `apply` and `update` too?** A per-run override is
   useful for testing and is also a way to run a deployment whose recorded state
   does not describe it. Leaning no, on the same grounds as decision 7.
2. **Should `config set` refuse when the release is not applied?** Setting a
   parameter that only takes effect on the next `apply` is either a convenience
   or a trap, depending on whether `status` says so. Leaning: allow it, and make
   `status` name the pending change.
3. **How does the parameter set change across an update?** A release that adds a
   parameter is fine — it defaults. A release that drops one leaves a recorded
   value with nothing to bind to. Refuse the update, warn, or silently drop?
   Leaning warn, on the grounds that a dropped parameter is the vendor's
   decision and blocking an update over it helps nobody.
