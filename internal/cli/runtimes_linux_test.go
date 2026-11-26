package cli

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// Root is offered no runtime beside the incumbent: a Quadlet installation runs
// as the user who owns it (RFC 0023 row 26), so `init --runtime quadlet` as
// root is refused as a runtime this manager cannot drive here.
func TestRootIsOfferedNoRuntimeBesideTheIncumbent(t *testing.T) {
	assert.Empty(t, hostRuntimes(nil, nil, nil, 0))
	assert.Len(t, hostRuntimes(nil, nil, nil, 1000), 1, "a user is offered Quadlet")
}
