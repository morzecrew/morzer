package ops

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/ports"
)

// The binding between the inventory and the collectors (RFC 0024 decision 2).
//
// The generated reference page is what an operator reads to decide whether this
// archive is safe to send. It is produced from `domain.SupportInventory`, so it
// describes what the *table* says -- and the table is only a description of the
// software if every collector is in it. Without this test the page and the
// archive are two documents that happen to agree today.

// A collector whose name is not classified is a component leaving the machine
// without a row on the page that promises what leaves it.
func TestEveryCollectorIsClassified(t *testing.T) {
	for _, c := range supportCollectors {
		require.Truef(t, domain.SupportCollected(c.Name),
			"a collector writes %s, which the inventory does not classify for inclusion: "+
				"the reference page would not mention it and an operator reading that "+
				"page would be wrong about what they sent", c.Name)
	}
}

// And a refusal can never acquire one, which is the direction that matters.
//
// `SupportCollected` answers false for an unknown name as well as for a refused
// one, so the test above would pass if the inventory dropped a row entirely.
// This asserts the refusals by name against the collectors directly.
func TestNoCollectorProducesARefusedComponent(t *testing.T) {
	for _, refusal := range domain.SupportComponents(domain.SupportNever) {
		for _, c := range supportCollectors {
			require.NotEqualf(t, refusal.Name, c.Name,
				"a collector produces %q, which is classified never", refusal.Title)
		}
	}
}

// Every collector's name is distinct, because they are written into one flat
// archive and the second writer of a name silently wins.
func TestNoTwoCollectorsWriteTheSameEntry(t *testing.T) {
	seen := map[string]bool{}
	for _, c := range supportCollectors {
		require.Falsef(t, seen[c.Name], "two collectors write %s", c.Name)
		seen[c.Name] = true
	}
}

// Every classified component is collected: the inventory and the build agree in
// both directions.
//
// This replaces the list P2 carried of components that were classified and not
// yet collected. That list held exactly one name, `logs/`, and deleting it is
// what P3 landing means -- so the assertion is now the strong one, and a row
// added to the page without a collector fails here rather than being tolerated.
func TestEveryClassifiedComponentIsCollected(t *testing.T) {
	collected := map[string]bool{}
	for _, name := range supportProduced() {
		collected[name] = true
	}

	for _, c := range domain.SupportInventory {
		if c.Class == domain.SupportNever {
			continue
		}
		require.Truef(t, collected[c.Name],
			"%s is on the reference page and nothing collects it, so the page "+
				"promises an operator a component the archive does not have", c.Name)
	}
}

// supportTitle answers for a per-service log file from its directory row.
//
// Written before the collector it serves, because the naming rule is the part
// that has to be decided rather than discovered: `logs/` is one component that
// happens to be several files, and a report that titled each file after itself
// would list a component the inventory does not have.
func TestALogFileIsTitledByItsComponent(t *testing.T) {
	var logsRow domain.SupportComponent
	for _, c := range domain.SupportInventory {
		if strings.HasSuffix(c.Name, "/") {
			logsRow = c
			break
		}
	}
	require.NotEmpty(t, logsRow.Name, "no directory-shaped component in the inventory")

	require.Equal(t, logsRow.Title, supportTitle(logsRow.Name+"web.log"))
	require.Equal(t, logsRow.Title, supportTitle(logsRow.Name))

	// And a name nothing claims is returned as itself rather than
	// mislabelled with a neighbour's title.
	require.Equal(t, "unknown.txt", supportTitle("unknown.txt"))
}

// A line that belongs to no service still gets a file.
//
// The runtime narrates about the stream itself -- a container that exited, a
// restart loop -- and those lines carry no frame. They are often the ones that
// explain why the rest stopped, so they go to `runtime.log` rather than being
// dropped for having no owner.
func TestALineWithNoServiceStillHasAFile(t *testing.T) {
	require.Equal(t, "web.log", logFileName(ports.LogLine{Service: "web", Container: "web-1"}))

	// No service attribution -- a container renamed by `container_name:`,
	// which the project listing cannot map back.
	require.Equal(t, "web-1.log", logFileName(ports.LogLine{Container: "web-1"}))

	require.Equal(t, "runtime.log", logFileName(ports.LogLine{Text: "container exited"}))
}

// An empty `--dir` is the working directory, and the answer is absolute.
//
// The default an operator hits, and the one no test reached: every suite test
// passes an explicit directory, so the branch that resolves the working
// directory was only ever exercised by the acceptance lane.
func TestAnEmptyDirIsTheWorkingDirectory(t *testing.T) {
	wd, err := os.Getwd()
	require.NoError(t, err)

	resolved, err := archiveDir("")
	require.NoError(t, err)
	require.Equal(t, wd, resolved)

	relative, err := archiveDir("some/nested/place")
	require.NoError(t, err)
	require.True(t, filepath.IsAbs(relative),
		"a relative --dir stayed relative, so `.data.path` would be too")
}
