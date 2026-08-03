# Acceptance stub images

Two images that stand in for a real product during the acceptance run: an
application that answers a health check, and a database that does nothing but
stay up.

They are stubs on purpose. The acceptance scenario exercises **the manager** —
whether `apply` converges, whether a backup restores, whether a failed update
puts the release pointer back — and a real product would add minutes of startup
and a second source of failures to diagnose. What matters is that these are
genuine images, pulled by digest from a registry, run by real Compose, with real
health checks against a real port. Everything the manager touches is real; only
the thing it manages is trivial.

Built and pushed to a throwaway local registry by
[`.github/scripts/acceptance.sh`](../../.github/scripts/acceptance.sh), which
then rewrites the example bundle's manifest to pin the digests the registry
returned. That is the only way to get a `name@sha256:…` reference for an image
that was built locally, and pinning by digest is what the manifest requires.
