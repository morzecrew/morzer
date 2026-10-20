package ops

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/lifecycle/engine"
	"github.com/morzecrew/morzer/internal/ports"
)

// The render step's Verify holds the owner to the declaration as it holds the
// mode (RFC 0032 decision 7). The store battery cannot reach this: it tests
// the stores, and a store that ignored owner is what Verify is there to
// catch. So the file here is written by hand, owned by this process, and
// checked against declarations it does and does not match.
func TestTheRenderedOwnerIsVerifiedNotAssumed(t *testing.T) {
	d := &Deps{Paths: domain.PathsUnder(t.TempDir(), "demo")}
	dir := d.Paths.SecretsRenderDir()
	require.NoError(t, os.MkdirAll(dir, 0o700))
	path := filepath.Join(dir, "backend_key")
	require.NoError(t, os.WriteFile(path, []byte("a-value"), 0o400))
	step := stepRenderSecrets(d, domain.Installation{}, domain.Release{}, Options{})

	verify := func(owner domain.SecretOwner) error {
		st := &engine.State{}
		st.Set(engine.KeySecretSchema, domain.SecretSchema{Secrets: []domain.SecretDeclaration{
			{Name: "backend_key", Owner: owner},
		}})
		st.Set(engine.KeyRenderedFiles, []ports.RenderedFile{{Name: "backend_key", Path: path}})
		return step.Verify(context.Background(), st)
	}
	uid, gid := uint32(os.Geteuid()), uint32(os.Getegid())

	require.NoError(t, verify(domain.SecretOwner{}), "undeclared, the file is the manager's")
	require.NoError(t, verify(domain.SecretOwner{UID: uid, GID: gid, HasGID: true, Set: true}))

	err := verify(domain.SecretOwner{UID: uid + 1, Set: true})
	require.Error(t, err, "a file its declared user does not own is one that user's container cannot read")
	assert.Equal(t, domain.ExitSecrets, domain.ExitCode(err))
	assert.Contains(t, err.Error(), "backend_key")

	require.Error(t, verify(domain.SecretOwner{UID: uid, GID: gid + 1, HasGID: true, Set: true}),
		"a declared gid is verified too")
}
