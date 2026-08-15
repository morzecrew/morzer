package domain_test

import (
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/domain"
)

// RFC 0026 decision 7 asks for one test covering every credential-bearing
// field, "so a third thing to drop cannot be added without failing it". This is
// that test, and the shape it needs is not a list of the fields that are
// dropped -- that list is the code, and a test asserting the code says what the
// code says catches nothing.
//
// It walks the installation document instead and demands a verdict per field.
// A field added to `Installation` next year fails this until somebody writes
// down which side of the line it is on, which is the only mechanism that
// survives the person who knew about it leaving.

// sandboxVerdict is what a sandbox does with one field.
type sandboxVerdict struct {
	// drop is whether Sandboxed removes it. `set` covers the one field
	// Sandboxed writes rather than clears.
	drop bool
	set  bool
	why  string
}

func keep(why string) sandboxVerdict { return sandboxVerdict{why: why} }
func drop(why string) sandboxVerdict { return sandboxVerdict{drop: true, why: why} }
func set(why string) sandboxVerdict  { return sandboxVerdict{set: true, why: why} }

// sandboxClassification is a verdict for every field of an installation, and
// for every field of the structs it holds.
//
// The reasons are the deliverable. A bare "kept" would let the next reader
// assume somebody thought about it, and the whole reason this table exists is
// that once, somebody did not.
func sandboxClassification() map[string]sandboxVerdict {
	return map[string]sandboxVerdict{
		"SchemaVersion": keep("the document's own version, not the machine's reach"),

		// The hazard's precondition, kept on purpose. Backups are stamped
		// with the id and `restore` checks against it, so a rebuilt
		// machine with a fresh one could not restore its own backups --
		// which is the entire point of having got this far. That is
		// exactly why the reach has to go instead.
		"ID":      keep("preserved deliberately: restore checks against it (RFC 0017)"),
		"Product": keep("which product this is; a sandbox runs the same one"),

		"CreatedAt":      keep("when the original was created; history, not reach"),
		"CreatedAt.Time": keep("the timestamp inside CreatedAt"),

		"Mode": set("this is what makes it a sandbox"),

		"Profile": keep("which topology to deploy; a sandbox needs one to run"),
		"Domains": keep("the names the product is served under -- a sandbox that " +
			"lost them renders nothing, and it cannot serve them anyway " +
			"without DNS pointing at it"),

		"Providers":         keep("which adapters to use; names, not endpoints"),
		"Providers.Runtime": keep("an adapter name"),
		"Providers.Secrets": keep("an adapter name"),
		"Providers.Backup":  keep("an adapter name"),
		"Providers.Health":  keep("an adapter name"),

		"Policy":                        keep("how this machine behaves, all of it local"),
		"Policy.RequireSignature":       keep("whether to refuse unsigned bundles; local"),
		"Policy.SigningKeys":            keep("whose releases this machine will install -- public keys, and a sandbox testing an update needs the same set"),
		"Policy.RetainReleases":         keep("local disk retention"),
		"Policy.RetainBackups":          keep("local disk retention"),
		"Policy.SkipBackupBeforeUpdate": keep("local behaviour"),
		"Policy.StaleBackupAfter":       keep("when doctor warns; local"),
		"Policy.BackupSchedule": keep("when scheduled backups run -- local behaviour, and a " +
			"sandbox with no targets left backs up to its own disk"),
		"Policy.SkipScheduledBackups": keep("whether this machine's backups are the manager's " +
			"job at all -- local, and a sandbox of a machine backed up at the " +
			"storage layer should not start taking its own on a timer just " +
			"because it is a copy"),

		"Parameters": keep("the operator's choices; a sandbox renders nothing without them, " +
			"and values reach templates rather than infrastructure"),

		"Update": keep("how this machine learns a release exists"),
		"Update.Check": keep("whether to contact the vendor's registry unprompted -- " +
			"a read, and testing an update is what a sandbox is for"),
		"Update.Channel":   keep("a reference this machine follows; a read"),
		"Update.AutoApply": keep("installs what the channel offers, here"),

		"Notify":         drop("a sandbox must not report into production's alerting"),
		"Notify.Targets": drop("a webhook URL is itself the credential, and it travels in the export"),

		"Backup":         drop("a sandbox must not write into production's bucket"),
		"Backup.Targets": drop("targets and their credentials; fleet rows go to the same list"),

		// Not on the drop list, and handled harder than dropping would
		// be: `installation import` calls SucceedSigning, which retires
		// the key as a predecessor and clears the current one, so the
		// machine does not claim to sign with a key it does not hold.
		"Signing":              keep("retired by SucceedSigning on import, not dropped"),
		"Signing.PublicKey":    keep("cleared by SucceedSigning; the new machine mints its own"),
		"Signing.PreviousKeys": keep("the chain of what this installation used to sign with"),

		"AttestationSalt": keep("preserved deliberately (RFC 0025 decision 10): re-minting " +
			"breaks configuration-digest continuity on the machine that " +
			"most needs its history to line up"),
	}
}

