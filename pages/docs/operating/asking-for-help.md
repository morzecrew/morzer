---
title: Asking for help
icon: lucide/life-buoy
summary: What to send when you ask somebody to look at your installation, and what that archive never contains
---

# Asking for help

An archive produced by `morzer support bundle` never contains your age
identity, your encrypted secrets, your machine's signing key, your backup
credentials, or anything from the directory where secrets are rendered for the
running deployment. That list is enforced by the build rather than promised
here, and [what a support bundle contains](../reference/support-bundle.md)
enumerates every component with the reason for its classification.

That sentence is first because it is the one that decides whether you send
anything at all.

## The problem this solves

You are asked for output. You paste `doctor` with the container names blurred.
Three round trips later somebody asks for the logs, and you paste those too —
into a forum, unredacted, because pasting was the only tool you had.

Meanwhile your machine already holds, in structured form, everything that
conversation needed: the journal of every operation it has run, `doctor`'s
results, the resolved manifest, where your configuration files differ from what
the release renders, the version history, and what each service is doing.

## Send one archive

```console
$ morzer support bundle --preview
```

`--preview` writes nothing. It prints the components, their sizes, and how many
secret values were scrubbed from each. Read it. An archive you have not looked
at is one you will either not send or send too much of.

```console
$ morzer support bundle
11 component(s), 25.2 KiB
  FILE               COMPONENT                            SIZE      REDACTIONS
  manifest.yaml      The resolved manifest                2.7 KiB   0
  installation.yaml  Installation state                   857 B     0
  parameters.json    Parameters and their values          636 B     0
  config-diff.txt    Configuration drift                  71 B      0
  journal.jsonl      The operation journal                10.3 KiB  0
  doctor.json        Diagnostic checks                    7.5 KiB   0
  releases.json      Version history                      303 B     0
  services.json      Service and health state             520 B     0
  manager.json       Manager version and build            43 B      0
  logs/app-1.log     Container logs                       1016 B    0
  meta.json          The archive's own account of itself  1.4 KiB   0
  written  /home/ops/support-demo-op_01KZY3DTYE605RA1B71CMVNK05-20260813T174241Z.tar.zst

  this archive is not encrypted: anyone who receives it can read all of it
```

That archive is 5,882 bytes compressed, from an installation that had run an
apply, three configuration changes, a backup, a restore, an update killed
mid-flight and a resume. Most of it is the journal.

Attach that file wherever you were going to paste a screenshot. Your vendor, or
a stranger on a forum, can open it with `tar --zstd -xf` — they need nothing
from this project to read it.

## What the redaction count means

`meta.json` inside the archive records, per file, how many values were replaced.

A count of zero is **not** proof that a file was clean. It is proof that no
secret value your installation currently holds appeared in it, which is a
smaller claim: a credential you rotated away last month, or one you never told
the manager about, is not something it can recognise.

Every component is scrubbed on its way in — not only the container logs. Most
of what goes into the archive was written long before you ran the command, and
the journal in particular is appended to across every operation the
installation has ever run.

Container logs are the one component treated as unsafe by default: if your
secret values cannot be loaded — a missing sops key, most often — the logs are
left out of the archive entirely and `meta.json` says why. Everything else
still ships.

## There is no flag to turn redaction off

A flag like that becomes the one every support article tells you to pass, and
then redaction is a feature nobody uses.

If you genuinely need unredacted output, `morzer logs --no-redact` makes that
choice one file at a time, in front of you, rather than for a whole archive you
are about to send to somebody else.

## What it does not do

The manager never transmits the archive. There is no upload, no endpoint, and
no retry queue — it writes a file, and what happens to that file is yours to
decide.

Encrypting the archive to a vendor's keys, so that it is unreadable by the
ticket system it passes through, is designed and not yet built (RFC 0024 P4).
Until then the archive is plaintext, and the command says so on every run.
