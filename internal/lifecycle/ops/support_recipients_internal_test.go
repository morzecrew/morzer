package ops

import (
	"testing"

	"github.com/goccy/go-yaml"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/domain"
)

// What the decoder actually puts in an `extensions` block.
//
// `SupportRecipients` type-asserts its way through `map[string]any`, and every
// one of those assertions is a guess about the YAML library until something
// runs one. The list case especially: a decoder that produced `[]string` for a
// list of strings would make the `[]any` branch dead and send a valid manifest
// down the "not a list" refusal. Asserted through `yaml.Unmarshal` rather than
// by building the map in Go, because a hand-built map proves what this test
// wrote, not what a vendor's file decodes to.
func TestTheDecoderPutsARecipientListWhereTheReaderLooks(t *testing.T) {
	const doc = `
extensions:
  morzer.dev/support:
    recipients:
      - age1qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqsx4gexk
      - age1pppppppppppppppppppppppppppppppppppppppppppppppppppsxa8ns
`
	var m domain.Manifest
	require.NoError(t, yaml.Unmarshal([]byte(doc), &m))

	block, ok := m.Extensions[domain.SupportExtension]
	require.True(t, ok, "the namespace did not survive decoding")

	_, isAnyList := block["recipients"].([]any)
	assert.True(t, isAnyList,
		"a YAML list decoded to %T, so the reader's []any assertion is wrong", block["recipients"])

	got, err := m.SupportRecipients()
	require.NoError(t, err)
	assert.Len(t, got, 2)
}
