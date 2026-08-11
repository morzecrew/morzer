package cli

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/lifecycle/ops"
	"github.com/morzecrew/morzer/internal/ui"
)

// What every command does on a machine holding several installations that
// nobody chose between.
//
// RFC 0020 §9 recorded this as unsettled and named the shape of the problem:
// the ambiguity refusal fired where an installation was *loaded*, so the
// commands that read the store without loading one -- `release list`, `secret
// list` -- answered about the placeholder layout and reported an empty machine.
// The two obvious fixes were both wrong: a maintained list of commands that
// need an installation is a list somebody forgets to append to, and refusing
// during path resolution would refuse `morzer version`.
//
// So the scope is declared per command and the tree is what is walked. These
// tests are the half that makes the declaration mean something: the first
// refuses a command that declares nothing, the second runs the real refusal
// against every command there is.

// walkCommands visits every command an operator can run.
//
// Cobra's own are excluded, as everywhere else -- by IsGenerated, which reads
// the scope annotation rather than a list of names, so `completion install` is
// walked and `completion bash` is not. So is anything non-runnable: a parent
// that only holds subcommands returns before the persistent pre-run, so
// `morzer secret` prints its help on an ambiguous machine rather than being
// refused.
func walkCommands(t *testing.T, root *cobra.Command, visit func(t *testing.T, cmd *cobra.Command, path string)) {
	t.Helper()

	var walk func(cmd *cobra.Command, path string)
	walk = func(cmd *cobra.Command, path string) {
		for _, sub := range cmd.Commands() {
			name := strings.Fields(sub.Use)[0]
			if sub.Hidden {
				continue
			}
			full := strings.TrimSpace(path + " " + name)
			// Cobra's own are not visited, but they are still walked
			// past: `completion` is generated and `completion
			// install` is ours, and pruning the subtree here would
			// exempt the one command this walk most needs to reach.
			if sub.Runnable() && !IsGenerated(sub) {
				visit(t, sub, full)
			}
			walk(sub, full)
		}
	}
	walk(root, "")
}

// declaredScope is scopeOf without its fallback, so a test can tell "declared
// installation" from "declared nothing".
func declaredScope(cmd *cobra.Command) (string, bool) {
	for c := cmd; c != nil; c = c.Parent() {
		scope, ok := c.Annotations[scopeAnnotation]
		if !ok || scope == scopeDelegated {
			// A parent that delegates has declared ownership, not a
			// scope. Reading it as one here would exempt every
			// child of `release` from the rule this test exists to
			// enforce.
			continue
		}
		return scope, true
	}
	return "", false
}

// TestEveryCommandDeclaresItsScope.
//
// The half that cannot be caught at runtime: an undeclared command resolves to
// installation scope, which is the safe direction and therefore the silent one.
// A new `morzer logs` that should run on any installation the operator names
// would be refused on an ambiguous machine and nothing would say why -- the
// refusal reads like a bug rather than a missing declaration.
func TestEveryCommandDeclaresItsScope(t *testing.T) {
	walkCommands(t, CommandTree(), func(t *testing.T, cmd *cobra.Command, path string) {
		scope, declared := declaredScope(cmd)
		switch {
		case !declared:
			t.Errorf("`morzer %s` declares no scope, so it inherits the safe default "+
				"and is refused on a machine with several installations — "+
				"wrap it in machineScope or installationScope where it is registered", path)
		case scope != scopeMachine && scope != scopeInstallation:
			t.Errorf("`morzer %s` declares scope %q, which is neither %q nor %q",
				path, scope, scopeMachine, scopeInstallation)
		}
	})
}

// TestTheWalkReachesOursUnderCobrasOwn.
//
// Both tests above are only as exhaustive as this walk, and a walk that stops
// at a generated command stops at `completion` — taking `completion install`
// with it, silently, while the comment above went on saying it was walked.
// Pruning is the whole risk here: everything else in the tree is ours all the
// way down, so this is the one command that tells the two behaviours apart.
func TestTheWalkReachesOursUnderCobrasOwn(t *testing.T) {
	var walked []string
	walkCommands(t, CommandTree(), func(_ *testing.T, _ *cobra.Command, path string) {
		walked = append(walked, path)
	})

	assert.Contains(t, walked, "completion install",
		"the walk stopped at cobra's `completion`, so neither test above covers ours")
	for _, generated := range []string{"completion", "completion bash", "help"} {
		assert.NotContains(t, walked, generated,
			"cobra's own command was visited, and it declares no scope to find")
	}
}

// TestAnUndeclaredCommandIsRefusedRatherThanAllowed.
//
// The direction of the default, on its own. Every command in the tree declares
// one, so nothing above reaches this branch — which is exactly why it needs a
// test of its own: it is the behaviour a command that slipped through the test
// above would get, and "refused with the installations named" is a bug report
// somebody can act on where "acted on whichever installation was guessed" is
// not.
func TestAnUndeclaredCommandIsRefusedRatherThanAllowed(t *testing.T) {
	parent := &cobra.Command{Use: "parent"}
	child := &cobra.Command{Use: "child", Run: func(*cobra.Command, []string) {}}
	parent.AddCommand(child)

	require.Equal(t, scopeInstallation, scopeOf(child))
	require.Error(t, ambiguousApp().confirmInstallationChosen(child))
}

