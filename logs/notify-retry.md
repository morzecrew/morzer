# notify-retry · RFC 0043 P1, the retry and the event id

On branch `notify-retry` from `8ebd593`, building
[RFC 0043](../rfcs/0043-notifications-that-survive-a-failed-delivery-and-a-scheduled-doctor.md)
P1 as ruled by the author on 2026-10-31: §5.1 and §5.3. That covers:

- one retry in the webhook adapter, after one second, for a connection error, a timeout, a 408, a 429 or a 5xx, inside the unchanged deadline;
- `ports.DeliveryError`, naming the target and saying whether a resend could succeed;
- an `id` on every forwarded event, in the body and as `Idempotency-Key`;
- the tests of §6 that P1 reaches, and the docs of §7 for the retry and the id.

Decision 9, the joined errors, shipped in #232. The outbox and the doctor timer
are P2.

**Drift count: 0.**

| Class | Test | Meaning |
|---|---|---|
| `discovery` | Could not have been known before code existed | Healthy — the spec was right to be silent |
| `spec-gap` | Could have been known; the spec was silent or at the wrong altitude | The design process missed something |
| `drift` | The spec covered it and it was built otherwise anyway | **A defect** |
| `irreducible` | No amount of design settles it | Stop and spike |

Two things the RFC leaves to the build, decided while planning and before any
code.

Decision 3 says the id is a ULID. Every other id morzer mints carries a prefix
naming what it identifies, `op_` and `inst_`.

```divergence
decision: 3
grade: ASSUMED
class: spec-gap
at: 2026-10-31T20:52:00Z
attempt: 1
claim: the event id needs a form, and morzer's other ULIDs carry a prefix naming what they identify
evidence: internal/lifecycle/ops/ops.go:222
action: departed
proposal: ASSUMED — the event id is `evt_` and a ULID, minted in `ops.notify` once the event passes the allowlist, and an event that already has one keeps it.
```

§5.1 classifies the answers a target gives, and says nothing of an event the
adapter cannot send at all: one it cannot encode, or a request it cannot build.

```divergence
decision: 1
grade: ASSUMED
class: spec-gap
at: 2026-10-31T20:53:00Z
attempt: 1
claim: a failure before any request reaches the target has no resend classification
evidence: internal/adapters/notify/webhook/webhook.go:133
action: departed
proposal: ASSUMED — an event the adapter cannot encode, or a request it cannot build, is a final `DeliveryError`, not retried: the same event would fail the same way.
```

**Drift count: 0.**

Review found a gap decision 3 leaves: the credential document names any header,
and `post` sets it after `Idempotency-Key`, so a document naming that header
replaces every event's id with one fixed value, and a receiver that drops
duplicates drops every event after the first.

```divergence
decision: UNLISTED
grade: UNLISTED
class: spec-gap
at: 2026-11-01T07:10:00Z
attempt: 1
claim: a notify credential whose header is `Idempotency-Key` replaces the event id on every request
evidence: internal/adapters/notify/webhook/webhook.go:181
action: decided
proposal: ASSUMED — a credential document naming `Idempotency-Key`, in any case, is refused where the other bad credentials are, so `notify target add` refuses it and `doctor` names it.
```

**Drift count: 0.**

The entry at 20:53 cited `webhook.go:133`, the line planned before the code,
which now falls in `Name`'s comment. The same entry, with evidence where the
code landed:

```divergence
decision: 1
grade: ASSUMED
class: spec-gap
at: 2026-11-01T07:45:00Z
attempt: 1
claim: a failure before any request reaches the target has no resend classification
evidence: internal/adapters/notify/webhook/webhook.go:152-154
action: departed
proposal: ASSUMED — an event the adapter cannot encode, or a request it cannot build, is a final `DeliveryError`, not retried: the same event would fail the same way.
```

**Followed, and §5.1's wording is wider than the design:** §5.1 says "an
operation never waits longer than today". That holds for the 15-second
deadline, which is unchanged. It does not hold for one slow target: a target
that times out twice holds the operation 11 seconds (two 5-second requests and
the one-second pause), where one attempt held it 5. The retry stays as decision
1 has it. The code comments now say the bound is unchanged, not the wait.

**Drift count: 0.**
