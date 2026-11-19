---
title: Running a release on a Mac
icon: lucide/laptop
summary: Declaring that a release runs on macOS, and what in a bundle decides whether it does
---

# Running a release on a Mac

A release is refused on a Mac until it declares `darwin`. Before you declare
it, check five things in the bundle that work on Linux and not on a Mac. What
a Mac changes for the operator, and why, is in
[Platforms](../explanation/platforms.md).

## Declare it

```yaml
requirements:
  platforms: [linux, darwin]
compatibility:
  min_manager_version: "0.18.0"
```

An older manager refuses `platforms` as unknown, so the floor comes with it.
The refusals, and how `platforms` differs from `os`, are in the
[manifest reference](../reference/manifest.md#platforms).

## Name secrets by their variable

On a Mac, secrets render to `/var/run/<product>/secrets`, since there is no
`/run`. Mount them by `<PRODUCT>_SECRETS_DIR`, which says where they went on
either platform:

```yaml
secrets:
  db_password:
    file: ${DEMO_SECRETS_DIR:-/run/demo/secrets}/db_password
```

A Compose file that names `/run/<product>/secrets` literally mounts a path a
Mac does not have, and has not been made for one. Do the same for every host
path: `<PRODUCT>_DATA_DIR`, `<PRODUCT>_CONFIG_FILE` and the rest of the
[Compose variables](../reference/variables.md#compose-interpolation).

## Mark what needs a Linux host

On a Mac, containers run in Docker Desktop's Linux VM. A container's host
network and `/dev/net/tun` are the VM's, so a profile that routes traffic would
route the VM, and look like success. Mark such a profile Linux-only:

```yaml
runtimes:
  compose:
    profiles:
      tun: [compose/tun.yaml]
    platforms:
      tun: [linux]
```

A Mac then refuses `--profile tun`, and runs the release without it.

## Write hooks for both hosts

Hooks run on the host, as root. On a Mac that host has macOS's tools, not
Linux's, and no `/proc` or `systemctl`.

- **Use what both have.** POSIX `sh` and the POSIX options of `sed`, `date`,
  `stat` and `grep`. `sed -i` without a suffix, `date -d` and `stat -c` are
  GNU's and fail on a Mac.
- **Do Linux work in a container.** `docker compose run` and `exec` run the
  same image on either host.
- **Do not count on Homebrew.** A hook's `PATH` on a Mac holds only the
  directories that only root can change, so a tool from Homebrew, or from the
  operator's home, is not found by name. Run it in a container instead.
- **Run `docker` by name.** On a Mac, `PATH` starts with the CLI the manager
  has checked. A hook that names `/usr/local/bin/docker` runs whatever that
  path holds, which root has not checked.

## Do not rely on `owner`

On a Mac, a secret's `owner` is not enforced: every container that mounts a
secret can read it, whatever uid it runs as. A release that keeps one service
from another's secret by `owner` alone keeps nothing apart there. Mount each
secret only into the services that need it.

## Pull every image

A Mac refuses a release whose images travel in the bundle. The manager serves
[bundled images](bundled-images.md) from the Mac's loopback, and Docker
Desktop's daemon runs in a VM with a loopback of its own, so it cannot pull
them. Ship a release for a Mac that marks no image `from: bundle`.

## Check it

`release verify` checks that the keys are well formed. It cannot tell you that
a release runs on a Mac. Install it on one, with Docker Desktop, run
`morzer doctor`, then run a backup and a restore, before you publish it with
`darwin` declared.
