package views_test

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/ui"
	"github.com/morzecrew/morzer/internal/ui/views"
)

// `release show` names the runtimes a bundle declares and what each was told.
//
// It used to print `providers.runtime.name` with the Compose project beside it,
// which said the wrong thing twice: the provider field is derived and is empty
// for a release declaring two runtimes, and the project came from the
// deprecated block, so a release on the `runtimes:` spelling was shown a
// grouping name it had never set.
func TestReleaseShowNamesEveryRuntimeAndItsOptions(t *testing.T) {
	render := func(m domain.Manifest) string {
		var b bytes.Buffer
		require.NoError(t, ui.Render(&b, ui.ModePlain, nil, views.Release{
			Manifest: m, Root: "/opt/demo/releases/1.2.0", Digest: "sha256:abc",
		}))
		return flatten(b.String())
	}

	base := domain.Metadata{Name: "demo", Version: domain.MustParseVersion("1.2.0")}

	t.Run("one runtime with an option", func(t *testing.T) {
		out := render(domain.Manifest{
			Metadata: base,
			Runtimes: domain.Runtimes{"compose": {
				Files:   []string{"compose.yaml"},
				Options: map[string]string{"project": "myapp"},
			}},
		})
		assert.Contains(t, out, "compose (project=myapp)")
	})

	t.Run("two runtimes, sorted, one of them plain", func(t *testing.T) {
		out := render(domain.Manifest{
			Metadata: base,
			Runtimes: domain.Runtimes{
				"quadlet": {Files: []string{"app.container"}},
				"compose": {Files: []string{"compose.yaml"}, Options: map[string]string{"project": "myapp"}},
			},
		})
		// Sorted, so two runs of `release show` on one bundle agree.
		assert.Contains(t, out, "compose (project=myapp), quadlet")
	})

	t.Run("the legacy block, folded", func(t *testing.T) {
		out := render(domain.Manifest{
			Metadata: base,
			Runtime:  domain.RuntimeSpec{Project: "myapp", Files: []string{"compose.yaml"}},
		})
		assert.Contains(t, out, "compose (project=myapp)",
			"a bundle on the old spelling reads as the compose runtime with its project")
	})

	t.Run("a manifest that declares none", func(t *testing.T) {
		// Not a legal release, and `release show` still has to print
		// something rather than an empty label an operator would read as
		// a rendering failure.
		assert.Contains(t, render(domain.Manifest{Metadata: base}), "none declared")
	})
}
