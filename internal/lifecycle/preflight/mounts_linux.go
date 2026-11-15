package preflight

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

// FilesystemType reports the filesystem a path sits on, or "" when it cannot
// be determined.
//
// Read from /proc/mounts rather than statfs, because the answer wanted is the
// *name* -- "ext4", "tmpfs" -- and a caller has to be told it in a message an
// operator can act on. The longest mount point that prefixes the path wins,
// which is how the kernel resolves it too.
//
// An empty result means "cannot tell", never "not tmpfs". A check that reported
// a missing /proc as a security finding would be crying wolf on every container
// that mounts it elsewhere.
func FilesystemType(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		return ""
	}

	f, err := os.Open("/proc/mounts")
	if err != nil {
		return ""
	}
	defer func() { _ = f.Close() }()

	var bestPoint, bestType string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		// device mountpoint fstype options dump pass
		fields := strings.Fields(scanner.Text())
		if len(fields) < 3 {
			continue
		}
		point := unescapeMount(fields[1])

		if !underMount(abs, point) {
			continue
		}
		if len(point) >= len(bestPoint) {
			bestPoint, bestType = point, fields[2]
		}
	}
	return bestType
}
