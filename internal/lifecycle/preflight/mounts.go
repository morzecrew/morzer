package preflight

import (
	"path/filepath"
	"strings"
)

// underMount reports whether path is at or below a mount point.
func underMount(path, point string) bool {
	if point == "/" {
		return true
	}
	return path == point || strings.HasPrefix(path, point+string(filepath.Separator))
}

// unescapeMount decodes the octal escapes /proc/mounts uses for spaces, tabs,
// newlines and backslashes in a path.
func unescapeMount(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	replacer := strings.NewReplacer(
		`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`,
	)
	return replacer.Replace(s)
}

// IsEphemeralFilesystem reports whether a filesystem keeps its contents in
// memory, so that overwriting a file there actually destroys it.
func IsEphemeralFilesystem(fstype string) bool {
	switch fstype {
	case "tmpfs", "ramfs":
		return true
	default:
		return false
	}
}
