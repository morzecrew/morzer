package domain

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A manifest declaring preflight checks, at the manager version that runs them
// (RFC 0045).
func manifestWithChecks(checks map[string]PreflightCheck) Manifest {
	m := validManifest()
	m.Compatibility.MinManagerVersion = MustParseVersion(PreflightMinManagerVersion)
	m.Preflight = checks
	m.ApplyDefaults()
	return m
}

func TestAWellFormedCheckValidatesAndTakesTheDefaultTimeout(t *testing.T) {
	m := manifestWithChecks(map[string]PreflightCheck{
		"max-map-count": {Description: "enough memory maps", Command: []string{"hooks/preflight", "max-map-count"}},
	})
	require.NoError(t, m.Validate())
	assert.Equal(t, DefaultPreflightTimeout, m.Preflight["max-map-count"].EffectiveTimeout())
	assert.Equal(t, DefaultPreflightTimeout, m.PreflightTotal())
}

func TestAMalformedCheckIsRefusedByField(t *testing.T) {
	ok := PreflightCheck{Description: "fine", Command: []string{"hooks/check"}}
	with := func(edit func(*PreflightCheck)) PreflightCheck {
		c := ok
		edit(&c)
		return c
	}
	cases := map[string]struct {
		name  string
		check PreflightCheck
		want  string
	}{
		"no description":        {"db", with(func(c *PreflightCheck) { c.Description = "" }), "preflight.db.description: is required"},
		"multi-line describe":   {"db", with(func(c *PreflightCheck) { c.Description = "a\nb" }), "preflight.db.description: must be one line"},
		"escape in describe":    {"db", with(func(c *PreflightCheck) { c.Description = "\x1b[2Jfine" }), "preflight.db.description: must be one line"},
		"long describe":         {"db", with(func(c *PreflightCheck) { c.Description = strings.Repeat("x", 201) }), "preflight.db.description: must be at most 200"},
		"escape in remedy":      {"db", with(func(c *PreflightCheck) { c.Remedy = "run \x1b]0;x\a" }), "preflight.db.remedy: must be one line"},
		"long remedy":           {"db", with(func(c *PreflightCheck) { c.Remedy = strings.Repeat("x", 201) }), "preflight.db.remedy: must be at most 200"},
		"blank description":     {"db", with(func(c *PreflightCheck) { c.Description = "   " }), "preflight.db.description: is required"},
		"bidi in describe":      {"db", with(func(c *PreflightCheck) { c.Description = "safe \u202edeteled" }), "preflight.db.description: must be one line"},
		"bidi in remedy":        {"db", with(func(c *PreflightCheck) { c.Remedy = "run \u2066x" }), "preflight.db.remedy: must be one line"},
		"no command":            {"db", with(func(c *PreflightCheck) { c.Command = nil }), "preflight.db.command: is required"},
		"absolute command":      {"db", with(func(c *PreflightCheck) { c.Command = []string{"/usr/bin/true"} }), "preflight.db.command[0]: must be relative"},
		"escaping command":      {"db", with(func(c *PreflightCheck) { c.Command = []string{"../check"} }), "preflight.db.command[0]: must not escape"},
		"negative timeout":      {"db", with(func(c *PreflightCheck) { c.Timeout = -1 }), "preflight.db.timeout: must be positive"},
		"timeout past the cap":  {"db", with(func(c *PreflightCheck) { c.Timeout = Duration(MaxPreflightTimeout + time.Second) }), "preflight.db.timeout: must be at most 2m0s"},
		"upper-case name":       {"DB", ok, "preflight.DB: is not a valid check name"},
		"name with whitespace":  {"db host", ok, "is not a valid check name"},
		"name with a dot":       {"db.host", ok, "is not a valid check name"},
		"name starting a digit": {"1db", ok, "is not a valid check name"},
	}
	for label, tc := range cases {
		t.Run(label, func(t *testing.T) {
			m := manifestWithChecks(map[string]PreflightCheck{tc.name: tc.check})
			err := m.Validate()
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}

// Each check may take its 2m and no more, and all of them 5m together: they
// run one after another before every operation and in every scheduled doctor
// (decision 18).
func TestChecksMayTakeFiveMinutesBetweenThem(t *testing.T) {
	checks := map[string]PreflightCheck{}
	for _, name := range []string{"a", "b"} {
		checks[name] = PreflightCheck{Description: "x", Command: []string{"hooks/check"}, Timeout: Duration(2 * time.Minute)}
	}
	checks["c"] = PreflightCheck{Description: "x", Command: []string{"hooks/check"}, Timeout: Duration(time.Minute)}
	full := manifestWithChecks(checks)
	require.NoError(t, full.Validate(), "exactly the ceiling is allowed")

	// The default counts: a fourth check that names no timeout takes 30s.
	checks["d"] = PreflightCheck{Description: "x", Command: []string{"hooks/check"}}
	over := manifestWithChecks(checks)
	err := over.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "preflight: may take at most 5m0s between its checks, and their timeouts add up to 5m30s")

	at := manifestWithChecks(map[string]PreflightCheck{
		"slow": {Description: "x", Command: []string{"hooks/check"}, Timeout: Duration(MaxPreflightTimeout)},
	})
	require.NoError(t, at.Validate(), "a check at exactly its ceiling is allowed")
}

// An older manager cannot decode `preflight:`, so the bundle has to say which
// manager it needs; one that skipped the checks would install onto the machine
// they exist to refuse (decision 2).
func TestChecksNeedTheManagerThatRunsThem(t *testing.T) {
	m := manifestWithChecks(map[string]PreflightCheck{
		"db": {Description: "x", Command: []string{"hooks/check"}},
	})
	m.Compatibility.MinManagerVersion = MustParseVersion("0.11.0")

	err := m.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "compatibility.min_manager_version: must be at least "+
		PreflightMinManagerVersion+", because the release declares preflight checks")
}

func TestChecksRunInNameOrder(t *testing.T) {
	m := manifestWithChecks(map[string]PreflightCheck{
		"zeta":  {Description: "x", Command: []string{"hooks/check"}},
		"alpha": {Description: "x", Command: []string{"hooks/check"}},
		"mid":   {Description: "x", Command: []string{"hooks/check"}},
	})
	assert.Equal(t, []string{"alpha", "mid", "zeta"}, m.PreflightNames())
}

// The bound is in characters, as the reference says: a description in a script
// whose letters take two bytes each is not refused at half the length.
func TestTheDescriptionBoundCountsCharacters(t *testing.T) {
	m := manifestWithChecks(map[string]PreflightCheck{
		"db": {Description: strings.Repeat("é", 200), Command: []string{"hooks/check"}},
	})
	require.NoError(t, m.Validate())
	m.Preflight["db"] = PreflightCheck{Description: strings.Repeat("é", 201), Command: []string{"hooks/check"}}
	err := m.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must be at most 200")
}
