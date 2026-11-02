package views

import (
	"fmt"
	"io"
	"maps"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/lifecycle/ops"
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
		Rich:  func(w io.Writer, t *theme.Theme, r Release) { emit(w, releaseDoc(doc(w, t), r)) },
		Plain: func(w io.Writer, r Release) { emit(w, releaseDoc(plainDoc(w), r)) },
	})
	ui.Register(ui.View[Verified]{
		Rich:  func(w io.Writer, t *theme.Theme, v Verified) { emit(w, verifiedDoc(doc(w, t), v)) },
		Plain: func(w io.Writer, v Verified) { emit(w, verifiedDoc(plainDoc(w), v)) },
	})
	ui.Register(ui.View[ops.ReleaseDiff]{
		Rich:  func(w io.Writer, t *theme.Theme, r ops.ReleaseDiff) { emit(w, releaseDiffDoc(doc(w, t), r)) },
		Plain: func(w io.Writer, r ops.ReleaseDiff) { emit(w, releaseDiffDoc(plainDoc(w), r)) },
	})
}

// runtimeSummary names the runtimes a release declares, with each one's
// options.
//
// It used to read `providers.runtime.name` and note the Compose project beside
// it, which said the wrong thing twice over: the provider field is derived and
// is empty for a release declaring two runtimes, and the project came from a
// field on the deprecated block, so a release on the `runtimes:` spelling
// showed one it had not set. The options are printed as the vendor wrote them
// because this layer does not know what any of them mean.
func runtimeSummary(m domain.Manifest) string {
	declared := m.DeclaredRuntimes()
	if len(declared) == 0 {
		return "none declared"
	}
	parts := make([]string, 0, len(declared))
	for _, name := range declared.Names() {
		options := declared[name].Options
		if len(options) == 0 {
			parts = append(parts, name)
			continue
		}
		keys := slices.Sorted(maps.Keys(options))
		settings := make([]string, 0, len(keys))
		for _, key := range keys {
			settings = append(settings, key+"="+options[key])
		}
		parts = append(parts, name+" ("+strings.Join(settings, ", ")+")")
	}
	return strings.Join(parts, ", ")
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
		{Label: "runtime", Value: runtimeSummary(m)},
	})

	d.Heading("images")
	rows := make([][]string, 0, len(m.Images))
	for _, name := range slices.Sorted(maps.Keys(m.Images)) {
		rows = append(rows, []string{name, m.Images[name].Ref})
	}
	d.Table(4, ui.Table{
		Columns:  []ui.Column{{Header: "name", Essential: true}, {Header: "reference", Essential: true}},
		Rows:     rows,
		NoHeader: true,
		Empty:    "the release declares no images",
	})

	if profiles := m.ProfileNames(); len(profiles) > 0 {
		d.Heading("profiles")
		d.Text(4, "%s", strings.Join(profiles, ", "))
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
		Rich:  func(w io.Writer, t *theme.Theme, b Built) { emit(w, builtDoc(doc(w, t), b)) },
		Plain: func(w io.Writer, b Built) { emit(w, builtDoc(plainDoc(w), b)) },
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

// Pushed is what `release push` answers (RFC 0039 §5.4).
//
// Two digests, and neither is ever printed bare. A registry reports the
// manifest's, which pins `oci://…@sha256:`; customers pass the content digest to
// `update --digest`. Pipelines confuse the two, so each carries its use.
type Pushed struct {
	Name           string          `json:"-"`
	VersionInfo    domain.Version  `json:"version"`
	Reference      string          `json:"reference"`
	ManifestDigest string          `json:"manifest_digest,omitempty"`
	ContentDigest  string          `json:"content_digest"`
	Published      bool            `json:"published"`
	DryRun         bool            `json:"dry_run"`
	Channels       []PushedChannel `json:"channels"`
}

// PushedChannel is one channel the push was asked to move. Neither moved nor an
// error means an earlier channel failed and this one was not tried.
type PushedChannel struct {
	Name  string `json:"name"`
	Moved bool   `json:"moved"`
	Error string `json:"error,omitempty"`
}

func init() {
	ui.Register(ui.View[Pushed]{
		Rich:  func(w io.Writer, t *theme.Theme, p Pushed) { emit(w, pushedDoc(doc(w, t), p)) },
		Plain: func(w io.Writer, p Pushed) { emit(w, pushedDoc(plainDoc(w), p)) },
	})
}

func pushedDoc(d *ui.Doc, p Pushed) *ui.Doc {
	verb := "pushed"
	switch {
	case !p.Published:
		verb = "already published"
	case p.DryRun:
		verb = "would push"
	}
	d.Title(fmt.Sprintf("%s %s %s to oci://%s", verb, p.Name, p.VersionInfo, p.Reference))

	// Verbatim, past the measure if they must: each line ends in something
	// to copy, and a reference wrapped mid-digest is one nobody can paste.
	if p.ManifestDigest != "" {
		repository := p.Reference[:strings.LastIndex(p.Reference, ":")]
		d.Verbatim(fmt.Sprintf("  manifest  %s  pin with oci://%s@%s",
			p.ManifestDigest, repository, p.ManifestDigest))
	}
	d.Verbatim(fmt.Sprintf("  content   %s  give customers: morzer update --digest %s",
		p.ContentDigest, p.ContentDigest))

	fields := make([]ui.Field, 0, len(p.Channels))
	for _, c := range p.Channels {
		value := "not tried"
		switch {
		case c.Moved && p.DryRun:
			value = "would move to " + p.VersionInfo.String()
		case c.Moved:
			value = "-> " + p.VersionInfo.String()
		case c.Error != "":
			value = "not moved: " + c.Error
		}
		fields = append(fields, ui.Field{Label: "channel " + c.Name, Value: value})
	}
	if len(fields) > 0 {
		d.Fields(2, fields)
	}
	return d
}

// releaseDiffDoc draws what changed between two releases: a section for each
// kind of declaration and one for the files, each only when something in it
// changed. Fields rather than a table, because a table cuts a cell to fit and
// the end of an image reference is its digest. A file's checksums are left to
// --json; its name is what a reader looks up.
func releaseDiffDoc(d *ui.Doc, r ops.ReleaseDiff) *ui.Doc {
	d.Title(shown(r.From) + " -> " + shown(r.To))
	if r.Same() {
		d.Text(2, "%s", d.Theme().Dim("the same declarations and the same files"))
		return d
	}
	for _, s := range []struct {
		name   string
		list   []ops.Change
		detail bool
	}{
		{"images", r.Images, true},
		{"parameters", r.Parameters, true},
		{"secrets", r.Secrets, true},
		{"compatibility", r.Compatibility, true},
		{"tasks", r.Tasks, true},
		{"profiles", r.Profiles, true},
		{"files", r.Files, false},
	} {
		if len(s.list) == 0 {
			continue
		}
		fields := make([]ui.Field, 0, len(s.list))
		for _, c := range s.list {
			value := string(c.Kind)
			if s.detail {
				value = changeDetail(c)
			}
			fields = append(fields, ui.Field{Label: shown(c.Name), Value: value})
		}
		d.Heading(s.name)
		d.Fields(4, fields)
	}
	return d
}

// changeDetail is the kind of a change and the declaration it added or
// removed, or both sides of one it changed. A change compared by name alone
// has no declaration to show.
func changeDetail(c ops.Change) string {
	switch {
	case c.Kind == ops.ChangeChanged:
		return "changed: " + shown(c.From) + " -> " + shown(c.To)
	case c.From+c.To == "":
		return string(c.Kind)
	default:
		return string(c.Kind) + ": " + shown(c.From+c.To)
	}
}

// shown quotes a string the bundle chose when it holds anything a terminal acts
// on rather than prints -- an escape sequence, a carriage return, a bidi
// override, a byte that is not UTF-8 -- so the reader sees what is there and the
// terminal does nothing. A stray byte is checked apart because ranging over it
// yields U+FFFD, which is printable.
func shown(s string) string {
	if !utf8.ValidString(s) || strings.IndexFunc(s, func(r rune) bool { return !unicode.IsPrint(r) }) >= 0 {
		return strconv.Quote(s)
	}
	return s
}
