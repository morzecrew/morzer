package views

import (
	"fmt"
	"io"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/lifecycle/ops"
	"github.com/morzecrew/morzer/internal/ports"
	"github.com/morzecrew/morzer/internal/ui"
	"github.com/morzecrew/morzer/internal/ui/theme"
)

// The four listings that were four hand-rolled printf tables.
//
// `%-28s %-14s %6s` in `secret list`, `%-24s` in `backup list`, `%-12s` in
// `release list`, `%-20s %-10s %-16s` in `config list`: four people's idea of a
// table, four hard-coded widths, and a 30-character secret name silently
// breaking the alignment of every row after it. The widths now come from the
// data, in one implementation.
//
// Registered against the slice types the commands already produce, so `--json`
// is byte-for-byte what it was: the value is the contract, and a view that
// reshaped it would turn a presentation change into a breaking one.

// stamp is the timestamp format every listing uses.
//
// One format, because a report an operator pastes into a ticket should not have
// two ways of writing the same instant.
const stamp = "2006-01-02 15:04:05Z"

func init() {
	ui.Register(ui.View[[]ports.SecretMetadata]{
		Rich: func(w io.Writer, t *theme.Theme, v []ports.SecretMetadata) {
			emit(w, secretsDoc(doc(w, t), v))
		},
		Plain: func(w io.Writer, v []ports.SecretMetadata) { emit(w, secretsDoc(plainDoc(w), v)) },
	})
	ui.Register(ui.View[[]ops.ReleaseEntry]{
		Rich: func(w io.Writer, t *theme.Theme, v []ops.ReleaseEntry) {
			emit(w, releasesDoc(doc(w, t), v))
		},
		Plain: func(w io.Writer, v []ops.ReleaseEntry) { emit(w, releasesDoc(plainDoc(w), v)) },
	})
	ui.Register(ui.View[[]ports.BackupRef]{
		Rich: func(w io.Writer, t *theme.Theme, v []ports.BackupRef) {
			emit(w, backupsDoc(doc(w, t), v))
		},
		Plain: func(w io.Writer, v []ports.BackupRef) { emit(w, backupsDoc(plainDoc(w), v)) },
	})
	ui.Register(ui.View[[]ports.Recipient]{
		Rich: func(w io.Writer, t *theme.Theme, v []ports.Recipient) {
			emit(w, recipientsDoc(doc(w, t), v))
		},
		Plain: func(w io.Writer, v []ports.Recipient) { emit(w, recipientsDoc(plainDoc(w), v)) },
	})
}

// secretsDoc lists secret names and metadata — never values.
func secretsDoc(d *ui.Doc, metadata []ports.SecretMetadata) *ui.Doc {
	rows := make([][]string, 0, len(metadata))
	for _, m := range metadata {
		changed := "unknown"
		if !m.LastChanged.IsZero() {
			changed = m.LastChanged.Format(stamp)
		}
		rows = append(rows, []string{
			m.Name, m.Fingerprint, fmt.Sprint(m.Length), changed,
		})
	}

	d.Table(0, ui.Table{
		Columns: []ui.Column{
			{Header: "name", Essential: true},
			{Header: "fingerprint", Essential: true},
			{Header: "length", Right: true},
			{Header: "last changed"},
		},
		Rows:  rows,
		Empty: "no secrets are set",
	})
	return d
}

// releasesDoc lists what is installed, newest first.
//
// The marker column is essential and stays at every width: `prune` refuses to
// remove a current, previous or staged release, and a listing that showed no
// reason for that leaves an operator arguing with the retention policy about a
// release it cannot see.
func releasesDoc(d *ui.Doc, entries []ops.ReleaseEntry) *ui.Doc {
	t := d.Theme()

	rows := make([][]string, 0, len(entries))
	for _, e := range entries {
		marker, note, style := " ", "", func(s string) string { return s }
		switch {
		case e.Current:
			marker, note, style = "*", "current", t.Highlight
		case e.Previous:
			marker, note, style = "-", "previous", t.Dim
		case e.Staged:
			marker, note, style = "+", "staged", t.Active
		}
		rows = append(rows, []string{
			style(marker), style(e.Version.String()), t.Dim(note), e.Root,
		})
	}

	d.Table(0, ui.Table{
		Columns: []ui.Column{
			{Essential: true},
			{Header: "version", Essential: true},
			{Header: "role"},
			{Header: "root"},
		},
		Rows:     rows,
		Empty:    "no releases are installed",
		NoHeader: true,
	})
	return d
}

