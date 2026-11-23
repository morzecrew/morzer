# https-credentials · RFC 0038 P1, stored credentials for HTTPS sources

On branch `https-credentials` from `2b142d74`, building
[RFC 0038](../rfcs/0038-credentials-for-https-bundle-sources.md) P1 as ruled
by the author on 2026-11-23 (decisions 10 to 17, recorded in #416): the
`sources.https` entries in installation state, the HTTPS source attaching a
configured credential per request, `release source add|remove|ls`, `init`'s
credential flags, the userinfo warning, the `doctor` check, `describe`, the
export, and the docs.

**Drift count: 0.**

| Class | Test | Meaning |
|---|---|---|
| `discovery` | Could not have been known before code existed | Healthy — the spec was right to be silent |
| `spec-gap` | Could have been known; the spec was silent or at the wrong altitude | The design process missed something |
| `drift` | The spec covered it and it was built otherwise anyway | **A defect** |
| `irreducible` | No amount of design settles it | Stop and spike |

Found while planning, before any code.

A sandbox imported from a production export drops the settings that would
reach production's services: its notification targets, backup targets and
sync. The RFC does not say which side `sources.https` falls on, and
`TestTheSandboxClassificationIsComplete` requires every field to be classed.

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-23T16:05:00Z
attempt: 1
claim: decision 9 carries the entries in an export without saying whether a sandbox imported from it keeps them, and the sandbox classification needs every field classed
evidence: internal/domain/sandbox_test.go:115-126
action: decided
proposal: ASSUMED — a sandbox keeps the `sources.https` entries. Fetching a release from the vendor writes nothing into production's services, which is what the dropped fields have in common, and the secrets travel in the export as every secret does.
```

Decision 11 journals each change of the entries. Neither command it names as a
model journals anything, so the record's shape is unsettled.

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-23T16:05:00Z
attempt: 1
claim: decision 11 journals each change without saying as what, and `backup target add` and `release trust add` only take the lock and write the state
evidence: internal/lifecycle/ops/trust.go:47-78
action: decided
proposal: ASSUMED — refines decision 11. `release source add` and `remove` append a succeeded `config` operation under the deployment lock, before the state is written, as `--forget-index` journals before it forgets (RFC 0037 decision 42). Its flags name the change (`source_add` or `source_remove`), the authority, the scheme and the secret's name. A plan journals nothing. Adding an entry that is already there as given changes nothing and journals nothing.
```

Decision 7 matches on the authority, with the port defaulted to 443, so an
entry has to be stored in one form for two spellings of one authority to be
one entry.

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-23T16:05:00Z
attempt: 1
claim: decision 7 compares authorities after defaulting the port, without saying how an entry is stored or what `add` does with a second spelling of one authority
evidence: rfcs/0038-credentials-for-https-bundle-sources.md:138-151
action: decided
proposal: ASSUMED — an entry stores its authority lower-cased, without the port when it is 443 and with it otherwise, so `https://Downloads.Vendor.Example:443` and `downloads.vendor.example` are one entry. `release source add` takes an `https://` URL with no path, query, fragment or userinfo, and refuses another. Adding an authority that has an entry replaces it, journaled, and says what it replaced.
```

Decision 12 leaves the default secret name to the build.

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-23T16:05:00Z
attempt: 1
claim: decision 12 leaves `init`'s default secret name to the build
evidence: internal/lifecycle/ops/init.go:417-470
action: decided
proposal: ASSUMED — without `--source-secret`, `init` names the secret `source_` followed by the authority with each character that is not a letter or a digit replaced by `_`, so `downloads.vendor.example` is `source_downloads_vendor_example`. `--source-secret` and `--source-auth` without `--source-secret-file`, or the file without `--release https://…`, are usage errors.
```

Found while building, and decided there. The holder in the first entry below
answers a finding on #416's review, which narrowed decision 12 before this
code was written.

```divergence
decision: UNLISTED
grade: UNLISTED
class: discovery
at: 2026-11-23T15:30:47Z
attempt: 2
claim: decision 12 holds init's credential from its first fetch, and that fetch runs in the CLI before the operation, through a release source built once and kept across the rewire init makes once it knows the product's name, so a credential held on Deps would be lost with the Deps it was held on
evidence: internal/lifecycle/ops/sources.go:93-105
action: decided
proposal: ASSUMED — the HTTPS source asks a credential holder built with it and kept with it across a rewire, which reads the entries through whichever Deps the command holds at the time of each request. `init` holds its credential there before its first fetch. A machine with no installation, or several and none chosen, configures no credential, so `release fetch` there works as before.
```

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-23T15:30:47Z
attempt: 2
claim: decision 14 trims a trailing newline and splits basic at the first colon, without saying what an empty value, a value holding a line break, or a basic value with no user does
evidence: internal/adapters/source/https/credentials.go:66-95
action: decided
proposal: ASSUMED — refines decision 14. One trailing newline, with a carriage return before it, is not part of the value. An empty value, one holding a control character, and a basic value with no colon or no user are refused before the request is made, naming the secret and never quoting the value, since a line break in a header value is a second header.
```

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-23T15:30:47Z
attempt: 2
claim: decision 12 does not say what `init --repair` does with the entries, nor what `--source-auth` defaults to, nor what happens when the credential's secret name is one the release declares
evidence: internal/lifecycle/ops/init.go:895
action: decided
proposal: ASSUMED — refines decision 12. `init --repair` keeps the entries, as it keeps the other settings, and refuses `--source-secret-file`, pointing at `release source add`. `--source-auth` defaults to `bearer`, which most download services issue. A credential secret named after one the release declares is refused before it is stored, since storing it would overwrite the release's secret.
```

```divergence
decision: 11
grade: ASSUMED
class: discovery
at: 2026-11-23T15:30:47Z
attempt: 2
claim: decision 11 names the listing command `release source ls`, and every listing beside it is `list`: `release trust list`, `secret list`, `backup target list`
evidence: internal/cli/release_source.go:80-81
action: departed
proposal: ASSUMED — the listing is `release source list`, with `ls` as its alias, as `config list` has. Decision 11 graded the names `ASSUMED`.
```
