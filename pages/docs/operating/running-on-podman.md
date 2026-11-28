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

- **Podman 5.3 or later, with its Quadlet generator**, which Podman installs
  as a systemd user generator. An older Quadlet does not read the directory
  the units are installed in, or a parameter in `PublishPort=`. Ubuntu 24.04
  ships Podman 4.9.3: there, a release that also declares Compose installs
  with `--runtime compose`, and one that declares only `quadlet` is refused.
  `doctor` with no installation reports `podman`, against 5.3, and
  `systemctl`.
- **Lingering**, so your units start at boot and keep running after you log
  out:

    ```sh
    sudo loginctl enable-linger "$USER"
    ```

- **A login session of your own**, which sets `XDG_RUNTIME_DIR`. The units and
  the rendered secrets live there, on your tmpfs. `sudo -u` does not set it.
- **One data home for you and your systemd manager.** Podman keeps its
  containers under `$XDG_DATA_HOME`, and your units run with your manager's
  environment, not your shell's. If the two differ, `morzer` looks for the
  containers in a store the units do not use, and `apply` waits for them
  until its deadline. Leave `XDG_DATA_HOME` unset, or set it for your manager
  too, in `~/.config/environment.d/`.
- **The manager where only you and root can replace it**, such as
  `~/.local/bin`, which is where the installer puts it when run without
  `sudo`.

Root cannot run this runtime. An installation of it runs as the user who
owns it, in that user's manager, so `init` run as root refuses it, and
`doctor` run as root does not report its tools.

## Create it

```sh
morzer init --release ./demo-1.4.0 --runtime quadlet \
    --recovery-recipient age1...
```

`--runtime quadlet` is needed only when the release declares Compose as
well. A release declaring `quadlet` alone installs with it. Either way, the
runtime is fixed for the life of the installation. `init` refuses a runtime the
release does not declare, a runtime the manager cannot drive on this machine,
and a Podman older than 5.3, before it writes anything.

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
- **A release can carry its images in the bundle.** `init` and `update` load
  them into your Podman store, as they load Docker's: Podman pulls each one
  out of the bundle, served on `127.0.0.1`, and checks every blob against its
  digest. Podman checks TLS even on `127.0.0.1`, so that pull alone runs with
  `--tls-verify=false`. A pull from a registry never does.
- **`CONTAINER_HOST` and `CONTAINER_CONNECTION` are ignored.** morzer's
  `podman` commands and its units both run on your own Podman, whichever
  service your shell or your systemd manager names.

## What doctor checks

With no installation, `doctor` reports the tools of every runtime this
manager drives here. A missing tool is a warning naming its runtime, so a
machine with Docker and no Podman is not reported unfit. The
`runtime.tools` check fails only when no runtime has every tool it needs.

With an installation, `doctor` checks what it checks on any installation of
your own. A Quadlet installation has no daemon of its own, so there is no
`runtime.engine-scope` check. `runtime.tool-floor` fails while Podman is
missing or older than 5.3, and every command that changes the installation
refuses an older one too.

`network.registry` asks the registry of the release's first pulled image for
its manifest, with `podman manifest inspect`, which pulls nothing. A registry
that refuses is a warning whose remedy is `podman login <registry>`, run as
you. Docker Hub refuses the same way for a repository that does not exist.

## Next

- [`runtimes.quadlet`](../reference/manifest.md#quadlet) is what a release
  ships for this runtime.
- [Running as your own user](running-as-your-own-user.md) covers the layout
  every installation of your own has.
