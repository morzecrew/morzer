//go:build docker

package suite

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/adapters/runtime/compose"
	infraexec "github.com/morzecrew/morzer/internal/infra/exec"
	"github.com/morzecrew/morzer/test/dockerlab"
)

// Compose reads a multi-line <PRODUCT>_PARAM_<NAME> from its process
// environment intact, which is the whole of RFC 0054 decision 5's delivery to
// Compose. The manager passes no env file, so newlines never meet one.
func TestComposeInterpolatesAMultiLineParameterIntact(t *testing.T) {
	dockerlab.Require(t)
	cfg := dockerlab.Project(t, fmt.Sprintf(`
services:
  app:
    image: %s
    environment:
      RULES: ${DEMO_PARAM_RULES}
`, dockerlab.ImageBusybox))
	value := "DOMAIN-SUFFIX,example.ru\n  - nested: \"quoted\"\n\tlast"
	cfg.Env = map[string]string{"DEMO_PARAM_RULES": value}

	rendered, err := compose.New(infraexec.New()).Validate(context.Background(), cfg)
	require.NoError(t, err)

	var doc struct {
		Services map[string]struct {
			Environment map[string]string `json:"environment"`
		} `json:"services"`
	}
	require.NoError(t, json.Unmarshal(rendered.Config, &doc))
	assert.Equal(t, value, doc.Services["app"].Environment["RULES"])
}
