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

	// Profiles come from the declared runtimes, not the block that stopped
	// being read. This rendered nothing for every current bundle and nothing
	// failed, because "no profiles declared" and "declared, not found" print
	// the same absence.
	t.Run("profiles declared only under runtimes", func(t *testing.T) {
		out := render(domain.Manifest{
			Metadata: base,
			Runtimes: domain.Runtimes{
				"compose": {Files: []string{"compose.yaml"}, Profiles: map[string][]string{
					"external-db": {"b.yaml"}, "embedded": {"a.yaml"},
				}},
				"quadlet": {Files: []string{"app.container"}, Profiles: map[string][]string{
					"embedded": {"x.container"}, "ha": {"y.container"},
				}},
			},
		})
		assert.Contains(t, out, "profiles")
		assert.Contains(t, out, "embedded, external-db, ha",
			"sorted and deduplicated across runtimes")
	})

	// Combinable names carry a suffix, so alternatives and overlays read in
	// one list (RFC 0053 decision 17).
	t.Run("combinable profiles are marked", func(t *testing.T) {
		out := render(domain.Manifest{
			Metadata: base,
			Runtimes: domain.Runtimes{"compose": {Files: []string{"compose.yaml"},
				Profiles: map[string][]string{
					"embedded": {"a.yaml"}, "xdns": {"x.yaml"}, "xhttp-reality": {"r.yaml"},
				},
				Combinable: []string{"xhttp-reality", "xdns"},
			}},
		})
		assert.Contains(t, out, "embedded, xdns (combinable), xhttp-reality (combinable)")
	})

	// A profile narrowed to some platforms says which, beside its other note,
	// and the release says what it runs on, Linux alone when undeclared (RFC
	// 0055 decisions 2 and 3).
	t.Run("platforms are shown", func(t *testing.T) {
		out := render(domain.Manifest{
			Metadata: base,
			Runtimes: domain.Runtimes{"compose": {Files: []string{"compose.yaml"},
				Profiles:   map[string][]string{"embedded": {"a.yaml"}, "tun": {"t.yaml"}, "xdns": {"x.yaml"}},
				Combinable: []string{"xdns"},
				Platforms: map[string][]domain.Platform{
					"tun": {domain.PlatformLinux}, "xdns": {domain.PlatformLinux},
				},
			}},
			Requirements: domain.Requirements{Platforms: []domain.Platform{domain.PlatformLinux, domain.PlatformDarwin}},
		})
		assert.Contains(t, out, "embedded, tun (linux only), xdns (combinable, linux only)")
		assert.Contains(t, out, "platforms linux, darwin rollback safe")
		assert.Contains(t, render(domain.Manifest{Metadata: base}), "platforms linux rollback safe")
	})

	t.Run("two runtimes narrowing one profile are both shown", func(t *testing.T) {
		narrowed := func(p domain.Platform) domain.RuntimeDecl {
			return domain.RuntimeDecl{Files: []string{"f"}, Profiles: map[string][]string{"tun": {"t"}},
				Platforms: map[string][]domain.Platform{"tun": {p}}}
		}
		out := render(domain.Manifest{Metadata: base, Runtimes: domain.Runtimes{
			"compose": narrowed(domain.PlatformLinux), "quadlet": narrowed(domain.PlatformDarwin),
		}})
		assert.Contains(t, out, "tun (linux only under compose, darwin only under quadlet)")
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

	// Was "the legacy block, folded". Migrating its fixture to `runtimes:`
	// made it a copy of the first subtest and left its name and message
	// describing a fold that no longer happens -- so it asserted a
	// contract the opposite of the one now in force, in words nobody
	// reading the output would question.
	t.Run("the legacy block declares nothing", func(t *testing.T) {
		out := render(domain.Manifest{
			Metadata: base,
			Runtime: domain.RuntimeSpec{
				Files:   []string{"compose.yaml"},
				Project: "myapp",
			},
		})
		assert.Contains(t, out, "none declared",
			"`runtime:` stopped being read in 0.3.0, so a bundle carrying only "+
				"it declares no runtime for `release show` to name")
		assert.NotContains(t, out, "myapp",
			"and its project reaches nothing, or the view would be reporting a "+
				"namespace the manager will not use")
	})

	t.Run("a manifest that declares none", func(t *testing.T) {
		// Not a legal release, and `release show` still has to print
		// something rather than an empty label an operator would read as
		// a rendering failure.
		assert.Contains(t, render(domain.Manifest{Metadata: base}), "none declared")
	})
}
