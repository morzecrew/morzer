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
