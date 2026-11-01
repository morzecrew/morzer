# verify-checks · what `release verify` still passes

On branch `verify-checks` from `af4facd`, building Phase 2's first item of the
gap report: `release verify` catching what a deployment would break on. No RFC
covers this item; the area is
[RFC 0013](../rfcs/0013-bundle-authoring-experience.md)'s, whose decision table
is ungraded, so its rows are treated as `LOCKED`.

The item as written asked for `verify` to render by default, which is the flip
RFC 0013 decision 12 forbids. Ruled by the author on 2026-11-01 before any
code: decision 12 stands, and plain `verify` gains a check that reads each
template without rendering it; the rest is built with its decisions logged here
as rows proposed to RFC 0013. That covers:

- plain `verify` refusing a template that names a secret, a parameter or a field nothing declares;
- `verify` refusing a `<PRODUCT>_IMAGE_*` or `<PRODUCT>_PARAM_*` variable in a runtime file that the manifest does not declare;
- `verify` refusing a secret whose `services` names a service no runtime file defines;
- `verify` warning about the scaffold's zero digest and TODO description, and `build` refusing them.

**Drift count: 0.**

| Class | Test | Meaning |
|---|---|---|
| `discovery` | Could not have been known before code existed | Healthy — the spec was right to be silent |
| `spec-gap` | Could have been known; the spec was silent or at the wrong altitude | The design process missed something |
| `drift` | The spec covered it and it was built otherwise anyway | **A defect** |
| `irreducible` | No amount of design settles it | Stop and spike |

Decision 12 keeps rendering opt-in because a synthetic context invents values
and so exercises only the branches they choose. A check that renders nothing
invents nothing.

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-01T19:10:00Z
attempt: 1
claim: plain `verify` passes a template naming a secret or parameter the bundle does not declare, and RFC 0013 settles only parsing and rendering
evidence: rfcs/0013-bundle-authoring-experience.md:460
action: decided
proposal: ASSUMED — plain `verify`, and every command sharing its integrity checks, walks each template's parse tree in every branch and refuses a field chain from the root, or a `secretFile .Secrets "<name>"` literal, that resolves to nothing in the render context: an undeclared secret or parameter, or a field the context does not have. A chain whose dot `range`, `with` or `define` has moved is not checked, because its type is not known without rendering.
```

The image and parameter variables are the runtime's interpolation ABI, and
reading a runtime's files is the adapter's job.

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-01T19:11:00Z
attempt: 1
claim: checking a bundle's runtime files needs the adapter to read them, and `verify` runs where no runtime is installed
evidence: internal/ports/runtime.go:614
action: decided
proposal: ASSUMED — an optional port, `ports.BundleReader`, type-asserted as `ports.DefinitionDrifter` is, reads the services a bundle's runtime files define and the variables they interpolate, from the files alone. A runtime without it is not checked.
```

`<PRODUCT>_` is `metadata.name`'s prefix, and the fixture's own
`compose.external-db.yaml` reads `DEMO_DB_HOST`, a name in that namespace that
nothing sets.

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-01T19:12:00Z
attempt: 1
claim: which interpolated variables `verify` may call undeclared is not settled
evidence: testdata/bundle/compose/compose.external-db.yaml:6
action: decided
proposal: ASSUMED — only the two families the manifest names: `<PRODUCT>_IMAGE_<NAME>` and `<PRODUCT>_PARAM_<NAME>`, refused when no image or parameter maps to `<NAME>`. Another name falls back to its default on every machine, and is left alone.
```

A Compose file can take services from another file through `include`.

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-01T19:13:00Z
attempt: 1
claim: a runtime file that includes another defines services the reader does not see
evidence: internal/domain/secret.go:150
action: decided
proposal: ASSUMED — when a runtime file uses `include`, the reader reports its services as incomplete and `verify` does not check secrets' `services` for that bundle, rather than refusing a service it could not see.
```

