package clitest_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/morzecrew/morzer/test/clitest"
)

// `--set-file name=path` reads a parameter's value from a file, or from
// standard input with `-`, on `config set` and `init` (RFC 0054 decision 9).

func valueFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "value")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestConfigSetReadsAValueFromAFile(t *testing.T) {
	r := clitest.NewInstalled(t)

	r.Run("config", "set", "--set-file", "log_level="+valueFile(t, "warn\n")).ExitCode(0)
	r.Run("config", "get", "log_level").ExitCode(0).StdoutContains("warn")

	r.RunWithInput("debug\n", "config", "set", "--set-file", "log_level=-").ExitCode(0)
	r.Run("config", "get", "log_level").ExitCode(0).StdoutContains("debug")
}

func TestConfigSetRefusesWhatAFileCannotCarry(t *testing.T) {
	r := clitest.NewInstalled(t)

	for name, tc := range map[string]struct {
		args    []string
		refusal string
	}{
		"a NUL byte, whatever the type": {
			[]string{"--set-file", "log_level=" + valueFile(t, "warn\x00")}, "holds a NUL byte"},
		"standard input twice": {
			[]string{"--set-file", "log_level=-", "--set-file", "http_port=-"}, "it can be read once"},
		"a setting": {
			[]string{"--set-file", "update.channel=" + valueFile(t, "x")}, "settings have no file form"},
		"a name given both ways": {
			[]string{"log_level=warn", "--set-file", "log_level=" + valueFile(t, "info")}, `parameter "log_level" is set twice`},
		"a file that is not there": {
			[]string{"--set-file", "log_level=/nonexistent/morzer-value"}, `cannot read the value of parameter "log_level"`},
	} {
		t.Run(name, func(t *testing.T) {
			r.Run(append([]string{"config", "set"}, tc.args...)...).Failed().StderrContains(tc.refusal)
		})
	}
	r.Run("config", "get", "log_level").ExitCode(0).StdoutContains("info")
}

func TestInitReadsAValueFromAFile(t *testing.T) {
	r := clitest.NewInstalled(t, "--set-file", "log_level="+valueFile(t, "error\n"))
	r.Run("config", "get", "log_level").ExitCode(0).StdoutContains("error")
}
