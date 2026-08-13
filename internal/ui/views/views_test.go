package views_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/events"
	"github.com/morzecrew/morzer/internal/lifecycle/ops"
	"github.com/morzecrew/morzer/internal/ports"
	"github.com/morzecrew/morzer/internal/ui"
	"github.com/morzecrew/morzer/internal/ui/theme"
	"github.com/morzecrew/morzer/internal/ui/views"
)

// flatten normalises whitespace, so an assertion about information is not an
// assertion about layout.
func flatten(s string) string { return strings.Join(strings.Fields(stripANSI(s)), " ") }

// TestEveryReportTypeHasAView is §6's runtime half.
//
// The compile-time half is the type parameter on ui.Register. This is the half
// that fails the build rather than an operator's terminal when a command starts
// rendering a report nobody wrote a view for: app.render returns an internal
// error on an unregistered type, and an internal error discovered in production
// is a defect discovered by the wrong person.
func TestEveryReportTypeHasAView(t *testing.T) {
	want := []any{
		ops.Status{},
		ops.DoctorReport{},
		views.Verbose{},
		ops.ConfigReport{},
		ops.SettingsReport{},
		views.Version{},
		views.KeyPair{},
		views.Value{},
		views.Release{},
		views.Verified{},
		views.Built{},
		[]ports.SecretMetadata{},
		[]ports.RenderedFile{},
		[]ports.Recipient{},
		[]ports.BackupRef{},
		[]ops.ReleaseEntry{},
		[]ops.RemoteBackup{},
		[]ops.TargetStatus{},
		[]ops.InstallationEntry{},
		views.WithServices{},
		views.Verification{},
		views.AttestationLog{},
		ops.SupportReport{},
	}

	for _, report := range want {
		var b bytes.Buffer
		require.NoErrorf(t, ui.Render(&b, ui.ModePlain, nil, report),
			"%T reaches app.render and has no view", report)
	}
}

