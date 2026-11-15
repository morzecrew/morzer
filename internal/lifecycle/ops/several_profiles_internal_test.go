package ops

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/morzecrew/morzer/internal/domain"
)

// `release diff` reports `combinable` as one entry beside the names, in
// layering order, since a reordering changes which overlay wins (RFC 0053
// decision 17).
func TestADiffReportsCombinableAsOneEntry(t *testing.T) {
	manifest := func(combinable ...string) domain.Manifest {
		return domain.Manifest{Runtimes: domain.Runtimes{"compose": {
			Files:      []string{"compose.yaml"},
			Profiles:   map[string][]string{"xdns": {"x.yaml"}, "xhttp-reality": {"r.yaml"}},
			Combinable: combinable,
		}}}
	}

	assert.Empty(t, profileChanges(manifest(), manifest()))
	assert.Equal(t, []Change{{Name: "combinable", Kind: ChangeAdded, To: "xdns, xhttp-reality"}},
		profileChanges(manifest(), manifest("xdns", "xhttp-reality")))
	assert.Equal(t, []Change{{Name: "combinable", Kind: ChangeChanged,
		From: "xdns, xhttp-reality", To: "xhttp-reality, xdns"}},
		profileChanges(manifest("xdns", "xhttp-reality"), manifest("xhttp-reality", "xdns")))
	assert.Equal(t, []Change{{Name: "combinable", Kind: ChangeRemoved, From: "xdns"}},
		profileChanges(manifest("xdns"), manifest()))
}

// `doctor` compares the report's selection with the state's as sets, and a
// report written before schema 18, naming one profile in the old field, is the
// same selection rather than drift.
func TestTheReportsSelectionIsComparedAsASet(t *testing.T) {
	recorded := domain.Installation{Profiles: []string{"embedded", "xdns"}}

	assert.Empty(t, installationDifferences(domain.Installation{Profiles: []string{"xdns", "embedded"}}, recorded))
	assert.Equal(t, []string{"profile"},
		installationDifferences(domain.Installation{Profiles: []string{"embedded"}}, recorded))
	assert.Empty(t, installationDifferences(domain.Installation{Profile: "embedded"},
		domain.Installation{Profiles: []string{"embedded"}}), "a report from before schema 18 is not drift")
}

// A profile's platforms are its value in a diff, and the release's are a
// compatibility entry, since an update and a rollback gate on both (RFC 0055
// decisions 2 and 3).
func TestADiffReportsPlatforms(t *testing.T) {
	manifest := func(release []domain.Platform, tun ...domain.Platform) domain.Manifest {
		decl := domain.RuntimeDecl{Files: []string{"compose.yaml"}, Profiles: map[string][]string{"tun": {"t.yaml"}}}
		if len(tun) > 0 {
			decl.Platforms = map[string][]domain.Platform{"tun": tun}
		}
		return domain.Manifest{Runtimes: domain.Runtimes{"compose": decl},
			Requirements: domain.Requirements{Platforms: release}}
	}
	both := []domain.Platform{domain.PlatformLinux, domain.PlatformDarwin}

	assert.Equal(t, []Change{{Name: "tun", Kind: ChangeChanged, From: "", To: "on linux"}},
		profileChanges(manifest(both), manifest(both, domain.PlatformLinux)))
	assert.Equal(t, "linux, darwin", compatibility(domain.Compatibility{}, manifest(both).Requirements)["platforms"])
	assert.NotContains(t, compatibility(domain.Compatibility{}, domain.Requirements{}), "platforms")
}
