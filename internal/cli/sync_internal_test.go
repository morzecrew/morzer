package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/lifecycle/ops"
)

// `sync status` is where a refusal's full text is read, since the fleet row
// never carries it.
func TestSyncStatusPrintsTheRefusalsText(t *testing.T) {
	at := domain.NewTime(time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC))
	var out strings.Builder
	printSyncStatus(&out, ops.SyncStatusReport{
		Enabled: true, Target: "s3://acme-state/demo", AuthorityKeyID: "F5FF50D699EF8CBE", Interval: "15m0s",
		Applied: &domain.SyncDocument{Serial: 100, At: at, ExpiresAt: domain.NewTime(at.Add(7 * 24 * time.Hour))},
		Refused: &domain.SyncDocument{
			Serial: 110, At: at, Step: "plan", Class: "invalid", Names: []string{"log_level"},
			Message: `parameter "log_level": "loud" is not one of debug, info, warn, error`,
		},
		LastFetch:   &domain.SyncFetch{At: at, Result: domain.SyncInvalid},
		LastReached: at,
	})
	for _, want := range []string{
		"sync:        enabled\n",
		"authority:   key F5FF50D699EF8CBE\n",
		"applied:     document 100 at 2026-11-10T12:00:00Z, expires 2026-11-17T12:00:00Z\n",
		"refused:     document 110 at 2026-11-10T12:00:00Z, at plan: invalid (log_level)\n",
		`"loud" is not one of`,
		"last fetch:  invalid at 2026-11-10T12:00:00Z\n",
	} {
		assert.Contains(t, out.String(), want)
	}

	out.Reset()
	printSyncStatus(&out, ops.SyncStatusReport{Interval: "15m0s"})
	assert.Equal(t, "sync:        off\ninterval:    15m0s\n", out.String())
}
