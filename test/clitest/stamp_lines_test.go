package clitest

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	"github.com/morzecrew/morzer/internal/domain"
)

// A launchd job's plist sets MORZER_STAMP_LINES, and every line of its log then
// names its time: a log record in its own field, so a JSON record stays one
// parseable line, and every other line on stdout and stderr as a prefix
// (logs/macos-host.md). Unset, nothing changes.
func TestALineNamesItsTimeWhereNothingElseDoes(t *testing.T) {
	stamp := regexp.MustCompile(`^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\dZ `)
	// init runs steps, so --verbose logs a record for each.
	initialise := func(r *Runner) Result {
		return r.Run("--log-format", "json", "--verbose", "init",
			"--release", r.Bundle, "--profile", "embedded", "--domain", "demo.example",
			"--no-recovery-recipient", "--install-units=false").ExitCode(0)
	}

	plain := initialise(New(t))
	for _, line := range strings.Split(plain.Stdout+plain.Stderr, "\n") {
		if stamp.MatchString(line) {
			t.Fatalf("with %s unset, a line was stamped: %q", domain.StampLinesEnv, line)
		}
	}

	t.Setenv(domain.StampLinesEnv, "1")
	r := New(t)
	ran := initialise(r)
	failed := r.Run("--log-format", "json", "config", "get", "no_such_parameter")
	if failed.Code == 0 {
		t.Fatal("config get of an undeclared parameter succeeded, so no error line was written")
	}
	// A result goes to stdout, which init's progress does not reach.
	listed := r.Run("config", "list").ExitCode(0)
	if !stamp.MatchString(listed.Stdout) {
		t.Errorf("a result on stdout names no time:\n%s", listed.Stdout)
	}

	var stamped, records int
	for _, line := range strings.Split(ran.Stdout+ran.Stderr+failed.Stderr+listed.Stdout, "\n") {
		switch {
		case line == "":
		case strings.HasPrefix(line, "{"):
			var record map[string]any
			if err := json.Unmarshal([]byte(line), &record); err != nil {
				t.Errorf("a log record is no longer one JSON line: %q: %v", line, err)
			} else if _, ok := record["time"]; !ok {
				t.Errorf("a log record carries no time: %q", line)
			}
			records++
		case stamp.MatchString(line):
			stamped++
		default:
			t.Errorf("a line names no time: %q", line)
		}
	}
	if stamped == 0 || records == 0 {
		t.Errorf("%d stamped line(s) and %d record(s): the test reached neither kind", stamped, records)
	}
}
