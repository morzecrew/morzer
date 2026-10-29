# RFC 0038 — Credentials for HTTPS bundle sources

- **Status:** 📝 Draft — proposed, **awaiting the author's rulings** on the
  `OPEN` decisions in §11; not scheduled
- **Scope:** How the manager authenticates to an HTTPS server that serves
  release bundles only to customers who log in. Covers where a credential lives
  (the installation's secret store, named per host in `installation.yaml`),
  which schemes are sent (bearer and basic), the rule that a credential goes only
  to the host it names and never across a redirect to another, how it appears in
  `installation describe` and an export (by name, never by value), and redaction.
  Touches the HTTPS source adapter and the installation schema; no change to the
  bundle format, the OCI source (which keeps the ambient Docker credentials) or
  the release side. Records a defect found while writing it: `update` journals
  a reference's userinfo password verbatim.
- **Related:** [SECURITY.md](../SECURITY.md) "Known gaps";
  [0004](0004-distribution-and-verification.md) §5.3 (HTTPS source) and §5.4
  (OCI and ambient credentials); [0009](0009-backup-targets.md) (credentials as
  named secrets, and how an export carries them);
  [`internal/adapters/source/https/https.go`](../internal/adapters/source/https/https.go),
  [`internal/adapters/source/oci/oci.go`](../internal/adapters/source/oci/oci.go),
  [`internal/ports/source.go`](../internal/ports/source.go) (`RedactRefCredentials`),
  [`internal/lifecycle/ops/update.go`](../internal/lifecycle/ops/update.go).

---

## 1. Summary

A vendor who serves bundles only to paying customers puts them behind HTTPS
authentication. The manager can only send a password today by having the
operator type it into the reference, where it is on the command line, in shell
history, and, through one call site, in the journal. This RFC proposes naming a
credential per host in `installation.yaml` and keeping its value in the
installation's secret store, as backup targets already do. The HTTPS source
attaches it to requests for exactly that host and drops it on any redirect away
from it. The recommendation is to build it when a vendor asks, and to fix the
journal defect now regardless.

## 2. Motivation

SECURITY.md states the gap:

> The manager sends no credentials over HTTPS. A bundle behind authentication
> has to be fetched out of band, or published to an OCI registry, where the
> ambient Docker credentials are used.

Measured, the first sentence is not quite true (§3). The point stands, though:
the only way to authenticate is a password embedded in the reference. That
means it is in argv, which any local user can read through `/proc`, and in
whatever records the reference. Bearer tokens, which most download services
issue, cannot be sent at all.

## 3. Current state

Verified against `main` at 8b3c37e.

- **Userinfo in a reference already works as basic auth.** The HTTPS source
  builds its request from the reference's URL, and Go's `net/http` client turns
  URL userinfo into an `Authorization: Basic` header. Measured with an
  `httptest` TLS server: the header is sent to the origin, kept on a same-host
  redirect, and **dropped** on a redirect to a different host. The adapter
  anticipates this: its messages name the URL with the password removed
  ([`https.go:171`](../internal/adapters/source/https/https.go)), and
  `ports.RedactRefCredentials` strips it before a reference is written to state.
- **The redaction misses one place.** `update` records the reference in the
  operation's journal flags as typed: `flags := map[string]string{"ref":
  opts.Ref}` ([`update.go:338`](../internal/lifecycle/ops/update.go)). Neither
  the engine nor the state store redacts flags, so `morzer update
  https://user:token@host/bundle.tar.zst` writes the token into
  `operations.jsonl`. The `SourceRef` written a few lines later is redacted;
  this one is not. It is a defect independent of this RFC (§11 row 1).
- **No bearer tokens, no stored credential.** Nothing reads a credential from
  anywhere but the reference, so every fetch needs the password typed again, and
  a token that is not a username/password pair cannot be sent.
- **Redirects may not leave TLS.** `checkRedirect` refuses a redirect to any
  scheme but `https` and caps the chain at five
  ([`https.go:407`](../internal/adapters/source/https/https.go)).
- **OCI uses ambient Docker credentials**, from the Docker config and its
  credential helpers ([`oci.go:479`](../internal/adapters/source/oci/oci.go)).
  An operator who ran `docker login` logs in once for both images and bundles.
- **Backup targets already name credentials as secrets.** A target's
  `credentials: backup_s3` is the name of a secret in the installation's store,
  and RFC 0009 decides how an export carries it.
- **HTTPS is never polled.** HTTPS implements neither `ChannelPeeker` nor
  `List`, so `update --check`, channels and the unattended tick do not reach it.
  Credentials matter for the commands an operator types: `update <ref>`,
  `release fetch`, `init --release`.

## 4. Goals / Non-goals

**Goals**

- An operator can install from an authenticated HTTPS URL without the credential
  appearing in argv, shell history, the journal, `status`, `describe` or an
  export in the clear.
- A credential is sent only to the host it was configured for, over TLS.
- Bearer tokens and basic auth both work.

**Non-goals**

- **OAuth flows, token refresh, client certificates.** A static token or
  password covers the download services vendors use. Anything interactive does
  not belong in an unattended tool.
- **Changing OCI's credentials.** Ambient Docker credentials are the right
  answer there, and two credential mechanisms for one registry would disagree.

## 5. Design

### 5.1 Where the credential lives

`installation.yaml` names a credential per host; the value is a secret in the
installation's store:

```yaml
sources:
  https:
    - host: downloads.vendor.example
      auth: bearer               # or basic
      secret: vendor_download_token
```

