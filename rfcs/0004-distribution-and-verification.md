# RFC 0004 — Distribution and verification

- **Status:** 🚧 In progress — P1–P3 shipped 2026-08-03. A source registry
  dispatches on reference scheme, `tar.zst` bundles install like directories,
  and `require_signature` enforces signing instead of refusing everything. Three
  design changes are recorded in §5.1, §5.2 and §5.5. P4–P6 (HTTPS, goreleaser
  and the JSON Schema, OCI) remain.
- **Scope:** Adds three release sources — `tar.zst`, HTTPS and OCI artifact —
  behind the existing `ReleaseSource` port, and a minisign `Verifier` alongside
  the checksum one. Makes the `require_signature` installation policy fail
  closed. Adds a source registry so a reference's scheme selects its adapter.
  Ships reproducible amd64/arm64 binaries via goreleaser, an offline
  installation path, and a published JSON Schema for `selfhost/v1alpha1`. No
  changes to the lifecycle layer, the engine, or the manifest schema — every
  adapter here implements an interface that already exists and is already
  called. Explicitly **not** in scope: cosign/keyless signing, and a private
  release index.
- **Related:** [`internal/ports/source.go`](../internal/ports/source.go),
  [`internal/adapters/source/local/local.go`](../internal/adapters/source/local/local.go),
  [`internal/adapters/verify/checksum/checksum.go`](../internal/adapters/verify/checksum/checksum.go),
  [`internal/infra/atomicfs/copy.go`](../internal/infra/atomicfs/copy.go),
  RFC [0001](0001-update-and-rollback.md), which consumes these sources

---

## 1. Summary

A bundle can currently only come from a local directory. This adds the three
transports the contract was designed around, plus signature verification and a
policy that refuses unsigned bundles when an installation demands signing. It
also makes the manager itself distributable: signed cross-compiled binaries, an
offline install path, and a schema bundle authors can validate against in CI
without running the manager.

## 2. Motivation

`ports.ParseRef` already normalizes `./path`, `file://`, `https://` and `oci://`,
and refuses plaintext `http://` with a message about fetching out of band. Three
of those four schemes then have no adapter. `release fetch https://…` reaches
this, in [`internal/cli/release.go`](../internal/cli/release.go):

```go
if ref.Scheme != app.Deps.Source.Scheme() {
    return domain.Usage("no source is configured for scheme %q", ref.Scheme).
        WithHint("this build supports %q sources; unpack the bundle and pass a path", ...)
}
```

So the reference vocabulary is complete and the transports are not. `Deps.Source`
is a *single* `ReleaseSource`, not a registry — adding a second adapter requires
a selection mechanism that does not yet exist.

Verification has the same shape. `ports.Expectation` carries `SignaturePath`,
`PublicKeys` and `Required`; `Installation.Policy.RequireSignature` is parsed
and defaults false; and the checksum verifier
([`checksum.go`](../internal/adapters/verify/checksum/checksum.go)) correctly
**refuses** rather than silently downgrading when a signature is required but
unavailable. Every piece of the policy exists except the verifier that could
satisfy it — so `require_signature: true` today is an unconditional failure
rather than a working control.

## 3. Current state

**Built and exercised.**

- `ReleaseSource` and `Verifier` interfaces, with the `dir` and `checksum`
  implementations.
- `ParseRef` handling all four schemes and rejecting `http://`.
- `atomicfs.CopyTree` with `ExtractLimits` (entry count, total size, per-file
  size) and rejection of symlinks and non-regular files — written for archive
  extraction and currently only used for directory copies. **The archive
  extractor can reuse these limits and this rejection logic wholesale.**
- `atomicfs.DigestTree` producing a stable content digest over path, executable
  bit and contents in sorted order — transport-independent by construction, so
  a bundle fetched over HTTPS and the same bundle unpacked locally hash
  identically.
- `checksum.VerifySumsFile` parsing `sha256sum`-format files, including
  rejection of entries whose path escapes the bundle.
