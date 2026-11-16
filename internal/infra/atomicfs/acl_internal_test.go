package atomicfs

import (
	"encoding/binary"
	"encoding/hex"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The parser over replies shaped as getattrlist writes them, so the rule runs
// on every platform; acl_darwin_test.go asks a real Mac.

type ace struct {
	guid          string // 32 hex digits
	flags, rights uint32
}

const (
	rootGUID     = "ffffeeeeddddccccbbbbaaaa00000000"
	everyoneGUID = "abcdefabcdefabcdefabcdef0000000c"
	writeRight   = 1 << 2
)

// filesec is a reply holding an ACL of these entries.
func filesec(aces ...ace) []byte {
	le := binary.LittleEndian
	sec := le.AppendUint32(nil, filesecMagic)
	sec = append(sec, make([]byte, 32)...) // the owner's and group's GUIDs
	sec = le.AppendUint32(sec, uint32(len(aces)))
	sec = le.AppendUint32(sec, 0)
	for _, a := range aces {
		guid, err := hex.DecodeString(a.guid)
		if err != nil || len(guid) != 16 {
			panic("a GUID is 32 hex digits")
		}
		sec = append(sec, guid...)
		sec = le.AppendUint32(sec, a.flags)
		sec = le.AppendUint32(sec, a.rights)
	}
	return attrReply(attrExtendedSecurity, sec)
}

// attrReply is a reply whose returned attributes are returned, carrying data.
func attrReply(returned uint32, data []byte) []byte {
	le := binary.LittleEndian
	out := le.AppendUint32(nil, uint32(attrFixed+len(data)))
	out = le.AppendUint32(out, returned)
	out = append(out, make([]byte, 16)...) // the other four attribute groups
	out = le.AppendUint32(out, attrFixed-attrHeader)
	out = le.AppendUint32(out, uint32(len(data)))
	return append(out, data...)
}

func TestAnACLEntryThatLetsSomeoneElseReplaceTheEntryIsNamed(t *testing.T) {
	for name, tc := range map[string]struct {
		reply []byte
		dir   bool
		want  string
	}{
		"no ACL":                       {reply: attrReply(0, nil)},
		"an empty one":                 {reply: attrReply(attrExtendedSecurity, nil)},
		"a filesec that holds no list": {reply: noList()},
		"every user may write it": {
			reply: filesec(ace{everyoneGUID, acePermit, writeRight}),
			want:  "/p has an ACL that grants every user write",
		},
		"every user may add a file to it": {
			reply: filesec(ace{everyoneGUID, acePermit, writeRight | 1<<6}), dir: true,
			want: "/p has an ACL that grants every user add_file, delete_child",
		},
		"one denied it":  {reply: filesec(ace{everyoneGUID, 2, writeRight})},
		"root may write": {reply: filesec(ace{rootGUID, acePermit, writeRight})},
		"a uid with no GUID of its own may change its owner": {
			reply: filesec(ace{"ffffeeeeddddccccbbbbaaaa000001f5", acePermit, 1 << 13}),
			want:  "/p has an ACL that grants uid 501 chown",
		},
		"a group may change its ACL": {
			reply: filesec(ace{"abcdefabcdefabcdefabcdef00000050", acePermit, 1 << 12}),
			want:  "/p has an ACL that grants group 80 writesecurity",
		},
		"someone only a GUID names may delete it": {
			reply: filesec(ace{"0123456789abcdef0123456789abcdef", acePermit, 1 << 4}),
			want:  "/p has an ACL that grants the user or group 01234567-89ab-cdef-0123-456789abcdef delete",
		},
		"every user may read its ACL, which replaces nothing": {
			reply: filesec(ace{everyoneGUID, acePermit, 1 << 11}),
		},
		"a grant only what it holds inherits": {
			reply: filesec(ace{everyoneGUID, acePermit | 1<<8, writeRight}), dir: true,
			want: "/p has an ACL that grants every user add_file",
		},
		"a generic write, after root's grant": {
			reply: filesec(ace{rootGUID, acePermit, writeRight}, ace{everyoneGUID, acePermit, 1 << 23}),
			want:  "/p has an ACL that grants every user generic_write",
		},
	} {
		t.Run(name, func(t *testing.T) {
			who, err := aclGrant("/p", tc.dir, tc.reply)
			require.NoError(t, err)
			assert.Equal(t, tc.want, who)
		})
	}
}

func noList() []byte {
	reply := filesec()
	binary.LittleEndian.PutUint32(reply[attrFixed+filesecHeader-8:], filesecNoACL)
	return reply
}

// A reply that cannot be read leaves the entry unchecked, never passed.
func TestAnACLThatCannotBeReadIsAnError(t *testing.T) {
	le := binary.LittleEndian
	grant := filesec(ace{everyoneGUID, acePermit, writeRight})
	for name, reply := range map[string][]byte{
		"shorter than its header": grant[:8],
		"longer than it says": func() []byte {
			r := append([]byte(nil), grant...)
			le.PutUint32(r, uint32(len(r)+1))
			return r
		}(),
		"cut short": func() []byte {
			r := append([]byte(nil), grant[:len(grant)-4]...)
			le.PutUint32(r, uint32(len(r)))
			return r
		}(),
		"no filesec magic": func() []byte {
			r := append([]byte(nil), grant...)
			le.PutUint32(r[attrFixed:], 0)
			return r
		}(),
		"more entries than bytes": func() []byte {
			r := append([]byte(nil), grant...)
			le.PutUint32(r[attrFixed+filesecHeader-8:], 1<<30)
			return r
		}(),
		"data before the reference": func() []byte {
			r := append([]byte(nil), grant...)
			le.PutUint32(r[attrHeader:], 0xfffffff0)
			return r
		}(),
	} {
		t.Run(name, func(t *testing.T) {
			who, err := aclGrant("/p", false, reply)
			require.Error(t, err)
			assert.Empty(t, who)
			assert.Contains(t, err.Error(), "cannot read the ACL of /p")
		})
	}
}
