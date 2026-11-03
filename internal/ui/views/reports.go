package views

import (
	"fmt"
	"io"
	"strings"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/lifecycle/ops"
	"github.com/morzecrew/morzer/internal/ui"
	"github.com/morzecrew/morzer/internal/ui/theme"
)

// Version is what `morzer version` answers.
//
// A named type rather than the `map[string]any` the command built, because the
// registry dispatches on type and a map would claim every other map-shaped
// report in the program. The JSON tags reproduce that map exactly, keys and all:
// this is a published contract and a refactor is not allowed to move it.
type Version struct {
	Version              string   `json:"version"`
	Commit               string   `json:"commit"`
	Built                string   `json:"built"`
	SupportedAPIVersions []string `json:"supported_api_versions"`

	// Check is what `--check` learned from the release feed, absent without
	// it.
	Check *ops.ManagerCheck `json:"check,omitempty"`
}

// KeyPair is a generated identity.
//
// Stdout carries the public key and nothing else, because
// `key=$(morzer secret recipients generate-recovery-key ./k)` is the documented
// invocation and the acceptance script's own first step. The warning about the
// private half is narration and goes to stderr as a callout — putting it on
// stdout was a regression this type's first draft shipped, and `tail -1` then
// returned the bottom of a box.
type KeyPair struct {
	PublicKey string `json:"public_key"`
	Path      string `json:"path"`
}

// RecoveryKeyCallout is the warning that travels beside a KeyPair on stderr.
//
// Exported so the command can hand it to App.notice: the callout is not part of
// the report, and a view that emitted it would put it on the stream a script is
// reading.
func RecoveryKeyCallout(path string) ui.Callout {
	return ui.Callout{
		Title: "keep this",
		Body: []string{
			fmt.Sprintf("The private key is at %s (0400).", path),
			"Move it off this machine. If this VM is lost, this key is how its " +
				"secrets are recovered — and it is the only way.",
		},
	}
}

// Value is a single scalar a script substitutes.
//
// `port=$(morzer config get http_port)` is the shape, so the plain rendering is
// the value and a newline with nothing around it. It goes through the boundary
// rather than around it so that "everything on stdout is a view" stays true
// without exception -- the exception is what the view says, not whether there
// is one.
type Value struct {
	Value string `json:"value"`
}

func init() {
	ui.Register(ui.View[Version]{
		Rich:  func(w io.Writer, t *theme.Theme, v Version) { emit(w, versionDoc(doc(w, t), v)) },
		Plain: func(w io.Writer, v Version) { emit(w, versionDoc(plainDoc(w), v)) },
	})
	ui.Register(ui.View[KeyPair]{
		Rich:  func(w io.Writer, t *theme.Theme, v KeyPair) { emit(w, keyPairDoc(doc(w, t), v)) },
		Plain: func(w io.Writer, v KeyPair) { emit(w, keyPairDoc(plainDoc(w), v)) },
	})
	ui.Register(ui.View[Value]{
		Rich:  func(w io.Writer, _ *theme.Theme, v Value) { fmt.Fprintln(w, v.Value) },
		Plain: func(w io.Writer, v Value) { fmt.Fprintln(w, v.Value) },
	})
	ui.Register(ui.View[ops.ManagerUpdate]{
		Rich:  func(w io.Writer, t *theme.Theme, u ops.ManagerUpdate) { emit(w, managerUpdateDoc(doc(w, t), u)) },
		Plain: func(w io.Writer, u ops.ManagerUpdate) { emit(w, managerUpdateDoc(plainDoc(w), u)) },
	})
	ui.Register(ui.View[ops.SettingsReport]{
		Rich:  func(w io.Writer, t *theme.Theme, r ops.SettingsReport) { emit(w, settingsDoc(doc(w, t), r)) },
		Plain: func(w io.Writer, r ops.SettingsReport) { emit(w, settingsDoc(plainDoc(w), r)) },
	})
}