// Every field of an installation has a verdict, and every verdict has a field.
func TestEveryInstallationFieldIsClassifiedForASandbox(t *testing.T) {
	classified := sandboxClassification()

	var walked []string
	for _, path := range installationFieldPaths() {
		walked = append(walked, path)
		if _, ok := classified[path]; !ok {
			t.Errorf("Installation.%s is not classified: nobody has said whether a "+
				"sandbox may keep it.\nAdd it to sandboxClassification() with a "+
				"reason, and to domain.SandboxDrops() if a sandbox rebuilt from a "+
				"production export could use it to reach production.", path)
		}
	}

	known := make(map[string]bool, len(walked))
	for _, p := range walked {
		known[p] = true
	}
	for path := range classified {
		assert.Truef(t, known[path],
			"%s is classified and no longer exists; the table is describing a "+
				"document that has moved on", path)
	}

	// A verdict with no reason is a verdict nobody can review.
	for path, verdict := range classified {
		assert.NotEmptyf(t, verdict.why, "%s is classified without saying why", path)
	}
}

// The classification and the code agree about what is dropped.
//
// The half the walk above cannot check: a field can be classified `drop` and
// not be on SandboxDrops, which is the failure that leaves the table honest and
// the machine leaky.
func TestTheDropListMatchesTheClassification(t *testing.T) {
	var claimed []string
	for path, verdict := range sandboxClassification() {
		// Sub-fields only: the parent is classified for the walk's
		// benefit, and naming both would count each drop twice.
		if verdict.drop && strings.Contains(path, ".") {
			claimed = append(claimed, path)
		}
	}
	sort.Strings(claimed)

	var actual []string
	for _, d := range domain.SandboxDrops() {
		// `backup.targets` as an operator reads it, `Backup.Targets` as
		// the walk produces it. Compared case-insensitively rather than
		// by mapping one onto the other, because a mapping is a third
		// spelling to keep in step.
		actual = append(actual, d.Field)
		assert.NotEmptyf(t, d.Why, "%s is dropped without saying what a sandbox would do with it", d.Field)
		assert.NotEmptyf(t, d.Noun, "%s is dropped without a name a sentence can use", d.Field)
	}
	sort.Strings(actual)

	require.Len(t, actual, len(claimed),
		"the drop list and the classification disagree about how much is dropped:\n"+
			"  dropped:    %v\n  classified: %v", actual, claimed)
	for i := range actual {
		assert.Equal(t, strings.ToLower(claimed[i]), strings.ToLower(actual[i]))
	}
}

// installationFieldPaths lists every field of an installation, one level into
// the structs it holds.
//
// One level rather than all of them: the leaves below that are inside slice
// elements -- one notify target's URL, one backup target's credential name --
// and those are properties of the entry rather than of the document. Dropping
// is by list, never by field within a list.
func installationFieldPaths() []string {
	var out []string
	t := reflect.TypeOf(domain.Installation{})
	for i := range t.NumField() {
		f := t.Field(i)
		out = append(out, f.Name)
		if f.Type.Kind() != reflect.Struct {
			continue
		}
		for j := range f.Type.NumField() {
			out = append(out, f.Name+"."+f.Type.Field(j).Name)
		}
	}
	return out
}