- **The value is a secret** like any other: set with `morzer secret set
  vendor_download_token`, encrypted at rest, rotated with `secret rotate`, and
  known to the redactor. For `basic`, the secret holds `user:password`.
- **Rejected: `.netrc`.** It is a second store of plaintext credentials, with its
  own permissions to get wrong and nothing to encrypt it.
- **Rejected: an environment variable.** The unattended timer and `systemd`
  units would need it in their environment files, which is a plaintext copy in
  a place nobody audits.

### 5.2 Which requests get it

The HTTPS source wraps its transport. For every outgoing request, including each
hop of a redirect, it looks up the request's host:

- The credential is attached only when the request's scheme is `https` and its
  host **equals** a configured `host`, case-insensitively, port included when one
  is configured. No suffix or wildcard matching: `downloads.vendor.example` does
  not authorise `cdn.downloads.vendor.example`.
- Because the check runs per request rather than per reference, a redirect to
  another host goes out without it. That is the behaviour Go already shows for
  userinfo (§3), made explicit and tested rather than inherited.
- A 401 or 403 from a configured host names the configured secret in its hint,
  and never its value. A 401 from a host with no configuration says no
  credential is configured for it.

### 5.3 Userinfo references

A reference with a password in it keeps working (row 4 decides whether it
warns). When both are present, the configured credential wins and the userinfo
is ignored, so one request never carries two answers.

### 5.4 Describe, export and redaction

- `installation describe` shows the `sources.https` entries with host, scheme and
  secret name. The value is not in that document, by construction.
- An export carries the entry and, as for backup credentials, the secret travels
  with the other secrets, encrypted to the recovery recipients. A rebuilt machine
  can fetch its next update without the vendor reissuing a token.
- The value is a known secret, so the redactor scrubs it from subprocess output,
  support bundles and attestation text. The `Authorization` header itself is
  never logged.

## 6. Tests

- Adapter, against `httptest` TLS servers: a configured host receives the bearer
  header and the basic header; an unconfigured host receives nothing; a redirect
  to another host carries nothing; a same-host redirect carries it; a 401 names
  the secret and not the value; a configured credential beats userinfo.
- Suite: `update` from an authenticated URL succeeds, and the journal, `status`
  and `describe` hold no part of the value.
- Regression for §11 row 1: `update` with a userinfo reference journals the
  reference without its password.

## 7. Docs

- The release-commands reference ("Where bundles come from", HTTPS): configuring
  a credential, the host rule, and why userinfo is discouraged.
- The installation schema reference for `sources.https`.
- SECURITY.md: the known-gap entry moves to Closed gaps once built, and its first
  sentence is corrected now (userinfo basic auth already works, at the cost of
  argv exposure).

## 8. Out of scope

- **Credentials for notification webhooks.** Those URLs already carry their
  secret in the URL and are redacted by `RedactRefCredentials`; moving them to the
  store would be the same pattern, decided separately.
- **Per-path credentials on one host.** No vendor has needed a host that serves
  different customers different tokens by path.

## 9. Risks

- **A vendor's redirect to a CDN on another host loses the credential.** That is
  the rule working. A CDN that needs the vendor's token needs its own host entry,
  which the operator adds deliberately. Signed CDN URLs, the usual design, need
  no token at all.
- **A token stored on the machine is readable by root.** Root is out of the
  threat model; the token is as protected as every other secret on the machine.

## 10. Unresolved questions

- The `OPEN` rows in §11 are the author's to rule.
- Whether `doctor` should check that a configured credential's secret exists.
  Cheap, probably yes; left to implementation.

## 11. Decisions

| # | Grade | Decision |
| --- | --- | --- |
| 1 | `OPEN` | **Fix the journal defect now, apart from this RFC?** Recommended yes: `update` journals a reference's userinfo password verbatim (`update.go:338`), and the fix is to pass the flag through `RedactRefCredentials` as `SourceRef` already is. One line and a regression test, whatever is decided about the rest. |
| 2 | `OPEN` | **Build stored credentials now, or gate them on a vendor asking?** Recommended: gate. Userinfo works for the occasional manual fetch once row 1 is fixed, OCI covers authenticated distribution with ambient credentials, and no vendor has asked. |
| 3 | `OPEN` | **Configuration shape: per host (`sources.https[].host`) or per reference?** Recommended per host: a credential belongs to a server, and a per-reference credential would have to be repeated for every version an operator installs. |
| 4 | `OPEN` | **Userinfo references once stored credentials exist: keep silently, warn, or refuse?** Recommended: warn that the password is on the command line and point at `sources.https`, without refusing, since refusing breaks scripts that work today. |
| 5 | `OPEN` | **Schemes: bearer and basic, or bearer only?** Recommended both: basic is what many simple download servers speak, and it costs one branch. |
| 6 | `ASSUMED` | The credential's value lives in the installation's secret store and is referenced by name, as backup credentials are (RFC 0009). Rejected: `.netrc` and environment variables (§5.1). |
| 7 | `ASSUMED` | Matching is per request, exact host (port included when configured), case-insensitive, `https` only, with no suffix or wildcard matching; a redirect to another host never carries the credential. |
| 8 | `ASSUMED` | A configured credential takes precedence over userinfo in the reference, so a request never carries two. |
| 9 | `ASSUMED` | An export carries the `sources.https` entries and their secrets as it carries backup credentials, encrypted to the recovery recipients. |

## 12. Phasing

- **Now, independent of the rest:** row 1, the journal fix, if ruled yes.
- **P1 — stored credentials** (§5), gated on row 2: the schema field, the
  transport wrapper, the hints, describe/export, docs, and SECURITY.md's entry
  moved to Closed gaps.
