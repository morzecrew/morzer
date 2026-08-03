package preflight

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestIsEphemeralFilesystem(t *testing.T) {
	// The two that keep their contents in RAM, which is what makes
	// overwriting a decrypted secret there actually destroy it.
	for _, fs := range []string{"tmpfs", "ramfs"} {
		assert.True(t, IsEphemeralFilesystem(fs), fs)
	}

	// Everything else, including the ones that look ephemeral and are not.
	// overlayfs is backed by whatever is under it; a journalling filesystem
	// can keep an old copy in the journal.
	for _, fs := range []string{"ext4", "xfs", "btrfs", "zfs", "overlay", "nfs", ""} {
		assert.False(t, IsEphemeralFilesystem(fs), fs)
	}
}

func TestFilesystemTypeResolvesTheLongestMount(t *testing.T) {
	// The root filesystem is mounted on every machine, so something has to
	// come back for a path under it.
	assert.NotEmpty(t, FilesystemType("/"), "/ must resolve to some filesystem")

	// A path that does not exist still resolves: the question is which
	// mount it *would* be under, which is what a check about where a
	// directory will be created needs.
	assert.NotEmpty(t, FilesystemType(filepath.Join(t.TempDir(), "not-created-yet")))
}

func TestUnescapeMountDecodesProcEscapes(t *testing.T) {
	// /proc/mounts octal-escapes the characters that would otherwise break
	// its own field separation. A mount point with a space in it is
	// unusual and entirely legal.
	assert.Equal(t, "/mnt/my drive", unescapeMount(`/mnt/my\040drive`))
	assert.Equal(t, "/plain/path", unescapeMount("/plain/path"))
}

func TestUnderMountIsNotAStringPrefix(t *testing.T) {
	assert.True(t, underMount("/run/demo/secrets", "/run"))
	assert.True(t, underMount("/run", "/run"))
	assert.True(t, underMount("/anything", "/"))

	// The trap a bare prefix check falls into: /runtime is not under /run.
	assert.False(t, underMount("/runtime/x", "/run"))
}
