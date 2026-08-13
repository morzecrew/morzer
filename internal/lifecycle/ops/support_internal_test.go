package ops

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/domain"
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

// What is classified and not yet collected, named here so the debt is visible.
//
// P2 ships every component except container logs, which is the RFC's phase
// order and not an oversight: logs are the only raw vendor bytes in the
// archive, and §9 is blunt that shipping them before the phase that proves
// redaction works is "a leak generator with a progress bar".
//
// P3 deletes this list. A test that merely tolerated uncollected rows would
// tolerate them forever; this one fails when the set changes in either
// direction, so adding a row without a collector is as loud as forgetting to
// remove this list once one exists.
func TestOnlyContainerLogsAreClassifiedAndNotYetCollected(t *testing.T) {
	collected := map[string]bool{}
	for _, name := range supportProduced() {
		collected[name] = true
	}

	var pending []string
	for _, c := range domain.SupportInventory {
		if c.Class != domain.SupportNever && !collected[c.Name] {
			pending = append(pending, c.Name)
		}
	}

	require.Equal(t, []string{"logs/"}, pending,
		"the set of classified-but-uncollected components changed; if this is P3 "+
			"landing container logs, delete this test")
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
