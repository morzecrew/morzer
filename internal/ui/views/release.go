package views

import (
	"fmt"
	"io"
	"strings"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/ui"
	"github.com/morzecrew/morzer/internal/ui/theme"
)

// Release is one bundle's manifest, its digest and where it lives.
//
// The JSON tags reproduce the map the command used to build, key for key: this
// is what `morzer release show --json | jq .manifest` reads.
type Release struct {
	Manifest domain.Manifest `json:"manifest"`
	Root     string          `json:"root"`
	Digest   string          `json:"digest"`
}

// Verified is what `release verify` answers when a bundle is sound.
//
// `valid` is always true here: the command returns an error rather than a
// report when it is not, so a script reading this field is reading a constant.
// It stays because it is published, and because `jq -e .valid` is a reasonable
// thing to have written against it.
type Verified struct {
	Valid       bool           `json:"valid"`
	Name        string         `json:"name"`
	VersionInfo domain.Version `json:"version"`
	Digest      string         `json:"digest"`
	RenderCheck bool           `json:"render_check"`
}

func init() {
	ui.Register(ui.View[Release]{
		Rich:  func(w io.Writer, t *theme.Theme, r Release) { emit(w, releaseDoc(doc(t), r)) },
		Plain: func(w io.Writer, r Release) { emit(w, releaseDoc(plainDoc(), r)) },
	})
	ui.Register(ui.View[Verified]{
		Rich:  func(w io.Writer, t *theme.Theme, v Verified) { emit(w, verifiedDoc(doc(t), v)) },
		Plain: func(w io.Writer, v Verified) { emit(w, verifiedDoc(plainDoc(), v)) },
	})
}

func releaseDoc(d *ui.Doc, r Release) *ui.Doc {
	t := d.Theme()
	m := r.Manifest

	d.Title(m.Metadata.Name + " " + m.Metadata.Version.String())
	if m.Metadata.Description != "" {
		d.Text(2, "%s", t.Dim(m.Metadata.Description))
	}

	d.Blank()
	d.Fields(2, []ui.Field{
		{Label: "api version", Value: string(m.APIVersion)},
		{Label: "digest", Value: r.Digest},
		{Label: "root", Value: r.Root},
		{Label: "runtime", Value: m.Providers.Runtime.Name,
			Note: "(project " + m.Runtime.Project + ")"},
	})

	d.Heading("images")
	rows := make([][]string, 0, len(m.Images))
	for _, name := range sortedKeys(m.Images) {
		rows = append(rows, []string{name, m.Images[name].Ref})
	}
	d.Table(4, ui.Table{
		Columns:  []ui.Column{{Header: "name", Essential: true}, {Header: "reference", Essential: true}},
		Rows:     rows,
		NoHeader: true,
		Empty:    "the release declares no images",
	})

	if len(m.Runtime.Profiles) > 0 {
		d.Heading("profiles")
		d.Text(4, "%s", strings.Join(sortedKeys(m.Runtime.Profiles), ", "))
	}

	d.Heading("compatibility")
	fields := []ui.Field{
		{Label: "rollback safe", Value: fmt.Sprintf("%t", m.Compatibility.RollbackSafe)},
	}
	if !m.Compatibility.UpgradeFrom.IsZero() {
		fields = append(fields, ui.Field{
			Label: "upgrade from", Value: m.Compatibility.UpgradeFrom.String()})
	}
	if m.Compatibility.DatabaseSchemaMax > 0 {
		fields = append(fields, ui.Field{
			Label: "database schema",
			Value: fmt.Sprintf("%d–%d",
				m.Compatibility.DatabaseSchemaMin, m.Compatibility.DatabaseSchemaMax)})
	}
	if !m.Compatibility.MinManagerVersion.IsZero() {
		fields = append(fields, ui.Field{
			Label: "min manager", Value: m.Compatibility.MinManagerVersion.String()})
	}
	d.Fields(4, fields)
	return d
}

func verifiedDoc(d *ui.Doc, v Verified) *ui.Doc {
	d.Title(v.Name + " " + v.VersionInfo.String())
	d.Fields(2, []ui.Field{{Label: "digest", Value: v.Digest}})
	return d
}

// Built is what `release build` and `release archive` answer.
//
// Root, version and digest are the published keys; the name is new to the
// rendering and deliberately not to the JSON, which is what `--json` promised
// before this refactor and must keep promising after it.
type Built struct {
	Root        string         `json:"root"`
	VersionInfo domain.Version `json:"version"`
	Digest      string         `json:"digest"`

	Name string `json:"-"`
}

func init() {
	ui.Register(ui.View[Built]{
		Rich:  func(w io.Writer, t *theme.Theme, b Built) { emit(w, builtDoc(doc(t), b)) },
		Plain: func(w io.Writer, b Built) { emit(w, builtDoc(plainDoc(), b)) },
	})
}

func builtDoc(d *ui.Doc, b Built) *ui.Doc {
	d.Title(b.Name + " " + b.VersionInfo.String())
	d.Fields(2, []ui.Field{
		{Label: "digest", Value: b.Digest},
		{Label: "root", Value: b.Root},
	})
	return d
}
