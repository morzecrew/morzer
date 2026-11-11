//go:build docker

package suite

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/adapters/secrets/sopsage"
	"github.com/morzecrew/morzer/internal/adapters/target"
	"github.com/morzecrew/morzer/internal/adapters/target/localdir"
	"github.com/morzecrew/morzer/internal/adapters/target/s3"
	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/lifecycle/ops"
	"github.com/morzecrew/morzer/internal/ports"
)

// What RFC 0057 decision 17 tells an operator to configure, against a real
// object store: a credential that can list and read one installation's
// documents prefix and nothing else is all a machine needs to follow its
// authority, and it cannot see another installation's documents.
func TestAReadOnlyPrefixScopedCredentialIsEnoughToSync(t *testing.T) {
	root, container := startS3Container(t)
	const bucket = "state-sync"
	createBucket(t, root, bucket)

	r := newSyncRig(t)
	ctx := context.Background()
	prefix := "state/" + r.inst.Product + "/" + r.inst.ID
	policy := fmt.Sprintf(`{"Version":"2012-10-17","Statement":[`+
		`{"Effect":"Allow","Action":["s3:ListBucket"],"Resource":["arn:aws:s3:::%[1]s"],`+
		`"Condition":{"StringLike":{"s3:prefix":["%[2]s","%[2]s/*"]}}},`+
		`{"Effect":"Allow","Action":["s3:GetObject"],"Resource":["arn:aws:s3:::%[1]s/%[2]s/*"]}]}`,
		bucket, prefix)
	scoped := userWithPolicy(t, container, root, "follower", policy)

	registry, err := target.NewRegistry(localdir.New(), s3.New())
	require.NoError(t, err)
	r.h.Deps.Targets, r.h.Deps.Objects = registry, registry
	authorityRef, err := ports.TargetURL("s3://" + bucket + "/")
	require.NoError(t, err)
	authorityRef = authorityRef.WithCredentials(root)
	r.write = func(t *testing.T, key string, data []byte) {
		t.Helper()
		require.NoError(t, s3.New().PutObject(ctx, authorityRef, key, data))
	}

	r.h.Secrets.Seed(map[string]string{"sync_s3": credentialDocument(scoped)})
	inst := installation(t, r.h)
	inst.Sync.Target, inst.Sync.Credentials = "s3://"+bucket+"/", "sync_s3"
	require.NoError(t, r.h.Deps.State.SaveInstallation(ctx, inst))

	r.publish(t, 100, "parameters:\n  log_level: debug\n")
	report := r.syncOK(t)
	require.Equal(t, domain.SyncOK, report.Result, report.Summary())
	assert.Equal(t, "debug", installation(t, r.h).Parameters["log_level"])

	scopedRef := authorityRef.WithCredentials(scoped)
	_, err = s3.New().ObjectKeys(ctx, scopedRef, "state/"+r.inst.Product)
	assert.Error(t, err, "the follower's credential listed every installation's documents")
	require.NoError(t, s3.New().PutObject(ctx, authorityRef, "state/demo/inst_01OTHERINSTALLATION/1.age", []byte("x")))
	_, err = s3.New().GetObject(ctx, scopedRef, "state/demo/inst_01OTHERINSTALLATION/1.age")
	assert.Error(t, err, "the follower's credential read another installation's document")

	_, err = ops.SyncNow(ctx, r.h.Deps, ops.SyncOptions{})
	require.NoError(t, err)
}

// credentialDocument is the secret a target's credentials are stored as.
func credentialDocument(c ports.TargetCredentials) string {
	return fmt.Sprintf("access_key_id: %s\nsecret_access_key: %s\nregion: %s\nendpoint: %s\n",
		c.AccessKeyID, c.SecretAccessKey, c.Region, c.Endpoint)
}

