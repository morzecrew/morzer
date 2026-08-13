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

func init() {
	ui.Register(ui.View[Verification]{
		Rich:  func(w io.Writer, t *theme.Theme, v Verification) { emit(w, verificationDoc(doc(w, t), v)) },
		Plain: func(w io.Writer, v Verification) { emit(w, verificationDoc(plainDoc(w), v)) },
	})
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