- Reproducible build flags in the justfile: `-trimpath`, `CGO_ENABLED=0`,
  version/commit/date via `-ldflags`, and a `build-all` recipe producing both
  architectures plus `SHA256SUMS`.

**Not built.**

- Any source other than `dir`; no registry to select between them.
- Any verifier other than `checksum`; no minisign dependency.
- No goreleaser config; `build-all` is a hand-rolled substitute.
- No published JSON Schema. `domain.Manifest` is the only schema, and only the
  Go type system expresses it.
- No offline install path.

## 4. Goals / Non-goals

**Goals**

- Fetch a bundle from a `tar.zst` archive, an HTTPS URL, or an OCI registry.
- Verify a detached minisign signature and make `require_signature` work.
- Select a source by reference scheme through a registry.
- Reproducible signed builds for linux/amd64 and linux/arm64.
- Install with no network access at all.
- Publish a JSON Schema so bundles can be validated in CI without the manager.

**Non-goals**

- **cosign / keyless signing.** `sigstore/cosign` as a library is enormous, and
  keyless signing assumes a transparency log and an OIDC identity that a
  self-hosted vendor may not have. minisign is a small dependency with simple
  key management. If keyless is needed later it arrives as another `Verifier`.
- **A private release index.** Enumerating available versions over HTTPS needs a
  format nobody has specified. `List` returning `ErrUnsupported` is a documented,
  acceptable answer.
- **Hosting.** The manager fetches; it does not run a registry.
- **Pulling container images.** Those always come from an OCI registry via the
  runtime, whatever the bundle source. Unchanged.

## 5. Design

### 5.1 Source registry

`Deps.Source` becomes a registry rather than a single adapter:

```go
// internal/adapters/source
type Registry struct{ sources map[string]ports.ReleaseSource }

func (r *Registry) For(ref ports.Ref) (ports.ReleaseSource, error) {
    s, ok := r.sources[ref.Scheme]
    if !ok {
        return nil, domain.Usage("no source is configured for scheme %q", ref.Scheme).
            WithHint("this build supports: %s", strings.Join(r.Schemes(), ", "))
    }
    return s, nil
}
```

The registry itself satisfies `ports.ReleaseSource` by dispatching on the ref,
so the lifecycle layer's call sites do not change at all.

> **Amendment, P1 (2026-08-03).** `ReleaseSource.Scheme() string` became
> `Schemes() []string`. A registry that is itself a source has to answer for
> everything registered in it, and one scheme per implementation would have made
> the composite lie about what it can do. It is also what builds the "this build
> supports: ..." half of a refusal, which is the only reason a caller ever asked.
>
> Both call sites that pre-checked the scheme -- in `ops.Update` and `release
> fetch` -- were deleted rather than adapted. Naming the supported transports is
> the registry's job, not every caller's, and `Resolve` already runs before the
> deployment lock is taken, so the refusal still arrives before anything mutates.
>
> Duplicate registration is an error rather than last-wins or first-wins: one
> would make behaviour depend on argument order, the other would silently ignore
> an adapter someone deliberately added. See decision 11.

### 5.2 Archive source (`tar.zst`)

Extraction reuses `atomicfs`'s existing limits and rejections rather than
restating them — the enforcement lives in one place:

- Stream through `klauspost/compress/zstd` into `archive/tar`.
- Extract through an `os.Root` on the destination, so a `../` entry fails at
  the syscall rather than at a check.
- Enforce `ExtractLimits` **during** extraction, not after: a zip-bomb must be
  refused while it is being written, not once the disk is full.
- Reject symlinks, hardlinks, device nodes, and any entry with a non-regular
  type flag — the same rule `CopyTree` already applies.
- Refuse an archive whose uncompressed size exceeds the limit even if its
  compressed size does not.

