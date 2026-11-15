package dirservice

import (
	"testing"

	"github.com/stretchr/testify/assert"
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

	assert.Equal(t, []string{"root"}, members("GroupMembership: root\n"))
	assert.Nil(t, members("No such key: GroupMembership\n"))

	users := "_www                     70\ndaemon                   1\nroot                     0\nalice                    20\n"
	assert.Equal(t, []string{"daemon"}, namesWith(users, 1))

	// A stock Mac's daemon account is macOS's own, and a person's is not.
	uids := "_www 70\ndaemon 1\nroot 0\nalice 501\n"
	assert.Empty(t, people(namesWith(users, 1), uids))
	assert.Equal(t, []string{"alice"}, people([]string{"alice", "daemon"}, uids))

	assert.Empty(t, others(append(members("GroupMembership: root\n"), namesWith(users, 0)...)))
	assert.Equal(t, []string{"alice", "daemon"},
		others([]string{"root", "daemon", "alice", "daemon"}))
}
