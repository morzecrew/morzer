//go:build docker

package suite

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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

	r.h.Secrets.Seed(map[string]string{"sync_s3": fmt.Sprintf(
		"access_key_id: %s\nsecret_access_key: %s\nregion: %s\nendpoint: %s\n",
		scoped.AccessKeyID, scoped.SecretAccessKey, scoped.Region, scoped.Endpoint)})
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
