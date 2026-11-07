package release_test

import (
	"strings"
	"testing"

	"github.com/morzecrew/morzer/internal/release"
)

// restartableManifest declares `restartable` on one operation, with a floor.
func restartableManifest(operation, value, floor string) string {
	return `api_version: selfhost/v1alpha1
kind: application-release
metadata: {name: demo, version: 1.0.0}
runtimes: {compose: {options: {project: demo}, files: [compose/compose.yaml]}}
images:
  app: registry.example/demo/app@sha256:` + strings.Repeat("a", 64) + `
operations:
  ` + operation + `: {kind: hook, command: [hooks/` + operation + `], restartable: ` + value + `}
compatibility:
  min_manager_version: "` + floor + `"
`
}

// A manager before 0.14.0 decodes strictly and refuses the key, whatever it
// holds, so it is the key's presence that needs the floor and is refused off
// `restore`, not its value (RFC 0051 decision 12).
func TestRestartableIsJudgedByItsPresenceNotItsValue(t *testing.T) {
	for name, tc := range map[string]struct{ operation, value, floor, refusal string }{
		"false below the floor": {"restore", "false", "0.13.0", "compatibility.min_manager_version"},
		"false on another one":  {"migrate", "false", "0.14.0", "operations.migrate.restartable"},
		"true on another one":   {"backup", "true", "0.14.0", "operations.backup.restartable"},
		"true below the floor":  {"restore", "true", "0.13.0", "compatibility.min_manager_version"},
		"false at the floor":    {"restore", "false", "0.14.0", ""},
		"true at the floor":     {"restore", "true", "0.14.0", ""},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := release.ParseManifest([]byte(restartableManifest(tc.operation, tc.value, tc.floor)), "manifest.yaml")
			switch {
			case tc.refusal == "" && err != nil:
				t.Fatalf("refused: %v", err)
			case tc.refusal != "" && (err == nil || !strings.Contains(err.Error(), tc.refusal)):
				t.Fatalf("want a refusal naming %s, got %v", tc.refusal, err)
			}
		})
	}
}
