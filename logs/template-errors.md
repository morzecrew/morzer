# template-errors · template errors in an author's terms

On branch `template-errors` from `ac45dd6`, building Phase 2's second item of
the gap report: a template that fails to render names the field as the
template wrote it and suggests the likely fix, rather than naming the Go type
behind it. No RFC covers it; the area is
[RFC 0013](../rfcs/0013-bundle-authoring-experience.md)'s, whose rows are
treated as `LOCKED`, and none of them settles a message. That covers:

- a render failure at `apply`, and under `verify --render-check`, naming the location, the chain and its owner by template path;
- a "did you mean" for a field, a parameter, a secret and a `secretFile` name, there and in plain `verify`'s reading.

**Drift count: 0.**

| Class | Test | Meaning |
|---|---|---|
| `discovery` | Could not have been known before code existed | Healthy — the spec was right to be silent |
| `spec-gap` | Could have been known; the spec was silent or at the wrong altitude | The design process missed something |
| `drift` | The spec covered it and it was built otherwise anyway | **A defect** |
| `irreducible` | No amount of design settles it | Stop and spike |

An operator sees an error's message and hint, never its cause
(`internal/cli/root.go:506`), so at `apply` a render failure said only
"cannot render template" and which file. text/template's error is a string,
and `renderHint` already reads it by its stable phrases.

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-01T22:30:00Z
attempt: 1
claim: a render failure's message omits where it failed and names Go types, and the only structure text/template offers is its error text
evidence: internal/adapters/render/gotemplate/render.go:143
action: decided
proposal: ASSUMED — the renderer reads text/template's location, action and cause from its error text, as `renderHint` already does, and states an unknown field by the template path of its owner, found by walking the render view's type. A failure it cannot read is quoted as before. `apply` and `--render-check` say it in the same words.
```

A suggestion that is wrong costs an author more than none.

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-01T22:31:00Z
attempt: 1
claim: how near a name must be to a candidate before it is offered is not settled
evidence: internal/cli/root.go:257
action: decided
proposal: ASSUMED — the nearest candidate is offered when it is within a third of the name's length, at least one edit, ignoring case and counting a swap of two neighbours as one edit; nothing is offered otherwise. Candidates are the owner's exported fields and methods, or the map's keys, which for parameters and secrets are what the bundle declares.
```

**Drift count: 0.**
