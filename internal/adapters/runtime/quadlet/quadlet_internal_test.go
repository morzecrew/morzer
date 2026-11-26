package quadlet

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/ports"
)

// bundleOf writes files under product and returns the configuration naming
// them.
func bundleOf(t *testing.T, product string, files map[string]string) ports.RuntimeConfig {
	t.Helper()
	dir := t.TempDir()
	cfg := ports.RuntimeConfig{Product: product, WorkingDir: dir}
	for name, text := range files {
		path := filepath.Join(dir, name)
		require.NoError(t, os.WriteFile(path, []byte(text), 0o600))
		cfg.Files = append(cfg.Files, path)
	}
	return cfg
}

const web = "[Container]\nImage=docker.io/library/busybox:1.36\n"

// Every name carries the product, so two installations' units, containers,
// volumes and networks cannot meet (logs/quadlet-adapter.md, Q1).
func TestAQuadletFileNamesItsProduct(t *testing.T) {
	_, err := checkBundle(bundleOf(t, "demo", map[string]string{"web.container": web}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), `does not start with "demo-"`)

	_, err = checkBundle(bundleOf(t, "demo", map[string]string{"demo-.container": web}))
	require.Error(t, err, "a prefix with no service name names nothing")

	b, err := checkBundle(bundleOf(t, "demo", map[string]string{"demo-web.container": web}))
	require.NoError(t, err)
	assert.Equal(t, []string{"web"}, b.started(), "the service is the name without the prefix")
}

func TestOnlyContainersVolumesAndNetworksAreCarried(t *testing.T) {
	for _, kind := range []string{"pod", "kube", "image", "build"} {
		_, err := checkBundle(bundleOf(t, "demo", map[string]string{
			"demo-web.container": web, "demo-x." + kind: "[X]\n",
		}))
		require.Error(t, err, kind)
		assert.Contains(t, err.Error(), "."+kind)
	}
}

// What the adapter cannot run as the vendor wrote it is refused, never
// rewritten.
func TestAUnitTheAdapterCannotRunAsWrittenIsRefused(t *testing.T) {
	cases := map[string]struct{ text, says string }{
		"an [Install] section":      {web + "[Install]\nWantedBy=default.target\n", "[Install]"},
		"an anonymous volume":       {web + "Volume=/data\n", "anonymous volume"},
		"a relative bind":           {web + "Volume=./data:/data\n", "relative path"},
		"another's named volume":    {web + "Volume=shared:/data\n", `does not start with "demo-"`},
		"a volume file not shipped": {web + "Volume=demo-gone.volume:/data\n", "not a file of the bundle"},
		"another's network":         {web + "Network=shared\n", `does not start with "demo-"`},
		"a container name":          {web + "ContainerName=web\n", "ContainerName=web"},
		"a pod":                     {web + "Pod=demo-p.pod\n", "pod"},
		"a relative env file":       {web + "EnvironmentFile=app.env\n", "relative path"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := checkBundle(bundleOf(t, "demo", map[string]string{"demo-web.container": c.text}))
			require.Error(t, err)
			assert.Contains(t, err.Error(), c.says)
		})
	}
}

func TestWhatTheAdapterCanRunIsAccepted(t *testing.T) {
	_, err := checkBundle(bundleOf(t, "demo", map[string]string{
		"demo-web.container": web + "Volume=demo-data.volume:/data\nVolume=${DEMO_SECRETS_DIR}/t:/run/t:ro\n" +
			"Volume=demo-cache:/cache\nNetwork=demo-net.network\nContainerName=demo-web-1\n",
		"demo-data.volume": "[Volume]\n",
		"demo-net.network": "[Network]\n",
	}))
	assert.NoError(t, err)
}

func TestOptionsAndProfilesAreRefused(t *testing.T) {
	cfg := bundleOf(t, "demo", map[string]string{"demo-web.container": web})
	cfg.Options = map[string]string{"project": "x"}
	_, err := checkBundle(cfg)
	assert.ErrorContains(t, err, "takes no options")

	cfg.Options, cfg.Profiles = nil, []string{"tools"}
	_, err = checkBundle(cfg)
	assert.ErrorContains(t, err, "no service profiles")
}

// A one-shot is a container Up never starts (Q2), and still a service.
func TestAOneShotIsDefinedAndNotStarted(t *testing.T) {
	b, err := checkBundle(bundleOf(t, "demo", map[string]string{
		"demo-web.container":     web,
		"demo-migrate.container": web + "\n[Service]\nType=oneshot\n",
	}))
	require.NoError(t, err)
	assert.Equal(t, []string{"web"}, b.started())
	assert.NoError(t, b.defines([]string{"migrate"}))
	assert.Error(t, b.defines([]string{"vpn"}))
}

func TestAUnitIsReadAsSystemdReadsIt(t *testing.T) {
	entries, err := parseUnit("x", []byte("# comment\n; also\n[Container]\nExec=sh -c \\\n  \"echo hi\"\nEnvironment=A=1\nEnvironment=B=2\n"))
	require.NoError(t, err)
	u := unitFile{entries: entries}
	assert.Equal(t, []string{"A=1", "B=2"}, u.values("Container", "Environment"), "a key may repeat")
	exec, _ := u.last("Container", "Exec")
	assert.Equal(t, `sh -c    "echo hi"`, exec, "a trailing backslash continues the line")

	_, err = parseUnit("x", []byte("Image=x\n"))
	assert.ErrorContains(t, err, "before any section")
}