func versionDoc(d *ui.Doc, v Version) *ui.Doc {
	d.Title("morzer " + v.Version)

	fields := []ui.Field{}
	if v.Commit != "" {
		fields = append(fields, ui.Field{Label: "commit", Value: v.Commit})
	}
	if v.Built != "" {
		fields = append(fields, ui.Field{Label: "built", Value: v.Built})
	}
	fields = append(fields, ui.Field{
		Label: "manifest api", Value: strings.Join(v.SupportedAPIVersions, ", ")})
	if c := v.Check; c != nil {
		fields = append(fields, ui.Field{Label: "newest", Value: c.Newest + "  " + c.URL})
	}
	d.Fields(2, fields)
	if c := v.Check; c != nil {
		d.Blank()
		d.Text(2, "%s", c.Summary())
		if c.Upgrade != "" {
			// Verbatim, broken only where a shell continuation can go:
			// wrapped as prose, the command split inside an argument and
			// no longer pasted as one.
			d.Text(2, "upgrade with")
			cmd, dir, found := strings.Cut(c.Upgrade, " --dir ")
			if !found {
				d.Verbatim("    " + cmd)
			} else {
				d.Verbatim("    " + cmd + ` \`)
				d.Verbatim("      --dir " + dir)
			}
		}
	}
	return d
}

// managerUpdateDoc says what `self-update` replaced, or why it replaced
// nothing.
func managerUpdateDoc(d *ui.Doc, u ops.ManagerUpdate) *ui.Doc {
	t := d.Theme()
	from := u.From
	if u.DevBuild {
		from = "dev build"
	}
	// A dry run that found something to install, which is the only kind
	// that got as far as the locks it would take.
	wouldUpdate := u.DryRun && (u.DevBuild || newer(u.To, u.From))
	switch {
	case u.Updated:
		d.Title(fmt.Sprintf("morzer %s -> %s", from, u.To))
	case wouldUpdate:
		d.Title(fmt.Sprintf("would update morzer %s -> %s", from, u.To))
	case u.From == u.To:
		d.Title(fmt.Sprintf("nothing to install: morzer %s is running", u.From))
	default:
		d.Title(fmt.Sprintf("nothing to install: morzer %s is ahead of %s", u.From, u.To))
	}

	fields := []ui.Field{{Label: "path", Value: u.Path}, {Label: "notes", Value: u.URL}}
	if u.Updated || wouldUpdate {
		held := strings.Join(u.Installations, ", ")
		if held == "" {
			held = t.Dim("none on this machine")
		}
		label := "locked"
		if !u.Updated {
			label = "would lock"
		}
		fields = append(fields, ui.Field{Label: label, Value: held})
	}
	d.Fields(2, fields)
	if len(u.Warnings) > 0 {
		d.Blank()
		rows := make([]ui.CheckRow, 0, len(u.Warnings))
		for _, w := range u.Warnings {
			rows = append(rows, ui.CheckRow{State: ui.CheckWarned, Description: w})
		}
		d.Checks(2, rows)
	}
	return d
}

// newer is a later than b, both versions as ops reports them.
func newer(a, b string) bool {
	va, errA := domain.ParseVersion(a)
	vb, errB := domain.ParseVersion(b)
	return errA == nil && errB == nil && va.GreaterThan(vb)
}

// keyPairDoc prints the public key, alone.
func keyPairDoc(d *ui.Doc, k KeyPair) *ui.Doc {
	d.Verbatim(k.PublicKey)
	return d
}

// settingsDoc lists the installation's own knobs.
func settingsDoc(d *ui.Doc, report ops.SettingsReport) *ui.Doc {
	t := d.Theme()

	rows := make([][]string, 0, len(report.Settings))
	for _, entry := range report.Settings {
		value := entry.Value
		if value == "" {
			// Nothing here defaults to on, and an empty cell would
			// read as "this setting does not exist".
			value = t.Dim("(unset)")
		}
		rows = append(rows, []string{entry.Name, value, entry.Description})
	}

	d.Table(0, ui.Table{
		Columns: []ui.Column{
			{Header: "setting", Essential: true},
			{Header: "value", Essential: true},
			{Header: "what it does"},
		},
		Rows:  rows,
		Empty: "this installation declares no settings",
	})
	return d
}
