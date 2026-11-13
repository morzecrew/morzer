# docker-29 · Docker 29's containerd image store

On branch `docker-29` from `main` at `b7d1b9ff`. `ubuntu-latest` became
`ubuntu-26.04`, and with it Docker moved from 28.0.4 to 29.4.2 on the
containerd image store. Since then every Docker job has been red, on `main`
too. This log records the one place where the new store contradicted a design
decision, as opposed to a defect.

**Drift count: 0.**

| Class | Test | Meaning |
|---|---|---|
| `discovery` | Could not have been known before code existed | Healthy — the spec was right to be silent |
| `spec-gap` | Could have been known; the spec was silent or at the wrong altitude | The design process missed something |
| `drift` | The spec covered it and it was built otherwise anyway | **A defect** |
| `irreducible` | No amount of design settles it | Stop and spike |

## RFC 0011 decision 19 on the containerd store

```divergence
decision: 19
grade: LOCKED
class: discovery
at: 2026-11-13T17:00:22Z
attempt: 1
claim: RFC 0011 decision 19 deploys a bundled image under a manager-made alias, because the vendor's digest reference was measured to resolve to nothing after ingest. On Docker 29.4.2's containerd image store (CI, ubuntu-26.04) it resolves. The alias also resolves, and it is still what the deployment runs. RFC 0011's table carries no grades, so the row is treated as LOCKED, and the work halted on it rather than editing the two tripwires that pin the measurement.
evidence: test/suite/ingest_docker_test.go:170
action: halted
proposal: UNLISTED — on the containerd image store the vendor's digest reference resolves after ingest, and on the classic store it does not; the alias is kept on both, and the tripwires read the store and pin each one's measurement
```

The author ruled on 2026-11-13: decision 19 stands on both stores. The
alias is what a deployment runs on either store, so nothing changes in
behaviour. The two tripwires now read the store from `docker info`, by
looking for `driver-type=io.containerd.snapshotter.v1`:
- `test/suite/ingest_docker_test.go` (through `dockerlab.ContainerdStore`);
- `.github/scripts/acceptance.sh`.

On the classic store the vendor's reference must still resolve to nothing. On
the containerd store it must resolve, and the alias must still be what runs.
A store that changes its behaviour then fails a tripwire rather than passing
silently. The proposal above is the RFC 0011 row this owes.
