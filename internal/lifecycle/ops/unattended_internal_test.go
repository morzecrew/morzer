package ops

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/infra/state"
)

// markInstallFailed writes after the failed update has released the deployment
// lock, so the record it re-reads may not be the one that was attempted: an
// operator's `update --stage` can have staged something newer in between.
// Marking that one would leave a release nobody tried uninstalled until the
// channel moved again.
func TestMarkingAFailedInstallLeavesANewerCandidateAlone(t *testing.T) {
	ctx := context.Background()
	d := &Deps{State: state.New(domain.PathsUnder(t.TempDir(), "demo"))}
	rec := domain.OperationRecord{ID: "op_1", Status: domain.StatusCompensated}
	attempted := domain.UpdateCandidate{UpstreamDigest: "sha256:aaa", Root: "/r/1.3.0"}

	require.NoError(t, d.State.SetUpdateCandidate(ctx,
		domain.UpdateCandidate{UpstreamDigest: "sha256:bbb", Root: "/r/1.3.1"}))
	d.markInstallFailed(ctx, attempted, rec)
	got, err := d.State.UpdateCandidate(ctx)
	require.NoError(t, err)
	assert.Empty(t, got.Failed, "a candidate staged after the attempt was marked as its failure")

	// The attempted one is marked, or the assertion above passes on a
	// function that writes nothing at all.
	require.NoError(t, d.State.SetUpdateCandidate(ctx, attempted))
	d.markInstallFailed(ctx, attempted, rec)
	got, err = d.State.UpdateCandidate(ctx)
	require.NoError(t, err)
	assert.Equal(t, "op_1 ended compensated", got.Failed)
}
