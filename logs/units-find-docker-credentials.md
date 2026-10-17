# Units find docker credentials · the update timer reads root's docker login

Found while checking ERP request M9's last point, on branch
`units-find-docker-credentials` from `f927b4c` (v0.4.0). The request asked
whether root's units find `/root/.docker/config.json` for a private registry.
They do not:

- `man systemd.exec` (systemd 261): "$HOME, $LOGNAME, and $SHELL are only set
  for the units that have User= set". The units this manager renders set no
  `User=` and no `Environment=` (`internal/adapters/supervisor/systemd/systemd.go:458`,
  `:482`, `:522`, `:578`).
- oras's `NewStoreFromDocker` reads `DOCKER_CONFIG` and then `os.UserHomeDir()`,
  which fails with `HOME` unset (oras-go v2.6.3,
  `registry/remote/credentials/store.go:200-214`). The adapter treats a failure
  there as "no configuration" and fetches anonymously
  (`internal/adapters/source/oci/oci.go:500-510` before this branch).
- The `docker` CLI falls back to the password database. With `HOME` and
  `DOCKER_CONFIG` unset, `strace -e openat docker version` opens
  `/home/<user>/.docker/config.json`, the passwd home. So in the same timer run,
  `docker compose pull` authenticates and the manager's channel fetch does not.

`TestRegistryCredentialsAreFoundWithoutHOME` failed against a resolver that
copied oras's rule, and passes with the fallback.

**Drift count: 0.**

| Class | Test | Meaning |
|---|---|---|
| `discovery` | Could not have been known before code existed | Healthy — the spec was right to be silent |
| `spec-gap` | Could have been known; the spec was silent or at the wrong altitude | The design process missed something |
| `drift` | The spec covered it and it was built otherwise anyway | **A defect** |
| `irreducible` | No amount of design settles it | Stop and spike |

The fix is in the adapter rather than in the units. An `Environment=HOME=%h`
line would fix the four timers and nothing else that runs without `HOME`, and
it would leave the manager and the `docker` CLI with two rules for one file.

```divergence
decision: unlisted
grade: UNLISTED
class: discovery
at: 2026-10-17T20:58:53Z
attempt: 1
claim: the registry credential file is resolved by the docker CLI's rule, DOCKER_CONFIG then HOME then the password database, instead of oras's, which stops at HOME and so read nothing under every systemd unit the manager installs
evidence: internal/adapters/source/oci/oci.go:538-542
action: decided
proposal: ASSUMED — the manager reads registry credentials from the file the docker CLI would read for the same user and environment
```

**Drift count: 0.**
