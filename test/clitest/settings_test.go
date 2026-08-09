package clitest_test

import (
	"testing"

	"github.com/morzecrew/morzer/test/clitest"
)

// Installation settings are the operator's arrangement with the manager, as
// opposed to the parameters a release declares. Before they existed,
// `update.check` had a name, a default, a documented meaning and no way at all
// to turn it on: its own refusal told operators to use `morzer config`, which
// read and wrote release parameters exclusively.

// TestUpdateCheckCanBeTurnedOn is that gap, closed and asserted end to end.
func TestUpdateCheckCanBeTurnedOn(t *testing.T) {
	r := clitest.NewInstalled(t)

	// Off by default and absent-means-off: a check contacts the vendor's
	// registry, which for a self-hosted product is a phone-home nobody
	// agreed to.
	r.Run("config", "settings").ExitCode(0).OutputContains("update.check")

	r.Run("config", "set", "update.check=true").ExitCode(0).
		OutputContains("update.check")

	// Read back through a different command, so this asserts the value was
	// persisted rather than that one code path echoed its own argument.
	r.Run("config", "get", "update.check").ExitCode(0).OutputContains("true")

	r.Run("config", "unset", "update.check").ExitCode(0)
	r.Run("config", "get", "update.check").ExitCode(0).OutputContains("false")
}

// TestASettingAndAParameterAreNotSetTogether.
//
// They run on different machinery -- one converges a deployment and re-creates
// services, the other writes a flag -- so a mixed command would half-apply on a
// failure with no single thing to report.
func TestASettingAndAParameterAreNotSetTogether(t *testing.T) {
	r := clitest.NewInstalled(t)

	r.Run("config", "set", "update.check=true", "log_level=debug").Failed().
		OutputContains("separately")
}

// TestAMistypedSettingIsReportedAsASetting.
//
// A dotted name can never be a parameter, so reporting `update.chanel` as an
// undeclared parameter would send an operator to the manifest to look for
// something that was never going to be there.
func TestAMistypedSettingIsReportedAsASetting(t *testing.T) {
	r := clitest.NewInstalled(t)

	out := r.Run("config", "set", "update.chanel=oci://example/x:dev").Failed()
	out.OutputContains("installation setting")
	out.OutputContains("update.channel")
}

// TestAChannelMustBeAReferenceThisManagerCouldFollow.
//
// Refused at configuration time rather than at the first tick, in the same
// shape as `--skip-backup` requiring `--force`: a machine that accepts a setting
// and then silently does nothing with it is worse than one that refuses it.
func TestAChannelMustBeAReferenceThisManagerCouldFollow(t *testing.T) {
	r := clitest.NewInstalled(t)

	// A directory has no server-side identity that changes when its
	// contents do, so there is nothing to watch.
	r.Run("config", "set", "update.channel=./some/bundle").Failed().
		OutputContains("channel")

	r.Run("config", "get", "update.channel").ExitCode(0)
}

// TestUpdateModesAreRefusedTogether.
//
// `--check`, `--stage` and `--unattended` each turn `update` into a different
// operation, and `--to` is the one that installs. An operator who typed two of
// them meant one; picking a winner silently would tell them the machine did what
// they asked when it did something else.
func TestUpdateModesAreRefusedTogether(t *testing.T) {
	r := clitest.NewInstalled(t)

	for _, args := range [][]string{
		{"update", "--check", "--stage"},
		{"update", "--check", "--unattended"},
		{"update", "--stage", "--unattended"},
		{"update", "--check", "--to", "1.3.0"},
		{"update", "--stage", "--to", "1.3.0"},
		{"update", "--unattended", "--to", "1.3.0"},
	} {
		r.Run(args...).Failed().OutputContains("alternatives")
	}
}
