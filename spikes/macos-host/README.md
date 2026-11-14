# Spike — macOS as a host, with Docker Desktop

Answers RFC 0055 §10, S1–S6, which P1 is gated on (§12). The deliverable is
the measurements appended to that RFC and the rulings on decisions 4, 6, 7 and
8; this directory is what produced them, kept so the numbers can be re-run
rather than believed.

## Why a real Mac

Every question here is about Docker Desktop's file sharing, its socket, or
launchd at boot. None of them can be asked of a Linux host or a CI runner
without an engine, and the documentation settles none of them. Any
Apple-silicon Mac answers them; `info` records which one did.

## Before running it

- Read `run.sh`. It runs as root. It creates `/etc/morzer-spike`,
  `/var/lib/morzer-spike`, `/opt/morzer-spike`, `/var/run/morzer-spike` and
  two LaunchDaemons labelled `dev.morzer.spike*`, and `clean` removes all of
  them. It touches no other container, image or setting.
- Docker Desktop installed and running, with its settings as they are. S1 and
  S3 ask what works **by default**, so do not add file-sharing directories or
  change the socket setting before the first run.
- Settings > General > "Start Docker Desktop when you sign in" on. The reboot
  needs it.
- The power connected and the lid open from `s4` to `after-reboot`. A Mac
  that sleeps pauses launchd's retries, and the timings would measure the
  sleep.

## Running it

From a checkout of this repository, in this order:

    sudo spikes/macos-host/run.sh               # info, S3, S1, S2, S6: about five minutes
    sudo spikes/macos-host/run.sh s4            # guided: asks you to quit and start Docker Desktop
    sudo spikes/macos-host/run.sh before-reboot # then restart, as it says
    sudo spikes/macos-host/run.sh after-reboot  # asks the time you logged in
    sudo spikes/macos-host/run.sh clean

Every phase appends to `report.txt` beside `run.sh`, and that file is the
result. A phase can be run again on its own, and the report keeps each run.

If S3 reports that root's clean environment does not reach the engine, turn on
Settings > Advanced > "Allow the default Docker socket to be used", let Docker
Desktop restart, and run `s3`, `s1` and `s2` again. The report then holds both
answers, which is what S3 asks: whether it works, and what enabling it takes.

## What each phase answers

| Phase | Question (RFC 0055 §10) | Can overturn |
| --- | --- | --- |
| `s3` | Does root's `docker`, started with launchd's environment, reach Docker Desktop through `/var/run/docker.sock`? Does it find the Compose plugin? | Decision 7 |
| `s1` | Do binds of `/etc`, `/var/lib`, `/opt` and `/var/run` paths work, and by which spelling, `/etc/…` or `/private/etc/…`? What owner does a file a container writes get on the Mac? | Decision 4 |
| `s2` | What owner and mode does a container see for a secret file, and can uids 65532, 0 and 1000 read it? | Decision 6 |
| `s4` | Does a LaunchDaemon with `RunAtLoad`, `KeepAlive {SuccessfulExit: false}` and `ThrottleInterval` 30 retry until the engine answers, and stop on the exit-12 record? Does `launchctl kickstart` from it start an overdue backup? | Decision 8 |
| `before-reboot`, `after-reboot` | S5: is `/private/var/run` cleared at boot? S4 again, across a real boot: when does the job first run, relative to your login? | §5.4, decision 8 |
| `s6` | Does a darwin/arm64 binary cross-compiled on Linux, fetched with `curl`, run? Does it still run after the rename `self-update` does? | Decision 12 |

## What it sets up

**S1** binds each spelling of each directory into a container through
Compose, reads a file and writes one, and reports the exit status, the error
when there is one, and the owner of the written file on the Mac.

**S2** renders a file the way the manager renders a secret, in `0700` root
directories, `0400` and owned by `65532:65532`, and mounts it as a Compose
`secrets:` file into containers running as 65532, 0 and 1000. Four more
variants each take one condition away (the path, the directory mode, the file
mode, the owner), so a `DENIED` can be told apart from a uid mapping. RFC 0055
§5.5 names three outcomes. A fourth is possible: Docker Desktop's file sharing
runs as the logged-in user, so a file that user cannot read may be denied to
every container uid. The variants are there to show that case if it happens.

**S4** installs `dev.morzer.spike`, whose program stands in for `apply
--startup`. It exits 7 while the engine does not answer, exits 12 once when
told to and records it, exits 0 while the record stands, and once converged
kickstarts `dev.morzer.spike-backup` if its stamp is missing and waits for it.
Each attempt logs launchd's `PATH`, `HOME` and the socket it saw.

**S6** builds two versions of the manager in a `golang` container, serves them
from a busybox `httpd` on localhost and fetches them with `curl`. It reports
the quarantine attribute, the signature and whether each runs. It runs the
binary after a rename over it, which is what `self-update` does. It runs it
after an overwrite of the same file in place, where macOS is expected to kill
the process (exit 137). It runs it with a browser's quarantine attribute
added, which may show a dialog; dismiss it.

## What it is not

The boot job is a shell script, not the manager. S4 measures launchd's
behaviour and the engine's availability, not `apply --startup`.

S1 and S2 use the engine as root when S3 says root reaches it, and as you
otherwise, so a failed S3 does not hide the other answers. Every row says which
it was.

With FileVault on, macOS keeps the data volume locked after a restart until
someone logs in, and `/Library/LaunchDaemons` is on that volume. If that holds,
nothing in this spike, and no engine, runs before a login after a power cut.
`after-reboot` compares the first attempt's time with your login to say
whether it does.
