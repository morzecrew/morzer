# Docs publish schemas · the schema URL every modeline names resolves

ERP request M8, on branch `docs-publish-schemas` from `f927b4c` (v0.4.0).
`https://morzecrew.github.io/morzer/schemas/selfhost-v1alpha1-manifest.json`
returned 404. RFC 0013 §5 put that URL in every scaffolded manifest's
`yaml-language-server` modeline, and the generated schemas carry it as their
`$id`. Its decision 4 calls the schema "an already-published artifact", but
nothing ever published it. The site is versioned: mike deploys each minor into
its own directory and owns the root, so `/schemas/` can only exist if something
writes it there.

**Drift count: 0.**

| Class | Test | Meaning |
|---|---|---|
| `discovery` | Could not have been known before code existed | Healthy — the spec was right to be silent |
| `spec-gap` | Could have been known; the spec was silent or at the wrong altitude | The design process missed something |
| `drift` | The spec covered it and it was built otherwise anyway | **A defect** |
| `irreducible` | No amount of design settles it | Stop and spike |

RFC 0013 carries no grades, so its rows are treated as `LOCKED`. The URL it
chose is kept; what was missing is the publication.

```divergence
decision: unlisted
grade: UNLISTED
class: spec-gap
at: 2026-10-17T20:50:33Z
attempt: 1
claim: no step published schemas/ anywhere the site serves; the root is the only home with a bundle's lifetime, for the reason RFC 0022 gave install.sh, so the step that publishes install.sh after mike now publishes schemas/ beside it, replaced whole so a retired schema stops answering
evidence: .github/scripts/publish-site-root.sh:55-56
action: decided
proposal: LOCKED — RFC 0013 decision 5: the generated schemas are published at the documentation site's root under schemas/, the URL their $id and the scaffold's modeline name, by the docs-release step that publishes install.sh
```

The script is renamed from `publish-install-script.sh` to
`publish-site-root.sh`. RFC 0022's phasing still names the old file, as a dated
record of what shipped then, and is left as written.

Not done: the nightly drift job compares only `install.sh` with what the site
serves. A schema missing from the root breaks editor validation, not an install,
so it is not added here.

**Drift count: 0.**