> **Amendment, P2 (2026-08-03).** The archive is not a separate source. `ParseRef`
> gives a bare path the `file` scheme whether it names a directory or an
> archive, so two sources claiming that scheme could not both be registered --
> the registry dispatches on scheme and would have had nothing to choose with.
> Deciding by what the path actually *is* belongs where the filesystem is
> already being touched, so the `dir` package became `local` and handles both.
> The extraction itself lives in `atomicfs` beside `CopyTree`, which is what
> decision 2 asked for. See decision 12.
>
> A second change the design did not anticipate: `ops.Update` read the source
> bundle's manifest straight from `ref.Location`, which works only for a
> directory. References that cannot be read where they lie are now materialised
> into the staging directory first, and removed when the operation ends. That is
> the mechanism HTTPS and OCI will reuse, and it is why a dry run against an
> archive fetches -- a plan that refused to could say nothing about the release
> it was asked about. See decision 13.

### 5.3 HTTPS source

`net/http` plus `hashicorp/go-retryablehttp` for flaky mirrors. Downloads to the
staging directory (`/var/lib/<product>/manager/staging`, already in the path
layout and currently unused), verifies, then extracts through the archive source.

Refusals: plaintext `http://` (already refused at parse); a redirect from HTTPS
to HTTP; a response without a `Content-Length` when a size limit is configured;
a body exceeding the limit mid-stream.

A sibling `<url>.minisig` is fetched when the installation requires signatures.
A missing signature file under `require_signature: true` is a hard failure, not
a downgrade.

### 5.4 OCI source

`oras-go/v2` pulls the bundle as an OCI artifact. Auth reuses the ambient Docker
credential store — an operator who has run `docker login` should not have to log
in twice. An `oci://` reference with a digest is pinned and verified against
the pulled manifest digest.

### 5.5 minisign verifier

```go
// internal/adapters/verify/minisign
func (v *Verifier) Verify(ctx context.Context, bundle ports.BundlePath, expect ports.Expectation) error
```

`jedisct1/go-minisign`, verification only — the manager never signs. Keys come
from `installation.yaml`:

```yaml
policy:
  require_signature: true
  signing_keys:
    - RWQf6LRCGA9i53mlYecO4IzT51TGPpvWucNSCh1CBM0QTaLn73Y7GFO3
```

Verifiers compose: the checksum verifier always runs; the signature verifier
runs additionally when keys are configured. A bundle failing either is refused.
`require_signature: true` with no configured keys is a **configuration error at
load time**, not a runtime surprise — the policy would otherwise be
unsatisfiable and fail every operation with a confusing message.

### 5.6 Offline installation

```text
morzer release fetch ./bundle.tar.zst      # no network
morzer apply --startup                     # skips pulls when images are local
```

`--startup` already skips pulls when images are present — written for boot-time
convergence and equally the offline path. What is missing is a documented
procedure for preloading images (`docker load` from a vendor tarball) and a
`doctor` check that reports which manifest images are absent locally, so an
operator can tell before losing connectivity whether an offline install will
work.

### 5.7 goreleaser and the JSON Schema

goreleaser replaces the hand-rolled `build-all`: linux/amd64 and linux/arm64,
`tar.zst` archives, `SHA256SUMS`, minisign signatures, a changelog assembled
from the conventional-commit history, and reproducible flags matching the
justfile's.

The JSON Schema is **generated from `domain.Manifest`** and checked into
`schemas/selfhost-v1alpha1.json`, with a test asserting the checked-in file
matches what the current types generate. Hand-writing it would guarantee drift
between the schema editors validate against and the types the manager enforces.

## 6. Tests

- **Contract suite for `ReleaseSource`** — the port has none today. One shared
  suite run against all four adapters: resolve without side effects, fetch into
  a caller-chosen directory, digest stability, `List` returning `ErrUnsupported`
  where unsupported.
- **The transport-independence assertion**: the same bundle delivered as a
  directory, a `tar.zst`, over HTTPS and via OCI must produce an identical
  `DigestTree`. This is what makes a digest recorded from one transport
  meaningful when verified from another.
- **Malicious archive fixtures** — path traversal, symlink escape, zip bomb,
  entry-count bomb, device node, hardlink — each asserted refused with a typed
  error rather than a panic or a partial extraction.
