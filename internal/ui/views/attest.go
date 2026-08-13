package views

import (
	"fmt"
	"io"
	"path/filepath"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/ui"
	"github.com/morzecrew/morzer/internal/ui/theme"
)

// Verification is what `attest verify` answers.
//
// The JSON keys are the report's own, because `--json` is a monitoring contract
// and this view exists to draw the same data rather than to reshape it.
type Verification struct {
	Statements []StatementVerdict    `json:"statements"`
	Chain      []domain.ChainBreak   `json:"chain_breaks,omitempty"`
	Live       []domain.LiveMismatch `json:"live_mismatches,omitempty"`

	LiveChecked bool   `json:"live_checked"`
	LiveAgainst string `json:"live_against,omitempty"`

	Problems int `json:"problems"`
}

// StatementVerdict is one statement's row.
type StatementVerdict struct {
	File      string                 `json:"file"`
	Operation string                 `json:"operation,omitempty"`
	Kind      string                 `json:"kind,omitempty"`
	Outcome   string                 `json:"outcome,omitempty"`
	Signature domain.SignatureResult `json:"signature"`

	Unreadable string `json:"unreadable,omitempty"`
}

// AttestationLog is what `attest log` prints.
//
// A wrapper around the slice rather than the slice itself, because the view
// registry keys on the type and a bare slice would claim every listing of that
// shape.
type AttestationLog struct {
	Entries []LogRow `json:"entries"`
}

// LogRow is one statement in the listing. The same seam as StatementVerdict:
// the operation computes the report, the view publishes the `--json` shape, and
// the terminal output can change without moving a monitoring contract.
type LogRow struct {
	Operation string      `json:"operation"`
	Kind      string      `json:"kind"`
	Outcome   string      `json:"outcome"`
	Started   domain.Time `json:"started"`

	From string `json:"from_version,omitempty"`
	To   string `json:"to_version,omitempty"`

	// Signed says a signature is *there*, never that it checks out --
	// that is `attest verify`'s answer.
	Signed bool `json:"signed"`

	File       string `json:"file"`
	Unreadable string `json:"unreadable,omitempty"`
}

func init() {
	ui.Register(ui.View[Verification]{
		Rich:  func(w io.Writer, t *theme.Theme, v Verification) { emit(w, verificationDoc(doc(w, t), v)) },
		Plain: func(w io.Writer, v Verification) { emit(w, verificationDoc(plainDoc(w), v)) },
	})
	ui.Register(ui.View[AttestationLog]{
		Rich:  func(w io.Writer, t *theme.Theme, v AttestationLog) { emit(w, attestationLogDoc(doc(w, t), v)) },
		Plain: func(w io.Writer, v AttestationLog) { emit(w, attestationLogDoc(plainDoc(w), v)) },
	})
}

func attestationLogDoc(d *ui.Doc, v AttestationLog) *ui.Doc {
	rows := make([][]string, 0, len(v.Entries))
	for _, e := range v.Entries {
		if e.Unreadable != "" {
			rows = append(rows, []string{
				filepath.Base(e.File), "", "unreadable", "", e.Unreadable,
			})
			continue
		}

		signed := "unsigned"
		if e.Signed {
			// "signed" and not "valid". Whether it checks out is
			// `attest verify`'s answer, and a listing that implied
			// it had checked would be the overclaim the whole
			// format is built to refuse.
			signed = "signed"
		}
		rows = append(rows, []string{
			e.Operation,
			e.Kind,
			e.Outcome,
			versionMove(e.From, e.To),
			signed,
		})
	}

	d.Title(fmt.Sprintf("%d statement(s), newest first", len(v.Entries)))
	d.Table(2, ui.Table{
		Columns: []ui.Column{
			{Header: "operation", Essential: true},
			{Header: "kind", Essential: true},
			{Header: "outcome", Essential: true},
			{Header: "release"},
			{Header: "signature"},
		},
		Rows:  rows,
		Empty: "no statements",
	})
	return d
}

// versionMove renders what an operation did to the installed version.
func versionMove(from, to string) string {
	switch {
	case from != "" && to != "":
		return from + " -> " + to
	case to != "":
		return to
	default:
		return ""
	}
}

func verificationDoc(d *ui.Doc, v Verification) *ui.Doc {
	rows := make([]ui.CheckRow, 0, len(v.Statements)+len(v.Chain)+len(v.Live)+1)

	for _, s := range v.Statements {
		rows = append(rows, statementRow(s))
	}
	for _, b := range v.Chain {
		rows = append(rows, ui.CheckRow{
			State:       ui.CheckFailed,
			Description: "chain",
			Message:     b.Detail,
		})
	}
	for _, m := range v.Live {
		rows = append(rows, ui.CheckRow{
			State:       ui.CheckFailed,
			Description: "live: " + m.Kind,
			Message:     m.Detail,
		})
	}
	if v.LiveChecked && len(v.Live) == 0 {
		rows = append(rows, ui.CheckRow{
			State:       ui.CheckPassed,
			Description: "live",
			Message:     "the running deployment matches " + v.LiveAgainst,
		})
	}

	d.Title(fmt.Sprintf("%d statement(s), %d problem(s)", len(v.Statements), v.Problems))
	d.Checks(2, rows)
	return d
}

func statementRow(s StatementVerdict) ui.CheckRow {
	name := filepath.Base(s.File)

	switch {
	case s.Unreadable != "":
		return ui.CheckRow{State: ui.CheckFailed, Description: name, Message: s.Unreadable}

	case s.Signature.Outcome == domain.Unverifiable:
		return ui.CheckRow{
			State:       ui.CheckFailed,
			Description: name,
			Message:     "no key this installation knows about signed it",
		}

	case s.Signature.Outcome == domain.Unsigned:
		// A warning rather than a failure: a machine that had no key
		// when it emitted this is the normal state of anything upgraded
		// into schema 6, and the record is still worth having.
		return ui.CheckRow{
			State:       ui.CheckWarned,
			Description: name,
			Message:     fmt.Sprintf("%s %s, unsigned", s.Kind, s.Outcome),
		}

	case s.Signature.Outcome == domain.SignedByPredecessor:
		// Passed, and the message carries what it establishes. The
		// tempting reading of "signed by a predecessor" is that the
		// artifact is simply fine; what it establishes is provenance,
		// and the retirement date is history rather than a check.
		msg := "signed by a predecessor of this installation"
		if !s.Signature.RetiredAt.IsZero() {
			msg += ", retired " + s.Signature.RetiredAt.Format("2006-01-02")
		}
		if s.Signature.Reason != "" {
			msg += " (" + string(s.Signature.Reason) + ")"
		}
		return ui.CheckRow{State: ui.CheckPassed, Description: name, Message: msg}

	default:
		return ui.CheckRow{
			State:       ui.CheckPassed,
			Description: name,
			Message:     fmt.Sprintf("%s %s", s.Kind, s.Outcome),
		}
	}
}
