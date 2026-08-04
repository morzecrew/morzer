---
title: What a bundle is
icon: lucide/package
summary: The contract a release bundle satisfies, and the division of labour it draws
---

# Authoring a release bundle

You ship a product. Your users run it on machines you do not have access to.
A **release bundle** is what you hand them: an immutable archive describing one
version of your product completely enough that a manager on their machine can
install, configure, update, back up and roll it back without you.

```text
release/
├── manifest.yaml       the contract — api_version: selfhost/v1alpha1
├── compose/            your service topology
├── templates/          configuration templates + the secret schema
├── hooks/              your product-specific logic
└── VERSION
```

## The division of labour

This is the whole design, and everything else follows from it:

| The manager owns | You own |
| --- | --- |
| Ordering, atomicity, journaling, compensation | What the product *is* |
| Taking the lock, taking the backup | How to back up your data |
| Deciding *whether* an update is safe | Declaring what makes it safe |
| Running hooks and reporting what they said | What the hooks do |
| Encrypting, rendering and rotating secrets | Declaring which secrets exist |

The manager never learns anything product-specific. That is not modesty — it is
what lets a bug fix in the manager reach every product without a coordinated
release, and what lets your product change shape without a manager that knows
about it.

## The four things you declare

**Identity and requirements.** Your name and version, the architectures and OS
versions you support, the tools and their minimum versions, memory, disk, ports.
The manager refuses to install on a machine that does not meet them, before
anything is written.

**Images, pinned by digest.** A bare tag is rejected at load time. An unpinned
image makes a release mutable, and a mutable release makes rollback meaningless
— the same version could produce a different system on a different day.

**Compatibility.** What this release can be installed over, which database
schemas it can read, and whether returning to the previous release is safe.
These are the declarations that let a manager decide an update is unsafe
*before* it starts, rather than discovering it halfway.

**Operations.** Executables in your bundle that the manager invokes at defined
points: migrate, smoke test, backup, restore, health checks. See
[Hooks](hooks.md).

## Start from something that works

The example bundle in the repository is a complete, valid bundle that this
project's test suite runs against on every commit — including an acceptance run
that installs it against real Docker, updates it, and rolls it back.

```sh
git clone https://github.com/morzecrew/morzer
cp -r morzer/testdata/bundle ./my-product
morzer release verify ./my-product
```

Every example on these pages is extracted from that bundle at build time, so
nothing here can drift from something that demonstrably works. The same is true
of the [three-tier example](a-three-tier-bundle.md), which the acceptance run
installs, reconfigures and probes on every change.

<div class="grid cards" markdown>

-   :lucide-file-code:{ .lg .middle } **Build your first bundle**

    ---

    [:octicons-arrow-right-24: Your first bundle](your-first-bundle.md)

-   :lucide-layers:{ .lg .middle } **Two tiers and a database**

    ---

    [:octicons-arrow-right-24: A three-tier bundle](a-three-tier-bundle.md)

-   :lucide-plug:{ .lg .middle } **Write the hooks**

    ---

    [:octicons-arrow-right-24: Hooks](hooks.md)

-   :lucide-upload:{ .lg .middle } **Sign and publish it**

    ---

    [:octicons-arrow-right-24: Publishing](publishing.md)

-   :lucide-list:{ .lg .middle } **Every field**

    ---

    [:octicons-arrow-right-24: Manifest reference](../reference/manifest.md)

</div>
