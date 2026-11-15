package dirservice

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/test/fakes"
)

// The listings dscl prints, read for the accounts that can write through a
// group: its members and the accounts it is the primary group of, root aside.
func TestTheGroupsWritersAreRead(t *testing.T) {
	groups := "_amavisd                 83\ndaemon                   1\nstaff                    20\nwheel                    0\n"
	name, ok := nameFor(groups, 1)
	assert.True(t, ok)
	assert.Equal(t, "daemon", name)
	_, ok = nameFor(groups, 4242)
	assert.False(t, ok)

	assert.Equal(t, []string{"root"}, values("GroupMembership: root\nNestedGroups: 2222-BBBB\n", "GroupMembership"))
	assert.Equal(t, []string{"root", "alice"}, values("GroupMembership:\n root\n alice\nRealName: System\n", "GroupMembership"))
	assert.Nil(t, values("No such key: GroupMembership\n", "GroupMembership"))

	users := "_www                     70\ndaemon                   1\nroot                     0\nalice                    20\n"
	assert.Equal(t, []string{"daemon"}, namesWith(users, 1))

	// A stock Mac's daemon account is macOS's own, and a person's is not.
	uids := "_www 70\ndaemon 1\nroot 0\nalice 501\n"
	assert.Empty(t, people(namesWith(users, 1), uids))
	assert.Equal(t, []string{"alice"}, people([]string{"alice", "daemon"}, uids))

	assert.Empty(t, others(append(values("GroupMembership: root\n", "GroupMembership"), namesWith(users, 0)...)))
	assert.Equal(t, []string{"alice", "daemon"},
		others([]string{"root", "daemon", "alice", "daemon"}))
}

// A group nested in the one that owns the directory gives its members that
// group's access, so it is a writer too, named; a stock Mac's daemon group
// nests none (logs/macos-host.md).
func TestAGroupNestedInTheOwningGroupWritesToo(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.Chmod(dir, 0o775))
	info, err := os.Stat(dir)
	require.NoError(t, err)
	gid, err := groupOf(info)
	require.NoError(t, err)
	listings := func(read string) *fakes.Scripted {
		return fakes.NewScripted().
			OnOutput("-list /Groups PrimaryGroupID", fmt.Sprintf("staff 20\ndaemon %d\n", gid)).
			OnOutput("-read /Groups/daemon", read).
			OnOutput("-list /Groups GeneratedUID", "daemon 1111-AAAA\nadmins 2222-BBBB\n").
			OnOutput("-list /Users PrimaryGroupID", "root 0\n").
			OnOutput("-list /Users UniqueID", "root 0\n")
	}

	writers, err := Writers(context.Background(), listings("GroupMembership: root\nNo such key: NestedGroups\n"), dir)
	require.NoError(t, err)
	assert.Empty(t, writers)

	writers, err = Writers(context.Background(), listings("GroupMembership: root\nNestedGroups: 2222-BBBB 3333-CCCC\n"), dir)
	require.NoError(t, err)
	assert.Equal(t, []string{"group 3333-CCCC", "group admins"}, writers)
}
