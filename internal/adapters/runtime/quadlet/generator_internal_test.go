package quadlet

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/infra/exec"
)

// The generator logs each file it loads before any reason, so a refusal
// reported by its first line names a file and says nothing. The reason is
// the `converting` line. Measured on Podman 6.1.1; 4.9.3 logs the same way.
func TestAGeneratorRefusalSaysWhy(t *testing.T) {
	stderr := `quadlet-generator[42]: Loading source unit file /tmp/x/demo-app.container
quadlet-generator[42]: Loading source unit file /tmp/x/demo-db.container
quadlet-generator[42]: converting "demo-app.container": invalid port format '${DEMO_PARAM_HTTP_PORT}'
quadlet-generator[42]: converting "demo-db.container": unsupported key 'Nope' in group 'Container'
quadlet-generator[42]: processing encountered some errors
`
	assert.Equal(t,
		`converting "demo-app.container": invalid port format '${DEMO_PARAM_HTTP_PORT}'; `+
			`converting "demo-db.container": unsupported key 'Nope' in group 'Container'`,
		generatorError(stderr, ""))
	assert.Equal(t, `converting "demo-db.container": unsupported key 'Nope' in group 'Container'`,
		generatorError(stderr, "demo-db.container"), "a unit's refusal names its own reason")
	assert.Equal(t, "quadlet-generator[42]: something else", generatorError("quadlet-generator[42]: something else\n", ""),
		"with no reason line, the first line is all there is")
}

// generatorRun answers the generator's dry run: with an exit, the way the
// runner reports one, its stderr carried whole on the result and in excerpt on
// the error; without one, with units on stdout.
type generatorRun struct {
	stdout, stderr string
	exit           bool
}

func (g generatorRun) Run(context.Context, exec.Command) (exec.Result, error) {
	res := exec.Result{Stdout: g.stdout, Stderr: g.stderr}
	if !g.exit {
		return res, nil
	}
	res.ExitCode = 1
	return res, &exec.ExitError{Argv: []string{"generator"}, ExitCode: 1, Stderr: g.stderr}
}

func (generatorRun) Look(name string) (string, error) { return name, nil }

// A refusal names each reason once, whether the generator exited on it or
// skipped the unit and exited 0.
func TestARefusedBundleNamesEachReasonOnce(t *testing.T) {
	ctx := context.Background()
	b := bundle{product: "demo", units: []unitFile{
		{name: "demo-app.container", kind: kindContainer, text: []byte("[Container]\n")},
		{name: "demo-db.container", kind: kindContainer, text: []byte("[Container]\n")},
	}}
	stderr := "quadlet-generator[7]: Loading source unit file /tmp/x/demo-app.container\n" +
		`converting "demo-db.container": invalid port format '[::1]: 80'` + "\n"
	reason := `converting "demo-db.container": invalid port format '[::1]: 80'`

	err := (&Runtime{runner: generatorRun{stderr: stderr, exit: true}, generator: "generator"}).generate(ctx, b)
	require.Error(t, err)
	message := domain.AsError(err).Message
	assert.Equal(t, 1, strings.Count(message, reason), "%s", message)

	err = (&Runtime{runner: generatorRun{stdout: "---demo-app.service---\n", stderr: stderr}, generator: "generator"}).generate(ctx, b)
	require.Error(t, err)
	assert.Contains(t, domain.AsError(err).Message, "would not generate demo-db.container: "+reason,
		"an unprefixed reason holding `]: ` is kept whole")
}
