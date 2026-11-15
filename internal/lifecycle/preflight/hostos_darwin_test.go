package preflight

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/events"
)

// On a Mac a release's OS list names Linux distributions, and the platform
// gate has already let the release run, so the check passes and says why
// rather than warning that the host is unknown (RFC 0055 §5.1).
func TestOnAMacTheOSListPassesAsLinuxDistributions(t *testing.T) {
	req := domain.Requirements{OS: []domain.OSRequirement{{ID: "debian"}}}
	res := OperatingSystem(req).Run(context.Background())
	assert.Equal(t, events.CheckOK, res.Status)
	assert.Equal(t, "the release's OS list names Linux distributions, and this host is macOS", res.Message)
}
