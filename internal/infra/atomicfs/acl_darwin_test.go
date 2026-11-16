package atomicfs

import (
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// What a Mac's kernel returns for an ACL chmod(1) wrote, which only the macOS
// lane can ask: the offsets, the bits and the GUIDs acl.go reads (RFC 0055
// decision 23). The lane does not run as root, so these ask aclOf directly;
// the walk's own check stops at an entry this process owns first.

func chmodACL(t *testing.T, args ...string) {
	t.Helper()
	out, err := exec.Command("/bin/chmod", args...).CombinedOutput()
	require.NoError(t, err, "chmod %v: %s", args, out)
}

func TestAMacsACLIsReadAsItsKernelKeepsIt(t *testing.T) {
	me, err := user.Current()
	require.NoError(t, err)

	for name, tc := range map[string]struct {
		entry string
		dir   bool
		want  string // "" when nothing is granted
	}{
		"none at all":                        {},
		"every user may write it":            {entry: "everyone allow write", want: "grants every user write"},
		"every user is denied it":            {entry: "everyone deny write"},
		"root may write and delete it":       {entry: "root allow write,delete"},
		"every user may read its ACL":        {entry: "everyone allow readsecurity"},
		"this user may change its owner":     {entry: me.Username + " allow chown", want: "chown"},
		"every user may add and remove":      {entry: "everyone allow add_file,delete_child", dir: true, want: "grants every user add_file, delete_child"},
		"what it holds may make a directory": {entry: "everyone allow add_subdirectory,only_inherit", dir: true, want: "grants every user add_subdirectory"},
	} {
		t.Run(name, func(t *testing.T) {
			entry := filepath.Join(t.TempDir(), "entry")
			if tc.dir {
				require.NoError(t, os.Mkdir(entry, 0o755))
			} else {
				require.NoError(t, os.WriteFile(entry, nil, 0o644))
			}
			if tc.entry != "" {
				chmodACL(t, "+a", tc.entry, entry)
			}

			who, err := aclOf(entry, tc.dir)

			require.NoError(t, err)
			if tc.want == "" {
				assert.Empty(t, who)
			} else {
				assert.Contains(t, who, tc.want)
				assert.Contains(t, who, entry+" has an ACL")
			}
		})
	}
}

// A link's own ACL, read without following it to the file it names.
func TestALinksOwnACLIsRead(t *testing.T) {
	dir := t.TempDir()
	file, link := filepath.Join(dir, "file"), filepath.Join(dir, "link")
	require.NoError(t, os.WriteFile(file, nil, 0o644))
	require.NoError(t, os.Symlink(file, link))
	if out, err := exec.Command("/bin/chmod", "-h", "+a", "everyone allow delete", link).CombinedOutput(); err != nil {
		t.Skipf("this Mac keeps no ACL on a link: %s", out)
	}

	who, err := aclOf(link, false)
	require.NoError(t, err)
	assert.Contains(t, who, "grants every user delete")

	who, err = aclOf(file, false)
	require.NoError(t, err)
	assert.Empty(t, who, "the link's ACL was read as the file's")
}
