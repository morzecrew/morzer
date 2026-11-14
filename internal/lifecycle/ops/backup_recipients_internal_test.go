package ops

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/ports"
)

// firstWriteStore is a store whose first write lands while a backup is asking
// who it is for: the state does not exist when the recipients are read, and
// does by the time anything else is.
type firstWriteStore struct {
	ports.SecretStore
	created bool
}

func (s *firstWriteStore) Recipients(context.Context) ([]ports.Recipient, error) {
	if !s.created {
		s.created = true
		return nil, nil
	}
	return []ports.Recipient{{PublicKey: "age1machine", Kind: ports.RecipientMachine}}, nil
}

func (s *firstWriteStore) Initialized(context.Context) (bool, error) { return s.created, nil }

func (s *firstWriteStore) IdentityPublicKey(context.Context) (string, error) {
	return "age1machine", nil
}

// TestABackupRacingTheFirstSecretWriteStillHasARecipient.
//
// Read in the other order, the recipients said no state and the state then
// said it existed, which together read as state naming nobody, and the backup
// was refused. Whichever way the race goes, there is a key to encrypt for.
func TestABackupRacingTheFirstSecretWriteStillHasARecipient(t *testing.T) {
	keys, err := BackupRecipients(context.Background(), &Deps{Secrets: &firstWriteStore{}})
	require.NoError(t, err)
	assert.Equal(t, []string{"age1machine"}, keys)
}
