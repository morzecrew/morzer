package ops

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/morzecrew/morzer/internal/ports"
)

// What a Mac's doctor names follows each unit's own switch (RFC 0055
// decision 26): every schedule set, and only those.
func TestUnsupervisedNamesEachScheduleSetAndNoOther(t *testing.T) {
	const boot = "`apply --startup` at boot"
	for name, tc := range map[string]struct {
		params ports.UnitParams
		want   []string
	}{
		"none":    {ports.UnitParams{SkipBackupTimer: true}, []string{boot}},
		"backups": {ports.UnitParams{}, []string{boot, "scheduled backups"}},
		"update":  {ports.UnitParams{SkipBackupTimer: true, UpdateTimer: true}, []string{boot, "update checks"}},
		"fleet":   {ports.UnitParams{SkipBackupTimer: true, FleetTimer: true}, []string{boot, "fleet publishing"}},
		"sync":    {ports.UnitParams{SkipBackupTimer: true, SyncTimer: true}, []string{boot, "sync"}},
		"doctor":  {ports.UnitParams{SkipBackupTimer: true, DoctorTimer: true}, []string{boot, "scheduled doctor runs"}},
		"metrics": {ports.UnitParams{SkipBackupTimer: true, MetricsDir: "/var/lib/metrics"}, []string{boot, "metrics"}},
		"all": {ports.UnitParams{UpdateTimer: true, FleetTimer: true, SyncTimer: true, DoctorTimer: true, MetricsDir: "/var/lib/metrics"},
			[]string{boot, "scheduled backups", "update checks", "fleet publishing", "sync", "scheduled doctor runs", "metrics"}},
	} {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tc.want, unsupervised(tc.params))
		})
	}
}