// TestAnUnregisteredReportIsAnErrorNotSilence.
//
// The failure mode this rules out is the worst one a renderer has: a command
// that runs, exits 0 and prints nothing, because the value fell through a type
// switch nobody updated.
func TestAnUnregisteredReportIsAnErrorNotSilence(t *testing.T) {
	type unknownReport struct{ Field string }

	var b bytes.Buffer
	err := ui.Render(&b, ui.ModePlain, nil, unknownReport{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "unknownReport")
	require.Empty(t, b.String())
}

// TestDoctorGroupsByCategoryInFirstSeenOrder.
//
// Categories appear where their first check ran and a category's checks stay
// adjacent, which is what ui.GroupChecks produces. Neither alphabetical nor
// execution order: a storage check running between two tools checks must not
// split the tools group.
func TestDoctorGroupsByCategoryInFirstSeenOrder(t *testing.T) {
	report := ops.DoctorReport{Results: []events.CheckResult{
		{Category: "tools", Description: "docker version", Status: events.CheckOK},
		{Category: "storage", Description: "free disk space", Status: events.CheckOK},
		{Category: "tools", Description: "compose version", Status: events.CheckOK},
		{Category: "secrets", Description: "age identity present", Status: events.CheckOK},
	}}

	out := render(t, 100, views.Verbose{DoctorReport: report})

	tools, storage, secrets := strings.Index(out, "tools"),
		strings.Index(out, "storage"), strings.Index(out, "secrets")
	require.Greater(t, storage, tools, "categories are not in first-seen order:\n%s", out)
	require.Greater(t, secrets, storage, "categories are not in first-seen order:\n%s", out)

	toolsBlock := out[tools:storage]
	require.Contains(t, toolsBlock, "docker version", "the tools group was split:\n%s", out)
	require.Contains(t, toolsBlock, "compose version", "the tools group was split:\n%s", out)
}

// TestDoctorCollapsesPassingGroupsAndVerboseExpands is the density rule.
//
// Asserted against a fixture rather than a live run, so what is pinned is the
// rule and not what a particular machine's checks happen to find. Three claims,
// because any one of them passes on a renderer that gets the other two wrong: a
// clean group is one line, a single warning expands exactly its own group, and
// --verbose expands all of them.
func TestDoctorCollapsesPassingGroupsAndVerboseExpands(t *testing.T) {
	clean := ops.DoctorReport{Results: []events.CheckResult{
		{Category: "tools", Description: "docker version", Status: events.CheckOK},
		{Category: "tools", Description: "compose version", Status: events.CheckOK},
		{Category: "storage", Description: "free disk space", Status: events.CheckOK},
	}}

	collapsed := render(t, 100, clean)
	require.NotContains(t, collapsed, "docker version",
		"a passing group was expanded:\n%s", collapsed)
	require.Contains(t, collapsed, "2 checks", "the collapsed group does not say how many")
	require.Contains(t, collapsed, "1 check", "a single check is reported as \"1 checks\"")

	// One warning, in tools. Its group expands; storage does not.
	warned := clean
	warned.Results = append([]events.CheckResult(nil), clean.Results...)
	warned.Results[1].Status = events.CheckWarn
	warned.Results[1].Message = "compose 2.20 is older than the release asks for"

	partial := render(t, 100, warned)
	require.Contains(t, partial, "compose version", "the warned check is hidden")
	require.Contains(t, partial, "older than the release asks for")
	require.NotContains(t, partial, "docker version",
		"a passing check in the warned group was expanded too:\n%s", partial)
	require.NotContains(t, partial, "free disk space",
		"an unrelated group expanded:\n%s", partial)

	verbose := render(t, 100, views.Verbose{DoctorReport: warned})
	for _, want := range []string{"docker version", "compose version", "free disk space"} {
		require.Containsf(t, verbose, want, "--verbose hid %q:\n%s", want, verbose)
	}
}

// TestEveryStateIsDistinguishableWithoutColour is the accessibility half, and
// the one that quietly rots.
//
// NO_COLOR, a monochrome console, a colour-blind reader and a pipe into a file
// are supported targets rather than degraded ones. Rendered with colour off, a
// passing check, a warning and a failure must still be three different things.
func TestEveryStateIsDistinguishableWithoutColour(t *testing.T) {
	report := ops.DoctorReport{Results: []events.CheckResult{
		{Category: "tools", Description: "docker version", Status: events.CheckOK},
		{Category: "network", Description: "registry reachable", Status: events.CheckWarn, Message: "timeout"},
		{Category: "runtime", Description: "images present", Status: events.CheckFail, Message: "2 missing"},
	}}

	t.Setenv("COLUMNS", "100")
	var b bytes.Buffer
	require.NoError(t, ui.Render(&b, ui.ModeRich, theme.New(false, false), views.Verbose{DoctorReport: report}))
	out := b.String()

	require.NotContains(t, out, "\x1b", "colour was disabled and the output is styled anyway")
	sym := theme.ASCIISymbols
	for name, symbol := range map[string]string{"ok": sym.OK, "warn": sym.Warn, "fail": sym.Fail} {
		require.Containsf(t, out, symbol, "no %s marker (%q) in a monochrome report:\n%s", name, symbol, out)
	}
}

// TestTheConfigViewNamesTheSourceWithoutColour.
//
// Highlighting an operator-set value is the fast path; the word is what survives
// a pipe, a CI log and a monochrome terminal.
func TestTheConfigViewNamesTheSourceWithoutColour(t *testing.T) {
	out := flatten(render(t, 100, configReport()))
	require.Contains(t, out, "installation",
		"a monochrome reader cannot tell a chosen value from a default:\n%s", out)
	require.Contains(t, out, "release")
}

// TestAParameterWithNoServicesSaysSo stops an operator assuming a change took
// effect. There is nothing to re-create, so it waits for the next apply.
func TestAParameterWithNoServicesSaysSo(t *testing.T) {
	t.Setenv("COLUMNS", "100")

	for _, mode := range []ui.Mode{ui.ModeRich, ui.ModePlain} {
		var b bytes.Buffer
		require.NoError(t, ui.Render(&b, mode, theme.New(false, false), configReport()))
		require.Containsf(t, flatten(b.String()), "next apply",
			"%s does not say that site_name waits for an apply:\n%s", mode, b.String())
	}
}

// TestANarrowTerminalDropsColumnsRatherThanWrappingCells.
//
// Decision 13. A wrapped cell destroys the alignment that is the only reason to
// draw a table, so the degradation is a declared decision: inessential columns
// go, in reverse declaration order, and a footer says which. The essential ones
// never go — a row whose name is missing identifies nothing.
func TestANarrowTerminalDropsColumnsRatherThanWrappingCells(t *testing.T) {
	wide := render(t, 100, configReport())
	require.Contains(t, wide, "SOURCE", "the source column is missing at full width")

	// Thirty rather than sixty: this table's four columns fit in 40, and a
	// test that asserted a drop at a width where nothing needs dropping
	// would be asserting the opposite of the rule.
	narrow := render(t, 30, configReport())
	require.NotContains(t, narrow, "SOURCE", "nothing was dropped at 30 columns:\n%s", narrow)
	require.Contains(t, narrow, "hidden", "the dropped columns are not reported:\n%s", narrow)
	require.Contains(t, narrow, "site_name", "an essential column was dropped:\n%s", narrow)
}

// TestASquashedTableStillTellsTwoReplicasApart is the one column `ps` and
// `stats` may not drop.
//
// Both draw one row per container, so a scaled service is several rows under
// one name — and without the instance the table degrades into identical rows,
// which is worse than the overflow it was avoiding, because nothing says two of
// them are two. Every other column goes first, and the footer names them.
func TestASquashedTableStillTellsTwoReplicasApart(t *testing.T) {
	image := "ghcr.io/demo/app@sha256:" + strings.Repeat("8a", 32)
	services := []ports.ServiceState{
		{Name: "app", Container: "demo-app-1", State: "running", Health: ports.HealthHealthy,
			Image: image, Status: "Up 3 hours"},
		{Name: "app", Container: "demo-app-2", State: "running", Health: ports.HealthHealthy,
			Image: image, Status: "Up 3 hours"},
	}
	rx := int64(1024)
	stats := []ports.ServiceStats{
		{Service: "app", Container: "demo-app-1", Replica: 1, CPUPercent: 12.3,
			MemoryBytes: 64 << 20, NetRxBytes: &rx, NetTxBytes: &rx},
		{Service: "app", Container: "demo-app-2", Replica: 2, CPUPercent: 0.4,
			MemoryBytes: 32 << 20, NetRxBytes: &rx, NetTxBytes: &rx},
	}

	// Narrow enough that the container is the *next* column a table
	// dropping from the right would take. Wider, the other columns have
	// already made room and it survives whether or not it is essential, so
	// a test there would assert nothing about the rule.
	for name, narrow := range map[string]string{
		"ps":    render(t, 24, services),
		"stats": render(t, 30, stats),
	} {
		require.NotContains(t, narrow, "I/O",
			"%s dropped nothing, so this proves nothing:\n%s", name, narrow)
		require.Contains(t, narrow, "demo-app-1",
			"%s dropped the container column:\n%s", name, narrow)
		require.Contains(t, narrow, "demo-app-2",
			"%s dropped the container column:\n%s", name, narrow)
	}
}

// TestPlainIsLineOrientedNotRichWithoutColour.
//
// Decision 5. A journal and a CI log read plain, so a box drawn in one is noise
// that outlives the terminal that wanted it. The callout keeps its content and
// loses its border.
func TestPlainIsLineOrientedNotRichWithoutColour(t *testing.T) {
	t.Setenv("COLUMNS", "100")

	status := ops.Status{
		Product: "demo",
		NeedsAttention: []domain.OperationRecord{{
			ID: "op_01KZPA2222", Type: domain.OpTypeRestore,
			Error: &domain.Error{Message: "the volume copy did not finish"},
		}},
	}

	var rich, plainOut bytes.Buffer
	require.NoError(t, ui.Render(&rich, ui.ModeRich, theme.New(false, true), status))
	require.NoError(t, ui.Render(&plainOut, ui.ModePlain, nil, status))

	require.Contains(t, rich.String(), "╭", "the styled callout has no border")
	// Every border character, not just the Unicode ones: a plain document
	// carries the ASCII theme, so a callout that took the styled path there
	// would draw `+---+` and slip past an assertion that only knew `╭`.
	for _, border := range []string{"╭", "╮", "╰", "╯", "│", "─", "+-", "-+", "|"} {
		require.NotContainsf(t, plainOut.String(), border,
			"plain drew a box: found %q", border)
	}
	require.Contains(t, plainOut.String(), "the volume copy did not finish",
		"plain lost the callout's content along with its border")
}

func configReport() ops.ConfigReport {
	return ops.ConfigReport{
		Product: "demo",
		Release: "1.2.0",
		Parameters: []ops.ConfigEntry{
			{
				Name: "http_port", Type: "int", Value: "8080", Source: "installation",
				Description: "the port the reverse proxy listens on",
				Services:    []string{"proxy"},
			},
			{
				Name: "site_name", Type: "string", Value: "Demo", Source: "release",
				Description: "shown in the page header",
			},
		},
	}
}

// TestAStoppedServiceIsMarkedWithoutColour.
//
// Per row, not per report: a summary that says "something is wrong" and a table
// where every row looks alike is the report an operator scrolls past. Colour off,
// because the marker is what a journal, a monochrome console and a colour-blind
// reader all still get.
func TestAStoppedServiceIsMarkedWithoutColour(t *testing.T) {
	status := ops.Status{
		Product: "demo",
		Services: []ports.ServiceState{
			{Name: "app", State: "running", Health: ports.HealthHealthy},
			{Name: "db", State: "exited (137)"},
		},
	}

	t.Setenv("COLUMNS", "100")
	var b bytes.Buffer
	require.NoError(t, ui.Render(&b, ui.ModeRich, theme.New(false, false), status))

	sym := theme.ASCIISymbols
	var app, db string
	for _, line := range strings.Split(b.String(), "\n") {
		switch {
		case strings.Contains(line, "app "):
			app = line
		case strings.Contains(line, "db "):
			db = line
		}
	}
	require.Containsf(t, app, sym.OK, "the running service is not marked ok: %q", app)
	require.Containsf(t, db, sym.Fail, "the stopped service is not marked failed: %q", db)
}

// TestACellTooWideForTheMeasureIsCutRatherThanPrinted.
//
// A table may be wider than the measure — one carrying a digest and a path
// legitimately needs 130 columns. One *cell* that is wider is a different thing:
// an unreachable target's error is a sentence, and left whole it makes a single
// row as long as the whole report. §5.4 says cells are truncated with an
// ellipsis; without this the helper that does it had no callers at all.
func TestACellTooWideForTheMeasureIsCutRatherThanPrinted(t *testing.T) {
	long := strings.Repeat("dial tcp 203.0.113.7:443: i/o timeout; ", 8)
	statuses := []ops.TargetStatus{
		{URL: "s3://backups/demo", Reachable: false, Error: long},
	}

	out := render(t, 100, statuses)
	for _, line := range strings.Split(out, "\n") {
		require.LessOrEqualf(t, ui.Width(line), ui.MaxContentWidth+ui.Gutter+len("s3://backups/demo"),
			"a single cell ran the row past any measure:\n%q", line)
	}
	require.Contains(t, out, "…", "the row was cut and does not say so")
	require.Contains(t, out, "dial tcp", "the beginning of the error was lost too")
}

// TestAValueAScriptSubstitutesIsNeverWrapped.
//
// An age public key is 62 characters and a narrow terminal is 60. Wrapped, the
// shell substitution that the documented invocation is built on captures a
// newline and produces a key `init --recovery-recipient` refuses — on a
// terminal, where an operator is most likely to be running it by hand.
//
// The measure governs prose. A value exists to be copied, and cutting it in
// half is not a smaller version of it.
func TestAValueAScriptSubstitutesIsNeverWrapped(t *testing.T) {
	const key = "age14zamz0thlnq8atx3t3lanyx2hfl0tdvpphtrfyad4m6fjxcmhgpsvqu82q"

	for _, width := range []int{40, 60, 80, 100} {
		out := render(t, width, views.KeyPair{PublicKey: key, Path: "/root/k"})
		require.Equalf(t, key, strings.TrimRight(out, "\n"),
			"the key was reflowed at %d columns:\n%q", width, out)
	}
}
