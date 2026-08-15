package ops

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The decrypt bound, tested where it can be tested.
//
// `support inspect` decrypts into memory, so this limit is the ceiling on what
// a hostile archive can make the process allocate. Driving it through a real
// 64 MiB decryption would be a minute of CI for one branch, and lowering the
// constant for a test would leave production running a number no test has ever
// exercised -- so the writer that enforces it is tested directly, which is the
// unit the rule actually lives in.
//
// The residue is stated rather than hidden: nothing here proves `agecrypt`
// writes through this writer. That wiring is covered by decryption working at
// all, which several suite tests assert.
func TestTheBoundedBufferRefusesPastItsLimit(t *testing.T) {
	b := &boundedBuffer{limit: 10}

	n, err := b.Write([]byte("12345"))
	require.NoError(t, err)
	assert.Equal(t, 5, n)
	assert.False(t, b.exceeded)

	// The write that crosses is refused whole rather than truncated: a
	// partial write would leave a half-decrypted archive that parses, which
	// is worse than a refusal.
	n, err = b.Write([]byte("678901"))
	require.Error(t, err)
	assert.Zero(t, n)
	assert.True(t, b.exceeded, "the writer did not record that it refused")
	assert.Equal(t, "12345", b.buf.String(),
		"the refused bytes were kept anyway")
}

// Exactly at the limit is allowed. A boundary that refuses the last legal byte
// would reject an archive of precisely the documented size, which is the one
// size a reader would have checked against the docs.
func TestTheBoundedBufferAllowsExactlyItsLimit(t *testing.T) {
	b := &boundedBuffer{limit: 4}

	n, err := b.Write([]byte("abcd"))
	require.NoError(t, err)
	assert.Equal(t, 4, n)
	assert.False(t, b.exceeded)

	_, err = b.Write([]byte("e"))
	require.Error(t, err)
}

// The empty write, which a streaming decryptor can produce at a frame boundary,
// must not be mistaken for the limit being reached.
func TestTheBoundedBufferAcceptsAnEmptyWriteAtTheLimit(t *testing.T) {
	b := &boundedBuffer{limit: 3}
	_, err := b.Write([]byte("xyz"))
	require.NoError(t, err)

	n, err := b.Write(nil)
	require.NoError(t, err, "an empty write at the limit was refused")
	assert.Zero(t, n)
	assert.False(t, b.exceeded)
}
