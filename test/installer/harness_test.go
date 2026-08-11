// Package installer drives `install.sh` the way an operator does.
//
// The script is shell, so it is tested by running it — against a fixture
// release served over TLS from this process, into a fake home and a fake
// prefix. Nothing here touches the machine running the tests: every path the
// script writes to is under t.TempDir(), and the assertion that it wrote
// nothing else is one of the tests.
//
// Two things are deliberately doubled rather than real:
//
//   - `minisign`, stubbed on PATH. The signatures this project publishes are
//     made in the release pipeline by a key that never leaves it, so there is
//     no key material here to sign a fixture with. What the stub exercises is
//     the script's four branches — verified, refused, absent, absent-and-
//     required — which is where the logic lives. Real minisign against a real
//     release is what the nightly job covers.
//   - The binary in the archive, which is a shell script that answers
//     `version` and records its argv. `completion install` is the binary's
//     job (RFC 0019 §5.8) and the point of the delegation is that the script
//     does not know where completions go, so a real binary here would test
//     the wrong half.
package installer

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/morzecrew/morzer/internal/infra/atomicfs"
)

const (
	fixtureVersion = "1.4.0"
	fixtureTag     = "v" + fixtureVersion
)

// repoRoot is the directory holding install.sh.
func repoRoot(t *testing.T) string {
	t.Helper()

	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Dir(filepath.Dir(wd)) // test/installer -> repo root
	if _, err := os.Stat(filepath.Join(root, "install.sh")); err != nil {
		t.Fatalf("install.sh is not where this test expects it: %v", err)
	}
	return root
}

// release is a fixture release: an archive, a checksum file, and a server.
type release struct {
	dir     string // what the server serves
	archive string // the archive's file name
	digest  string // its sha256, as SHA256SUMS carries it
	url     string // the server's base
	ca      string // a PEM file holding the server's certificate
	hits    *hitLog
}

// hitLog records what was fetched, so a test can assert that nothing was —
// which is the only way to prove a refusal happened before any download.
type hitLog struct {
	paths []string
}

func (h *hitLog) add(p string) { h.paths = append(h.paths, p) }

// stubBinary is the "morzer" the fixture archive carries.
//
// It answers `version` with the version it was built for, and appends its
// whole argv to a log the test reads. Everything else exits 0.
const stubBinary = `#!/bin/sh
printf '%s\n' "$*" >>"__ARGV_LOG__"
case "$1" in
version) printf 'morzer __VERSION__\n' ;;
completion) exit __COMPLETION_EXIT__ ;;
esac
exit 0
`

type fixtureOptions struct {
	// version the stub reports, when it must differ from the version the
	// archive is named for.
	reportsVersion string
	// completionExit is what `morzer completion install` exits with.
	completionExit int
	// corruptArchive serves bytes whose checksum will not match.
	corruptArchive bool
	// omitSumsLine serves a SHA256SUMS with no line for this archive —
	// the `--ignore-missing` trap, which used to report OK.
	omitSumsLine bool
	// arch names the architecture the archive is built for.
	arch string
}

