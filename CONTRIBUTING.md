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

## Tests

| Level | What it is for |
| --- | --- |
| Unit | domain rules — validation, compatibility, scalars |
| Contract | one shared suite per port, run against **both** the fake and the real adapter |
| Fake-adapter integration | whole operations, no Docker, no root, milliseconds |
| Fault injection | fail each step in turn; assert compensation and final state |

Two rules learned the hard way:

- **A new adapter passes the existing contract suite.** That is what stops a
  fake from passing tests the real implementation would fail.
- **Assert what the code *should* do, not what it does.** These tests are often
  the first time a function has ever run; transcribing current behaviour just
  freezes its bugs. The first run of the compatibility suite found two.

`just contract-strict` fails if a contract suite *skips*. A skipped
real-adapter suite means the production code went unexercised and the fake
carried the run alone.

## Coverage

`just coverage-gate` enforces a floor, currently 45%. It is a floor, not a
target: raise it deliberately, and if a change genuinely lowers it, lower
`COVERAGE_FLOOR` in the same pull request so the decision is reviewable.

Coverage is measured with `-coverpkg=./internal/...` so the integration suite
gets credit for the packages it exercises. Without that the total reads 8.7%
instead of 45%.

## Go version

The floor is whatever `go.mod` declares — currently 1.25.0, driven by
`golang.org/x/term`, `golang.org/x/sys` and `renameio/v2`. CI tests that floor
and `stable`. If you raise it, move `go.mod`, the CI matrix and the README's
install note together.
