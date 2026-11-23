# RFC 0038 — Credentials for HTTPS bundle sources

- **Status:** ✅ Complete — row 1 shipped in #170, and P1 2026-11-23 in #417.
  Proposed, and ruled by the author 2026-11-01: §11's `OPEN` rows as
  recommended. P1 was gated on a vendor asking, and the author ruled 2026-11-23
  to build it now: decision 10 lifts the gate, and decisions 11–17 settle what
  the design left open. Decisions 18–28 record what P1's build and review
  added, from [logs/https-credentials.md](../logs/https-credentials.md), whose
  planning entries carry a time corrected at the log's end.
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
  **authority equals** a configured one. Hosts compare case-insensitively, and
  ports compare after both sides default to 443. So an entry with no port
  matches port 443 only, and a redirect to `downloads.vendor.example:8443` is a
  different authority that gets nothing unless it has its own entry. There is no
  suffix or wildcard matching either: `downloads.vendor.example` does not
  authorise `cdn.downloads.vendor.example`.
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
  to another host carries nothing; a redirect to the same host on another port
  carries nothing; a same-authority redirect carries it; a 401 names
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
| 1 | `ASSUMED` | **Fix the journal defect now, apart from this RFC?** Recommended yes: `update` journals a reference's userinfo password verbatim (`update.go:338`), and the fix is to pass the flag through `RedactRefCredentials` as `SourceRef` already is. One line and a regression test, whatever is decided about the rest. Was `OPEN`; ruled by the author 2026-11-01: as recommended; the fix had already landed in #170, and the journalled flag is redacted at `update.go:356`. |
| 2 | `ASSUMED` | **Build stored credentials now, or gate them on a vendor asking?** Recommended: gate. Userinfo works for the occasional manual fetch once row 1 is fixed, OCI covers authenticated distribution with ambient credentials, and no vendor has asked. Was `OPEN`; ruled by the author 2026-11-01: as recommended, gated on a vendor asking. Superseded by decision 10. |
| 3 | `ASSUMED` | **Configuration shape: per host (`sources.https[].host`) or per reference?** Recommended per host: a credential belongs to a server, and a per-reference credential would have to be repeated for every version an operator installs. Was `OPEN`; ruled by the author 2026-11-01: as recommended. |
| 4 | `ASSUMED` | **Userinfo references once stored credentials exist: keep silently, warn, or refuse?** Recommended: warn that the password is on the command line and point at `sources.https`, without refusing, since refusing breaks scripts that work today. Was `OPEN`; ruled by the author 2026-11-01: as recommended. |
| 5 | `ASSUMED` | **Schemes: bearer and basic, or bearer only?** Recommended both: basic is what many simple download servers speak, and it costs one branch. Was `OPEN`; ruled by the author 2026-11-01: as recommended. |
| 6 | `ASSUMED` | The credential's value lives in the installation's secret store and is referenced by name, as backup credentials are (RFC 0009). Rejected: `.netrc` and environment variables (§5.1). |
| 7 | `ASSUMED` | Matching is per request, on the exact authority: host case-insensitive, port defaulted to 443 on both sides, `https` only, with no suffix or wildcard matching. A redirect to another host, or to the same host on another port, never carries the credential. |
| 8 | `ASSUMED` | A configured credential takes precedence over userinfo in the reference, so a request never carries two. |
| 9 | `ASSUMED` | An export carries the `sources.https` entries and their secrets as it carries backup credentials, encrypted to the recovery recipients. |
| 10 | `ASSUMED` | **Supersedes decision 2.** Stored credentials, P1, are built now, without waiting for a vendor to ask. Ruled by the author 2026-11-23. |
| 11 | `ASSUMED` | An operator manages the `sources.https` entries with `morzer release source add https://<host>[:port] --bearer --secret <name>` (or `--basic`), `release source remove <host>` and `release source ls` (names `ASSUMED`). Each change is journaled, naming the host, the scheme and the secret's name and never its value. `backup target add` and `release trust add` only take the deployment lock and write the state, but a change to where a credential is sent is worth a record. `remove` leaves the secret in the store and says so. Rejected: entries only through a `config apply` document, which leaves no command to type; and a setting per host, which would put the scheme and the secret's name into one value. Ruled by the author 2026-11-23. The listing's name is superseded by decision 25. |
| 12 | `ASSUMED` | `init --release https://…` takes the credential with `--source-auth` (`bearer` or `basic`), `--source-secret-file <file>` and `--source-secret <name>` (names `ASSUMED`; the default secret name is left to the build). Init fetches the release before it creates the secret store, so it holds the value in memory from its first fetch on, including the one that reads the product's name from the bundle when `--product` is not given, then stores it as the named secret and writes the entry into the installation, and later updates use it. Without the flags, a password in the reference still works, with decision 4's warning. Rejected: leaving `init` to a password in the reference or to a local path, which keeps the argv exposure at a customer's first contact with the vendor's server; and reordering `init` so that the store exists before the fetch. Ruled by the author 2026-11-23. |
| 13 | `ASSUMED` | A desired-state document ([RFC 0057](0057-desired-state-an-installation-pulls-and-applies-to-itself.md)) does not manage the `sources.https` entries or their secrets in P1: they stay local to each machine. The settings a sync may manage are a closed list, and adding these needs a row of its own, deciding among other things where a token may be sent. Ruled by the author 2026-11-23. |
| 14 | `ASSUMED` | A credential secret's value has a trailing newline trimmed. For `bearer` it is the token. For `basic` it holds `user:password`, split at the first colon, so a password may contain colons. Ruled by the author 2026-11-23. |
| 15 | `ASSUMED` | `doctor` checks that each `sources.https` entry's secret is set, and warns rather than fails when one is not: the machine still runs, and only its next fetch from that host would fail. Answers §10's question. Ruled by the author 2026-11-23. |
| 16 | `ASSUMED` | **Refines decision 4.** The warning about a password in a reference is given by `update`, `release fetch` and `init`, the commands that fetch over HTTPS, and none of them refuses. Ruled by the author 2026-11-23. |
| 17 | `ASSUMED` | `sources.https` is a new field of installation state, so `InstallationSchemaVersion` goes from 22 to 23 with a migration, for RFC 0007 decision 9's reason. The manifest does not change, so no release needs a version floor. Ruled by the author 2026-11-23. |
| 18 | `ASSUMED` | *(2026-11-23, from the plan of #417)* A sandbox imported from a production export keeps the `sources.https` entries. Fetching a release from the vendor writes nothing into production's services, which is what the settings a sandbox drops have in common, and the secrets travel in the export as every secret does. See [logs/https-credentials.md](../logs/https-credentials.md) (unlisted, attempt 1, the sandbox). |
| 19 | `ASSUMED` | *(2026-11-23, from the plan of #417)* **Refines decision 11.** `release source add` and `remove` append a succeeded `config` operation under the deployment lock, before the state is written. Its flags name the change (`source_add` or `source_remove`), the authority, the scheme and the secret's name. A plan journals nothing, and adding an entry that is already there as given changes nothing and journals nothing. See [logs/https-credentials.md](../logs/https-credentials.md) (unlisted, attempt 1, the journal record). |
| 20 | `ASSUMED` | *(2026-11-23, from the plan of #417 and its review)* **Refines decision 7.** An entry stores its authority lower-cased, its port written as its number and left out when it is 443, and an IPv6 zone kept escaped as `%25`, so every spelling of one authority is one entry and matches a request's host written the same way. `release source add` takes an `https://` URL with no path, query, fragment or userinfo. Adding an authority that has an entry replaces it, journaled, and says what it replaced. An entry read from state that is not in this form, or whose port is out of range, is refused. See [logs/https-credentials.md](../logs/https-credentials.md) (unlisted, attempt 1, the authority's form; and unlisted, attempt 3, 2026-11-23T21:46:40Z, its port and zone). |
| 21 | `ASSUMED` | *(2026-11-23, from the plan of #417)* **Refines decision 12.** Without `--source-secret`, the secret is named `source_` followed by the authority with each character that is not an ASCII letter or digit replaced by `_`, so `downloads.vendor.example` is `source_downloads_vendor_example`. `--source-secret` or `--source-auth` without `--source-secret-file`, and the file without an https `--release`, are usage errors. See [logs/https-credentials.md](../logs/https-credentials.md) (unlisted, attempt 1, the default secret name). |
| 22 | `ASSUMED` | *(2026-11-23, from the build of #417)* **Refines decision 12.** The HTTPS source asks a credential holder that is built with it and kept with it across the rewire `init` makes once it knows the product's name. The holder reads the entries through whichever Deps the command holds at each request, and `init` holds its credential there before its first fetch. A machine with no installation, or several and none chosen, configures no credential, so `release fetch` there works as before. See [logs/https-credentials.md](../logs/https-credentials.md) (unlisted, attempt 2, 2026-11-23T15:30:47Z, the holder). |
| 23 | `ASSUMED` | *(2026-11-23, from the build of #417)* **Refines decision 14.** One trailing newline, with a carriage return before it, is not part of the value. An empty value, one holding a control character, and a basic value with no colon or no user are refused before the request is made, naming the secret and never quoting the value, since a line break in a header value is a second header. See [logs/https-credentials.md](../logs/https-credentials.md) (unlisted, attempt 2, 2026-11-23T15:30:47Z, the value's rules). |
| 24 | `ASSUMED` | *(2026-11-23, from the build of #417)* **Refines decision 12.** `init --repair` keeps the entries, as it keeps the other settings, and refuses `--source-secret-file`, pointing at `release source add`. `--source-auth` defaults to `bearer`, which most download services issue. A credential secret named after one the release declares is refused before it is stored, since storing it would overwrite the release's secret. See [logs/https-credentials.md](../logs/https-credentials.md) (unlisted, attempt 2, 2026-11-23T15:30:47Z, `--repair`, the default scheme and a declared name). |
| 25 | `ASSUMED` | *(2026-11-23, from the build of #417)* **Supersedes decision 11's listing name.** The listing is `release source list`, with `ls` as its alias, as `config list` has: every listing beside it is `list`, as in `release trust list`, `secret list` and `backup target list`. Decision 11 graded the names `ASSUMED`. See [logs/https-credentials.md](../logs/https-credentials.md) (decision 11, attempt 2, 2026-11-23T15:30:47Z, departed). |
| 26 | `ASSUMED` | *(2026-11-23, from the review of #417)* **Refines §5.4.** The client's errors quote text the server chose, a redirect's `Location` and URL among it, so a fetch records each credential it sent: the value, the header, and for basic the base64 of the login and the password, each as written, as `%q` escapes it, and path- and query-encoded. An error whose text carries one loses its cause. Once a credential was sent, so does an error quoting a `Location`, where no list of known forms can be complete. Every URL an error quotes loses its query, and a URL whose path is not the one requested is quoted by its server alone. A certificate refusal is classed by the original error and wraps the cleared one. A source URL that does not parse is quoted with its password removed. See [logs/https-credentials.md](../logs/https-credentials.md) (unlisted, attempt 3, 2026-11-23T21:46:40Z, what an error quotes). |
| 27 | `ASSUMED` | *(2026-11-23, from the review of #417)* **Refines decision 12.** `--source-secret-file` is opened without blocking and refused unless it is a regular file, so a named pipe cannot hang `init`, and it is read through a 64 KiB bound, since a file may grow after its size is looked at. The flag applies to every reference `ParseRef` reads as https, whatever the scheme's case or the space around it. See [logs/https-credentials.md](../logs/https-credentials.md) (unlisted, attempt 3, 2026-11-23T21:46:40Z, the credential file). |
| 28 | `ASSUMED` | *(2026-11-23, from the review of #417)* **Refines decision 16.** Each command warns before its first fetch, which for `release fetch`, and for `init` reading the product's name, runs in the CLI before any operation, so a fetch that fails still carries the warning. It reads the reference as the source does, with `ParseRef`, so the scheme's case and the space around it do not hide the password. See [logs/https-credentials.md](../logs/https-credentials.md) (unlisted, attempt 3, 2026-11-23T21:46:40Z, when the warning is given). |

## 12. Phasing

- **Now, independent of the rest:** row 1, the journal fix, if ruled yes.
- **P1 — stored credentials** (§5), gated on row 2: the schema field, the
  transport wrapper, the hints, describe/export, docs, and SECURITY.md's entry
  moved to Closed gaps.
  *(2026-11-23: decision 10 superseded row 2, and P1 is built now; decisions
  11–17 settle what it left open.)*
  *(2026-11-23: P1 was built in #417.)*
