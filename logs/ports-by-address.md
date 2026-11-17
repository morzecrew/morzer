# ports-by-address · RFC 0059, ports a release needs by address, protocol and profile

On branch `rfc-0059-ports` from `main` at `8b3492a1`, executing
[RFC 0059](../rfcs/0059-ports-a-release-needs-by-address-protocol-and-profile.md)
as its §12 asks, in one pull request, with decisions 5 and 9 as ruled on
2026-11-16. §10's measurement was made first, on 2026-11-17, and is recorded
in the RFC's §13. The plan found three questions the RFC does not settle, and
the author ruled them on 2026-11-17 as the plan leaned.

**Drift count: 0.**

Where building this disagreed with the design for it, written at the moment it
happened. Nothing here is revised afterwards to agree with what was later
settled, and nothing here has been folded back into the spec's own text. The
rows proposed below are put forward for the author to accept or refuse.

| Class | Test | Meaning |
|---|---|---|
| `discovery` | Could not have been known before code existed | Healthy — the spec was right to be silent |
| `spec-gap` | Could have been known; the spec was silent or at the wrong altitude | The design process missed something |
| `drift` | The spec covered it and it was built otherwise anyway | **A defect** |
| `irreducible` | No amount of design settles it | Stop and spike |

## The measurement, and the plan's questions

```divergence
decision: 10
grade: ASSUMED
kind: resolved
at: 2026-11-17T19:04:29Z
attempt: 1
claim: Decision 10 has the probe follow Docker if §10's measurement shows the two disagree for UDP/53. They do: on Ubuntu 24.04 beside systemd-resolved's stub, a plain UDP bind of port 53 on every address is refused, and Docker 28.0.4 publishes `53:53/udp`, binding with `SO_REUSEADDR`. A UDP bind with `SO_REUSEADDR` agrees with Docker in every case measured, and TCP needs no change, since both refuse 53/tcp there. Ruled by the author: the probe sets `SO_REUSEADDR` on every UDP bind, and decision 11 records it. Refused: binding plainly and, on `EADDRINUSE`, retrying with `SO_REUSEADDR` to warn rather than fail, since that warns on every Ubuntu machine with the stub on.
evidence: rfcs/0059-ports-a-release-needs-by-address-protocol-and-profile.md:433-477
action: decided
```

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-17T19:04:29Z
attempt: 1
claim: §5.2 gives the manifest's templates `has`, "the same function as in the configuration templates, in the same argument order", and does not say where it lives. The configuration templates' `has` is in the renderer, an adapter, and `internal/domain` resolves the manifest's templates and cannot import it. Ruled by the author as the plan leaned: `domain` has its own `has`, `has "xdns" .Profiles`, and a test holds the two to the same answers.
evidence: internal/domain/manifest_template.go:78
action: decided
proposal: ASSUMED — the manifest's `has` is defined in `domain`, with the configuration templates' argument order
```

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-17T19:04:29Z
attempt: 1
claim: §5.3 has `EADDRNOTAVAIL` reported with "a hint naming the parameter if the entry interpolates one", and does not say how the probe, which sees a resolved entry, knows the parameter. Ruled by the author as the plan leaned: resolution keeps, beside each resolved entry, the parameters its template names, read from the template's parse tree, and the hint names them.
evidence: internal/lifecycle/preflight/preflight.go:365
action: decided
proposal: ASSUMED — a resolved port entry carries the parameters its template names, for the hint of a refusal
```

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-17T19:04:29Z
attempt: 1
claim: §7 asks for "an authoring note on sharing a machine with a web server the operator already runs", and does not say where it goes. Ruled by the author as the plan leaned: a how-to of its own, `authoring/sharing-a-machine.md`, linked from `requirements.ports` in the manifest reference.
evidence: rfcs/0059-ports-a-release-needs-by-address-protocol-and-profile.md:356-358
action: decided
proposal: ASSUMED — sharing a machine with the operator's own web server is an authoring how-to
```
