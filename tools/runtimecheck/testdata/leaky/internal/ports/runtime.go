package ports

// Every line here is a leak the checker must see.

type ComposeStack struct {
	QuadletUnits []string
	DockerHost   string
}

func PodmanSocket() string { return "" }

// A neutral name holding a runtime's name, and a comparison against it. Rule 1
// sees nothing (the name is neutral), rule 2 sees nothing (the comparison is
// against an identifier). Only the literal rule catches this.
const defaultRuntime = "compose"

// The same value spelled so that trimming quotes leaves the escape intact.
const escaped = "\x64ocker"

// A concatenation whose operand *is* a runtime name: a literal finding,
// and -- because concatenation is not comparison -- never a branch one.
const prefix = "quadlet" + "-unit"

func Decide(kind string) string {
	// Decision 7: the port may grow methods, it may not grow a switch kind.
	if kind == "compose" {
		return "a"
	}
	// Reversed and negated are the same decision.
	if "podman" == kind {
		return "r"
	}
	if kind != "quadlet" {
		return "n"
	}
	if kind == defaultRuntime {
		return "i"
	}
	switch kind {
	case "quadlet":
		return "b"
	}
	composeFiles := []string{}
	_ = composeFiles
	return ""
}
