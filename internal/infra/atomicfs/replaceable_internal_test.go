package atomicfs

import (
	"io/fs"
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type statInfo struct {
	mode fs.FileMode
	uid  uint32
	gid  uint32
}

func (i statInfo) Name() string       { return "" }
func (i statInfo) Size() int64        { return 0 }
func (i statInfo) Mode() fs.FileMode  { return i.mode }
func (i statInfo) ModTime() time.Time { return time.Time{} }
func (i statInfo) IsDir() bool        { return i.mode.IsDir() }
func (i statInfo) Sys() any           { return &syscall.Stat_t{Uid: i.uid, Gid: i.gid} }

// Each directory above the binary counts as much as the binary: whoever can
// write one can rename the binary away and put another in its place.
func TestEveryDirectoryAboveTheBinaryCounts(t *testing.T) {
	root := statInfo{mode: fs.ModeDir | 0o755}
	for name, tc := range map[string]struct {
		tree map[string]statInfo
		want string
	}{
		"root's all the way up": {
			tree: map[string]statInfo{"/usr/local/bin/morzer": {mode: 0o755}, "/usr/local/bin": root, "/usr/local": root, "/usr": root},
		},
		"root's binary in a user's directory": {
			tree: map[string]statInfo{"/home/alice/.local/bin/morzer": {mode: 0o755}, "/home/alice/.local/bin": {mode: fs.ModeDir | 0o755, uid: 1000}},
			want: "/home/alice/.local/bin is owned by uid 1000, not root",
		},
		"a directory every user can write": {
			tree: map[string]statInfo{"/srv/bin/morzer": {mode: 0o755}, "/srv/bin": {mode: fs.ModeDir | 0o777}, "/srv": root},
			want: "/srv/bin is writable by every user",
		},
		"a sticky one": {
			tree: map[string]statInfo{"/srv/bin/morzer": {mode: 0o755}, "/srv/bin": {mode: fs.ModeDir | fs.ModeSticky | 0o777}, "/srv": root},
		},
		"a directory another group can write": {
			tree: map[string]statInfo{"/usr/local/bin/morzer": {mode: 0o755}, "/usr/local/bin": {mode: fs.ModeDir | 0o775, gid: 50}, "/usr/local": root, "/usr": root},
			want: "/usr/local/bin is writable by group 50",
		},
		"root's own group": {
			tree: map[string]statInfo{"/usr/local/bin/morzer": {mode: 0o755}, "/usr/local/bin": {mode: fs.ModeDir | 0o775}, "/usr/local": root, "/usr": root},
			want: "/usr/local/bin is writable by group 0",
		},
		"a binary every user can write": {
			tree: map[string]statInfo{"/usr/local/bin/morzer": {mode: 0o757}},
			want: "/usr/local/bin/morzer is writable by every user",
		},
	} {
		t.Run(name, func(t *testing.T) {
			lstat := func(p string) (fs.FileInfo, error) {
				if p == "/" {
					return root, nil
				}
				if info, ok := tc.tree[p]; ok {
					return info, nil
				}
				if p == "/home/alice/.local" || p == "/home/alice" || p == "/home" {
					return root, nil
				}
				return nil, os.ErrNotExist
			}
			var path string
			for p := range tc.tree {
				if len(p) > len(path) {
					path = p
				}
			}
			who, err := replaceableByNonRoot(path, lstat)
			require.NoError(t, err)
			assert.Equal(t, tc.want, who)
		})
	}
}
