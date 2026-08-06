// Package clitest drives morzer the way an operator's shell does.
//
// Everything below the CLI is covered by the fake-backed suites in test/suite,
// and everything above real Docker by the acceptance script. This is the layer
// between: flag parsing, argument validation, confirmations, the JSON envelope,
// the error formatter, and the exit-code mapping that systemd units and CI
// pipelines depend on.
//
// It was missing entirely. Measured before it existed, `internal/cli` was 47.6%
// covered with 604 uncovered statements, and they were whole `RunE` closures:
// nothing drove `morzer release prune` or `morzer secret recipients add` as
// commands, only the operations beneath them.
//
// Runs against a temporary `--root`, so nothing touches /etc, and without
// Docker: the commands exercised here are the ones that read state, validate
// arguments, or refuse.
package clitest

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/morzecrew/morzer/internal/cli"
	"github.com/morzecrew/morzer/internal/ui"
)

// Runner is a temporary installation and the commands run against it.
type Runner struct {
	t    *testing.T
	Root string

	// Bundle is the example release, copied so a test may modify it.
	Bundle string
}

// New returns a runner over an empty root. Nothing is installed yet.
func New(t *testing.T) *Runner {
	t.Helper()
	requireSOPS(t)

	root := t.TempDir()
	return &Runner{t: t, Root: root, Bundle: hermeticBundle(t)}
}

// NewInstalled returns a runner over an initialised installation.
//
// The commands worth testing mostly need one, and doing it through `init`
// rather than by writing state files means the fixture is built by the code
// under test -- a change that breaks `init` breaks these too, rather than
// leaving them passing against a shape nothing produces any more.
func NewInstalled(t *testing.T, extra ...string) *Runner {
	t.Helper()
	r := New(t)

	args := append([]string{
		"init",
		"--release", r.Bundle,
		"--profile", "embedded",
		"--domain", "demo.example",
		"--no-recovery-recipient",
		"--install-units=false",
	}, extra...)

	r.Run(args...).ExitCode(0)
	return r
}

// Run invokes one command and captures everything it produced.
//
// `--root` and `--plain` are always prepended: the first keeps the test out of
// /etc, the second keeps the output stable regardless of whether the terminal
// running `go test` happens to be one.
func (r *Runner) Run(args ...string) Result {
	r.t.Helper()
	return r.RunWithInput("", args...)
}

// RunWithInput is Run with something on stdin.
//
// A `strings.Reader` is not a terminal, which is exactly the signal the piped
// path keys on: `printf %s 'value' | morzer secret set x` is the only way a
// script can supply a credential, because a --value flag would put it in the
// process table.
func (r *Runner) RunWithInput(input string, args ...string) Result {
	r.t.Helper()

	var out, errOut bytes.Buffer
	argv := append([]string{"--root", r.Root, "--plain"}, args...)

	code := cli.ExecuteWith(
		context.Background(),
		cli.BuildInfo{Version: "test"},
		argv,
		ui.Streams{Out: &out, Err: &errOut, In: strings.NewReader(input)},
	)

	return Result{
		t: r.t, args: argv, Code: code,
		Stdout: out.String(), Stderr: errOut.String(),
	}
}

// Result is one command's output.
type Result struct {
	t    *testing.T
	args []string

	Code   int
	Stdout string
	Stderr string
}

// ExitCode asserts the process status.
//
// The codes are a published contract that systemd units and CI pipelines read,
// so they are asserted rather than "it failed somehow".
func (r Result) ExitCode(want int) Result {
	r.t.Helper()
	if r.Code != want {
		r.t.Fatalf("`morzer %s` exited %d, want %d\n--- stdout ---\n%s--- stderr ---\n%s",
			strings.Join(r.args, " "), r.Code, want, r.Stdout, r.Stderr)
	}
	return r
}