RFC 0013 decision 9 has the scaffold write a placeholder version that `build`
refuses, and decision 11 has the scaffold pass `verify` unedited.

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-01T19:14:00Z
attempt: 1
claim: the scaffold's zero digest and TODO description pass `build`, and nothing says which command should stop them
evidence: internal/cli/release_new.go:203
action: decided
proposal: ASSUMED — `verify` warns about an image pinned to the all-zero digest and a `metadata.description` starting with `TODO`, and `build` refuses both, as it refuses 0.0.0. An unedited scaffold still verifies; it does not build until someone fills them in.
```

**Drift count: 0.**

## Found while building

Run against every bundle in the tree, the variable check refused the three-tier
example: its `external-db` profile reads `${WEB_PARAM_DB_HOST}`, and the
manifest declares no `db_host`, so an operator on that profile could never say
where their database is. `release verify` runs on that bundle in the acceptance
script, so the check would have failed it there.

```divergence
decision: UNLISTED
grade: UNLISTED
class: discovery
at: 2026-11-01T20:05:00Z
attempt: 1
claim: the three-tier example interpolates a parameter variable it never declares
evidence: testdata/bundle-web/compose/compose.external-db.yaml:6
action: decided
proposal: ASSUMED — the example declares `db_host`, a string defaulting to `db.internal` and restarting `backend`, which is what its profile already read.
```

**Drift count: 0.**

The `LOCKED` rows this branch touches, honored. RFC 0013 decision 12: plain
`verify` reads templates and renders none, and rendering still runs only under
the flag.

```divergence
decision: 12
grade: LOCKED
kind: resolved
at: 2026-11-01T20:06:00Z
attempt: 1
claim: `--render-check` stays opt-in, and nothing in plain `verify` renders
evidence: internal/cli/release.go:281
action: decided
```

RFC 0013 decision 11: the scaffold still verifies unedited, now with warnings
about what it leaves to fill in.

```divergence
decision: 11
grade: LOCKED
kind: resolved
at: 2026-11-01T20:07:00Z
attempt: 1
claim: the scaffold's output passes `verify --render-check` unedited
evidence: test/clitest/deprecation_test.go:80
action: decided
```

RFC 0023 decision 7: the runtime files are read through an optional
interface, and nothing above `internal/adapters` asks which runtime it is.

```divergence
decision: 7
grade: LOCKED
kind: resolved
at: 2026-11-01T20:08:00Z
attempt: 1
claim: the runtime-file checks branch on no runtime's name
evidence: internal/cli/release.go:478
action: decided
```

**Drift count: 0.**

## Corrected in review

Three of the entries above were decided too narrowly, and review showed it.
Each is replaced by the entry below it rather than edited.

The 19:13 entry skipped the services check whenever a file used `include`, and
read no variable from an included file, so an undeclared image variable there
passed. Compose follows a local `include` relative to the file that includes
it (measured with Compose 5.5.1), and the reader can too.

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-01T21:10:00Z
attempt: 2
claim: an included file's services and variables were not read, though a local include is a file inside the bundle like any other
evidence: internal/adapters/runtime/compose/bundle.go:67
action: decided
proposal: ASSUMED — an `include` is followed relative to the file that includes it while it stays inside the bundle, in the short and the long spelling, for services and variables both. One that leaves the bundle, such as a remote or absolute one, is not read, and only then is the services check skipped.
```

The 19:10 entry checked field chains and `secretFile`, and not
`index .Parameters "name"`. An index a map does not hold renders as nothing
rather than failing, which makes it the lookup most worth reading.

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-01T21:11:00Z
attempt: 2
claim: `index` of a root map with a literal key was not read, and a key the map does not hold renders empty
evidence: internal/adapters/render/gotemplate/references.go:108
action: decided
proposal: ASSUMED — `index <chain> "key"`, with the chain from the root context and the key a literal, is read as the chain followed by the key, so an undeclared parameter or secret named that way is refused like a field.
```

The 19:14 entry matched a description starting with `TODO`, which also refuses
a real one such as "TODOist".

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-01T21:12:00Z
attempt: 2
claim: a description that merely starts with the letters TODO is not the scaffold's placeholder
evidence: internal/cli/release_new.go:192
action: decided
proposal: ASSUMED — the placeholder is the scaffold's own opening, `TODO -- `, which the scaffold now writes from the same constant the check matches.
```

**Drift count: 0.**

Review then found the edges of both. `index` of a struct fails at render, and
`index (.Parameters) "key"` is the same lookup in parentheses. An include that
climbs out of the bundle with `../`, or whose path Compose interpolates, cannot
be read here either, and was refused as unreadable instead of left unknown.

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-01T21:40:00Z
attempt: 3
claim: `index` of a struct, and of a parenthesised root chain, were not read as the lookups they are
evidence: internal/adapters/render/gotemplate/references.go:107
action: decided
proposal: ASSUMED — `index` reads its receiver bare or in parentheses; a map must hold the key, and a struct is refused, since `index` cannot look up a field.
```

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-01T21:41:00Z
attempt: 3
claim: an include climbing out of the bundle, or naming a path Compose interpolates, was refused as unreadable
evidence: internal/adapters/runtime/compose/bundle.go:103
action: decided
proposal: ASSUMED — such an include is not read and leaves the services unknown, as a remote or absolute one does.
```

**Drift count: 0.**
