# mac-bundle-relay · RFC 0060, a Mac loads a bundle's images through a relay

On branch `rfc-0060-mac-relay` from `main` at `4759b6fd`, executing
[RFC 0060](../rfcs/0060-a-mac-loads-a-bundle-s-images-through-a-relay.md)
as its §12 asks, in one pull request, with decisions 3 and 8 as ruled on
2026-11-19 and decisions 15 to 21 as ruled on 2026-11-20. The measurements
decisions 12 and 13 ask for came first, on 2026-11-20, and are in the RFC's
§13.

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

## The measurements, before the code

```divergence
decision: 12
grade: ASSUMED
kind: resolved
at: 2026-11-20T09:50:49Z
attempt: 1
claim: Measured as decision 12 asks, before the code. On the containerd image store, the Mac's, the helper saved and loaded as the offline page says does not answer to its pinned reference, and its ID is the pinned digest, by which it runs. On the classic store, measured on Linux (decision 21), it answers to neither. So the offline page's procedure fails on every store today, and decision 18's resolver is what is built: the reference, else the ID where it is the pinned digest, else a refusal naming both remedies.
evidence: rfcs/0060-a-mac-loads-a-bundle-s-images-through-a-relay.md:337-372
action: decided
```

```divergence
decision: 13
grade: ASSUMED
kind: resolved
at: 2026-11-20T09:50:49Z
attempt: 1
claim: Measured as §10 asks: Docker Desktop's settings carried no key for its opt-in host networking or for Enhanced Container Isolation, and the relay carried the pull with both off, so it needs neither turned on. Built on that: a relay that is not created is refused naming Enhanced Container Isolation, which Docker documents as refusing `--network host` and which is the one setting known to stop the relay, and a request through a running relay that is not answered is refused naming decision 14's three settings.
evidence: rfcs/0060-a-mac-loads-a-bundle-s-images-through-a-relay.md:338-362
action: decided
```

```divergence
decision: 17
grade: ASSUMED
kind: resolved
at: 2026-11-20T09:50:49Z
attempt: 1
claim: Decision 9's bind was measured on the Mac in the form decision 17 names: the bound relay carried the pull, a bridge network was refused at the VM's gateway, and an unbound control was reached. What is built is the rest of decision 17: the adapter's test pins `-s 127.0.0.1` in the relay's command, and the acceptance run checks that no relay is left once the air-gapped `init` and `apply` end.
evidence: rfcs/0060-a-mac-loads-a-bundle-s-images-through-a-relay.md:345-350
action: decided
```

The texts the relay's refusals key on were measured on Linux, Docker 29.7.2,
with the pinned helper, since a Mac's daemon prints the same runc and busybox
messages:

```text
docker run -d --pull=never <helper> nosuchcmd        exit 127, the container created:
  ... exec: "nosuchcmd": executable file not found in $PATH
docker exec <running> nosuchcmd                      exit 127:
  OCI runtime exec failed: ... exec: "nosuchcmd": executable file not found in $PATH
docker exec <running> wget -q -T 2 -O /dev/null http://127.0.0.1:1/v2/   exit 1:
  wget: can't connect to remote host (127.0.0.1): Connection refused
docker run -d --pull=never <helper> timeout 30 nonc -l
  docker logs: timeout: can't execute 'nonc': No such file or directory
  docker inspect: Running false, ExitCode 127
docker run -d --pull=never busybox@sha256:111…1 true exit 125:
  docker: Error response from daemon: No such image: busybox@sha256:111…1
```

## What the plan settles that the RFC does not

```divergence
decision: 10
grade: ASSUMED
kind: resolved
at: 2026-11-20T09:50:49Z
attempt: 1
claim: Decision 10 labels a relay with its installation and relies on the deployment lock to make another relay of the same installation a leftover, without saying what names the installation to the compose adapter, which is given a layout and references only. The label's value is the layout's manager directory, set where the darwin engine is wired: the lock files live under it, so two layouts never share a label, while two installations of one product under different roots, as the acceptance run makes, would share a product name.
evidence: internal/domain/paths.go:129-194
action: decided
```