- **Verifier contract suite** over checksum and minisign: good signature,
  tampered bundle, wrong key, absent signature with and without
  `require_signature`.
- **HTTPS against `httptest`**: retry on 5xx, refusal of an HTTPS→HTTP redirect,
  refusal of an oversized body mid-stream.
- **Schema test**: the checked-in JSON Schema matches generation from the
  current `domain.Manifest`, and the example bundle validates against it.

## 7. Docs

- README: a "Where bundles come from" section with the four reference forms.
- A vendor-facing note on producing a bundle: build, sign, publish, and what
  digest to record. This is the audience the schema and the signature exist for.
- The offline procedure written as an ordered list an operator can follow
  without a network connection to read it — so it must not live only online.
- Honest wording on the threat model: signature verification proves a bundle came
  from the holder of a key, not that the bundle is safe to run. The existing
  threat-model section says as much and must not be softened when signing lands.

## 8. Out of scope

- **GitHub Releases as a distinct source.** It is an HTTPS URL with a
  well-known shape; the HTTPS source covers it. A dedicated adapter would only
  add release-note fetching, which is not needed to install.
- **Delta updates.** Bundles are small — manifests, Compose files, templates,
  hooks. The images are the large part and OCI already deduplicates layers.
- **A `morzer sign` command.** The manager verifies; signing belongs in the
  vendor's release pipeline, where the key lives. Building signing in would
  invite the key onto a deployment host.
- **Mirror or failover lists.** Retry handles a flaky endpoint; choosing between
  endpoints is a policy nobody has asked for.

## 9. Risks

- **Archive extraction is the largest attack surface added.** Mitigated by
  `os.Root`, by reusing the already-tested `ExtractLimits` and type rejections,
  and by the malicious-fixture suite in §6. This is the part of the RFC that
  most deserves review.
- **`oras-go` is a heavy dependency for one source.** If it proves burdensome,
  OCI is the source to drop — it is the only one whose absence has a workaround
  (`oras pull` out of band, then a directory reference). Phased last for exactly
  this reason.
- **Signature verification read as more than it is.** It proves provenance, not
  safety. A signed bundle still runs hooks as root on the host. The docs must
  keep saying so.
- **Generated schema drift.** If the generator is not run, the checked-in schema
  silently diverges from the types. The equality test in §6 is what prevents it,
  and it must fail the build rather than warn.
- **`require_signature: true` with no keys** would fail every operation with a
  confusing message. Refused at configuration load instead.

## 10. Decisions

| # | Decision |
| --- | --- |
| 1 | A source registry dispatching on `Ref.Scheme`, itself satisfying `ReleaseSource`. Lifecycle call sites do not change when a transport is added. |
| 2 | Archive extraction reuses `atomicfs`'s existing `ExtractLimits` and non-regular-file rejection rather than restating them. One place to review, one place to fix. |
| 3 | Limits are enforced during extraction, not after. A zip bomb must be refused while it is being written, not once the disk is full. |
| 4 | The content digest stays transport-independent, asserted by a test comparing all four sources. A digest recorded from one transport must verify from another. |
| 5 | Verifiers compose: checksum always, signature additionally when keys are configured. A bundle failing either is refused. |
| 6 | `require_signature: true` with no configured signing keys is a configuration error at load time. An unsatisfiable policy must not present as a runtime failure. |
| 7 | minisign over cosign. Cosign as a library is enormous and keyless signing assumes infrastructure a self-hosted vendor may not have. Keyless can arrive later as another `Verifier`. |
| 8 | No `morzer sign`. The manager verifies; signing belongs in the vendor's pipeline where the key lives. Building it in invites the key onto a deployment host. |
| 9 | The JSON Schema is generated from `domain.Manifest` and its checked-in copy is equality-tested. A hand-written schema drifts from the types that actually enforce it. |
| 10 | OCI ships last. It is the heaviest dependency and the only source with an out-of-band workaround. |
| 11 | `ReleaseSource` declares `Schemes() []string`, not one scheme, and duplicate registration is an error. A registry that is itself a source answers for all of them; ordering-dependent resolution of a duplicate would be a bug nobody could see. See §5.1. |
| 12 | Directories and archives are one `local` source, not two. `ParseRef` gives both the `file` scheme, so a scheme-dispatching registry could not tell them apart; the filesystem can. See §5.2. |
| 13 | A reference that cannot be read in place is materialised into the staging directory before its manifest is read, and removed when the operation ends. Dry runs materialise too — a plan that would not fetch could say nothing about the release. See §5.2. |
| 14 | The signature covers `SHA256SUMS` and ships inside the bundle. Signing a tree digest would need this program to produce or check a signature; a sibling file would not survive being archived. See §5.5. |
| 15 | The checksum verifier knows nothing about signatures. One policy decision in two adapters is two adapters that can disagree about it. See §5.5. |
| 16 | Extracted permissions are normalised to `0755` or `0644`. Only the executable bit is part of a release — the digest records nothing else — and normalising means a bundle cannot ship a world-writable or setuid file at all, rather than being checked for one. |
| 17 | The verifier contract suite is parameterised by what each verifier claims to check. minisign genuinely cannot detect a file edited along with its checksum entry, so a shared assertion would either fail a correct implementation or be weakened until it proved nothing. Declaring a claim falsely is what the suite now catches. |

