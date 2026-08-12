package ops

// Nothing in this tree may produce a finding.

// Prose that mentions runtimes. Go spells string concatenation with the same
// node type as comparison, so without the operator guard the branch rule reads
// every one of these as a decision about which runtime is running.
const help = "Deploys a self-hosted product with Docker Compose on one Linux machine.\n" +
	"A Docker daemon that is down costs you the service table and nothing else.\n"

func Hint(runtime string) string {
	// A comparison, but not against a runtime name.
	if runtime == "" {
		return help
	}
	// A comparison whose literal *contains* a runtime name without being
	// one. Only the whole-value match keeps this from being a finding.
	if runtime == "check docker logs" {
		return ""
	}
	return "check service logs with `docker compose logs`"
}
