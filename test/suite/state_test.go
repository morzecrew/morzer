package suite

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	signer "github.com/morzecrew/morzer/internal/adapters/sign/minisign"
	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/lifecycle/ops"
	"github.com/morzecrew/morzer/internal/ports"
)

// The authority's side (RFC 0057 P4): `state publish` addresses a document to
// a machine only through a row the roster's key verifies, and the machine
// applies what it wrote.

// authorityRig is a sync rig whose machine publishes its fleet row to the
// target it follows, with a roster naming its key.
type authorityRig struct {
	*syncRig
	target string
	roster domain.FleetRoster
}

func newAuthorityRig(t *testing.T) *authorityRig {
	t.Helper()
	ctx := context.Background()
	r := newSyncRig(t)
	r.h.Secrets.SetMachineRecipient(r.recipient)
	a := &authorityRig{syncRig: r, target: "file://" + r.offsite}

	inst := installation(t, r.h)
	inst.Backup.Targets = []domain.BackupTargetConfig{{URL: a.target}}
	require.NoError(t, r.h.Deps.State.SaveInstallation(ctx, inst))
	_, err := ops.FleetPublish(ctx, r.h.Deps, ops.FleetPublishOptions{})
	require.NoError(t, err)

	a.roster = domain.FleetRoster{Schema: 1, Installations: []domain.FleetRosterEntry{{
		Product: inst.Product, ID: inst.ID, PublicKey: inst.Signing.PublicKey,
	}}}
	return a
}

func (a *authorityRig) publishOpts(doc string) ops.StatePublishOptions {
	return ops.StatePublishOptions{
		TargetOptions: ops.TargetOptions{URL: a.target},
		Roster:        a.roster, Product: a.inst.Product, InstallationID: a.inst.ID,
		Document: []byte(doc), Signer: a.authority,
	}
}

func (a *authorityRig) statePublish(t *testing.T, opts ops.StatePublishOptions) (ops.StatePublishReport, error) {
	t.Helper()
	return ops.StatePublish(context.Background(), a.h.Deps, opts)
}

// What `state publish` writes is what the machine applies, and `state ls`
// shows the serial published and the serial applied.
func TestAnAuthorityPublishesAndTheMachineApplies(t *testing.T) {
	a := newAuthorityRig(t)
	report, err := a.statePublish(t, a.publishOpts("parameters:\n  log_level: debug\n"))
	require.NoError(t, err)
	now := a.h.Deps.Now()
	assert.Equal(t, now.UnixMilli(), report.Serial, "the serial is not the time in milliseconds")
	assert.Equal(t, now.Add(ops.DefaultDesiredStateValidity).UTC(), report.ExpiresAt.UTC())
	assert.Equal(t, a.recipient, report.Recipient)
	assert.Equal(t, domain.MinisignKeyID(a.key), report.AuthorityKeyID)

	assert.Equal(t, domain.SyncOK, a.syncOK(t).Result)
	assert.Equal(t, "debug", installation(t, a.h).Parameters["log_level"])

	list, err := ops.StateList(context.Background(), a.h.Deps, ops.StateListOptions{
		TargetOptions: ops.TargetOptions{URL: a.target}, Roster: a.roster,
	})
	require.NoError(t, err)
	require.Len(t, list.Rows, 1)
	row := list.Rows[0]
	assert.Equal(t, report.Serial, row.Published)
	assert.Equal(t, ops.FleetVerified, row.Signature)
	require.NotNil(t, row.Sync)
	require.NotNil(t, row.Sync.Applied, "the row the cycle published does not report the document")
	assert.Equal(t, report.Serial, row.Sync.Applied.Serial)
}

// A document reaches only a machine the roster vouches for, through a row its
// key signed (decision 13).
func TestStatePublishAddressesOnlyAMachineTheRosterVouchesFor(t *testing.T) {
	other := signer.New(filepath.Join(t.TempDir(), "other.key"), "other")
	otherKey, err := other.EnsureKey(context.Background())
	require.NoError(t, err)

	for name, tc := range map[string]struct {
		edit func(a *authorityRig, o *ops.StatePublishOptions)
		want string
	}{
		"no roster": {
			edit: func(_ *authorityRig, o *ops.StatePublishOptions) { o.Roster = domain.FleetRoster{} },
			want: "needs --expect",
		},
		"an installation the roster does not list": {
			edit: func(_ *authorityRig, o *ops.StatePublishOptions) {
				o.InstallationID = "inst_01NOTINTHEROSTER0000000000"
			},
			want: "does not list",
		},
		"a roster key the row was not signed with": {
			edit: func(_ *authorityRig, o *ops.StatePublishOptions) {
				o.Roster.Installations[0].PublicKey = otherKey.Line
			},
			want: "does not verify against the roster's key",
		},
		"no row published": {
			edit: func(a *authorityRig, _ *ops.StatePublishOptions) {
				require.NoError(t, os.RemoveAll(filepath.Join(a.offsite, "fleet")))
			},
			want: "no fleet row is published",
		},
		"no target": {
			edit: func(_ *authorityRig, o *ops.StatePublishOptions) { o.URL = "" },
			want: "needs --target",
		},
		"not a change document": {
			edit: func(_ *authorityRig, o *ops.StatePublishOptions) { o.Document = []byte("colour: blue\n") },
			want: "is not a section of a change document",
		},
	} {
		t.Run(name, func(t *testing.T) {
			a := newAuthorityRig(t)
			opts := a.publishOpts("parameters:\n  log_level: debug\n")
			tc.edit(a, &opts)
			_, err := a.statePublish(t, opts)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
			_, statErr := os.Stat(filepath.Join(a.offsite, "state"))
			assert.True(t, os.IsNotExist(statErr), "a refused publish wrote under state/")
		})
	}
}