## 11. Phasing

- **P1** — Source registry plus the `ReleaseSource` contract suite, with only
  `dir` registered. Pure refactor, no new transport, no new dependency.
- **P2** — `tar.zst` source and the malicious-archive fixtures. The security
  work; worth landing and reviewing on its own.
- **P3** — minisign verifier and `require_signature`. Independent of the
  transports.
- **P4** — HTTPS source.
- **P5** — goreleaser, the offline procedure and its `doctor` check, and the
  generated JSON Schema. Distribution of the manager rather than of bundles.
- **P6** — OCI source, gated on demand.

P1–P3 are the ones that change the security posture and are worth doing whether
or not the transports follow.

### What P1–P3 shipped

- `internal/adapters/source.Registry`, dispatching on `Ref.Scheme` and itself a
  `ReleaseSource`. `local` replaces `dir` and handles a bundle directory or a
  `tar.zst`.
- `atomicfs.ExtractTarZst`, sharing `ExtractLimits` and the non-regular-file
  rejection with `CopyTree`, plus a zstd decoder window cap — a format that lets
  an archive declare its own window is a way to make a 200-byte file allocate
  gigabytes before anything is written.
- A `ReleaseSource` contract suite run against the directory source, the archive
  source and the registry, whose central assertion is that all three produce
  the *same* content digest for the same bundle. Without it, pinning a release
  would silently pin a transport.
- Thirteen malicious-archive fixtures, each built in Go in the test that asserts
  its refusal rather than checked in as a file nobody can review by reading:
  traversal, absolute path, symlink escape, hardlink, device node, FIFO,
  entry-count bomb, oversized declaration, total-size overrun mid-write,
  decompression bomb, non-archive, truncated archive, and mode normalisation.
- `minisign` verifier and a `verify.Chain`, wired as checksum + minisign
  everywhere. `Policy.SigningKeys`, `init --signing-key` and
  `--require-signature`, and `release verify --signing-key` for a vendor's own
  CI, which needs no installation on the machine.
- A verifier contract suite parameterised by claims (§decision 17), run against
  the checksum verifier, minisign, and the chain production actually wires.

**Verified with the real binary, not only in tests**: a bundle fetched as an
archive reports the identical digest to the same bundle as a directory; a
traversal archive is refused by name; a truncated one is refused as corrupt;
`release verify --signing-key` accepts a signed bundle, refuses the wrong key,
and catches a hook edited after signing; `init --require-signature` without a
key is refused, and with one, an unsigned bundle is refused at fetch.

**Not** done: `https://` and `oci://` still parse and have no adapter, so the
registry has exactly one entry. That is worth saying plainly — the registry's
value today is the refusal message and the shape it sets up, not a transport
anyone can use.
