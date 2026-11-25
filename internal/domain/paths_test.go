package domain_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/domain"
)

// TestRootIsTheInverseOfTheConstructors.
//
// `Paths.Root()` is what lets one installation's layout answer "what else is on
// this machine": `ls` derives a layout per product from it, and `doctor`'s
// machine checks read every installation through the same road. It is derived
// by string surgery on EtcDir rather than stored, so this is the assertion that
// keeps it honest -- both constructors, both roots, and a product name that
// contains the separator characters the layout is built from.
func TestRootIsTheInverseOfTheConstructors(t *testing.T) {
	for _, product := range []string{"demo", "a", "web-ui", "etc", "var-lib"} {
		t.Run(product, func(t *testing.T) {
			assert.Empty(t, domain.DefaultPaths(product).Root(),
				"the production layout has no prefix, and any other answer sends "+
					"`morzer ls` looking under it")

			for _, root := range []string{"/tmp/x", "/tmp/x/etc", "/srv/hosts/one"} {
				assert.Equal(t, root, domain.PathsUnder(root, product).Root())
			}
		})
	}
}

// TestARelocatedLayoutRoundTrips is the use the accessor exists for: a layout
// for one product, taken apart and rebuilt for another, must land where that
// other installation actually lives.
func TestARelocatedLayoutRoundTrips(t *testing.T) {
	const root = "/srv/machine"

	demo := domain.PathsUnder(root, "demo")
	sandbox := domain.PathsUnder(demo.Root(), "sandbox")

	require.Equal(t, domain.PathsUnder(root, "sandbox"), sandbox)
	assert.Equal(t, "/srv/machine/etc/sandbox", sandbox.EtcDir)
	assert.Equal(t, "/srv/machine/var/lib/sandbox", sandbox.VarDir)
}

// TestTheProductionLayoutRoundTripsToo is the same journey with no prefix,
// which is the one every real machine takes -- and the one where an accessor
// that returned "/" instead of "" would put every path under a doubled root.
func TestTheProductionLayoutRoundTripsToo(t *testing.T) {
	demo := domain.DefaultPaths("demo")
	sandbox := domain.PathsUnder(demo.Root(), "sandbox")

	assert.Equal(t, domain.DefaultPaths("sandbox"), sandbox)
	assert.Equal(t, "/etc/sandbox", sandbox.EtcDir)
}

// TestEachLayoutKnowsItsKindAndItsSiblings holds the two derived answers to the
// constructors, as Root is held above. A user layout read as a --root one would
// render its secrets in the tree, on disk, and a sibling built from Root alone
// did exactly that (logs/quadlet-adapter.md, U2 and U8).
func TestEachLayoutKnowsItsKindAndItsSiblings(t *testing.T) {
	layouts := []struct {
		name string
		of   func(product string) domain.Paths
		user bool
	}{
		{"system", domain.DefaultPaths, false},
		{"under a root", func(p string) domain.Paths { return domain.PathsUnder("/srv/machine", p) }, false},
		{"user", func(p string) domain.Paths {
			return domain.UserPaths("/home/u/.local/share", "/run/user/1000", p)
		}, true},
	}
	for _, l := range layouts {
		t.Run(l.name, func(t *testing.T) {
			demo := l.of("demo")
			assert.Equal(t, l.user, demo.UserLayout())
			assert.Equal(t, l.of("sandbox"), demo.For("sandbox"))
		})
	}

	user := domain.UserPaths("/home/u/.local/share", "/run/user/1000", "demo")
	assert.Equal(t, "/home/u/.local/share/morzer", user.Root())
	assert.Equal(t, "/home/u/.local/share/morzer/etc/demo", user.EtcDir)
	assert.Equal(t, "/home/u/.local/share/morzer/var/lib/demo", user.VarDir)
	assert.Equal(t, "/home/u/.local/share/morzer/opt/demo", user.OptDir)
	assert.Equal(t, "/run/user/1000/morzer/demo", user.RunDir)
	assert.False(t, domain.Paths{}.UserLayout(), "a zero layout is nobody's")
}
