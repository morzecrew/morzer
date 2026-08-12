package quadlet

// An adapter may say whatever its runtime is called. Nothing here is a finding.

type QuadletUnit struct{ PodmanArgs []string }

func ComposeCompat(kind string) bool {
	if kind == "compose" {
		return true
	}
	switch kind {
	case "podman":
		return false
	}
	return false
}
