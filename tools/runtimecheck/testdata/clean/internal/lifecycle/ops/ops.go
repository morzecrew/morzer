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
