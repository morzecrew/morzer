package ports

// Every line here is a leak the checker must see.

type ComposeStack struct {
	QuadletUnits []string
	DockerHost   string
}

func PodmanSocket() string { return "" }

func Decide(kind string) string {
	// Decision 7: the port may grow methods, it may not grow a switch kind.
	if kind == "compose" {
		return "a"
	}
	switch kind {
	case "quadlet":
		return "b"
	}
	composeFiles := []string{}
	_ = composeFiles
	return ""
}
