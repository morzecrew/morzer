# RFC 0004 — Distribution and verification

- **Status:** 📝 Draft
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
  [`internal/adapters/source/dir/dir.go`](../internal/adapters/source/dir/dir.go),
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
