package ops

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/lifecycle/engine"
)

// The suite drives the case it can stage -- a hook reporting a schema the old
// release cannot read, and one it can. These are the branches no fixture
// reaches: a hook that reports nothing, a fallback whose manifest could not be
// read, a release declaring its migrations one-way, and an operation with no
// fallback at all.
func TestTheWayBackClosesOnlyWhenTheFallbackCannotRead(t *testing.T) {
	v := domain.MustParseVersion
	oldRelease := fallbackRelease{
		Version: v("1.2.0"), Known: true,
		Compatibility: domain.Compatibility{DatabaseSchemaMax: 12, RollbackSafe: true},
	}
	newRelease := func(produces int, rollbackSafe bool) domain.Release {
		var rel domain.Release
		rel.Manifest.Metadata.Version = v("1.3.0")
		rel.Manifest.Compatibility = domain.Compatibility{
			DatabaseSchemaProduces: produces, RollbackSafe: rollbackSafe,
		}
		return rel
	}

	for _, tc := range []struct {
		name     string
		fallback *fallbackRelease
		rel      domain.Release
		reported int
		closes   bool
	}{
		{"no fallback: apply, rollback, a first install", nil, newRelease(14, true), 14, false},
		{"reported schema the fallback reads", &oldRelease, newRelease(14, true), 12, false},
		{"reported schema beyond it", &oldRelease, newRelease(12, true), 14, true},
		{"nothing reported, the declaration is beyond it", &oldRelease, newRelease(14, true), 0, true},
		{"nothing reported or declared, release says rollback is safe", &oldRelease, newRelease(0, true), 0, false},
		{"migrations declared one-way", &oldRelease, newRelease(12, false), 12, true},
		{"fallback manifest unreadable", &fallbackRelease{Version: v("1.2.0")}, newRelease(12, true), 12, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := &engine.State{}
			if tc.fallback != nil {
				st.Set(engine.KeyFallbackRelease, *tc.fallback)
			}
			passIfFallbackCannotRead(st, tc.rel, tc.reported)
			assert.Equal(t, tc.closes, st.PointOfNoReturn() != "", "reason: %q", st.PointOfNoReturn())
		})
	}
}
