package domain

// Signing succession across a rebuild.
//
// An export carries the installation identity and the *encrypted* secret state,
// and deliberately not the machine's private keys -- neither the age identity
// (RFC 0003) nor the signing key (RFC 0028 decision 4). An export travels to a
// bucket, and a signing key inside one signs as the machine for whoever finds
// it.
//
// So a rebuilt machine is honestly a **different signer**, and the design makes
// that visible rather than silent: the predecessor's public key is recorded, so
// a verifier following a chain across a rebuild can say "signed by a predecessor
// of this installation" instead of "unknown signer".
//
// What this is not: proof that the rebuild was legitimate. Anyone holding the
// export can import it and record the same predecessor. It is provenance for an
// operator reading their own history, and every consumer carries that bound in
// its own artifact rather than relying on this comment (RFC 0025 decision 2).

// SucceedSigning returns the installation as a *new signer* that remembers the
// old one.
//
// The current public key becomes the newest entry in PreviousKeys, and
// PublicKey is cleared: the importing machine has not minted its key yet, and
// leaving the predecessor's there would produce a machine claiming to sign with
// a key it does not hold. That is the disagreement RFC 0028 §5.4 refuses, so
// writing it here would be manufacturing the exact state the refusal exists to
// catch.
//
// Idempotent in the way that matters: an export from a machine that never
// signed has no key to carry, and this records nothing rather than an empty
// predecessor entry that a verifier would have to skip.
func (i Installation) SucceedSigning(at Time, reason RetirementReason) Installation {
	if !i.Signing.HasKey() {
		// Nothing to succeed. Note that PreviousKeys is still carried
		// through untouched by the copy below -- a machine rebuilt
		// twice, whose middle incarnation never signed, keeps the
		// chain from before it.
		i.Signing.PreviousKeys = copyRetiredKeys(i.Signing.PreviousKeys)
		return i
	}

	previous := make([]RetiredKey, 0, len(i.Signing.PreviousKeys)+1)
	previous = append(previous, RetiredKey{
		Key:       i.Signing.PublicKey,
		RetiredAt: at,
		Reason:    reason,
	})
	previous = append(previous, i.Signing.PreviousKeys...)

	i.Signing = Signing{PreviousKeys: previous}
	return i
}

// copyRetiredKeys detaches the slice so a caller mutating the returned
// installation cannot reach into the one it was built from.
//
// The whole point of these functions being value-to-value is that the input is
// unchanged, and a shared backing array is that promise being false in the one
// case nobody tests.
func copyRetiredKeys(in []RetiredKey) []RetiredKey {
	if in == nil {
		return nil
	}
	return append([]RetiredKey(nil), in...)
}
