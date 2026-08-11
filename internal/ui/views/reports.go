package views

import (
	"fmt"
	"io"
	"sort"
	"strings"

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
		Rich:  func(w io.Writer, t *theme.Theme, v Version) { emit(w, versionDoc(doc(t), v)) },
		Plain: func(w io.Writer, v Version) { emit(w, versionDoc(plainDoc(), v)) },
	})
	ui.Register(ui.View[KeyPair]{
		Rich:  func(w io.Writer, t *theme.Theme, v KeyPair) { emit(w, keyPairDoc(doc(t), v)) },
		Plain: func(w io.Writer, v KeyPair) { emit(w, keyPairDoc(plainDoc(), v)) },
	})
	ui.Register(ui.View[Value]{
		Rich:  func(w io.Writer, _ *theme.Theme, v Value) { fmt.Fprintln(w, v.Value) },
		Plain: func(w io.Writer, v Value) { fmt.Fprintln(w, v.Value) },
	})
	ui.Register(ui.View[ops.SettingsReport]{
		Rich:  func(w io.Writer, t *theme.Theme, r ops.SettingsReport) { emit(w, settingsDoc(doc(t), r)) },
		Plain: func(w io.Writer, r ops.SettingsReport) { emit(w, settingsDoc(plainDoc(), r)) },
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
	d.Fields(2, fields)
	return d
}

// keyPairDoc prints the public key, alone.
func keyPairDoc(d *ui.Doc, k KeyPair) *ui.Doc {
	d.Text(0, "%s", k.PublicKey)
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

// sortedKeys is the ordering every map-derived listing uses, so two runs against
// the same release print the same bytes.
func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
