//go:build docker

package suite

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/adapters/runtime/compose"
	infraexec "github.com/morzecrew/morzer/internal/infra/exec"
	"github.com/morzecrew/morzer/internal/ports"
	"github.com/morzecrew/morzer/test/dockerlab"
)

// The definition hashes against a real Compose (RFC 0044 §5.4). The scripted
// tests read the shapes Compose 5.5.1 printed; this is what notices the day a
// Compose release prints them differently, or computes the label and the
// declared hash apart.

const definitionComposeYAML = `
services:
  web:
    image: ` + dockerlab.ImageBusybox + `
    init: true
    command: ["sh", "-c", "while true; do sleep 3600; done"]
    deploy:
      replicas: 2
`

func TestDefinitionDriftAgainstARealCompose(t *testing.T) {
	dockerlab.Require(t)
	dockerlab.Pull(t, dockerlab.ImageBusybox)

	cfg := dockerlab.Project(t, definitionComposeYAML)
	runtime := compose.New(infraexec.New())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	require.NoError(t, runtime.Up(ctx, cfg, ports.UpOptions{}))

	// A `compose run` container carries the service's name and a hash of
	// its own; it is not compared.
	_, err := runtime.RunOneShot(ctx, cfg, "web", ports.RunOptions{Argv: []string{"true"}})
	require.NoError(t, err)

	defs, err := runtime.DefinitionDrift(ctx, cfg)
	require.NoError(t, err)
	require.Len(t, defs, 1)
	assert.Equal(t, "web", defs[0].Service)
	assert.Len(t, defs[0].Running, 2, "the run container was counted, or a replica was missed")
	assert.False(t, defs[0].Drifted(), "a project `up` just created reads as drifted: %+v", defs[0])

	// The definition changes and nothing re-creates the containers: both
	// replicas, stopped ones included, now differ from what is declared.
	require.NoError(t, runtime.Stop(ctx, cfg, nil, 5*time.Second))
	changed := strings.Replace(definitionComposeYAML, "sleep 3600", "sleep 3601", 1)
	require.NoError(t, os.WriteFile(cfg.Files[0], []byte(changed), 0o600))

	defs, err = runtime.DefinitionDrift(ctx, cfg)
	require.NoError(t, err)
	require.Len(t, defs, 1)
	assert.True(t, defs[0].Drifted(), "a changed definition read as the one the containers run: %+v", defs[0])
	assert.Len(t, defs[0].Running, 2)

	require.NoError(t, runtime.Up(ctx, cfg, ports.UpOptions{}))
	defs, err = runtime.DefinitionDrift(ctx, cfg)
	require.NoError(t, err)
	assert.False(t, defs[0].Drifted(), "`up` re-created the services and they still read as drifted: %+v", defs[0])
}
