# definition-drift · RFC 0044 P2, services whose definition drifted

On branch `definition-drift` from `4496ab3`, building
[RFC 0044](../rfcs/0044-configuration-drift-an-operator-can-see.md) P2 as
ruled by the author on 2026-10-31: §5.4. That covers:

- `ports.DefinitionDrifter`, optional and type-asserted, and its Compose implementation;
- the drifted services in `config diff`, under their own heading, and in `--json` as `services`;
- the `doctor` check `runtime.definition`;
- the P2 tests of §6, and the docs of §7 that P2 reaches.

**Drift count: 0.**

| Class | Test | Meaning |
|---|---|---|
| `discovery` | Could not have been known before code existed | Healthy — the spec was right to be silent |
| `spec-gap` | Could have been known; the spec was silent or at the wrong altitude | The design process missed something |
| `drift` | The spec covered it and it was built otherwise anyway | **A defect** |
| `irreducible` | No amount of design settles it | Stop and spike |

Measured with Docker 29.7.2 and Compose 5.5.1 before any code:
`docker compose config --hash '*'` prints `<service> <hash>` for each service
whose profile is active, and `docker compose ps --all --format json` carries
each container's labels as one comma-separated `Labels` string, with
`com.docker.compose.config-hash` among them. A stopped container keeps its
label. A container `compose run` started is listed too, labelled
`com.docker.compose.oneoff=True`, with a hash of its own that never matches the
service's.

Four things §5.4 leaves to the build, decided while planning and before any
code.

```divergence
decision: 11
grade: ASSUMED
class: spec-gap
at: 2026-11-01T08:41:00Z
attempt: 1
claim: a one-off container `compose run` started carries the service's name and a hash that never matches the declared one, so counting it reports every task run as drift
evidence: internal/adapters/runtime/compose/compose.go:624
action: departed
proposal: ASSUMED — a container labelled `com.docker.compose.oneoff=True` is not compared; `up` never re-creates one.
```

Decision 3 makes `config.drift` warn because a hand edit can be deliberate, and
§5.4 names `runtime.definition` without its severity.

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-01T08:42:00Z
attempt: 1
claim: whether `runtime.definition` warns or fails is not settled
evidence: rfcs/0044-configuration-drift-an-operator-can-see.md:179
action: decided
proposal: ASSUMED — `runtime.definition` warns, as `config.drift` does: a service created by hand, or by an older manager, may be what an operator meant, and the next `apply` re-creates it either way.
```

Decision 4 holds an unattended update while a configuration file differs, and
§5.3 names `configComparison` as what the tick checks.

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-01T08:43:00Z
attempt: 1
claim: whether a service whose definition drifted holds an unattended update is not settled
evidence: rfcs/0044-configuration-drift-an-operator-can-see.md:166
action: decided
proposal: ASSUMED — it does not. An update re-creates the services from the new release's definition whatever they were created from, and nothing on disk is lost by it, which is the whole of decision 4's reason to hold.
```

§5.4 adds the services to `--json` as `services`, and says nothing of how a
reader tells "none drifted" from "not compared".

```divergence
decision: 11
grade: ASSUMED
class: spec-gap
at: 2026-11-01T08:44:00Z
attempt: 1
claim: an empty `services` reads the same whether no service drifted or the runtime could not be asked
evidence: rfcs/0044-configuration-drift-an-operator-can-see.md:179
action: departed
proposal: ASSUMED — `services` lists the drifted services, each `{service, declared, running}`, and is `[]` when none did; `services_compared` is false when the runtime could not be asked, and the text output says why.
```

**Drift count: 0.**

The `LOCKED` row this branch touches, honored: decision 1 defines drift as a
configuration target's bytes, the gate still holds on it, and a service created
from another definition is not drift in that sense.

```divergence
decision: 4
grade: LOCKED
kind: resolved
at: 2026-11-01T08:51:00Z
attempt: 1
claim: a configuration file that differs still holds an unattended update; the services of §5.4 do not, by the 08:43 entry
evidence: internal/lifecycle/ops/channel.go:523
action: decided
```

**Drift count: 0.**
