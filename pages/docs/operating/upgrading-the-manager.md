---
title: Upgrading the manager
icon: lucide/arrow-up-circle
summary: Knowing that morzer itself is out of date, and moving every installation on the machine to a newer one
---

# Upgrading the manager

This page is about the `morzer` binary. A product's releases are
[updated](updating.md) separately.

Only the newest minor line of the manager is supported (see
[SECURITY.md](https://github.com/morzecrew/morzer/blob/main/SECURITY.md#supported-versions)).
A fix ships in the next release, and a machine on an older one has to upgrade
to get it.

## Knowing a newer one exists

```sh
morzer version --check
```

```text
morzer 0.11.0
  commit        9ac42a3
  built         2026-10-29T17:38:58Z
  manifest api  selfhost/v1alpha1
  newest        0.12.0  https://github.com/morzecrew/morzer/releases/tag/v0.12.0

  0.11.0 is running and no longer supported; 0.12.0 is the newest release
  upgrade with
    install.sh --version 0.12.0 --require-signature \
      --dir /usr/local/bin
```

It asks GitHub for the newest published release, skipping drafts and
prereleases. Asking is the consent, so no setting gates it. It needs no
installation. Under `--json` the answer is `data.check`, and its `outdated`
says whether to act on it.

To have `doctor` ask as well, an installation has to consent, because the
scheduled `doctor` would otherwise contact GitHub unprompted:

```sh
morzer config set manager.check=true
```

From then on `doctor` runs `manager.version`. It warns while a newer manager is
published, and the scheduled run notifies any target that takes warnings.
Without the setting, the check reports "not checked" and asks nobody.

## Upgrading

Run the install script again over the binary the units run.

1. Find that binary. Every unit names it by path:

    ```sh
    systemctl cat <product>.service | grep ExecStart
    ```

2. Read the release notes of the version you are moving to.

3. Install it into that binary's directory:

    ```sh
    curl -fsSL https://morzecrew.github.io/morzer/install.sh \
      | sh -s -- --version 0.12.0 --require-signature --dir /usr/local/bin
    ```

`--dir` matters. Left to itself, the script picks `/usr/local/bin` or
`~/.local/bin`. A manager installed anywhere else would get a second copy, and
the units would go on running the old one. `doctor`'s remedy and
`version --check` both name the directory of the binary that ran them.

## What happens to what is running

- **The units need nothing.** They name the path, which is unchanged, so the
  next timer to fire runs the new manager. The next `apply`, `start` or `update`
  reconciles the units anyway.
- **An operation already running finishes on the old binary.** Replacing the
  file does not touch a process that has it open.
- **Every installation whose units name this binary moves at once.** One
  binary normally serves them all
  ([several installations](several-installations.md)). An installation whose
  units name another copy keeps running that copy until it is upgraded too.
- **State moves forward only across a schema change.** When the new manager
  brings a newer installation schema, it migrates an installation's state the
  next time it writes it. From then on an older manager refuses that state at
  exit 9, "written by a newer manager", rather than misreading it. The
  changelog's upgrade notes say when the schema changes.

## Going back

Going back means running the install script again with the older `--version`.
If the newer manager changed the installation schema and has since written an
installation's state, the older one refuses that state. Read the release notes
before upgrading rather than planning to undo it.