// systemd reads the parameter file at every start (RFC 0023 row 29, measured),
// and a value is quoted so it arrives as it was set. Measured on systemd 261:
// a value with a space, a quote, a backslash, a dollar and a percent sign came
// back unchanged in the container.
func TestTheParameterFileCarriesEachValueAsItWasSet(t *testing.T) {
	data, err := environmentFile(map[string]string{
		"B": `q"uote and back\slash`, "A": "has space and $dollar and %pct",
	})
	require.NoError(t, err)
	assert.Equal(t, "A=\"has space and \\$dollar and %pct\"\nB=\"q\\\"uote and back\\\\slash\"\n", string(data))

	_, err = environmentFile(map[string]string{"C": "two\nlines"})
	assert.ErrorContains(t, err, "line break")
}

// The parameter file is referenced without the `-`, so a unit started before
// the manager rendered it fails rather than running on empty values (row 21).
func TestTheDropInNamesTheParameterCopyWithoutTheDash(t *testing.T) {
	got := string(dropInFor(unitFile{kind: kindContainer}, "/run/user/1/x/parameters.env", "abc"))
	assert.Contains(t, got, "\nEnvironmentFile=/run/user/1/x/parameters.env\n")
	assert.NotContains(t, got, "EnvironmentFile=-")
	assert.Contains(t, got, "Label="+definitionLabel+"=abc")
}

// A changed value a unit names re-creates its container; one it does not name
// does not.
func TestTheDefinitionFollowsTheValuesAUnitNames(t *testing.T) {
	u := unitFile{text: []byte("[Container]\nPublishPort=${DEMO_PARAM_PORT}:80\nExec=sh -c \"echo $$HOME\"\n")}
	before := definition(u, map[string]string{"DEMO_PARAM_PORT": "8080", "OTHER": "1"})
	assert.NotEqual(t, before, definition(u, map[string]string{"DEMO_PARAM_PORT": "8081", "OTHER": "1"}))
	assert.Equal(t, before, definition(u, map[string]string{"DEMO_PARAM_PORT": "8080", "OTHER": "2"}))
	assert.Equal(t, []string{"DEMO_PARAM_PORT"}, variables(u.text), "`$$` is a literal dollar, not a variable")
}

func TestAWordReachesTheContainerAsOneArgument(t *testing.T) {
	assert.Equal(t, `"a \"b\" $$HOME 100%% \\n"`, systemdWord(`a "b" $HOME 100% \n`))
}

func TestAUnitWithNoContainerReportsWhatItsStateMeans(t *testing.T) {
	state, code := stateOfUnit(unitState{active: "failed", exitStatus: 137})
	assert.Equal(t, ports.StateExited, state)
	assert.Equal(t, 137, code)
	state, _ = stateOfUnit(unitState{active: "inactive"})
	assert.False(t, ports.ServiceState{State: state}.OccupiesVolume(), "a stopped service holds no volume")
	state, _ = stateOfUnit(unitState{active: "deactivating"})
	assert.True(t, ports.ServiceState{State: state}.OccupiesVolume(), "one stopping may still hold it")
}

func TestOnlyAContainersOwnLinesAreFramed(t *testing.T) {
	var out strings.Builder
	w := &lineWriter{out: &out, frame: func(line string) (string, bool) {
		return line, strings.HasPrefix(line, "keep")
	}}
	_, _ = w.Write([]byte("keep 1\ndrop\nkee"))
	_, _ = w.Write([]byte("p 2\n"))
	w.flush()
	assert.Equal(t, "keep 1\nkeep 2\n", out.String())

	e := journalEntry{Message: []byte(`[104,105]`)}
	assert.Equal(t, "hi", e.text(), "a message that is not UTF-8 arrives as bytes")
}

func TestStorageIsReadFromTheFiles(t *testing.T) {
	b, err := checkBundle(bundleOf(t, "demo", map[string]string{
		"demo-web.container": web + "Volume=demo-data.volume:/data\nVolume=${DEMO_DATA_DIR}/up:/up\n",
		"demo-db.container":  web + "Volume=demo-data.volume:/db\n",
		"demo-data.volume":   "[Volume]\n",
	}))
	require.NoError(t, err)
	s := b.storage(map[string]string{"DEMO_DATA_DIR": "/srv/demo"})
	require.Len(t, s.Volumes, 1)
	assert.Equal(t, ports.NamedVolume{Name: "data", Actual: "systemd-demo-data", Services: []string{"db", "web"}}, s.Volumes[0])
	require.Len(t, s.Binds, 1)
	assert.Equal(t, "/srv/demo/up", s.Binds[0].Source)
}

// A one-shot's log names each line's stream and keeps the order lines arrived
// in. A P entry is a piece of a line the next entry of that stream continues,
// and the last one of a stream, with no newline after it, is how it ended.
func TestAOneShotsLogIsReadBackStreamByStream(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.log")
	require.NoError(t, os.WriteFile(path, []byte(
		"2026-11-26T17:44:33.1+03:00 stdout F out\n"+
			"2026-11-26T17:44:33.2+03:00 stderr P er\n"+
			"2026-11-26T17:44:33.3+03:00 stdout F \n"+
			"2026-11-26T17:44:33.4+03:00 stderr F r\n"+
			"2026-11-26T17:44:33.5+03:00 stdout P last\n"), 0o600))
	stdout, stderr, output, truncated := readRunLog(path)
	assert.Equal(t, "out\n\nlast", stdout, "a blank line stays, and the unterminated end is kept")
	assert.Equal(t, "err", stderr, "the pieces of one line are joined")
	assert.Equal(t, "out\nerr\nlast", output, "the tail keeps arrival order and drops blank lines")
	assert.False(t, truncated)

	stdout, _, _, _ = readRunLog(filepath.Join(t.TempDir(), "absent.log"))
	assert.Empty(t, stdout, "a one-shot that wrote nothing has nothing to read")
}