// TestAnAncestorsScopeIsInherited is the other half of the resolution: `secret`
// declares once for eight subcommands, and `release` declares per command
// because its two halves differ.
func TestAnAncestorsScopeIsInherited(t *testing.T) {
	parent := machineScope(&cobra.Command{Use: "parent"})
	child := &cobra.Command{Use: "child", Run: func(*cobra.Command, []string) {}}
	own := installationScope(&cobra.Command{Use: "own", Run: func(*cobra.Command, []string) {}})
	parent.AddCommand(child, own)

	assert.Equal(t, scopeMachine, scopeOf(child))
	assert.Equal(t, scopeInstallation, scopeOf(own),
		"a command that declares its own scope was overruled by its parent's")
}

// ambiguousApp is an App pointed at a machine holding two installations, with
// nothing selecting either.
func ambiguousApp() *App {
	return &App{
		Stream: ui.DefaultStreams(),
		Deps: &ops.Deps{
			Paths:           domain.DefaultPaths("morzer"),
			MachineProducts: []string{"demo", "sandbox"},
		},
	}
}

// TestAnAmbiguousMachineRefusesEveryInstallationCommand.
//
// Exhaustive over the real tree rather than over a sample, because the defect
// this closes was one command's worth of behaviour that nobody had thought
// about: `release list` said "no releases are installed" on a machine with
// three installations, which is the shape of answer an operator acts on.
func TestAnAmbiguousMachineRefusesEveryInstallationCommand(t *testing.T) {
	app := ambiguousApp()

	walkCommands(t, CommandTree(), func(t *testing.T, cmd *cobra.Command, path string) {
		err := app.confirmInstallationChosen(cmd)

		if scopeOf(cmd) == scopeMachine {
			if err != nil {
				t.Errorf("`morzer %s` is machine-scoped and was refused: %v", path, err)
			}
			return
		}

		if err == nil {
			t.Fatalf("`morzer %s` acts on one installation and ran on a machine "+
				"with two, having been told which by nobody", path)
		}
		e := domain.AsError(err)
		if got := domain.ExitCode(err); got != domain.ExitUsage {
			t.Errorf("`morzer %s` refused with exit %d, want %d: the machine is fine "+
				"and the fix is on the command line", path, got, domain.ExitUsage)
		}
		// The names, or the operator has to go and find them -- which
		// is the advice the old refusal gave, and it was advice to
		// create a third installation.
		for _, want := range []string{"demo", "sandbox"} {
			if !strings.Contains(e.Message+e.Hint, want) {
				t.Errorf("`morzer %s` refused without naming %q:\n%s\n%s",
					path, want, e.Message, e.Hint)
			}
		}
	})
}

// TestChoosingAnInstallationSatisfiesEveryCommand is the other direction, and
// the regression that would matter most: a refusal that fired even when the
// operator had said which installation they meant would make an ambiguous
// machine unusable rather than merely explicit.
func TestChoosingAnInstallationSatisfiesEveryCommand(t *testing.T) {
	app := ambiguousApp()
	app.Deps.ProductNamed = true

	walkCommands(t, CommandTree(), func(t *testing.T, cmd *cobra.Command, path string) {
		if err := app.confirmInstallationChosen(cmd); err != nil {
			t.Errorf("`morzer %s` was refused although an installation was named: %v", path, err)
		}
	})
}

// TestOneInstallationRefusesNothing pins the shape every existing test runs in.
// A machine with one installation has no ambiguity to report, and a refusal
// here would be a regression in every deployment there is.
func TestOneInstallationRefusesNothing(t *testing.T) {
	app := ambiguousApp()
	app.Deps.MachineProducts = []string{"demo"}

	walkCommands(t, CommandTree(), func(t *testing.T, cmd *cobra.Command, path string) {
		if err := app.confirmInstallationChosen(cmd); err != nil {
			t.Errorf("`morzer %s` was refused on a machine with one installation: %v", path, err)
		}
	})
}

// TestADelegatingParentIsNotItselfAScope.
//
// `release` and `installation` declare `per-command` because their subtrees hold
// commands of both kinds. That marker says who owns the command, not what it
// acts on, and scopeOf must walk past it: a child that declares nothing has to
// reach the installation default, which is the refusing direction. Reading the
// marker as a scope would return a third value that is neither — harmless
// against today's caller, which compares against `machine`, and silently
// exempting every child of `release` against a caller that compared against
// `installation` instead.
func TestADelegatingParentIsNotItselfAScope(t *testing.T) {
	parent := perCommandScope(&cobra.Command{Use: "release"})
	child := &cobra.Command{Use: "forgot", Run: func(*cobra.Command, []string) {}}
	parent.AddCommand(child)

	if got := scopeOf(child); got != scopeInstallation {
		t.Errorf("a child that declares nothing resolves to %q, want %q",
			got, scopeInstallation)
	}
	if got := scopeOf(parent); got != scopeInstallation {
		t.Errorf("the delegating parent itself resolves to %q, want the safe default %q",
			got, scopeInstallation)
	}

	// And it is still this project's command, which is the other half of
	// what the marker is for.
	if IsGenerated(parent) || IsGenerated(child) {
		t.Error("a delegating parent reads as one of cobra's own")
	}
}
