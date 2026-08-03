# Contributing

## The loop

```sh
just ci          # exactly what CI runs; if this passes, CI passes
```

`just ci` needs `golangci-lint` and `sops` installed. `just check` is the
lighter version — formatting, vet and tests — for when you have neither.

```sh
just             # list every recipe
just test -run TestUpdate -v    # pass-through args
just demo        # a throwaway installation under ./tmp, touching nothing real
```

CI invokes the same `just` recipes rather than restating commands in YAML, so a
failure there is reproducible here.

## Before a large change: write an RFC

Anything that adds a command, changes a contract, or spans more than a couple of
packages gets a design document in [`rfcs/`](rfcs/) first. See
[`rfcs/INDEX.md`](rfcs/INDEX.md) for the index and the next free number.

RFCs exist so decisions survive context loss and rejected alternatives stay
rejected. Ground "Current state" in the code — an RFC that argues from memory is
a fiction with headings, and we have already had one claim a dependency floor
that was wrong.

Small fixes do not need one.

## Commits

`<gitmoji> <type>(<scope>): <description>` — for example:

```
✨ feat(update): install a new release over the current one
🐛 fix(secrets): stop generation hanging on power-of-two alphabets
📝 docs: add CHANGELOG in Keep a Changelog format
```

Imperative, one line, no trailing period, ideally under 72 characters. The body
takes at most four bullets. Explain *why* in the body; the diff already says
what.

## The changelog

User-facing changes go in [`CHANGELOG.md`](CHANGELOG.md) under `Unreleased`, in
Keep a Changelog format. Tests, CI, lint configuration and build tooling do not
— if it does not change how the tool is used, leave it out.

## Documentation

The site lives in [`pages/`](pages/) and is published to
<https://morzecrew.github.io/morzer/>. It is split by audience and by kind:

| You changed | It goes in |
| --- | --- |
| A command, a flag, an exit code | `pages/docs/reference/` |
| A manifest field, the hook ABI | `pages/docs/reference/manifest.md` or `hooks.md` |
| How an operator does a task | `pages/docs/operating/` |
| How a vendor builds a bundle | `pages/docs/authoring/` |
| Why the system is shaped this way | `pages/docs/explanation/` |
| A design that has not shipped | [`rfcs/`](rfcs/), not the site |

```sh
just docs-check  # the drift gate; part of `just ci`
just serve-docs  # live reload on localhost:8046
just build-docs  # build into pages/site
```

**`just docs-check` fails until the new surface is documented.** It reads the
command tree, the manifest and secret schemas, the error and exit codes and the
hook ABI out of the source, so a new flag or a new manifest field is a build
failure until some page names it. It also fails on a broken relative link and on
a page missing from the nav in `pages/zensical.toml`.

A mention counts only in a page's own prose. Fenced code blocks are stripped
before matching, so an example that happens to contain a new field is not
documentation of it. Adding a page means adding it to the nav in the same
change.

## The testing levels

| Level | What it covers |
| --- | --- |
| Unit | manifest validation, version and compatibility rules, scalars, redaction |
| Contract | one shared suite per port, run against **both** the fake and the real adapter |
| Fake-adapter integration | full `apply`, `update`, `rollback`, `backup`, `restore`, `doctor` runs — no Docker, no root, milliseconds |
| Fault injection | every step of an operation failed in turn; compensation order, journal state and exit codes asserted |
| Acceptance | the whole lifecycle against real Docker, real Compose, a real registry and real sops — about forty seconds |

```sh
just test          # everything
just contract      # the shared port contract suites
just contract-strict  # and fail if any of them skipped
just test-race     # the bus and the engine under -race
just acceptance    # real Docker; needs docker, sops and jq
```

**The contract suites are the load-bearing ones.**
`TestSecretStoreContract_SOPSAge` runs the *same* tests as the fake against the
real sops+age adapter, so a fake that passes tests the real thing would fail
cannot exist — which is what keeps the fast integration tests honest.

`just contract-strict` fails when one of them *skips*. A skipped real-adapter
suite means the fake carried the run alone and CI was greener than the code.

**The acceptance run is what fakes cannot answer.** It found, on its first
execution, that the manifest's digest-pinned images decided nothing at all: the
pull ignored the list it was given and the Compose file's image references were
never substituted. Every fake-backed test had passed throughout.

Two rules learned the hard way:

- **A new adapter passes the existing contract suite.** That is what stops a
  fake from passing tests the real implementation would fail.
- **Assert what the code *should* do, not what it does.** These tests are often
  the first time a function has ever run, and transcribing current behaviour
  just freezes its bugs. The first run of the compatibility suite found two.

## Architecture rules, and how they are enforced

```text
cli -> ui -> lifecycle -> ports <- adapters
                  \-> domain <- everything
```

- `internal/domain` imports nothing from this repository, and nothing beyond
  stdlib and semver.
- The lifecycle layer speaks only to ports. The string `docker` appears nowhere
  above `internal/adapters`.
- `internal/cli` is the single place adapters are named.

`depguard` in [`.golangci.yml`](.golangci.yml) enforces these mechanically. When
it objects, the boundary is usually wrong — add a port method or an optional
capability interface rather than widening the rule. It has already caught three
real violations that had been described as compliant.

## Coverage

`just coverage-gate` enforces a floor, currently 50%. It is a floor, not a
target: raise it deliberately, and if a change genuinely lowers it, lower
`COVERAGE_FLOOR` in `.github/workflows/ci.yml` in the same pull request, so the
decision is reviewable rather than silent.

Coverage is measured with `-coverpkg=./internal/...` so the integration suite
gets credit for the packages it exercises. Without that the total read 8.7%
instead of 45% when the floor was first set — it was measuring where tests live
rather than what they cover.

## Go version

The floor is whatever `go.mod` declares — currently 1.25.0, driven by
`golang.org/x/term`, `golang.org/x/sys` and `renameio/v2`. CI tests that floor
and `stable`. If you raise it, move `go.mod`, the CI matrix and
`pages/docs/get-started/installation.md` together.
