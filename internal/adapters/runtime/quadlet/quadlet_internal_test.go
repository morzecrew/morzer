package quadlet

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/infra/exec"
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
		"a relative env file":       {web + "EnvironmentFile=app.env\n", "resolves against"},
		// Quadlet joins it onto the unit directory before systemd expands
		// it (measured on Podman 6.1.1).
		"an env file under a variable":   {web + "EnvironmentFile=${DEMO_RELEASE_DIR}/app.env\n", "resolves against"},
		"a container name specifier":     {web + "ContainerName=demo-%N\n", "systemd expands"},
		"a container name variable":      {web + "ContainerName=demo-$X\n", "systemd expands"},
		"a mount the manager cannot see": {web + "Mount=type=volume,source=demo-data,target=/m\n", "Mount="},
		"a variable naming a volume":     {web + "Volume=${DEMO_PARAM_DATA}:/data\n", "not a path once its values are set"},
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
			"Volume=demo-cache:/cache\nNetwork=demo-net.network\nContainerName=demo-web-1\n" +
			"Mount=type=tmpfs,target=/scratch\n",
		"demo-data.volume": "[Volume]\n",
		"demo-net.network": "[Network]\n",
	}))
	assert.NoError(t, err)
}

// A variable that sets a path is a bind, which systemd expands at start; one
// whose value is not a path would make a named volume no file declares.
func TestAVolumeUnderAVariableIsAPathOnceSet(t *testing.T) {
	cfg := bundleOf(t, "demo", map[string]string{"demo-web.container": web + "Volume=${DEMO_PARAM_DATA}:/data\n"})
	cfg.Env = map[string]string{"DEMO_PARAM_DATA": "/srv/data"}
	_, err := checkBundle(cfg)
	assert.NoError(t, err)
	cfg.Env["DEMO_PARAM_DATA"] = "demo-data"
	_, err = checkBundle(cfg)
	assert.ErrorContains(t, err, "not a path once its values are set")
}

// A volume or network unit's name, and a container's, can coincide:
// both files would generate demo-data-volume.service.
func TestTwoFilesGeneratingOneUnitAreRefused(t *testing.T) {
	_, err := checkBundle(bundleOf(t, "demo", map[string]string{
		"demo-web.container":         web,
		"demo-data-volume.container": web,
		"demo-data.volume":           "[Volume]\n",
	}))
	assert.ErrorContains(t, err, "generates the unit demo-data-volume.service")
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

	entries, err = parseUnit("x", []byte("[Container]\nExec=sh \\\n# a comment\n  -c true\n"))
	require.NoError(t, err, "a comment inside a continuation is skipped, as systemd skips it")
	exec, _ = (unitFile{entries: entries}).last("Container", "Exec")
	assert.Equal(t, "sh    -c true", exec)
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

// No record, line or stream of a one-shot's log is held past the capture
// bound, however long a line the one-shot wrote without a newline.
func TestAOneShotsLogIsBoundedHoweverLongALine(t *testing.T) {
	limit := exec.DefaultMaxCapture
	path := filepath.Join(t.TempDir(), "run.log")
	piece := strings.Repeat("x", limit/2)
	var log strings.Builder
	for range 4 {
		log.WriteString("2026-11-26T17:44:33.1+03:00 stdout P " + piece + "\n")
	}
	log.WriteString("2026-11-26T17:44:33.2+03:00 stdout F end\n")
	log.WriteString("2026-11-26T17:44:33.3+03:00 stderr F " + strings.Repeat("y", 2*limit) + "\n")
	require.NoError(t, os.WriteFile(path, []byte(log.String()), 0o600))

	stdout, stderr, output, truncated := readRunLog(path)
	assert.True(t, truncated)
	assert.LessOrEqual(t, len(stdout), limit, "the pieces of one line are bounded as they are joined")
	assert.LessOrEqual(t, len(stderr), limit, "one record is bounded as it is read")
	assert.LessOrEqual(t, len(output), 2*limit)
}

// The copy of a service a one-shot runs has a container of its own, so a
// service that names its container keeps running beside it; and its timeout
// keeps a fraction of a second.
func TestAOneShotsCopyNamesItsOwnContainer(t *testing.T) {
	dir := t.TempDir()
	p := runPaths{
		unit: filepath.Join(dir, "demo-web-run-1.container"), dropIn: filepath.Join(dir, "demo-web-run-1.container.d"),
		env: filepath.Join(dir, "demo-web-run-1.env"), log: filepath.Join(dir, "demo-web-run-1.log"),
		status: filepath.Join(dir, "demo-web-run-1.status"),
	}
	u := unitFile{name: "demo-web.container", kind: kindContainer, text: []byte(web + "ContainerName=demo-web-1\n")}
	require.NoError(t, p.write(u, "demo-web-run-1", nil, ports.RunOptions{Timeout: 1500 * time.Millisecond}))
	got, err := os.ReadFile(filepath.Join(p.dropIn, dropIn))
	require.NoError(t, err)
	assert.Contains(t, string(got), "\nContainerName=demo-web-run-1\n")
	assert.Contains(t, string(got), "\nTimeoutStartSec=1500ms\n")
}

// With no service installed, as after a reboot before the first apply, an
// unfiltered journal would be every other service's, so nothing is read.
func TestLogsWithNothingInstalledReadNothing(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("the adapter refuses root")
	}
	r := &Runtime{runtimeDir: t.TempDir()}
	rc, err := r.Logs(context.Background(), ports.RuntimeConfig{Product: "demo"}, ports.LogOptions{})
	require.NoError(t, err)
	got, err := io.ReadAll(rc)
	require.NoError(t, err)
	assert.Empty(t, got)
}

// recorder is a runner that runs nothing and remembers what it was asked.
type recorder struct{ argv [][]string }

func (r *recorder) Run(_ context.Context, cmd exec.Command) (exec.Result, error) {
	r.argv = append(r.argv, cmd.Argv)
	return exec.Result{}, nil
}
func (r *recorder) Look(name string) (string, error) { return name, nil }

// An installed `.volume` file and the declared one of the same name may set
// different names; Down with volumes removes both.
func TestBothNamesOfAVolumeAreRemoved(t *testing.T) {
	rec := &recorder{}
	r := &Runtime{runner: rec, podman: "podman"}
	b := bundle{product: "demo", units: []unitFile{
		{name: "demo-data.volume", kind: kindVolume, entries: []entry{{section: "Volume", key: "VolumeName", value: "demo-old"}}},
		{name: "demo-data.volume", kind: kindVolume, entries: []entry{{section: "Volume", key: "VolumeName", value: "demo-new"}}},
	}}
	require.NoError(t, r.removeVolumes(context.Background(), b))
	var removed []string
	for _, a := range rec.argv {
		removed = append(removed, a[len(a)-1])
	}
	assert.ElementsMatch(t, []string{"demo-old", "demo-new"}, removed)
}
