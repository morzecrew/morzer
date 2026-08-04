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
| A secret a hook prints is scrubbed from its output and from the error | `TestSecretsAreScrubbedFromHookOutput` |

### The age identity and its recipients

The identity is the only thing that can read the encrypted state, so every
operation that could destroy or weaken it is a refusal.

| Claim | Test |
| --- | --- |
| A generated identity is `0400` in a `0700` directory, and never widened | `TestGenerateIdentityWritesAKeyNobodyElseCanRead` |
| An identity that exists but cannot be parsed is never replaced | `TestEnsureIdentityRefusesToReplaceOneItCannotParse` |
| A failed decryption says which of three problems it is: a wrong key, a missing identity, or something else | `TestDecryptionFailuresAreClassifiedByRemedy` |
| A failed encryption never replaces the existing state with something half-written | `TestEncryptionFailuresAreReported` |
| A secret the release stopped declaring is removed from the render directory, not left where the product can read it | `TestRenderingRemovesWhatNoDeclarationBacks` |
| Walking away from the recovery question generates a key rather than waiving one | `TestEndOfInputDoesNotCancelInAccessibleMode` |
| Creating an identity twice returns the first, never a second key | `TestEnsureIdentityIsIdempotent` |
| A malformed recipient is refused before it reaches the file | `TestValidateRecipient` |
| Re-encrypting validates every key before rewriting anything | `TestReencryptForValidatesEveryKeyBeforeTouchingAnything` |
| The machine's own key is identified by comparison, not by a sidecar that could be edited | `TestRecipientsIdentifiesTheMachineKeyByComparison` |
| An import that could not be decrypted is refused before it overwrites the state | `TestImportRefusesAnythingItCannotVouchFor` |
| Imported state is written `0600` | `TestImportWritesTheStateAtSixHundred` |

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
| A bundle containing a symlink or a device node is refused, not partially copied | `TestCopyTreeRefusesEverythingThatIsNotAFileOrADirectory` |
| A bundle cannot exhaust the disk or the inode table before anything validates it | `TestCopyTreeEnforcesItsLimits` |
| Every way an archive can be hostile — an escaping path, a link, a device node, a count or a size — is refused by name | `TestEveryWayAnArchiveCanBeHostileIsRefused` |
| An archive's own modes are normalised, never trusted | `TestArchiveModesAreNormalisedNotTrusted` |
| A `Secret` cannot serialise its value, even inside a struct somebody marshals without thinking | `TestASecretNeverSerialisesItsValue` |
| The content digest covers paths, contents and the executable bit — but not the umask | `TestDigestTreeCoversPathsModesAndContents` |
| A hook is resolved inside the release, never from `PATH` | `TestAHookPathCannotEscapeTheRelease` |
| A hook that arrives without the executable bit is a broken bundle, named as one | `TestAHookWithoutTheExecutableBitIsRefused` |
| A hook's timeout reaches the whole process group, so nothing survives it | `TestATimeoutReachesTheWholeProcessGroup` |

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
| An export only the exporting machine could read is refused: that is not a recovery plan | `TestExportRefusesWhenNothingElseCouldReadIt` |
| A recovered machine keeps the original installation id, or every backup it holds belongs to somebody else | `TestARecoveredMachineKeepsTheOriginalIdentity` |
| Generating a recovery key does not by itself grant it access | `TestSecretRecipientsAddAndRemove` |
| A secret value never reaches argv: stdin is the only channel | `TestAPipedSecretIsTakenWhole` |
| A value larger than a megabyte on stdin is refused rather than read | `TestAnUnreasonablyLargeValueIsRefused` |
| `secret edit` says it needs a terminal rather than hanging without one | `TestSecretEditRefusesWithoutATerminal` |
| A rollback with no previous release is refused rather than guessed at | `TestRollbackWithNothingToRollBackTo` |
| A second operation cannot run against one installation, and is told who holds the lock | `TestTheRefusalNamesWhoHoldsItAndForHowLong` |
| A lock record left by a killed process is not reported as a live holder | `TestAStaleRecordIsNotReportedAsAHolder` |
| Removing the last recipient, or this machine's own, is refused | `TestReencryptForRefusesAnEmptyRecipientSet` |
| An installation written by a newer manager is refused, not silently downgraded | `TestAnInstallationFromANewerManagerIsRefusedClearly` |

### Backups, against a real database

A backup that has never been restored is a hope. These run `pg_dump` and
`psql` against a real Postgres, drop the rows in between, and query them back.

| Claim | Test |
| --- | --- |
| A backup taken by the manager can be restored, and the rows come back | `TestABackupOfARealDatabaseCanBeRestored` |
| A corrupt backup is refused before it reaches a live database | `TestARestoreIsRefusedWhenTheBackupIsCorrupt` |
| A backup belonging to another installation is refused by name | `TestARestoreIsRefusedAcrossInstallations` |
| A failed backup leaves nothing a later restore could mistake for one | `TestAFailedBackupLeavesNothingBehind` |
| A hook cannot record an artifact outside the backup directory | `TestAHookThatWritesOutsideTheBackupDirectoryIsRefused` |
| Retention never removes the only copy, whatever the policy says | `TestPruneNeverRemovesTheOnlyCopy` |
| Retention keeps the reasons it was told to keep | `TestPruneKeepsTheReasonsItWasToldTo` |

