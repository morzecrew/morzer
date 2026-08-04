package ports

import (
	"context"
	"reflect"
	"sort"

	"github.com/morzecrew/morzer/internal/domain"
)

// Renderer turns a release template into bytes.
//
// It runs in strict mode: an unknown key is an error, never an empty string.
// A configuration file silently missing a value is the failure mode this rule
// exists to prevent -- the product starts, appears fine, and behaves wrongly.
type Renderer interface {
	Render(ctx context.Context, tmpl TemplateRef, data TemplateData) ([]byte, error)
}

// TemplateRef locates a template inside a release.
type TemplateRef struct {
	// Path is the absolute path to the template file.
	Path string

	// Name is the bundle-relative name, used in error messages so an
	// author sees the path they wrote in the manifest.
	Name string
}

// TemplateData is the documented, stable context every template sees.
//
// It deliberately does not include secret *values*. Templates reference
// secrets by the path they are rendered to, so a configuration file in /etc
// never contains a credential -- it contains a pointer to one in /run.
type TemplateData struct {
	// Installation is the machine-specific state.
	Installation domain.Installation `json:"installation"`

	// Release is the release metadata: name, version, digest.
	Release ReleaseInfo `json:"release"`

	// Profile is the active deployment profile.
	Profile string `json:"profile"`

	// Paths is the resolved directory layout.
	Paths PathInfo `json:"paths"`

	// Secrets maps a secret name to the absolute path of its rendered
	// file. References, never values.
	Secrets map[string]string `json:"secrets"`

	// Domains is the configured public names, first one canonical.
	Domains []string `json:"domains"`

	// Parameters is the release's declared knobs, resolved for this
	// installation: every declared name is present, holding either the
	// operator's value or the release's default.
	Parameters domain.Parameters `json:"parameters"`
}

// ReleaseInfo is the release facts templates may use.
type ReleaseInfo struct {
	Name    string         `json:"name"`
	Version domain.Version `json:"version"`
	Digest  string         `json:"digest"`
	Root    string         `json:"root"`
	Vendor  string         `json:"vendor,omitempty"`
}

// PathInfo is the directory layout templates may use.
type PathInfo struct {
	Etc       string `json:"etc"`
	Var       string `json:"var"`
	Run       string `json:"run"`
	Opt       string `json:"opt"`
	Data      string `json:"data"`
	Backups   string `json:"backups"`
	Secrets   string `json:"secrets"`
	Generated string `json:"generated"`
}

// TemplateFields are the top-level names a configuration template may use.
//
// The render context is an ABI: a vendor writes `{{ .Paths.Data }}` against it
// and a rename breaks every bundle in the field. Derived from the struct rather
// than restated, so the list cannot claim a field that does not exist.
//
// `tools/docscheck` gates it, and a test asserts the set is what is documented.
func TemplateFields() []string {
	t := reflect.TypeOf(TemplateData{})
	out := make([]string, 0, t.NumField())
	for i := range t.NumField() {
		out = append(out, t.Field(i).Name)
	}
	sort.Strings(out)
	return out
}