// A sandbox keeps its identity and loses its reach.
func TestSandboxedDropsReachAndKeepsEverythingElse(t *testing.T) {
	inst := domain.Installation{
		ID:      "inst_01PRODUCTION",
		Product: "demo",
		Domains: []string{"app.example"},
		Backup: domain.BackupConfig{Targets: []domain.BackupTargetConfig{
			{URL: "s3://customer-bucket/backups", Credentials: "s3"},
		}},
		Notify: domain.NotifyConfig{Targets: []domain.NotifyTargetConfig{
			{Name: "oncall", URLSecret: "slack_webhook"},
		}},
		Parameters:      map[string]string{"http_port": "8443"},
		AttestationSalt: "0123456789abcdef",
	}

	sandbox, dropped := inst.Sandboxed()

	assert.Equal(t, domain.ModeDev, sandbox.Mode)
	assert.Empty(t, sandbox.Backup.Targets, "a sandbox kept production's bucket")
	assert.Empty(t, sandbox.Notify.Targets, "a sandbox kept production's alerting")

	// Identity and everything a sandbox needs to run.
	assert.Equal(t, inst.ID, sandbox.ID, "the id is preserved deliberately")
	assert.Equal(t, inst.Domains, sandbox.Domains)
	assert.Equal(t, inst.Parameters, sandbox.Parameters)
	assert.Equal(t, inst.AttestationSalt, sandbox.AttestationSalt)

	// The original is untouched, so a caller comparing before and after is
	// comparing two things rather than one thing twice.
	//
	// **Contents and not just length.** `out := i` copies the struct and
	// hands over its slice headers intact, so a drop that cleared a slice
	// in place rather than replacing the header would reach through into
	// the caller's installation -- and a length assertion passes for that,
	// because the elements are still there and merely empty. Found by a
	// mutation that did exactly this and survived.
	assert.Equal(t, []domain.BackupTargetConfig{
		{URL: "s3://customer-bucket/backups", Credentials: "s3"},
	}, inst.Backup.Targets)
	assert.Equal(t, []domain.NotifyTargetConfig{
		{Name: "oncall", URLSecret: "slack_webhook"},
	}, inst.Notify.Targets)

	require.Len(t, dropped, 2, "the report does not name everything it removed")
	assert.Equal(t, "backup.targets", dropped[0].Field)
	assert.Equal(t, "notify.targets", dropped[1].Field)

	// The sentence an operator actually reads: what was taken, then why,
	// once rather than per item.
	assert.Equal(t,
		"dropped 1 backup target and 1 notify target -- a sandbox must not "+
			"write into production's bucket or report into production's alerting",
		domain.DescribeSandboxDrops(dropped))
}

// An export from a machine that had none says nothing about dropping.
//
// A summary line reporting "dropped 0 backup targets" on the ordinary import is
// noise that trains an operator to skip the sentence that will one day matter.
func TestSandboxedSaysNothingWhenThereWasNothingToDrop(t *testing.T) {
	sandbox, dropped := domain.Installation{ID: "inst_01A", Product: "demo"}.Sandboxed()

	assert.Equal(t, domain.ModeDev, sandbox.Mode)
	assert.Empty(t, dropped)
	assert.Empty(t, domain.DescribeSandboxDrops(dropped))
}

// The sentence counts, which is why it is built rather than written.
//
// "dropped 2 backup target" reads as a typo, and a summary an operator stops
// reading the details of is a summary that stops working -- the details are
// what tell them their sandbox is not wired to production.
func TestTheDropSummaryAgreesWithItsCount(t *testing.T) {
	inst := domain.Installation{
		Backup: domain.BackupConfig{Targets: []domain.BackupTargetConfig{
			{URL: "s3://one/backups"}, {URL: "s3://two/backups"},
		}},
	}

	_, dropped := inst.Sandboxed()
	require.Len(t, dropped, 1)
	assert.Equal(t,
		"dropped 2 backup targets -- a sandbox must not write into production's bucket",
		domain.DescribeSandboxDrops(dropped))
}