// A pair is written once: a serial that is not newer than the newest published
// is refused, and a dry run writes nothing.
func TestStatePublishWritesEachSerialOnce(t *testing.T) {
	a := newAuthorityRig(t)
	opts := a.publishOpts("parameters:\n  log_level: debug\n")
	opts.Serial = 50
	_, err := a.statePublish(t, opts)
	require.NoError(t, err)

	for _, serial := range []int64{50, 49} {
		opts.Serial = serial
		_, err := a.statePublish(t, opts)
		require.Error(t, err, "serial %d", serial)
		assert.Contains(t, err.Error(), "not newer than document 50")
	}

	opts.Serial, opts.DryRun = 60, true
	report, err := a.statePublish(t, opts)
	require.NoError(t, err)
	assert.True(t, report.DryRun)
	_, err = os.Stat(filepath.Join(a.offsite, filepath.FromSlash(report.Document)))
	assert.True(t, os.IsNotExist(err), "a dry run wrote the document")
}

// A publish leaves the newest three complete pairs, and removes the rest.
func TestStatePublishKeepsTheNewestThree(t *testing.T) {
	a := newAuthorityRig(t)
	opts := a.publishOpts("parameters:\n  log_level: debug\n")
	var last ops.StatePublishReport
	for serial := int64(1); serial <= 5; serial++ {
		opts.Serial = serial
		var err error
		last, err = a.statePublish(t, opts)
		require.NoError(t, err)
	}
	assert.Equal(t, []int64{2}, last.Pruned, "the fifth publish pruned other than the one pair below the newest three")

	entries, err := os.ReadDir(filepath.Join(a.offsite, "state", a.inst.Product, a.inst.ID))
	require.NoError(t, err)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	assert.ElementsMatch(t, []string{
		"3.age", "3.age.minisig", "4.age", "4.age.minisig", "5.age", "5.age.minisig",
	}, names)
}

// The machine reads a document back through a 1 MiB bound, so a ciphertext
// over it is refused before it is written (decision 20).
func TestStatePublishRefusesACiphertextAMachineCannotRead(t *testing.T) {
	a := newAuthorityRig(t)
	body := "secrets:\n  db_password: " + strings.Repeat("x", (1<<20)-400) + "\n"
	_, err := a.statePublish(t, a.publishOpts(body))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "over the 1048576 a machine reads")
}

// The validity is the authority's to choose, and the document carries it.
func TestStatePublishStampsTheValidityGiven(t *testing.T) {
	a := newAuthorityRig(t)
	opts := a.publishOpts("parameters:\n  log_level: debug\n")
	opts.ValidFor = time.Hour
	report, err := a.statePublish(t, opts)
	require.NoError(t, err)
	assert.Equal(t, a.h.Deps.Now().Add(time.Hour).UTC(), report.ExpiresAt.UTC())
	a.syncOK(t)
	assert.Equal(t, report.ExpiresAt, a.state(t).Applied.ExpiresAt)
}

// putFailing is a target that refuses to write one key.
type putFailing struct {
	ports.ObjectStore
	suffix string
}

func (p putFailing) PutObject(ctx context.Context, ref ports.TargetRef, key string, data []byte) error {
	if strings.HasSuffix(key, p.suffix) {
		return assert.AnError
	}
	return p.ObjectStore.PutObject(ctx, ref, key, data)
}

// A publish cut off before its signature leaves the previous document in force:
// the ciphertext is written first, and without its signature it is not a
// document (decision 26).
func TestAPublishCutOffBeforeItsSignatureLeavesThePreviousDocument(t *testing.T) {
	a := newAuthorityRig(t)
	opts := a.publishOpts("parameters:\n  log_level: debug\n")
	opts.Serial = 10
	_, err := a.statePublish(t, opts)
	require.NoError(t, err)
	assert.Equal(t, domain.SyncOK, a.syncOK(t).Result)

	objects := a.h.Deps.Objects
	a.h.Deps.Objects = putFailing{ObjectStore: objects, suffix: ".minisig"}
	opts.Serial, opts.Document = 11, []byte("parameters:\n  log_level: warn\n")
	_, err = a.statePublish(t, opts)
	require.Error(t, err)
	a.h.Deps.Objects = objects

	assert.Equal(t, domain.SyncCurrent, a.syncOK(t).Result)
	assert.Equal(t, "debug", installation(t, a.h).Parameters["log_level"],
		"a document whose signature was never written was applied")
}
