---
title: Running on Podman
icon: lucide/box
summary: An installation on rootless Podman, each service a unit of your own systemd manager, for a release that declares the quadlet runtime
---

# Running an installation on Podman

A release that declares the `quadlet` runtime runs on rootless Podman instead
of Docker. Each service is a Quadlet `.container` unit that your own systemd
manager starts, with no daemon in between. The installation belongs to you,
as on [rootless Docker](running-as-your-own-user.md), and no `morzer` command
needs `sudo`.

This runtime exists on Linux only. It is new: the acceptance run does not
exercise it yet.

## Before you start

- **Podman with its Quadlet generator**, which Podman installs as a systemd
  user generator. `doctor` with no installation reports `podman` and
  `systemctl`.
- **Lingering**, so your units start at boot and keep running after you log
  out:

    ```sh
    sudo loginctl enable-linger "$USER"
    ```

- **A login session of your own**, which sets `XDG_RUNTIME_DIR`. The units and
  the rendered secrets live there, on your tmpfs. `sudo -u` does not set it.
- **The manager where only you and root can replace it**, such as
  `~/.local/bin`, which is where the installer puts it when run without
  `sudo`.

Root cannot run this runtime. An installation of it runs as the user who
owns it, in that user's manager, and the adapter refuses root.

## Create it

```sh
morzer init --release ./demo-1.4.0 --runtime quadlet \
    --recovery-recipient age1...
```

`--runtime quadlet` is needed only when the release declares Compose as
well. A release declaring `quadlet` alone installs with it. Either way, the
runtime is fixed for the life of the installation. `init` refuses a runtime the
release does not declare, and a runtime the manager cannot drive on this
machine, before it writes anything.

The installation's product name is the release's `metadata.name`. Every
Quadlet file in the release starts with it, so an installation under another
name could not run them.

Then start it as any installation:

```sh
morzer apply
```

## What is different

- **The units live in your runtime directory**, under
  `$XDG_RUNTIME_DIR/containers/systemd/morzer/<product>/`, which a reboot
  clears. At boot the product's unit runs `apply --startup`, which renders
  the secrets and parameters, installs the units again and starts them.
  Nothing from an earlier boot starts on its own.
- **Ask systemd about a service** with
  `systemctl --user status <product>-<service>`. Its container is
  `systemd-<product>-<service>` unless the release names it, and a volume
  file's volume is `systemd-<product>-<name>`.
- **Logs come from your journal.** `morzer logs` reads each service's
  container lines from it, and so does
  `journalctl --user CONTAINER_NAME=<container>`.
- **A stop removes a service's container**, as Quadlet always does, and a
  start creates it again from the same unit and the values it was last
  applied with. Keep data in named volumes or bind mounts: anything written
  inside a container is gone at its next stop.
- **A one-shot**, a migration or a task, runs as a copy of its service's unit,
  with a container of its own beside the running service.
- **Images come from a registry.** A release that carries its images in the
  bundle is refused, since this runtime cannot load them yet.

## What doctor checks

With no installation, `doctor` reports the tools of every runtime this
manager drives here. A missing tool is a warning naming its runtime, so a
machine with Docker and no Podman is not reported unfit. The
`runtime.tools` check fails only when no runtime has every tool it needs.

With an installation, `doctor` checks what it checks on any installation of
your own. A Quadlet installation has no daemon of its own, so there is no
`runtime.engine-scope` check.

## Next

- [`runtimes.quadlet`](../reference/manifest.md#quadlet) is what a release
  ships for this runtime.
- [Running as your own user](running-as-your-own-user.md) covers the layout
  every installation of your own has.