### The runtime boundary

| Claim | Test |
| --- | --- |
| Only declared parameters reach a Compose file; the invoking shell's environment does not | `TestComposeDoesNotInheritTheOperatorsEnvironment` |
| A parameter cannot shadow a variable the manager owns | `TestAParameterCannotShadowAManagedVariable` |
| The Compose interpolation ABI is exactly what is documented | `TestTheComposeABIMatchesItsDeclaration` |
| The template render context is exactly what is documented, and does not expose the process environment | `TestTheTemplateContextMatchesItsDocumentation` |
| A published port, its conflict check and its health probe all follow one value | `TestAChangedPortMovesEverythingTogether` |
| A `down` run by a compensation preserves the volume; only the explicit flag removes it — checked against real Docker | `TestComposeDownKeepsTheVolumeAndDownWithVolumesDoesNot` |
| A failed configuration change is unwound, so the recorded value never describes a container that does not exist | `TestConfigSetReportsARuntimeThatWillNotRecreate` |
| A failed step stops the operation and the exit code says whether the system was put back | `TestEveryPortFailureStopsApply` |

### Health and reporting

| Claim | Test |
| --- | --- |
| A service that is up but not ready is reported unhealthy | `TestHTTPProbeReportsWhatTheServerDid` |
| A refused connection is a result an operator can read, not a wall of syscall errors | `TestHTTPProbeOnNothingListening` |
| A probe that never answers times out rather than hanging an `apply` | `TestHTTPProbeTimesOutRatherThanHanging` |
| Rich output never carries information plain output omits | `TestRichNeverShowsWhatPlainDoesNot` |
| Output mode is resolved by the documented table | `TestResolveModeFollowsItsDocumentedTable` |
| A service that is up but not ready is reported unhealthy by a real web server too | `TestHTTPProbeAgainstCaddy` |
| A service that takes seconds to start is waited for, not failed | `TestWaitReadyAgainstAServiceThatStartsSlowly` |
| A service that never comes up is named in the refusal, with what it last said | `TestWaitReadyTimesOutNamingWhatNeverCameUp` |
| One broken probe never hides the state of the others | `TestCheckOnceKeepsGoingWhenOneProberIsBroken` |
| A converged service is not re-probed every two seconds while the rest start | `TestWaitReadyStopsReprobingWhatAlreadyPassed` |
| `doctor` works on a machine where every adapter is broken — the only kind anyone runs it on | `TestDoctorSurvivesEveryAdapterBeingBroken` |
| An absent healthcheck reads as "no probe", not as "unhealthy" — against real Docker | `TestComposeStatusReportsRealHealth` |
| A journal line half-written by a crash does not make `status` unusable | `TestACorruptFinalJournalLineIsDiscardedNotFatal` |

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
- **`config set` refuses an undeclared parameter by name; `config unset` does
  not.** The merge treats "not recorded" as "already at its default" without
  asking whether the release declares it, so an operator who mistypes an unset
  is told it worked. Recorded as
  `TestUnsettingSomethingTheReleaseDoesNotDeclareIsANoOp`, which fails if that
  ever becomes a refusal.
- **`preflight.NoUnfinishedOperation` is written but never wired in.** It is
  documented as refusing to start while a previous operation is flagged, and
  nothing calls it — so `apply` runs straight over an operation that asked for
  a human. Pinned by `TestAnUnfinishedOperationDoesNotBlockANewOne`, which
  fails the day it is wired.
- **Cancelling the `init` wizard is not honoured in accessible mode.** huh's
  accessible renderer ignores the context and discards each field's error, so
  ctrl-D completes the form with defaults. What is asserted instead is that the
  defaults are the safe ones.
- **`ByteSize` does not round-trip decimal units.** `5GB` marshals as `4.7GiB`.
  Harmless because sizes are read from a manifest and never written back.
- **The registry probe's success path is not covered.** `docker manifest
  inspect` speaks HTTPS unless given `--insecure`, which the adapter never
  passes — a reachability probe that accepted plaintext could be answered by
  anyone on the path. A plain-HTTP registry is the only kind a test can stand
  up without reconfiguring the daemon, so only the three failure
  classifications are asserted.
- **Overwriting before deletion is meaningful on tmpfs and very little
  elsewhere.** The function's own documentation says so; the test asserts the
  files are overwritten and gone, not that the bytes are unrecoverable.
- **Coverage is not proof.** See
  [the testing levels](https://github.com/morzecrew/morzer/blob/main/CONTRIBUTING.md)
  for what each suite does and does not reach.
