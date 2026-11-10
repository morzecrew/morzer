package views

import (
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/ui"
	"github.com/morzecrew/morzer/internal/ui/theme"
)

// State is `state ls`: per installation, the newest desired-state document
// published for it, and what its fleet row says it did with documents (RFC
// 0057 §5.11).
type State struct {
	Targets     []string   `json:"targets"`
	Rows        []StateRow `json:"rows"`
	Expected    int        `json:"expected,omitempty"`
	Limitations []string   `json:"limitations"`
}

// StateRow is one installation's line.
type StateRow struct {
	Target         string `json:"target,omitempty"`
	Product        string `json:"product,omitempty"`
	InstallationID string `json:"installation_id,omitempty"`
	Signature      string `json:"signature,omitempty"`
	Expected       bool   `json:"expected,omitempty"`
	Absent         bool   `json:"absent,omitempty"`

	// Published is the newest complete pair on the target, zero when none.
	Published int64 `json:"published,omitempty"`

	// Sync is the row's block, nil when there is no row this run read.
	Sync *domain.FleetSync `json:"sync,omitempty"`

	Problem string `json:"problem,omitempty"`
}

func init() {
	ui.Register(ui.View[State]{
		Rich:  func(w io.Writer, t *theme.Theme, v State) { emit(w, stateDoc(doc(w, t), v)) },
		Plain: func(w io.Writer, v State) { emit(w, stateDoc(plainDoc(w), v)) },
	})
}

func stateDoc(d *ui.Doc, v State) *ui.Doc {
	d.Title(fmt.Sprintf("%d installation(s) on %s", len(v.Rows), strings.Join(v.Targets, ", ")))

	rows := make([][]string, 0, len(v.Rows))
	var notes [][]string
	for _, r := range v.Rows {
		name := r.Product + "/" + r.InstallationID
		rows = append(rows, []string{
			name, stateSerial(r.Published), stateApplied(r), stateRefused(r), stateFetch(r), r.Signature,
		})
		if r.Problem != "" {
			notes = append(notes, []string{name, r.Problem})
		}
		if v.Expected > 0 && !r.Expected {
			notes = append(notes, []string{name, "the roster does not name this installation"})
		}
		if r.Sync != nil && r.Sync.Refused != nil && len(r.Sync.Refused.Names) > 0 {
			notes = append(notes, []string{name, "refused for " + strings.Join(r.Sync.Refused.Names, ", ")})
		}
	}
	d.Table(2, ui.Table{
		Columns: []ui.Column{
			{Header: "installation", Essential: true},
			{Header: "published", Essential: true},
			{Header: "applied", Essential: true},
			{Header: "refused"},
			{Header: "last fetch"},
			{Header: "signature"},
		},
		Rows:  rows,
		Empty: "no installation publishes a row here",
	})
	if len(notes) > 0 {
		d.Table(2, ui.Table{
			Columns: []ui.Column{{Header: "installation", Essential: true}, {Header: "what it says", Essential: true}},
			Rows:    notes,
		})
	}
	for _, limitation := range v.Limitations {
		d.Blank()
		d.Text(2, "%s", limitation)
	}
	return d
}

func stateSerial(n int64) string {
	if n == 0 {
		return "-"
	}
	return strconv.FormatInt(n, 10)
}

func stateApplied(r StateRow) string {
	switch {
	case r.Absent:
		return "no row"
	case r.Sync == nil:
		return "not following"
	case r.Sync.Applied == nil:
		return "-"
	}
	return stateSerial(r.Sync.Applied.Serial)
}

func stateRefused(r StateRow) string {
	if r.Sync == nil || r.Sync.Refused == nil {
		return "-"
	}
	f := r.Sync.Refused
	return fmt.Sprintf("%d at %s: %s", f.Serial, f.Step, f.Class)
}

func stateFetch(r StateRow) string {
	if r.Sync == nil || r.Sync.LastFetch == nil {
		return "-"
	}
	return string(r.Sync.LastFetch.Result)
}
