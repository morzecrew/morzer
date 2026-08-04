---
title: What is tested
icon: lucide/shield-check
summary: Every security property this project claims, and the test that fails when it stops holding
---

# What is tested

This project makes security claims. A release is refused unless its images are
digest-pinned; secrets are encrypted at rest and rendered `0400` into tmpfs;
archive extraction cannot escape its root; no secret value reaches a log, a
journal entry or an argument vector; no flag disables TLS verification.

Each of those is a claim about behaviour, and behaviour that nothing checks is a
claim about intent. The table below names, for every one, the test that fails
when it stops being true.

**The table is gated.** `just docs-check` fails when a row names a test that
does not exist, so a test deleted or renamed breaks the build rather than
quietly leaving a claim unbacked.

## The claims

### Secrets

| Claim | Test |
| --- | --- |
| A secret value never reaches a log line, by any route: the message, an attribute, a group, an error, a `Stringer`, a struct, or a recovered panic | `TestASecretIsScrubbedFromEveryRoute` |
| Nor through a logger built with `.With(...)`, which carries its attributes into every later line | `TestASecretCapturedByWithIsScrubbed` |
| Nor through a group | `TestASecretUnderWithGroupIsScrubbed` |
| A secret cannot be printed by accident: the type has no usable `String` | `TestSecretRedactionIsStructural` |
| No secret value reaches the operation journal | `TestSecretsNeverReachTheJournal` |
| `secret list` prints names and fingerprints, never values — in any output mode | `TestSecretListNeverPrintsAValue` |
| Editing secrets leaves no plaintext behind, however the editor exits | `TestSecretEditLeavesNoPlaintextBehind` |
| A failed editor session is cleaned up | `TestSecretEditCleansUpAfterAFailedEditor` |
| Every value in a secret set is registered for scrubbing, not just the first | `TestRegisterSetTakesEveryValueInASecretSet` |
| `doctor` reports a render directory that is not memory-backed | `TestIsEphemeralFilesystem` |

### Releases and verification

| Claim | Test |
| --- | --- |
| An image that is not pinned by digest is refused at load | `TestImagesMustBePinnedByDigest` |
| A bundle whose contents do not match their digest is refused | `TestOCIRefusesABlobThatDoesNotMatchItsDigest` |
| `require_signature` with no key is refused, because no bundle could satisfy it | `TestRequireSignatureWithoutKeysIsRefusedAtLoad` |
| The manifest's pinned images are what actually runs | `TestApplyPullsTheImagesTheManifestPins` |

### Filesystem containment

| Claim | Test |
| --- | --- |
| A path in a bundle cannot escape the release root | `TestPathsMayNotEscapeTheReleaseRoot` |
| Nor can a read from inside a root | `TestReadFileInReadsAndRefuses` |
| Extracted files get normalised modes, not the archive's | `TestArchiveExtractionNormalisesModes` |
| A directory that exists with the wrong permissions is corrected | `TestMkdirExactSetsTheModeEvenWhenTheDirectoryExists` |
| A wrong mode is reported to `doctor`, not raised as a failure of `doctor` | `TestCheckModeReportsRatherThanFails` |
| Rendered secrets are overwritten before removal | `TestRemoveWithOverwriteClearsEveryFile` |

### Refusals

The commands that refuse are the ones that protect data, so each refusal is
asserted by *which* refusal fires — not merely that something failed.

| Claim | Test |
| --- | --- |
| A restore requires both `--force` and the installation id typed out | `TestRestoreRefusesWithoutTheTypedConfirmation` |
| A parameter the release does not declare is refused by name | `TestConfigRefusesWhatTheReleaseDoesNotDeclare` |
| A signature policy nothing could satisfy is refused before anything is created | `TestInitRefusesAPolicyNothingCouldSatisfy` |
| An installation is never silently reconfigured by a second `init` | `TestInitCreatesAnInstallationAndRefusesASecond` |
| A mistyped command is a usage error, not an internal one | `TestUnknownInputIsAUsageErrorNotABug` |

### The runtime boundary

| Claim | Test |
| --- | --- |
| Only declared parameters reach a Compose file; the invoking shell's environment does not | `TestComposeDoesNotInheritTheOperatorsEnvironment` |
| A parameter cannot shadow a variable the manager owns | `TestAParameterCannotShadowAManagedVariable` |
| The Compose interpolation ABI is exactly what is documented | `TestTheComposeABIMatchesItsDeclaration` |
| The template render context is exactly what is documented, and does not expose the process environment | `TestTheTemplateContextMatchesItsDocumentation` |
| A published port, its conflict check and its health probe all follow one value | `TestAChangedPortMovesEverythingTogether` |

### Health and reporting

| Claim | Test |
| --- | --- |
| A service that is up but not ready is reported unhealthy | `TestHTTPProbeReportsWhatTheServerDid` |
| A refused connection is a result an operator can read, not a wall of syscall errors | `TestHTTPProbeOnNothingListening` |
| A probe that never answers times out rather than hanging an `apply` | `TestHTTPProbeTimesOutRatherThanHanging` |
| Rich output never carries information plain output omits | `TestRichNeverShowsWhatPlainDoesNot` |
| Output mode is resolved by the documented table | `TestResolveModeFollowsItsDocumentedTable` |

## What a test in this table has to do

Naming a test is not enough. Each one must **fail when the property is
removed**, and each was verified that way — the behaviour was deleted, the test
was watched to fail, and the behaviour was restored.

That discipline is why this table is worth more than the coverage percentage
beside it. A test that executes a line without asserting on it raises the number
and pins nothing.

## What is *not* claimed here

Being explicit about the edges is part of the point.

- **Redaction is eager on `.With`.** A value captured before its secret is
  registered is written in the clear. Not reachable today — the only call sites
  pass operation ids — and recorded as `TestRegisteringAfterWithIsAKnownLimit`
  so it is a known limit rather than a surprise.
- **A parameter's `services` list is not checked against the topology.** A
  vendor who names one tier for a value two tiers read gets a change that
  reports success and leaves one stale.
- **Overwriting before deletion is meaningful on tmpfs and very little
  elsewhere.** The function's own documentation says so; the test asserts the
  files are overwritten and gone, not that the bytes are unrecoverable.
- **Coverage is not proof.** See
  [the testing levels](https://github.com/morzecrew/morzer/blob/main/CONTRIBUTING.md)
  for what each suite does and does not reach.
