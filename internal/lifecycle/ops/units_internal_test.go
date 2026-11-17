package ops

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/morzecrew/morzer/internal/ports"
)

// What a Mac's doctor names follows each unit's own switch (RFC 0055
// decision 26): every schedule set, and only those.
func TestUnsupervisedNamesEachScheduleSetAndNoOther(t *testing.T) {
	assert.Equal(t, []string{
		"`apply --startup` at boot", "scheduled backups", "update checks", "fleet publishing",
		"sync", "scheduled doctor runs", "metrics",
	}, unsupervised(ports.UnitParams{
		UpdateTimer: true, FleetTimer: true, SyncTimer: true, DoctorTimer: true, MetricsDir: "/var/lib/metrics",
	}))
	assert.Equal(t, []string{"`apply --startup` at boot"}, unsupervised(ports.UnitParams{SkipBackupTimer: true}))
}
