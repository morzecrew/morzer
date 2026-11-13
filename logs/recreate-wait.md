# recreate-wait · #351, a re-create waits on its dependencies' health

On branch `recreate-wait-named` from `main` at `60c9d74d`, fixing
[#351](https://github.com/morzecrew/morzer/issues/351): `config set` re-creates
the services its parameters declare with `up --wait <service>`, and Compose's
wait covers the named services' dependencies too. A dependency that is already
unhealthy fails the change, and the rollback, which runs the same `up`, fails
the same way, ending at manual intervention. `restart` shares `d.recreate`
([RFC 0050](../rfcs/0050-restarting-a-service-and-an-interactive-exec.md)
decision 1) and fails the same way.

The issue suggests `--no-deps`. RFC 0050 decision 1 (`ASSUMED`) keeps it off:
a dependency that is down is then neither started nor resolved. The author
ruled 2026-11-13, in the session that took the issue: dependencies are still
converged as any `up` converges them, and the wait covers only the services the
re-create names.

**Drift count: 0.**

| Class | Test | Meaning |
|---|---|---|
| `discovery` | Could not have been known before code existed | Healthy — the spec was right to be silent |
| `spec-gap` | Could have been known; the spec was silent or at the wrong altitude | The design process missed something |
| `drift` | The spec covered it and it was built otherwise anyway | **A defect** |
| `irreducible` | No amount of design settles it | Stop and spike |

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-13T20:32:29Z
attempt: 1
claim: RFC 0050 decision 1 says what a re-create converges, and nothing says what its wait covers. Compose's `up --wait <service>` waits on the dependencies it found running as well, so a dependency already unhealthy fails a change that does not touch it, and the compensation with it (#351). Ruled by the author 2026-11-13: an Up that names services and waits starts and converges their dependencies as today, and waits only on the named services. The Compose adapter runs `up --detach` for the named services, then `up --detach --wait --no-deps` for the same names, which re-creates nothing and waits on them alone. A dependency the release declares with `condition: service_healthy` still holds the start, because the release asks for that.
evidence: internal/lifecycle/ops/config.go:374-384
action: decided
proposal: UNLISTED — an Up that names services and waits waits on those services only; their dependencies are started and converged as before, and not waited on
```
