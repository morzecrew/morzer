//go:build docker

package suite

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/adapters/target/s3"
	"github.com/morzecrew/morzer/internal/ports"
	"github.com/morzecrew/morzer/test/dockerlab"
)

// The measurement RFC 0026 §10.3 owed, taken against a real object store.
//
// §9 answers the shared-bucket risk -- one machine can overwrite another's row
// -- with "write-only, prefix-scoped credentials", and §10.3 recorded that
// nobody had checked whether that is a thing an operator can actually
// configure. P1 was not allowed to write to a shared bucket before somebody
// did.
//
// This is that check. A MinIO user is given a policy holding exactly one
// permission -- `s3:PutObject` under one prefix -- and the adapter is asked to
// do the one thing a publisher does, and then the things a publisher must not
// be able to do.
//
// A docker test rather than a unit test because there is nothing to fake: the
// whole question is what a real S3 implementation does with a real policy, and
// a stub would answer whatever it was written to answer.

// scopedUser creates a MinIO user whose policy is exactly the actions given, on
// one prefix of one bucket.
func scopedUser(
	t *testing.T, container *dockerlab.Container, root ports.TargetCredentials,
	bucket, prefix, name string, actions ...string,
) ports.TargetCredentials {
	t.Helper()

	resources := fmt.Sprintf(`"arn:aws:s3:::%s/%s/*"`, bucket, prefix)
	quoted := make([]string, 0, len(actions))
	for _, action := range actions {
		quoted = append(quoted, `"`+action+`"`)
	}
	policy := fmt.Sprintf(
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":[%s],"Resource":[%s]}]}`,
		strings.Join(quoted, ","), resources)

	// An alias of our own, with the root credentials. The image ships a
	// `local` alias that can read and write and cannot administer, which is
	// the right default and the wrong thing for creating users.
	const admin = "rootalias"
	out, err := container.Exec(t, "mc", "alias", "set", admin,
		"http://localhost:9000", minioAccessKey, minioSecretKey)
	require.NoErrorf(t, err, "cannot point mc at the server: %s", out)

	// Written inside the container: `mc` runs there, and the policy has to
	// be a file it can read.
	path := "/tmp/" + name + ".json"
	out, err = container.Exec(t, "sh", "-c", "printf '%s' '"+policy+"' > "+path)
	require.NoErrorf(t, err, "cannot write the policy: %s", out)

	secret := name + "-secret-value"
	out, err = container.Exec(t, "mc", "admin", "user", "add", admin, name, secret)
	require.NoErrorf(t, err, "cannot create the scoped user: %s", out)

	out, err = container.Exec(t, "mc", "admin", "policy", "create", admin, name, path)
	require.NoErrorf(t, err, "cannot create the policy: %s", out)

	out, err = container.Exec(t, "mc", "admin", "policy", "attach", admin, name, "--user", name)
	require.NoErrorf(t, err, "cannot attach the policy: %s", out)

	scoped := root
	scoped.AccessKeyID = name
	scoped.SecretAccessKey = secret
	return scoped
}

// Can an operator actually configure what §9 tells them to?
//
// One server for all three questions: they are three properties of one
// credential, and starting three MinIOs to ask them separately would triple the
// slowest part of the suite for nothing.
func TestAWriteOnlyPrefixScopedCredential(t *testing.T) {
	dockerlab.Require(t)

	root, container := startMinIOContainer(t)
	const bucket = "fleet-writeonly"
	createBucket(t, root, bucket)

	creds := scopedUser(t, container, root, bucket, "fleet", "publisher", "s3:PutObject")

	// Pointed at the bucket root rather than at the prefix, so the adapter
	// imposes no prefix of its own and the only thing scoping these calls
	// is the policy.
	ref, err := ports.TargetURL("s3://" + bucket + "/")
	require.NoError(t, err)
	ref = ref.WithCredentials(creds)

	target := s3.New()
	ctx := context.Background()

	// The property §9's whole mitigation rests on. If this fails, every
	// machine in a fleet needs read access to every other machine's row in
	// order to publish its own, and "write-only" is advice nobody can take.
	t.Run("it can publish a row", func(t *testing.T) {
		require.NoError(t, target.PutObject(ctx, ref,
			"fleet/demo/inst_WRITEONLY/status.json", []byte(`{"schema":1}`)))
	})

	// And the other half: a mitigation that permitted the write and also
	// permitted the read would be no mitigation at all. The point is that a
	// machine holding one of these cannot enumerate or fetch what the rest
	// of the fleet published.
	t.Run("it cannot read the fleet", func(t *testing.T) {
		_, err := target.ObjectKeys(ctx, ref, "fleet")
		assert.Error(t, err, "a write-only credential enumerated the fleet")

		_, err = target.GetObject(ctx, ref, "fleet/demo/inst_WRITEONLY/status.json")
		assert.Error(t, err, "a write-only credential read a row back")
	})

	// Prefix scoping, enforced by the store rather than by the manager. The
	// adapter refuses a key that climbs out of the target's own prefix too,
	// and that refusal protects nothing against a *different* machine --
	// this one does.
	t.Run("it cannot write outside its prefix", func(t *testing.T) {
		assert.Error(t, target.PutObject(ctx, ref, "attestations/op_01.json", []byte("{}")),
			"a credential scoped to fleet/ wrote outside it, so prefix scoping buys nothing")
	})
}
