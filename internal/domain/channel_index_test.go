package domain_test

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/domain"
)

const indexDigest = "sha256:bcca96e8020143562b3040e9c443f36ae57d8e2594c59b7f2499902416b211c5"

// The index a vendor issues reads back as the same index, in the field order
// RFC 0037 §5.2 shows, with the issue time in milliseconds as its sequence.
func TestAChannelIndexReadsBackAsIssued(t *testing.T) {
	now := time.Date(2026, 10, 29, 8, 0, 0, 123e6, time.UTC)
	idx := domain.NewChannelIndex("stable", domain.MustParseVersion("1.4.0"), indexDigest, now, 7*24*time.Hour)

	data, err := idx.Encode()
	require.NoError(t, err)
	assert.Equal(t, `{
  "schema": 1,
  "channel": "stable",
  "sequence": 1793260800123,
  "issued_at": "2026-10-29T08:00:00Z",
  "expires_at": "2026-11-05T08:00:00Z",
  "release": {
    "version": "1.4.0",
    "digest": "`+indexDigest+`"
  }
}
`, string(data))

	back, err := domain.ParseChannelIndex(data)
	require.NoError(t, err)
	assert.Equal(t, idx.Sequence, back.Sequence)
	assert.True(t, back.ExpiresAt.Equal(idx.ExpiresAt.Time))
	assert.Equal(t, "1.4.0", back.Release.Version.String())
}

func TestAChannelIndexThatIsNotWellFormedIsRefused(t *testing.T) {
	good := `{"schema":1,"channel":"stable","sequence":5,"issued_at":"2026-10-29T08:00:00Z",` +
		`"expires_at":"2026-11-05T08:00:00Z","release":{"version":"1.4.0","digest":"` + indexDigest + `"}}`
	_, err := domain.ParseChannelIndex([]byte(good))
	require.NoError(t, err)

	cases := map[string][2]string{
		"not JSON":            {good, "{"},
		"a newer schema":      {`"schema":1`, `"schema":2`},
		"no channel":          {`"channel":"stable"`, `"channel":" "`},
		"no sequence":         {`"sequence":5`, `"sequence":0`},
		"no expiry":           {`"expires_at":"2026-11-05T08:00:00Z"`, `"expires_at":""`},
		"expiry before issue": {`"expires_at":"2026-11-05T08:00:00Z"`, `"expires_at":"2026-10-01T08:00:00Z"`},
		"no version":          {`"version":"1.4.0",`, ``},
		"a manifest digest":   {indexDigest, "sha256:abc"},
		"an uppercase digest": {indexDigest, "sha256:" + strings.ToUpper(strings.TrimPrefix(indexDigest, "sha256:"))},
	}
	for name, edit := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := domain.ParseChannelIndex([]byte(strings.Replace(good, edit[0], edit[1], 1)))
			assert.Error(t, err)
		})
	}
}

// An accepted index holds until its expiry and not at it, by this machine's
// clock alone (RFC 0037 decision 32).
func TestAnAcceptedIndexExpiresAtItsExpiry(t *testing.T) {
	at := time.Date(2026, 11, 5, 8, 0, 0, 0, time.UTC)
	idx := domain.AcceptedIndex{ExpiresAt: domain.NewTime(at)}
	assert.False(t, idx.Expired(at.Add(-time.Nanosecond)))
	assert.True(t, idx.Expired(at))
	assert.True(t, idx.Expired(at.Add(time.Second)))
}

func TestAnEmptyIndexRecordIsZero(t *testing.T) {
	assert.True(t, domain.ChannelIndexRecord{}.IsZero())
	assert.False(t, domain.ChannelIndexRecord{Unchecked: true}.IsZero())
	assert.False(t, domain.ChannelIndexRecord{Accepted: &domain.AcceptedIndex{}}.IsZero())
}
