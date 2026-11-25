---
title: Running as your own user
icon: lucide/user
summary: An installation in your own home directory, run by your own systemd manager on rootless Docker, with no command that needs root
---

# Running an installation as your own user

An installation does not have to belong to root. Run `morzer init` as an
ordinary user and the installation lives in your own directories, its units
run under your own systemd manager, and no command needs `sudo`. Nothing in it
is root's, so nothing in it hands anyone root.

With Docker Compose this is an untested configuration: it needs rootless
Docker, and the acceptance run does not exercise it. It exists on Linux only;
on a Mac the manager runs as root.

## Before you start

- **Rootless Docker for your user**, selected as your default context:

    ```sh
    dockerd-rootless-setuptool.sh install
    docker context use rootless
    ```

    The manager refuses a daemon that is not your own. On the system's
    daemon, reached through the `docker` group, your installation and a
    system installation of the same product would select the same Compose
    project and share its containers and volumes. Select it with a context
    rather than `DOCKER_HOST` exported in a shell: your units read the
    context too.

- **Lingering**, so your units start at boot and keep running after you log
  out:

    ```sh
    sudo loginctl enable-linger "$USER"
    ```

- **A login session of your own**, which sets `XDG_RUNTIME_DIR`. Decrypted
  secrets are rendered there, on your tmpfs. A command without it is refused
  rather than rendering them on disk, and `sudo -u` does not set it.

- **The manager where only you and root can replace it**, such as
  `~/.local/bin`, which is where the installer puts it when run without
  `sudo`:

    ```sh
    curl -fsSL https://morzecrew.github.io/morzer/install.sh | sh
    ```

## Create it

```sh
morzer init --release ./demo-1.4.0 --profile embedded \
    --recovery-recipient age1...
```

No `sudo`, and no flag naming where: `init` run by anyone but root creates the
installation in your own tree, and installs your units. Every later command
you run with no flag finds it there.

| Directory | Holds |
| --- | --- |
| `$XDG_DATA_HOME/morzer/etc/<product>` | Configuration and the encrypted secret state |
| `$XDG_DATA_HOME/morzer/var/lib/<product>` | Application data, backups and the operation journal |
| `$XDG_DATA_HOME/morzer/opt/<product>` | Releases |
| `$XDG_RUNTIME_DIR/morzer/<product>` | Rendered secrets and generated files, on tmpfs |
| `$XDG_CONFIG_HOME/systemd/user` | The units |

`$XDG_DATA_HOME` is `~/.local/share` when it is unset, and `$XDG_CONFIG_HOME`
is `~/.config`. The units carry the data home they were written with, so they
find the same installation if you set it only in your shell.

## Which installation a command manages

Who runs the command decides:

- **Root**, including under `sudo`, manages the system's installations, as
  always.
- **You** manage your own when your tree holds one, and for `init`.
- **Otherwise** you manage the system's, as before: with no installation of
  your own, `morzer status` still reads the system's.

`--config` still names an installation outright, and a path inside your tree
is yours. `morzer ls` says which you are looking at: each row carries
`"layout": "user"`, and the table says so beneath it.

## What is different

- **Your units are your manager's.** Ask about them with
  `systemctl --user status <product>` and `systemctl --user list-timers`. The
  product's unit starts with your manager and after `docker.service`, which
  for you is rootless Docker's own unit.
- **Ports below 1024** cannot be bound by a rootless engine unless
  `net.ipv4.ip_unprivileged_port_start` is lowered to them, or the engine has
  `CAP_NET_BIND_SERVICE`.
- **Hooks run as you**, with your permissions and nobody else's.
- **Secrets reach the bundle** through `<PRODUCT>_SECRETS_DIR`, as they do in
  any installation (see [Variables](../reference/variables.md)), so a bundle
  that names no `/run` path runs unchanged. A secret that declares an
  [`owner`](../reference/manifest.md#owner) other than you is refused, since
  only root can give a file to another user.

## What doctor checks

`doctor` adds three checks for an installation of your own, and holds the
binary your units run to the same rule as a system installation's, with you
in root's place:

| Check | What it means |
| --- | --- |
| `runtime.engine-scope` | Fails while the Docker daemon is not your own. `init` refuses there too. |
| `system.linger` | Warns while your account does not linger: your units stop when you log out and do not start at boot. |
| `network.unprivileged-ports` | Warns while the release publishes a port below `net.ipv4.ip_unprivileged_port_start`. |
| `manager.binary` | Fails while anyone but you and root could replace the binary your units run. |

## Next

- [Several installations](several-installations.md): what two installations
  on one machine share, which still holds for yours beside the system's.
- [Commands](../reference/commands.md#which-installation-a-command-acts-on):
  every way a command is pointed at an installation.
