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
	link string // where a link points
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
		path string // the deepest entry when empty
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
			tree: map[string]statInfo{"/usr/local/bin/morzer": {mode: 0o757}, "/usr/local/bin": root, "/usr/local": root, "/usr": root},
			want: "/usr/local/bin/morzer is writable by every user",
		},
		"a user's link in a sticky directory, which its owner may replace": {
			tree: map[string]statInfo{
				"/srv/drop/morzer": {mode: fs.ModeSymlink | 0o777, uid: 1000, link: "/usr/bin/env"}, "/srv/drop": {mode: fs.ModeDir | fs.ModeSticky | 0o777}, "/srv": root,
				"/usr/bin/env": {mode: 0o755}, "/usr/bin": root, "/usr": root,
			},
			path: "/srv/drop/morzer",
			want: "/srv/drop/morzer is owned by uid 1000, not root",
		},
		"a dot-dot after a link, which the kernel reads from the link's target": {
			tree: map[string]statInfo{
				"/opt/safe/link": {mode: fs.ModeSymlink | 0o777, link: "/home/alice/subdir"}, "/opt/safe": root, "/opt": root,
				"/opt/safe/morzer":   {mode: 0o755},
				"/home/alice/subdir": {mode: fs.ModeDir | 0o755, uid: 1000}, "/home/alice": {mode: fs.ModeDir | 0o755, uid: 1000}, "/home": root,
				"/home/alice/morzer": {mode: 0o755, uid: 1000},
			},
			path: "/opt/safe/link/../morzer",
			want: "/home/alice is owned by uid 1000, not root",
		},
		"a chain of links only root can repoint": {
			tree: map[string]statInfo{
				"/opt/safe/morzer": {mode: fs.ModeSymlink | 0o777, link: "/usr/local/bin/morzer"}, "/opt/safe": root, "/opt": root,
				"/usr/local/bin/morzer": {mode: fs.ModeSymlink | 0o777, link: "../lib/morzer/morzer"}, "/usr/local/bin": root, "/usr/local": root, "/usr": root,
				"/usr/local/lib/morzer/morzer": {mode: 0o755}, "/usr/local/lib/morzer": root, "/usr/local/lib": root,
			},
			path: "/opt/safe/morzer",
		},
		"a chain through a link a user can repoint": {
			tree: map[string]statInfo{
				"/opt/safe/morzer": {mode: fs.ModeSymlink | 0o777, link: "/home/alice/link"}, "/opt/safe": root, "/opt": root,
				"/home/alice/link": {mode: fs.ModeSymlink | 0o777, uid: 1000, link: "/usr/bin/env"}, "/home/alice": {mode: fs.ModeDir | 0o755, uid: 1000}, "/home": root,
				"/usr/bin/env": {mode: 0o755}, "/usr/bin": root, "/usr": root,
			},
			path: "/opt/safe/morzer",
			want: "/home/alice is owned by uid 1000, not root",
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
			readlink := func(p string) (string, error) { return tc.tree[p].link, nil }
			path := tc.path
			for p := range tc.tree {
				if path == "" || (tc.path == "" && len(p) > len(path)) {
					path = p
				}
			}
			who, err := replaceableByNonRoot(path, lstat, readlink)
			require.NoError(t, err)
			assert.Equal(t, tc.want, who)
		})
	}
}

// A relative path is made absolute without being cleaned: `..` after a link is
// the link's target's parent, which only the walk can know.
func TestAPathKeepsItsDotDotsForTheWalk(t *testing.T) {
	assert.Equal(t, "/opt/safe/link/../morzer", absolute("/opt/safe/link/../morzer", "/ignored"))
	assert.Equal(t, "/work/bin/../morzer", absolute("bin/../morzer", "/work"))
}