// backupsDoc lists what this machine holds.
func backupsDoc(d *ui.Doc, backups []ports.BackupRef) *ui.Doc {
	rows := make([][]string, 0, len(backups))
	for _, b := range backups {
		rows = append(rows, []string{
			b.ID, b.At.Format(stamp), domain.ByteSize(b.Size).String(),
		})
	}

	d.Table(0, ui.Table{
		Columns: []ui.Column{
			{Header: "id", Essential: true},
			{Header: "taken", Essential: true},
			{Header: "size", Right: true},
		},
		Rows:  rows,
		Empty: "no backups",
	})
	return d
}

// recipientsDoc lists who can read this installation's secrets.
//
// The public key is essential and the comment is not: the key is what an
// operator compares against the one they hold, and a comment is a label
// somebody chose.
func recipientsDoc(d *ui.Doc, recipients []ports.Recipient) *ui.Doc {
	rows := make([][]string, 0, len(recipients))
	for _, r := range recipients {
		rows = append(rows, []string{r.PublicKey, string(r.Kind), r.Comment})
	}

	d.Table(0, ui.Table{
		Columns: []ui.Column{
			{Header: "public key", Essential: true},
			{Header: "kind", Essential: true},
			{Header: "comment"},
		},
		Rows:  rows,
		Empty: "no recipients; the secret state has not been created yet",
	})
	return d
}

func init() {
	ui.Register(ui.View[[]ports.RenderedFile]{
		Rich: func(w io.Writer, t *theme.Theme, v []ports.RenderedFile) {
			emit(w, renderedDoc(doc(w, t), v))
		},
		Plain: func(w io.Writer, v []ports.RenderedFile) { emit(w, renderedDoc(plainDoc(w), v)) },
	})
	ui.Register(ui.View[[]ops.RemoteBackup]{
		Rich: func(w io.Writer, t *theme.Theme, v []ops.RemoteBackup) {
			emit(w, remoteBackupsDoc(doc(w, t), v))
		},
		Plain: func(w io.Writer, v []ops.RemoteBackup) { emit(w, remoteBackupsDoc(plainDoc(w), v)) },
	})
	ui.Register(ui.View[[]ops.TargetStatus]{
		Rich: func(w io.Writer, t *theme.Theme, v []ops.TargetStatus) {
			emit(w, targetsDoc(doc(w, t), v))
		},
		Plain: func(w io.Writer, v []ops.TargetStatus) { emit(w, targetsDoc(plainDoc(w), v)) },
	})
}

// renderedDoc lists where the secrets were written, and with what mode.
//
// Never a value: this command exists to put secrets on tmpfs for the product to
// read, and printing one here would put it in the operator's scrollback.
func renderedDoc(d *ui.Doc, files []ports.RenderedFile) *ui.Doc {
	rows := make([][]string, 0, len(files))
	for _, f := range files {
		rows = append(rows, []string{f.Name, f.Path, fmt.Sprintf("%04o", f.Mode)})
	}

	d.Table(0, ui.Table{
		Columns: []ui.Column{
			{Header: "name", Essential: true},
			{Header: "path", Essential: true},
			{Header: "mode", Right: true},
		},
		Rows:  rows,
		Empty: "the release declares no secret files",
	})
	return d
}

// remoteBackupsDoc lists what is on the targets rather than on this machine.
//
// The target column is essential: the whole reason to run this is that the
// copies are somewhere else, and a listing that dropped where would answer a
// different question.
func remoteBackupsDoc(d *ui.Doc, backups []ops.RemoteBackup) *ui.Doc {
	rows := make([][]string, 0, len(backups))
	for _, b := range backups {
		rows = append(rows, []string{
			b.Manifest.ID,
			b.Manifest.CreatedAt.Format(stamp),
			b.Manifest.ReleaseVersion.String(),
			b.Target,
		})
	}

	d.Table(0, ui.Table{
		Columns: []ui.Column{
			{Header: "id", Essential: true},
			{Header: "taken"},
			{Header: "release"},
			{Header: "target", Essential: true},
		},
		Rows:  rows,
		Empty: "no backups on the target",
	})
	return d
}

// targetsDoc lists the configured targets and whether they answer.
func targetsDoc(d *ui.Doc, statuses []ops.TargetStatus) *ui.Doc {
	t := d.Theme()

	rows := make([][]string, 0, len(statuses))
	for _, s := range statuses {
		state := t.OK(fmt.Sprintf("%d backup(s)", s.Backups))
		if !s.Reachable {
			state = t.Fail("unreachable: " + s.Error)
		}
		rows = append(rows, []string{s.URL, state, s.Latest})
	}

	d.Table(0, ui.Table{
		Columns: []ui.Column{
			{Header: "target", Essential: true},
			{Header: "state", Essential: true},
			// The newest backup the target holds. Inessential because
			// a target that cannot be reached has none to name, and
			// "can I reach it" is the question this command answers.
			{Header: "latest"},
		},
		Rows:  rows,
		Empty: "no backup targets: every copy of this deployment's data is on this machine",
	})
	return d
}
