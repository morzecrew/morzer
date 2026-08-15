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
10 component(s), 16.1 KiB
  FILE               COMPONENT                            SIZE     REDACTIONS
  manifest.yaml      The resolved manifest                2.8 KiB  0
  installation.yaml  Installation state                   521 B    0
  parameters.json    Parameters and their values          631 B    0
  config-diff.txt    Configuration drift                  1.2 KiB  0
  journal.jsonl      The operation journal                899 B    0
  doctor.json        Diagnostic checks                    7.9 KiB  0
  releases.json      Version history                      227 B    0
  services.json      Service and health state             3 B      0
  manager.json       Manager version and build            25 B     0
  meta.json          The archive's own account of itself  1.9 KiB  0
  NOT COLLECTED  WHY
  logs/          the deployment produced no log output to capture

  written
/home/ops/support-demo-op_01M03BW5G7SMMHKDFMTC932W5T-20260815T184516Z.tar.zst

  this archive is not encrypted: anyone who receives it can read all of it

  signed, with the signature beside it
/home/ops/support-demo-op_01M03BW5G7SMMHKDFMTC932W5T-20260815T184516Z.tar.zst.minisig
```

The path is printed unindented and unwrapped on purpose: it is there to be
copied into an `scp` or an upload, and a path broken across two lines by a
narrow terminal is one you paste wrong. The signature beside it travels with
the archive; send both.

The deployment behind this run had never started a container, which is why
`logs/` is listed as a gap rather than as a row. A component that is missing is
shown as missing — an archive that simply lacked a file would leave whoever
reads it guessing whether the component does not exist here, failed to collect,
or was never part of the format.

That archive is 5,132 bytes compressed, from an installation that had run an
`init` and one configuration change. Most of it is `doctor`'s output; on a
machine with a longer history, most of it is the journal.

Attach that file wherever you were going to paste a screenshot. Your vendor, or
a stranger on a forum, can open it with `tar --zstd -xf` — they need nothing
from this project to read it.

## What the redaction count means

`meta.json` inside the archive records, per file, how many values were replaced.

A count of zero is **not** proof that a file was clean. It is proof that no
secret value your installation currently holds appeared in it, which is a
smaller claim: a credential you rotated away last month, or one you never told
the manager about, is not something it can recognise.

`meta.json` does not list itself. A file cannot state its own redaction count —
the count is only known once the file exists, and scrubbing it would change the
bytes the count describes — so that number is in the command's output, printed
after the file was scrubbed, rather than inside a file claiming zero.

Every component is scrubbed on its way in — not only the container logs. Most
of what goes into the archive was written long before you ran the command, and
the journal in particular is appended to across every operation the
installation has ever run.

Container logs are the one component treated as unsafe by default: if your
secret values cannot be loaded — a missing sops key, most often — the logs are
left out of the archive entirely and `meta.json` says why. Everything else
still ships.

## Checking something you are sending by hand

The archive is safe by construction. A log you tailed into a file, or a config
you exported to paste into a chat window, is not — and that is the leak this
feature would otherwise watch happen beside it.

```console
$ morzer support redact --check /tmp/paste.txt
2 secret value(s) found in paste.txt
  do not send this file as it is
```

It reports and writes nothing. The file is yours, and rewriting it would
destroy what you were about to send.

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

The archive above is plaintext because the release that produced it declares
nobody to encrypt it to, and the command says so on every run. A release that
does declare recipients gets an archive readable by them alone —
[encrypting it to your vendor](../reference/support-bundle.md#encrypting-it-to-your-vendor)
is that half. It is not something you turn on: whether it happens is your
vendor's declaration, not a flag of yours.

## Reading one back

`morzer support inspect <file>` prints the same table for an archive that
already exists — yours, or one somebody sent you — and says what the signature
beside it established.

```console
$ morzer support inspect support-demo-op_01M03BW5G7SMMHKDFMTC932W5T-20260815T184516Z.tar.zst
  signature verified against this installation's recorded keys
RWSbkfcEJXP0Nt8GsDcJYJaaWlZGC4PFT0OETCB222gEu34G9+PwMepB
```

**The key printed inside the archive is not what it is checked against.** The
archive names the key that signed it, and that name proves nothing: whoever
wrote the archive wrote it. So the check runs against this installation's own
record when you inspect your own archive, or against `--key` when somebody sent
you one — a key you got from them, not from the file. With neither, it prints
the claimed key and says the signature was not checked, which is the honest
answer rather than a tick nobody earned.

An encrypted archive needs `--identity` to list its contents, and the signature
is checked before anything is decrypted — so a file you hold no key for still
tells you where it came from. Nothing is extracted: inspecting an encrypted
bundle leaves no readable copy on the machine doing the inspecting.
