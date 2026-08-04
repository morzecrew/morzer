# Acceptance stub images

Four images that stand in for real products during the acceptance run.

| Image | Stands in for | Answers |
| --- | --- | --- |
| `app` | the single-tier example's application | `ok` |
| `db` | a database, for both examples | nothing; it stays up |
| `frontend` | the three-tier example's web tier | `frontend` |
| `backend` | the three-tier example's API tier | `backend` |

`frontend` and `backend` answer with their own names rather than a shared `ok`,
and the acceptance run asserts the body. Two stubs answering identically would
let a swapped port mapping pass — which is exactly the failure a multi-tier
example exists to catch.

They are stubs on purpose. The acceptance scenario exercises **the manager** —
whether `apply` converges, whether a backup restores, whether a failed update
puts the release pointer back — and a real product would add minutes of startup
and a second source of failures to diagnose. What matters is that these are
genuine images, pulled by digest from a registry, run by real Compose, with real
health checks against a real port. Everything the manager touches is real; only
the thing it manages is trivial.

`db` is pushed to two repositories, once for each example. That is why the
script selects a digest by repository prefix rather than by index: an image in
two repositories has a `RepoDigests` entry for each, and taking the first one
pinned the three-tier example to the digest the other had been issued.

Built and pushed to a throwaway local registry by
[`.github/scripts/acceptance.sh`](../../.github/scripts/acceptance.sh), which
then rewrites the example bundle's manifest to pin the digests the registry
returned. That is the only way to get a `name@sha256:…` reference for an image
that was built locally, and pinning by digest is what the manifest requires.