// Failed asserts the command refused, without pinning which code.
//
// For a refusal whose code is the contract, use ExitCode. This is for the ones
// where the message is the assertion and the code is asserted elsewhere.
func (r Result) Failed() Result {
	r.t.Helper()
	if r.Code == 0 {
		r.t.Fatalf("`morzer %s` succeeded, and was supposed to refuse\n--- stdout ---\n%s--- stderr ---\n%s",
			strings.Join(r.args, " "), r.Stdout, r.Stderr)
	}
	return r
}

// OutputContains asserts a string appears on either stream.
//
// Where the split matters -- a result on stdout, a diagnostic on stderr --
// StdoutContains and StderrContains are the assertions to use. This is for
// text whose stream is not part of the contract.
func (r Result) OutputContains(want ...string) Result {
	r.t.Helper()
	for _, w := range want {
		if !strings.Contains(r.Stdout, w) && !strings.Contains(r.Stderr, w) {
			r.t.Errorf("neither stream contains %q:\n--- stdout ---\n%s--- stderr ---\n%s",
				w, r.Stdout, r.Stderr)
		}
	}
	return r
}

// FieldLen reads a dotted path and asserts it is a list of the given length.
func (r Result) FieldLen(path string, want int) Result {
	r.t.Helper()
	list, ok := r.Field(path).([]any)
	if !ok {
		r.t.Fatalf("%s is not a list:\n%s", path, r.Stdout)
	}
	if len(list) != want {
		r.t.Errorf("%s has %d entries, want %d:\n%s", path, len(list), want, r.Stdout)
	}
	return r
}

// StdoutContains asserts on the result stream.
func (r Result) StdoutContains(want ...string) Result {
	r.t.Helper()
	for _, w := range want {
		if !strings.Contains(r.Stdout, w) {
			r.t.Errorf("stdout does not contain %q:\n%s", w, r.Stdout)
		}
	}
	return r
}

// StderrContains asserts on the diagnostic stream, which is where errors,
// hints and narration go.
func (r Result) StderrContains(want ...string) Result {
	r.t.Helper()
	for _, w := range want {
		if !strings.Contains(r.Stderr, w) {
			r.t.Errorf("stderr does not contain %q:\n%s", w, r.Stderr)
		}
	}
	return r
}

// NoOutputContains asserts a string appears on neither stream.
//
// The assertion secrets need: "it is not in stdout" is not the claim, "it is
// nowhere" is.
func (r Result) NoOutputContains(unwanted ...string) Result {
	r.t.Helper()
	for _, u := range unwanted {
		if strings.Contains(r.Stdout, u) || strings.Contains(r.Stderr, u) {
			r.t.Errorf("%q appears in the output, and must not appear anywhere", u)
		}
	}
	return r
}

// JSON decodes the envelope from stdout.
//
// Fails when stdout is not exactly one JSON object: that is the contract
// `--json` publishes, and a stray narration line on stdout breaks every
// consumer of it.
func (r Result) JSON() map[string]any {
	r.t.Helper()

	var envelope map[string]any
	dec := json.NewDecoder(strings.NewReader(r.Stdout))
	if err := dec.Decode(&envelope); err != nil {
		r.t.Fatalf("stdout is not a JSON object: %v\n%s", err, r.Stdout)
	}
	if dec.More() {
		r.t.Fatalf("stdout carries more than one JSON value:\n%s", r.Stdout)
	}
	return envelope
}

// Field reads a dotted path out of the JSON envelope.
func (r Result) Field(path string) any {
	r.t.Helper()

	var cur any = r.JSON()
	for _, part := range strings.Split(path, ".") {
		obj, ok := cur.(map[string]any)
		if !ok {
			r.t.Fatalf("%s: %q is not an object", path, part)
		}
		cur, ok = obj[part]
		if !ok {
			r.t.Fatalf("the envelope has no %s:\n%s", path, r.Stdout)
		}
	}
	return cur
}

// FieldEquals asserts one envelope field.
func (r Result) FieldEquals(path string, want any) Result {
	r.t.Helper()
	if got := r.Field(path); got != want {
		r.t.Errorf("%s = %v (%T), want %v (%T)", path, got, got, want, want)
	}
	return r
}