// newRelease builds the fixture and starts serving it.
func newRelease(t *testing.T, opts fixtureOptions) *release {
	t.Helper()

	if opts.arch == "" {
		opts.arch = hostArch(t)
	}
	if opts.reportsVersion == "" {
		opts.reportsVersion = fixtureVersion
	}

	dir := t.TempDir()
	argvLog := filepath.Join(dir, "argv.log")

	stage := t.TempDir()
	body := strings.NewReplacer(
		"__ARGV_LOG__", argvLog,
		"__VERSION__", opts.reportsVersion,
		"__COMPLETION_EXIT__", fmt.Sprint(opts.completionExit),
	).Replace(stubBinary)
	if err := os.WriteFile(filepath.Join(stage, "morzer"), []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}

	archive := fmt.Sprintf("morzer_%s_linux_%s.tar.zst", fixtureVersion, opts.arch)
	archivePath := filepath.Join(dir, archive)
	if err := atomicfs.WriteTarZst(archivePath, stage, []string{"morzer"}, time.Unix(0, 0)); err != nil {
		t.Fatal(err)
	}

	digest := sha256File(t, archivePath)

	if opts.corruptArchive {
		// The digest stays what SHA256SUMS will claim; the bytes change.
		// A corrupted download is exactly this: a file whose checksum
		// no longer describes it.
		if err := os.WriteFile(archivePath, []byte("this is not an archive\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	// Two lines, because the real file covers both architectures — which is
	// what makes `sha256sum -c --ignore-missing` tempting and wrong.
	sums := fmt.Sprintf("%s  %s\n%s  morzer_%s_linux_%s.tar.zst\n",
		digest, archive,
		strings.Repeat("0", 64), fixtureVersion, otherArch(opts.arch))
	if opts.omitSumsLine {
		sums = fmt.Sprintf("%s  morzer_%s_linux_%s.tar.zst\n",
			strings.Repeat("0", 64), fixtureVersion, otherArch(opts.arch))
	}
	if err := os.WriteFile(filepath.Join(dir, "SHA256SUMS"), []byte(sums), 0o600); err != nil {
		t.Fatal(err)
	}
	// Content is irrelevant: minisign is stubbed. Its presence is not —
	// the script's "no signature published" branch is a different one.
	if err := os.WriteFile(filepath.Join(dir, "SHA256SUMS.minisig"),
		[]byte("untrusted comment: fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	hits := &hitLog{}
	mux := http.NewServeMux()
	mux.HandleFunc("/releases/download/"+fixtureTag+"/",
		func(w http.ResponseWriter, r *http.Request) {
			hits.add(r.URL.Path)
			name := filepath.Base(r.URL.Path)
			http.ServeFile(w, r, filepath.Join(dir, name))
		})
	mux.HandleFunc("/repos/morzecrew/morzer/releases/latest",
		func(w http.ResponseWriter, r *http.Request) {
			hits.add(r.URL.Path)
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"tag_name": %q, "name": "fixture"}`, fixtureTag)
		})

	srv := httptest.NewTLSServer(mux)
	t.Cleanup(srv.Close)

	ca := filepath.Join(t.TempDir(), "ca.pem")
	certPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: srv.Certificate().Raw,
	})
	if err := os.WriteFile(ca, certPEM, 0o600); err != nil {
		t.Fatal(err)
	}

	return &release{
		dir: dir, archive: archive, digest: digest,
		url: srv.URL, ca: ca, hits: hits,
	}
}

// argvLog is what the stub binary recorded, one invocation per line.
func (r *release) argvLog(t *testing.T) string {
	t.Helper()

	body, err := os.ReadFile(filepath.Join(r.dir, "argv.log"))
	if os.IsNotExist(err) {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

// script copies install.sh with its two base URLs pointed at the fixture.
//
// A copy rather than an environment override: a shipped script that reads the
// download base out of the environment is a script whose verification can be
// redirected by anything that can set a variable. The substitution is asserted
// to have happened, because a rename upstream would otherwise leave these tests
// quietly talking to the real github.com — passing on a machine with network
// and failing on one without, which is the worst way for a harness to break.
func script(t *testing.T, rel *release) string {
	t.Helper()

	body, err := os.ReadFile(filepath.Join(repoRoot(t), "install.sh"))
	if err != nil {
		t.Fatal(err)
	}

	const (
		realAPI      = `API="https://api.github.com/repos/${REPO}"`
		realDownload = `DOWNLOAD="https://github.com/${REPO}/releases/download"`
	)
	text := string(body)
	for _, line := range []string{realAPI, realDownload} {
		if strings.Count(text, line) != 1 {
			t.Fatalf("install.sh no longer contains exactly one\n  %s\n"+
				"so this harness cannot point it at the fixture", line)
		}
	}
	text = strings.Replace(text, realAPI,
		fmt.Sprintf(`API="%s/repos/${REPO}"`, rel.url), 1)
	text = strings.Replace(text, realDownload,
		fmt.Sprintf(`DOWNLOAD="%s/releases/download"`, rel.url), 1)

	path := filepath.Join(t.TempDir(), "install.sh")
	if err := os.WriteFile(path, []byte(text), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

// run drives the script and returns everything a caller can observe.
type result struct {
	stdout string
	stderr string
	code   int
}

// env is the process environment a run gets: everything the script reads is
// named here, so a test never depends on the developer's own.
type env struct {
	home  string
	shell string
	stubs string // prepended to PATH, to shadow a real tool
	// pathOnly replaces PATH entirely. Prepending cannot express "this
	// machine does not have minisign", because the real one further down
	// PATH is still found; only a PATH without it says that.
	pathOnly string
	extra    []string
	release  *release
}

func run(t *testing.T, scriptPath string, e env, args ...string) result {
	t.Helper()

	path := os.Getenv("PATH")
	if e.pathOnly != "" {
		path = e.pathOnly
	}
	if e.stubs != "" {
		path = e.stubs + string(os.PathListSeparator) + path
	}

	cmd := exec.Command("sh", append([]string{scriptPath}, args...)...)
	cmd.Env = append([]string{
		"HOME=" + e.home,
		"PATH=" + path,
		"SHELL=" + e.shell,
		"CURL_CA_BUNDLE=" + e.release.ca,
		// wget reads this one; both downloaders are covered.
		"SSL_CERT_FILE=" + e.release.ca,
	}, e.extra...)

	var out, errOut strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	err := cmd.Run()

	code := 0
	var exitErr *exec.ExitError
	if err != nil {
		if !asExitError(err, &exitErr) {
			t.Fatalf("cannot run the script: %v", err)
		}
		code = exitErr.ExitCode()
	}
	return result{stdout: out.String(), stderr: errOut.String(), code: code}
}

func asExitError(err error, target **exec.ExitError) bool {
	e, ok := err.(*exec.ExitError)
	if ok {
		*target = e
	}
	return ok
}

// all is stdout and stderr together, for assertions about a message without
// caring which stream carried it. Where the stream itself is the promise, the
// test names the stream.
func (r result) all() string { return r.stdout + r.stderr }

func (r result) requireFailed(t *testing.T, because string) result {
	t.Helper()
	if r.code == 0 {
		t.Fatalf("expected a refusal (%s), got exit 0:\n%s", because, r.all())
	}
	return r
}

func (r result) requireOK(t *testing.T) result {
	t.Helper()
	if r.code != 0 {
		t.Fatalf("exit %d:\n%s", r.code, r.all())
	}
	return r
}

func (r result) requireSays(t *testing.T, wants ...string) result {
	t.Helper()
	for _, want := range wants {
		if !strings.Contains(r.all(), want) {
			t.Errorf("the output does not mention %q:\n%s", want, r.all())
		}
	}
	return r
}

// newHome is a home directory with nothing in it, which is what a freshly
// provisioned machine has.
func newHome(t *testing.T) string {
	t.Helper()
	return t.TempDir()
}

// stubDir writes executables that shadow the real ones for one run.
func stubDir(t *testing.T, scripts map[string]string) string {
	t.Helper()

	dir := t.TempDir()
	for name, body := range scripts {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// installScriptTools is every external command install.sh runs, minus minisign
// — the one it probes for and works without.
//
// It is a hand-written list on purpose. A run under a PATH holding exactly
// these proves the dependency set the documentation claims, because a command
// the script grew a dependency on and nobody wrote down here fails the run
// that uses this. zstd is on the list rather than treated as optional: GNU
// tar's --zstd runs the zstd binary as a filter, so a machine without it
// cannot extract the archive whatever its tar accepts.
var installScriptTools = []string{
	"awk", "basename", "cat", "chmod", "cp", "curl", "dirname", "grep",
	"head", "mkdir", "mktemp", "mv", "readlink", "rm", "sed", "sha256sum",
	"tar", "uname", "zstd",
}

// minimalPATH is a directory holding links to exactly the tools above, so a
// test can say "this machine does not have minisign" and mean it. A stub that
// exits non-zero would say something else entirely.
func minimalPATH(t *testing.T, extra ...string) string {
	t.Helper()

	dir := t.TempDir()
	for _, tool := range append(append([]string{}, installScriptTools...), extra...) {
		found, err := exec.LookPath(tool)
		if err != nil {
			t.Fatalf("this machine has no %s, which install.sh needs: %v", tool, err)
		}
		if err := os.Symlink(found, filepath.Join(dir, tool)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := exec.LookPath("minisign"); err == nil {
		// Only meaningful when the developer's machine has one to
		// exclude; on a machine without minisign the test is the same
		// test, and this says so rather than passing vacuously.
		t.Log("excluding the real minisign from PATH")
	}
	return dir
}

// minisignThat is a stub minisign whose verification succeeds or fails.
func minisignThat(t *testing.T, verifies bool) string {
	t.Helper()

	exit := 1
	if verifies {
		exit = 0
	}
	return stubDir(t, map[string]string{
		"minisign": fmt.Sprintf("#!/bin/sh\n"+
			"printf 'stub minisign: %%s\\n' \"$*\" >&2\nexit %d\n", exit),
	})
}

func sha256File(t *testing.T, path string) string {
	t.Helper()

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

// hostArch is the architecture name the script will resolve on this machine,
// so the fixture archive is one it will actually ask for.
func hostArch(t *testing.T) string {
	t.Helper()

	out, err := exec.Command("uname", "-m").Output()
	if err != nil {
		t.Fatal(err)
	}
	switch strings.TrimSpace(string(out)) {
	case "x86_64", "amd64":
		return "amd64"
	case "aarch64", "arm64":
		return "arm64"
	default:
		t.Fatalf("these tests run on amd64 and arm64; this is %q", out)
		return ""
	}
}

func otherArch(arch string) string {
	if arch == "amd64" {
		return "arm64"
	}
	return "amd64"
}
