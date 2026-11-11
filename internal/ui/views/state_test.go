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

// A row this run could not read or trust says nothing about sync, so the
// table says it does not know, not that the machine follows nothing.
func TestStateSaysUnknownForARowItCouldNotTrust(t *testing.T) {
	var b bytes.Buffer
	require.NoError(t, ui.Render(&b, ui.ModePlain, nil, views.State{
		Targets: []string{"s3://acme-state/demo"},
		Rows: []views.StateRow{
			{Product: "demo", InstallationID: "inst_A", Signature: "signed-by-another-key", Problem: "the roster names another key"},
			{Product: "demo", InstallationID: "inst_B", Signature: "verified"},
			{Product: "demo", InstallationID: "inst_C", Signature: "verified", Published: 12, Sync: &domain.FleetSync{
				Enabled: true, Applied: &domain.FleetSyncDocument{Serial: 11},
				Refused:   &domain.FleetSyncDocument{Serial: 12, Step: "validate", Class: "unmanageable", Names: []string{"http_port"}},
				LastFetch: &domain.SyncFetch{Result: domain.SyncUnmanageable},
			}},
		},
	}))
	out := b.String()
	assert.Regexp(t, `inst_A\s+-\s+unknown`, out)
	assert.Regexp(t, `inst_B\s+-\s+not following`, out)
	assert.Regexp(t, `inst_C\s+12\s+11\s+12 at validate: unmanageable\s+unmanageable`, out)
	assert.Contains(t, out, "refused for http_port")
}
