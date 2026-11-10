package domain_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/adapters/sign/minisign"
	"github.com/morzecrew/morzer/internal/domain"
)

// A document's objects are found by name alone, so the name parser is what
// stands between a listing and a fetch: only `<digits>.age` and its signature
// directly under the prefix are documents.
func TestOnlySerialNamedPairsUnderThePrefixAreDocuments(t *testing.T) {
	prefix := domain.DesiredStatePrefix("demo", "inst_01")
	doc, sig := domain.DesiredStateKeys("demo", "inst_01", 42)
	assert.Equal(t, "state/demo/inst_01/42.age", doc)
	assert.Equal(t, doc+".minisig", sig)

	for key, want := range map[string]struct {
		serial  int64
		sig, ok bool
	}{
		doc:                                 {serial: 42, ok: true},
		sig:                                 {serial: 42, sig: true, ok: true},
		prefix + "+5.age":                   {},
		prefix + "007.age":                  {},
		prefix + "01.age.minisig":           {},
		prefix + "0.age":                    {},
		prefix + "05x.age":                  {},
		prefix + "7.yaml":                   {},
		prefix + "sub/7.age":                {},
		"state/demo/inst_02/7.age":          {},
		prefix + "99999999999999999999.age": {},
	} {
		serial, isSig, ok := domain.ParseDesiredStateKey(prefix, key)
		assert.Equal(t, want.ok, ok, key)
		if want.ok {
			assert.Equal(t, want.serial, serial, key)
			assert.Equal(t, want.sig, isSig, key)
		}
	}
}

func TestADesiredStateCommentReadsBack(t *testing.T) {
	product, id, serial, ok := domain.ParseDesiredStateComment(domain.DesiredStateComment("demo", "inst_01", 7))
	require.True(t, ok)
	assert.Equal(t, []any{"demo", "inst_01", int64(7)}, []any{product, id, serial})

	for _, comment := range []string{
		"",
		"timestamp:1794310000\tfile:7.age",
		"morzer desired-state product=demo installation=inst_01",
		"morzer desired-state product=demo installation=inst_01 serial=+7",
		"morzer desired-state product=demo installation=inst_01 serial=07",
		"morzer desired-state product=demo installation=inst_01 serial=0",
		"morzer fleet-row product=demo installation=inst_01 serial=7",
		"morzer desired-state product= installation=inst_01 serial=7",
	} {
		_, _, _, ok := domain.ParseDesiredStateComment(comment)
		assert.False(t, ok, "%q", comment)
	}
}

// The id the watermark is kept under is the one the signer prints for the
// same key, so an operator can match `sync status` to `minisign -G`'s output.
func TestMinisignKeyIDIsTheSignersID(t *testing.T) {
	key, err := minisign.New(filepath.Join(t.TempDir(), "authority.key"), "authority").EnsureKey(context.Background())
	require.NoError(t, err)
	assert.Equal(t, key.KeyID, domain.MinisignKeyID(key.Line))
	assert.Empty(t, domain.MinisignKeyID("RWQnot-a-key"))
	assert.Empty(t, domain.MinisignKeyID(""))
}
