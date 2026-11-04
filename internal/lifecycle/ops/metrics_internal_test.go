package ops

import (
	"strings"
	"testing"

	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A label value is escaped as the text format requires, whatever it holds, so
// a name the manager did not expect cannot end a series early (RFC 0047
// decision 7). A family with no series has no HELP either.
func TestTheExpositionEscapesWhatItCannotTrust(t *testing.T) {
	m := newExposition(`de"mo`)
	m.family("morzer_service_running", "Containers\nrunning.").sample(1, "service", "a\"b\\c\nd")
	m.family("morzer_unused", "never sampled")
	text := m.String()

	assert.Contains(t, text, `morzer_service_running{product="de\"mo",service="a\"b\\c\nd"} 1`)
	assert.Contains(t, text, `# HELP morzer_service_running Containers\nrunning.`)
	assert.NotContains(t, text, "morzer_unused")

	parser := expfmt.NewTextParser(model.LegacyValidation)
	families, err := parser.TextToMetricFamilies(strings.NewReader(text))
	require.NoError(t, err, text)
	got := families["morzer_service_running"].GetMetric()[0].GetLabel()
	assert.Equal(t, "a\"b\\c\nd", got[1].GetValue())
}
