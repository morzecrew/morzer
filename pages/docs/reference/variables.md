---
title: Variables
icon: lucide/braces
summary: Everything a bundle may reference — Compose interpolation, the configuration-template render context, and where each is available
---

# Variables

A bundle references the manager from three places, and each has its own set of
names. This is the complete list of all three.

| Where you write it | Form | Filled by |
| --- | --- | --- |
| Compose files | `${<PRODUCT>_DATA_DIR}` | [Compose interpolation](#compose-interpolation) |
| Configuration templates | `{{ .Paths.Data }}` | [The render context](#the-render-context) |
| Hooks | `$<PRODUCT>_DATA_DIR` | [The hook ABI](hooks.md) |

`<PRODUCT>` is your release's `metadata.name`, upper-cased, with `-` and `.`
becoming `_`. For the example bundle, whose product is `demo`, `DATA_DIR` is
`DEMO_DATA_DIR`. It is the release's name whatever the installation is called:
a second installation of the same release, named `sandbox`, still reads
`DEMO_DATA_DIR`, while its paths are `/var/lib/sandbox` and so on.

All three are contracts. A rename breaks every bundle in the field, so
`just docs-check` fails the build when a name exists in the code and not on this
page — and when a name is on this page and not in the code.

## Compose interpolation

Your Compose files may interpolate these, and **nothing else**. The runtime
subprocess receives an allow-listed environment plus exactly this set, so a
variable that is not here resolves to its `:-` fallback or to empty.

| Variable | Meaning |
| --- | --- |
| `<PRODUCT>_DATA_DIR` | Persistent product data on the host. |
| `<PRODUCT>_SECRETS_DIR` | The tmpfs directory secrets are rendered into. |
| `<PRODUCT>_CONFIG_FILE` | The rendered configuration file. |
| `<PRODUCT>_RELEASE_DIR` | The unpacked release, for mounting files the bundle ships. |
| `<PRODUCT>_VERSION` | The release version. |
| `<PRODUCT>_PROFILE` | The active deployment profiles, comma-joined in layering order: one name for one, empty for none. |
| `<PRODUCT>_DOMAIN` | The canonical domain. **Absent** when the installation has none, so carry a `:-` fallback. |

Two families take their names from your manifest:

| Family | One per | Example |
| --- | --- | --- |
| `<PRODUCT>_IMAGE_<NAME>` | entry in `images` | `app` → `${DEMO_IMAGE_APP}` |
| `<PRODUCT>_PARAM_<NAME>` | entry in [`parameters`](parameters.md) | `http_port` → `${DEMO_PARAM_HTTP_PORT}` |

`-` and `.` in an image name become `_`: `web-ui` is `DEMO_IMAGE_WEB_UI`.
Parameter names are already restricted to lowercase, digits and underscores, so
they only upper-case.

A [text parameter](parameters.md#text-values) arrives whole, newlines
included. The manager gives Compose its variables in the process environment,
never through an env file, and Compose interpolates a multi-line value intact.

```yaml
services:
  app:
    image: ${DEMO_IMAGE_APP:-registry.example/demo/app@sha256:…}
    ports:
      - "${DEMO_PARAM_HTTP_PORT:-18080}:8080"
    volumes:
      - ${DEMO_CONFIG_FILE:-/etc/demo/application.yaml}:/etc/demo/application.yaml:ro
      - ${DEMO_DATA_DIR:-/var/lib/demo/data}:/var/lib/demo:rw
```

!!! tip "Mount a rendered file so Docker cannot invent it"

    With the short syntax above, a container that starts while its bind
    source is missing gets an empty **directory** created at that path, and the
    next render finds a directory where its file belongs. The long syntax with
    `create_host_path: false` makes Docker refuse to start the container
    instead:

    ```yaml
    volumes:
      - type: bind
        source: ${DEMO_CONFIG_FILE:-/etc/demo/application.yaml}
        target: /etc/demo/application.yaml
        read_only: true
        bind: {create_host_path: false}
    ```

    A first `apply` that fails takes down the containers it started, so
    they do not restart over the files its rollback removes. A render that
    finds a directory at a target says so, and names the `rmdir`.

!!! warning "Nothing from your shell reaches a Compose file"

    Setting `DEMO_ANYTHING` in the environment you run `morzer` from does not
    interpolate. The manager builds the runtime's environment from a fixed
    allow-list — `PATH`, `HOME`, `TMPDIR`, the `XDG_*` directories, Docker's own
    client configuration, `SSH_AUTH_SOCK` and the proxy variables — plus the
    table above.

    This is deliberate. The environment used to be inherited wholesale, which
    meant any product-prefixed variable in a shell silently interpolated:
    undocumented, unvalidated, unrecorded, and invisible to the manifest. A
    value an operator should be able to change is a
    [parameter](parameters.md).

**Secrets are absent from this list on purpose.** They reach containers as files
under `<PRODUCT>_SECRETS_DIR`, referenced by a Compose `secrets:` block, never
as environment — an environment variable is visible to anyone who can run
`docker inspect`.

## The render context

Configuration templates are Go templates. These are the top-level names
available:

| Field | Holds |
| --- | --- |
| `.Installation` | `.ID`, `.Product`, `.Profile`, `.Profiles`, `.Domains`, `.Domain` (the first), `.URL`. Its profile fields are the recorded selection, which a one-run `--profile` does not change. |
| `.Release` | `.Name`, `.Version`, `.Digest`, `.Root`, `.Vendor`. |
| `.Profile` | The active profile, as a string, empty for none. With several selected it fails the render: use `.Profiles`. |
| `.Profiles` | The active profiles, a list in layering order, empty for none. |
| `.Paths` | `.Etc`, `.Var`, `.Run`, `.Opt`, `.Data`, `.Backups`, `.Secrets`, `.Generated`. |
| `.Secrets` | Secret name → the **path** of its rendered file. Never a value. |
| `.Domains` | Every configured domain, first one canonical. |
| `.Parameters` | Every declared [parameter](parameters.md), holding the operator's value or the release's default. |

```yaml
server:
  url: {{ .Installation.URL | default "http://localhost:8080" }}
  http_port: {{ .Parameters.http_port }}
  domains:
{{- range .Domains }}
    - {{ . }}
{{- end }}

release:
  name: {{ .Release.Name }}
  version: {{ .Release.Version }}

paths:
  data: {{ .Paths.Data }}

secrets:
  db_password_file: {{ secretFile .Secrets "db_password" }}
```

`secretFile` is a helper that fails loudly when the named secret is not
declared, rather than rendering an empty path a service would then fail to open
with no explanation.

`has` tests whether a list holds a name, which is how a template asks about one
profile of a selection:

```yaml
{{- if has "xdns" .Profiles }}
dns_tunnel: enabled
{{- end }}
```

`.Profile` stops the render when several profiles are selected, because `eq
.Profile "xdns"` would otherwise be false under a selection that holds `xdns`.
Only a release that declares [`combinable`](manifest.md#combinable-profiles) can
have several selected, so its templates use `.Profiles`. `.Profiles` and `has`
need `compatibility.min_manager_version` of at least `0.15.0`.

### lines

`lines` splits a [text parameter](parameters.md#text-values) into its lines
for a `range`. An empty value is no lines:

```yaml
rules:
{{- range lines .Parameters.rules }}
  - {{ printf "%q" . }}
{{- else }} []
{{- end }}
```

The `else` branch renders `rules: []` for an empty value. Without it the key
would have no value, which YAML reads as null rather than as an empty list.
`printf "%q"` writes each line as a double-quoted string, so `a: b` stays a
string and `hello # note` keeps its comment. `quote` escapes `"` but not `\`,
so it is not safe for this.

A fragment pasted whole keeps its indentation with `indent`:

```yaml
extra:
{{ .Parameters.fragment | indent 2 }}
```

A product that reads a file rather than its own configuration gets one from a
template whose body is the value, `{{ .Parameters.rules }}`, rendered to a
target the Compose file mounts. `lines` needs
`compatibility.min_manager_version` of at least `0.15.0`, as `text` does.

!!! danger "`.Secrets` holds paths, never values"

    A configuration file in `/etc` must never contain a credential. The render
    context carries the *path* of each rendered secret so the product opens the
    file itself, and the acceptance run asserts that no secret value appears in
    the rendered configuration.

There is deliberately **no** access to the process environment. A template that
could read `os.Environ()` would be a second unvalidated channel of exactly the
kind the Compose allow-list closes — anything an operator should be able to
change is a [parameter](parameters.md).

## The hook ABI

Hooks receive the same product-namespaced variables plus the ones that only make
sense during an operation — the operation id, the phase, the previous version,
`DRY_RUN`. See [Hooks](hooks.md) for the full table and the result descriptor.

Parameters reach hooks too, under the same `<PRODUCT>_PARAM_<NAME>` names the
Compose files use, so a hook and a topology file refer to a port the same way.
