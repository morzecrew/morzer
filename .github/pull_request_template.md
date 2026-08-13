<!--
Title: <gitmoji> <type>[scope][!]: <description>   (gitmoji-conventional)
-->

## Summary

<!-- What changed and why, in 2–4 sentences. -->

Closes #
RFC: <!-- rfcs/NNNN, or "none — reason it's small enough to skip" -->

## Type

- [ ] Feature
- [ ] Fix
- [ ] Refactor (no behaviour change)
- [ ] Docs
- [ ] Chore / CI / dependencies
- [ ] Breaking change

## Design

- [ ] Behaviour matches the accepted RFC
- [ ] The RFC is amended in this PR, because the design changed

## Lifecycle invariants

- [ ] Every new step appears in `--dry-run` plan output
- [ ] Every new step is journaled and verifiable after the fact
- [ ] Failure mid-step leaves a recoverable installation; re-running converges
- [ ] An undo path exists (rollback / restore), or the step is deliberately
      one-way and says so at the prompt

## Compatibility

- [ ] Manifest schema (`schemas/`) unchanged
- [ ] Schema changed — a bundle built before this commit still installs
- [ ] Hook ABI unchanged, or versioned and documented
- [ ] New or changed exit codes are in the reference docs

## Secrets & safety

- [ ] No secret value reaches stdout, stderr, the journal, or an error message
- [ ] Destructive paths confirm first, or take a backup first
- [ ] Any new external tool or version requirement is declared in the manifest,
      checked in preflight, and reported by `doctor`

## Verification

- [ ] `just ci` green
- [ ] `just demo`, `just demo-plan`, `just demo-recovery` still pass
- [ ] The acceptance run against real Docker exercises this path

What I ran and what it proved:

## Risk & rollback

<!-- What a bad merge does to a live installation, and how an operator gets
     out of it without the fixed binary. -->