// The whole loop against a real object store (RFC 0057 P4): the machine
// publishes its row, the authority publishes a document through the roster,
// the machine applies it with a credential that can only read its own
// documents, `state ls` sees it applied, and a publish keeps only the newest
// three.
func TestAnAuthorityReachesAMachineThroughAnObjectStore(t *testing.T) {
	root, container := startS3Container(t)
	const bucket = "state-loop"
	createBucket(t, root, bucket)
	ctx := context.Background()

	r := newSyncRig(t)
	r.h.Secrets.SetMachineRecipient(r.recipient)
	registry, err := target.NewRegistry(localdir.New(), s3.New())
	require.NoError(t, err)
	r.h.Deps.Targets, r.h.Deps.Objects = registry, registry

	prefix := "state/" + r.inst.Product + "/" + r.inst.ID
	scoped := userWithPolicy(t, container, root, "follower", fmt.Sprintf(`{"Version":"2012-10-17","Statement":[`+
		`{"Effect":"Allow","Action":["s3:ListBucket"],"Resource":["arn:aws:s3:::%[1]s"],`+
		`"Condition":{"StringLike":{"s3:prefix":["%[2]s","%[2]s/*"]}}},`+
		`{"Effect":"Allow","Action":["s3:GetObject"],"Resource":["arn:aws:s3:::%[1]s/%[2]s/*"]}]}`,
		bucket, prefix))
	r.h.Secrets.Seed(map[string]string{"fleet_s3": credentialDocument(root), "sync_s3": credentialDocument(scoped)})
	url := "s3://" + bucket + "/"
	inst := installation(t, r.h)
	inst.Backup.Targets = []domain.BackupTargetConfig{{URL: url, Credentials: "fleet_s3"}}
	inst.Sync.Target, inst.Sync.Credentials = url, "sync_s3"
	require.NoError(t, r.h.Deps.State.SaveInstallation(ctx, inst))
	_, err = ops.FleetPublish(ctx, r.h.Deps, ops.FleetPublishOptions{})
	require.NoError(t, err)

	roster := domain.FleetRoster{Schema: 1, Installations: []domain.FleetRosterEntry{{
		Product: inst.Product, ID: inst.ID, PublicKey: inst.Signing.PublicKey,
	}}}
	authority := ops.TargetOptions{URL: url, Credentials: root}
	publish := func(serial int64, doc string) ops.StatePublishReport {
		t.Helper()
		report, err := ops.StatePublish(ctx, r.h.Deps, ops.StatePublishOptions{
			TargetOptions: authority, Roster: roster, Product: inst.Product, InstallationID: inst.ID,
			Document: []byte(doc), Signer: r.authority, Serial: serial,
		})
		require.NoError(t, err)
		return report
	}

	published := publish(10, "parameters:\n  log_level: debug\n")
	report := r.syncOK(t)
	require.Equal(t, domain.SyncOK, report.Result, report.Summary())
	assert.Equal(t, "debug", installation(t, r.h).Parameters["log_level"])

	list, err := ops.StateList(ctx, r.h.Deps, ops.StateListOptions{TargetOptions: authority, Roster: roster})
	require.NoError(t, err)
	require.Len(t, list.Rows, 1)
	assert.Equal(t, published.Serial, list.Rows[0].Published)
	require.NotNil(t, list.Rows[0].Sync)
	require.NotNil(t, list.Rows[0].Sync.Applied)
	assert.Equal(t, published.Serial, list.Rows[0].Sync.Applied.Serial)

	var last ops.StatePublishReport
	for serial := int64(11); serial <= 14; serial++ {
		last = publish(serial, "parameters:\n  log_level: warn\n")
	}
	assert.Empty(t, last.PruneProblem)
	keys, err := s3.New().ObjectKeys(ctx, authorityRef(t, url, root), prefix)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{
		prefix + "/12.age", prefix + "/12.age.minisig",
		prefix + "/13.age", prefix + "/13.age.minisig",
		prefix + "/14.age", prefix + "/14.age.minisig",
	}, keys)

	// Facts going back, through the same bucket.
	identity := filepath.Join(t.TempDir(), "authority.age")
	recipient, err := sopsage.GenerateIdentity(identity)
	require.NoError(t, err)
	r.h.Runtime.OneShotResults["app"] = ports.ExitResult{Stdout: "public_key=RELAY\n"}
	_, err = ops.SetSettings(ctx, r.h.Deps, ops.SetSettingsOptions{Set: map[string]string{"sync.recipient": recipient}})
	require.NoError(t, err)
	_, err = ops.FleetPublish(ctx, r.h.Deps, ops.FleetPublishOptions{})
	require.NoError(t, err)
	facts, err := ops.StateFacts(ctx, r.h.Deps, ops.StateFactsOptions{
		TargetOptions: authority, Roster: roster, Product: inst.Product, InstallationID: inst.ID, Identity: identity,
	})
	require.NoError(t, err)
	assert.Equal(t, "public_key=RELAY\n", facts.Facts)
}

func authorityRef(t *testing.T, url string, creds ports.TargetCredentials) ports.TargetRef {
	t.Helper()
	ref, err := ports.TargetURL(url)
	require.NoError(t, err)
	return ref.WithCredentials(creds)
}