```divergence
decision: 16
grade: ASSUMED
kind: resolved
at: 2026-11-20T09:50:49Z
attempt: 1
claim: Decision 16 names the request and not when it is sent. `docker run -d` returns once the relay's process has started, which can be before `nc` listens, so a first request can be refused by a relay that works. The request is sent up to three times, a second apart, while the relay runs, all within the check's one-minute limit that decision 15's bound counts; a relay that has exited is read at once rather than retried.
evidence: internal/adapters/runtime/compose/ingest.go:117
action: decided
```

```divergence
decision: 11
grade: ASSUMED
class: spec-gap
kind: departed
at: 2026-11-20T09:50:49Z
attempt: 1
claim: Decision 11 tells a helper without the relay's requirements apart by what a relay that exits at start prints. A helper without `timeout`, the relay's first word, never starts: `docker run -d` itself exits 127 with the container created, as measured above, so a start that exits 126 or 127 is decision 11's refusal too, and not decision 13's refusal of a relay that is not created. A helper without `nc` is a relay that exits at start, as decided.
evidence: `docker run -d --pull=never <helper> nosuchcmd` — exit 127, `exec: "nosuchcmd": executable file not found in $PATH`
action: departed
```

```divergence
decision: 18
grade: ASSUMED
class: spec-gap
kind: departed
at: 2026-11-20T09:50:49Z
attempt: 1
claim: Decision 18 has the file-sharing probe use the resolver, whose last case is a refusal. The probe lets the engine pull the helper (RFC 0055 decision 16), because it runs at the first apply, before the pull step fetches the helper, so a refusal there would leave an online Mac's first apply unprobed. The probe runs what the resolver finds, the reference or the ID, and when it finds neither it runs the reference and lets the engine pull it, as today.
evidence: internal/adapters/runtime/compose/sharing.go:40-42
action: departed
```

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-20T09:50:49Z
attempt: 1
claim: Every apply and update pulls the helper while the machine has a network, skipping it only when its reference is local, and warns that a backup cannot capture volumes when the pull fails. With decision 18, an offline machine on the containerd store whose helper is local by its ID alone would be warned on every apply that a backup cannot run, when it can. The pull is skipped when the resolver finds the helper, by its reference or its ID.
evidence: internal/lifecycle/ops/apply.go:934-955
action: decided
proposal: ASSUMED — the volume helper is pulled only when decision 18's resolver does not find it, so a helper local by its ID is not pulled or warned about
```

```divergence
decision: 6
grade: ASSUMED
kind: resolved
at: 2026-11-20T09:50:49Z
attempt: 1
claim: Without its darwin branch `Manifest.PlatformRefusal` only calls `Requirements.PlatformRefusal`, so it is removed and its callers call the requirements again, as before #397; the check #397 added to `release ingest` goes, as decision 6 says.
evidence: rfcs/0060-a-mac-loads-a-bundle-s-images-through-a-relay.md:277
action: decided
```

```divergence
decision: 15
grade: ASSUMED
kind: resolved
at: 2026-11-20T09:50:49Z
attempt: 1
claim: Built as ruled: the bound is the time left on the ingest's context when each relay starts, and with no deadline the check's one-minute limit plus 45, 2 and 2 minutes for each pending image's pull, tag and untag. busybox `timeout` is given whole seconds, rounded up.
evidence: internal/lifecycle/ops/ingest.go:134
action: decided
```

## Corrections

2026-11-20T16:32:43Z. The decision 18 entry above credits RFC 0055 decision 16 with
the probe pulling an absent helper. That decision sets the darwin layout's
paths and modes, and has nothing on pulls. The probe's pull was settled in
RFC 0055's execution, in logs/macos-host.md (unlisted, 2026-11-16T01:35:36Z),
and RFC 0055's table has no row for it. The entry is left as written.
