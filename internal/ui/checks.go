package ui

import "github.com/morzecrew/morzer/internal/events"

// CheckGroup is one category's diagnostics.
type CheckGroup struct {
	Category string
	Results  []events.CheckResult
}

// GroupChecks arranges diagnostics by category, keeping categories in the order
// they first appeared.
//
// Checks arrive in execution order, which interleaves categories: a storage
// check runs early and another runs late. Grouping keeps the report scannable
// instead of naming "storage" three times.
//
// It lives here, above both presenters, because plain and rich must group
// identically. Two implementations of "the same table" is how the two renderers
// start disagreeing about what the system found.
func GroupChecks(results []events.CheckResult) []CheckGroup {
	var groups []CheckGroup
	index := map[string]int{}

	for _, res := range results {
		i, seen := index[res.Category]
		if !seen {
			i = len(groups)
			index[res.Category] = i
			groups = append(groups, CheckGroup{Category: res.Category})
		}
		groups[i].Results = append(groups[i].Results, res)
	}
	return groups
}

// Remedies are the actions a report suggests, in execution order.
//
// Collected into their own section rather than inlined, so the table stays
// scannable and the actions stay together. A check with no remedy is not
// listed: a diagnostic that says something is wrong without saying what to do
// has done half a job, and repeating the complaint is not the other half.
func Remedies(results []events.CheckResult) []events.CheckResult {
	var out []events.CheckResult
	for _, res := range results {
		if res.Status != events.CheckOK && res.Remedy != "" {
			out = append(out, res)
		}
	}
	return out
}
