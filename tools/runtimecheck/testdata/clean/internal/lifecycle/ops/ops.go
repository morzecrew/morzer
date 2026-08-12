package ops

// Prose that mentions runtimes, and string concatenation, must not be findings:
// Go spells concatenation with the same node type as comparison.

const help = "Deploys a self-hosted product with Docker Compose on one Linux machine.\n" +
	"A Docker daemon that is down costs you the service table and nothing else.\n"

func Hint(runtime string) string {
	// Not a decision about which runtime is running: a decision about a word.
	if runtime == "" {
		return help
	}
	return "check service logs with `docker compose logs`"
}

// Two cases that isolate the branch rule's two guards. Without them the guards
// mask each other: a mutation removing either one alone changes nothing, and a
// precision test that only proves the conjunction proves neither part.

// Concatenation whose operand *is* a runtime name. Only the operator guard
// keeps this from being read as a comparison.
const prefix = "compose" + "-project"

// A comparison whose literal *contains* a runtime name without being one. Only
// the whole-value match keeps this from being read as a runtime branch.
func isHint(s string) bool { return s == "check docker logs" }