// Path builds a path inside the installation root.
func (r *Runner) Path(parts ...string) string {
	return filepath.Join(append([]string{r.Root}, parts...)...)
}

// bundlePath locates the example bundle from the test's own directory.
func bundlePath(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("cannot resolve the working directory: %v", err)
	}
	return filepath.Join(wd, "..", "..", "testdata", "bundle")
}

// hermeticBundle stages the example bundle with its named volume removed.
//
// These tests drive the real binary, so `morzer backup` runs the real volume
// capture -- which reads a volume through a helper container and refuses when
// that image is not on the machine. The example bundle declares a named volume
// on purpose, so the acceptance run and the container suites exercise capture
// against real Docker; this suite is neither. Left as-is, every backup test
// here fails on any machine that has not pulled busybox, which is exactly what
// a CI runner that only runs unit tests looks like.
//
// So the volume is stripped rather than the assertion weakened: what these
// tests are about is the CLI -- exit codes, confirmations, the JSON envelope,
// where a backup goes -- and none of that is about volumes.
func hermeticBundle(t *testing.T) string {
	t.Helper()

	dir := filepath.Join(t.TempDir(), "bundle")
	copyDir(t, bundlePath(t), dir)

	// The comment goes with the declaration it describes. A fixture that
	// still explains the named volume it no longer has is a fixture that
	// lies to whoever reads it next -- most likely someone reading it
	// *because* a test failed against it, and now looking for capture
	// behaviour that was deliberately removed.
	strip(t, filepath.Join(dir, "compose", "compose.yaml"),
		"      # A named volume, so the bundle exercises the one kind of storage the\n"+
			"      # manager backs up for itself. The two mounts above are bind mounts,\n"+
			"      # which are reported by `doctor` and never captured -- a product whose\n"+
			"      # data lives on one is a product with data in no backup.\n"+
			"      - uploads:/var/lib/demo/uploads\n")
	strip(t, filepath.Join(dir, "compose", "compose.yaml"),
		"volumes:\n  uploads: {}\n\n")
	strip(t, filepath.Join(dir, "manifest.yaml"),
		"# What the manager may do about this project's named volumes, which it captures\n"+
			"# itself rather than through the backup hook above.\n"+
			"#\n"+
			"# A volume left out of this map is captured *cold*: the services that mount it\n"+
			"# are stopped for the copy. That is correct for every volume and slow for some,\n"+
			"# so a vendor declares only where they want something else.\n"+
			"#\n"+
			"# `consistency: hot` is a claim that a copy taken while the product is running\n"+
			"# is a usable one -- true for files written once and never modified, false for\n"+
			"# anything with a write-ahead log. It is the vendor's claim, not the manager's\n"+
			"# guess, and every backup manifest records which one applied.\n"+
			"backup:\n  volumes:\n    uploads: {consistency: hot}\n\n")

	return dir
}

// strip rewrites one exact fragment out of a fixture file, and fails when the
// fragment is not there.
//
// Loudly, because the alternative is a fixture that quietly stops stripping
// anything the day the bundle is reformatted -- and the failure it would then
// produce is six unrelated backup tests going red on a machine without
// busybox, which is a long way from the cause.
func strip(t *testing.T, path, fragment string) {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), fragment) {
		t.Fatalf("%s no longer contains the fragment this fixture removes, so it "+
			"is stripping nothing:\n%q\n\nfile:\n%s", path, fragment, data)
	}
	rewritten := strings.Replace(string(data), fragment, "", 1)
	if err := os.WriteFile(path, []byte(rewritten), 0o644); err != nil {
		t.Fatal(err)
	}
}

// requireSOPS skips when the real secret store cannot run.
//
// Same rule the contract suites follow, so `just contract-strict` catches a CI
// machine that quietly stopped installing sops rather than letting the suite
// evaporate.
func requireSOPS(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("sops"); err != nil {
		t.Skip("sops is not installed; skipping the CLI command tests")
	}
}
