package views

import (
	"fmt"
	"io"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/lifecycle/ops"
	"github.com/morzecrew/morzer/internal/ui"
	"github.com/morzecrew/morzer/internal/ui/theme"
)

// `support inspect`'s view (RFC 0024 P4b).
//
// The listing half calls the bundle view's own table functions, so an archive
// described by the machine that wrote it and the same archive described by
// whoever received it cannot be described differently. That is not tidiness:
// the two readers are comparing notes across a ticket, and a column that meant
// something else on one of the two screens is the failure sharing the code
// avoids. (This comment claimed the sharing before the sharing existed --
// found by the self-audit, along with the copy it was describing.)
//
// The verification half is where the care goes, because it is the half a reader
// will take a verdict from.

func init() {
	ui.Register(ui.View[ops.SupportInspectReport]{
		Rich: func(w io.Writer, t *theme.Theme, r ops.SupportInspectReport) {
			emit(w, inspectSupportDoc(doc(w, t), r))
		},
		Plain: func(w io.Writer, r ops.SupportInspectReport) {
			emit(w, inspectSupportDoc(plainDoc(w), r))
		},
	})
}

func inspectSupportDoc(d *ui.Doc, r ops.SupportInspectReport) *ui.Doc {
	// The index is counted out loud rather than folded in, because
	// `support bundle` prints it as a row and this prints what it
	// enumerates -- one archive described honestly by two commands that
	// would otherwise appear to disagree by one.
	d.Title(fmt.Sprintf("%d component(s), %s, plus the index (%s)",
		len(r.Entries), humanBytes(r.TotalBytes), humanBytes(r.IndexBytes)))

	d.Fields(2, []ui.Field{
		{Label: "product", Value: r.Product},
		{Label: "installation", Value: r.InstallationID},
		{Label: "written by", Value: r.ManagerVersion},
	})

	if r.Unreadable != "" {
		// Above the table rather than below it: an empty component list
		// reads as an empty archive, and the reader has to meet the
		// reason before they draw that conclusion.
		d.Text(2, "the contents could not be read: %s", r.Unreadable)
	}
	writeSupportEntries(d, r.Entries, "nothing in it")
	writeSupportOmissions(d, r.Omitted)

	d.Blank()
	inspectSignature(d, r.Signature)
	return d
}

// inspectSignature prints what the signature established, and against what.
//
// The two are printed together on purpose. "Signature valid" without naming
// what it was checked against is the sentence decision 11 exists to prevent
// somebody writing, because the reader supplies the missing half themselves and
// supplies it wrong -- they assume it was checked against something
// trustworthy.
func inspectSignature(d *ui.Doc, sig ops.SupportSignature) {
	if !sig.Present {
		d.Text(2, "no signature beside this archive")
		// Two different machines produce this, and an operator can act on
		// the difference: one wrote the archive without a key, the other
		// wrote it with one and the `.minisig` did not travel.
		if sig.ClaimedKey != "" {
			d.Text(2, "the archive names a signing key, so the signature file did not travel with it")
			d.Verbatim(sig.ClaimedKey)
		}
		return
	}

	switch sig.Source {
	case ops.SignatureSourceNone:
		// The unchecked case, and it must not read as a soft pass. What
		// the reader has to do next is the sentence, not the state.
		d.Text(2, "signature present and NOT checked: there was no key to check it against")
		if sig.ClaimedKey != "" {
			d.Text(2, "the archive says this key signed it -- get that key from the "+
				"operator, not from this file, and pass it with --key")
			d.Verbatim(sig.ClaimedKey)
		}
	case ops.SignatureSourceExpectedKey:
		inspectOutcome(d, sig, "the key you named")
	case ops.SignatureSourceInstallation:
		inspectOutcome(d, sig, "this installation's recorded keys")
	case ops.SignatureSourceMachineKey:
		inspectOutcome(d, sig, "the signing key on this machine's disk, "+
			"which installation state does not record")
	}

	if sig.Bound != "" {
		d.Blank()
		d.Text(2, "%s", sig.Bound)
	}
}

// inspectOutcome renders a verdict that was actually reached.
//
// `against` names the trust anchor in the same sentence as the verdict, so no
// line here can be quoted on its own and mean more than it did.
func inspectOutcome(d *ui.Doc, sig ops.SupportSignature, against string) {
	switch sig.Result.Outcome {
	case domain.SignedByCurrentKey:
		d.Text(2, "signature verified against %s", against)
		d.Verbatim(sig.Result.Key)
	case domain.SignedByPredecessor:
		// Provenance, never validity -- RFC 0028 decision 10. Folding
		// this into "verified" would mean a key retired after a suspected
		// compromise still passes whatever it signs, which is the one
		// case retiring it was for.
		d.Text(2, "signed by a key this installation has RETIRED, checked against %s", against)
		d.Verbatim(sig.Result.Key)
		if !sig.Result.RetiredAt.IsZero() {
			d.Fields(2, []ui.Field{
				{Label: "retired", Value: sig.Result.RetiredAt.String()},
				{Label: "because", Value: string(sig.Result.Reason)},
			})
		}
		d.Text(2, "this establishes where the archive came from, not that it is current: "+
			"the date comes from the archive, and a stolen key back-dates as easily as it signs")
	case domain.Unverifiable:
		d.Text(2, "signature does NOT verify against %s", against)
		if sig.ClaimedKey != "" {
			d.Text(2, "the archive claims this key signed it, which is a claim and not a check")
			d.Verbatim(sig.ClaimedKey)
		}
	case domain.Unsigned:
		// Unreachable from here -- the caller returns early when no
		// signature is present -- and rendered rather than omitted so a
		// future path that reaches it prints something true.
		d.Text(2, "no signature beside this archive")
	}
}
