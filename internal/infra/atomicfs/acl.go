package atomicfs

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"

	"github.com/morzecrew/morzer/internal/domain"
)

// A Mac keeps an ACL beside an entry's mode bits, and an entry in it can let a
// named user replace a file root owns that no mode bit lets them write, or
// give themselves a right that does (RFC 0055 decision 23). What getattrlist
// returns for ATTR_CMN_RETURNED_ATTRS and ATTR_CMN_EXTENDED_SECURITY, and the
// kauth_filesec inside it, are laid out as XNU's sys/attr.h and sys/kauth.h
// have them, in host order, which is little-endian on every Mac the manager
// runs on. The parser is here rather than in a darwin file so it is tested on
// every platform.
const (
	attrExtendedSecurity = 0x00400000 // ATTR_CMN_EXTENDED_SECURITY
	filesecMagic         = 0x012cc16d // KAUTH_FILESEC_MAGIC
	filesecNoACL         = 0xffffffff // KAUTH_FILESEC_NOACL
	// The reply's length, the attributes that came back, and the reference
	// to the filesec, whose offset counts from the reference itself.
	attrHeader = 4 + 20
	attrFixed  = attrHeader + 8
	// The magic, the owner's and group's GUIDs, the entry count and flags.
	filesecHeader = 4 + 16 + 16 + 4 + 4
	// An entry's GUID, flags and rights.
	aceSize = 16 + 4 + 4

	aceKindMask = 0xf
	acePermit   = 1
)

// aclRights are the rights decision 23 counts, by the names chmod(1) gives
// them on a file and on a directory, where one bit means two things. The
// generic rights count too, since the kernel expands them to these.
var aclRights = []struct {
	bit       uint32
	file, dir string
}{
	{1 << 2, "write", "add_file"},
	{1 << 4, "delete", "delete"},
	{1 << 5, "append", "add_subdirectory"},
	{1 << 6, "delete_child", "delete_child"},
	{1 << 12, "writesecurity", "writesecurity"},
	{1 << 13, "chown", "chown"},
	{1 << 21, "generic_all", "generic_all"},
	{1 << 23, "generic_write", "generic_write"},
}

// aclGrant says which entry of the ACL in a getattrlist reply lets someone
// other than root replace p, or give themselves a right that does, or "" when
// none does. Every allow entry counts, whatever its inheritance flags: root
// makes files in the directories it checks, and they would inherit the grant.
// Deny, audit and alarm entries grant nothing (logs/macos-host.md). A reply it
// cannot read is an error, so the entry is not passed.
func aclGrant(p string, dir bool, reply []byte) (string, error) {
	unreadable := func(format string, args ...any) error {
		return domain.Internal(nil, "cannot read the ACL of %s: %s", p, fmt.Sprintf(format, args...))
	}
	le := binary.LittleEndian
	if len(reply) < attrHeader {
		return "", unreadable("the reply is %d bytes", len(reply))
	}
	size := le.Uint32(reply)
	if size < attrHeader || uint64(size) > uint64(len(reply)) {
		return "", unreadable("the reply says it is %d bytes, of %d", size, len(reply))
	}
	reply = reply[:size]
	// Absent where there is no ACL, or on a filesystem that keeps none.
	if le.Uint32(reply[4:])&attrExtendedSecurity == 0 {
		return "", nil
	}
	if len(reply) < attrFixed {
		return "", unreadable("the reply is %d bytes", len(reply))
	}
	//nolint:gosec // attr_dataoffset is signed, and read as the kernel wrote it.
	offset, length := int64(int32(le.Uint32(reply[attrHeader:]))), int64(le.Uint32(reply[attrHeader+4:]))
	start := attrHeader + offset
	if length == 0 {
		return "", nil
	}
	if start < attrFixed || start+length > int64(len(reply)) {
		return "", unreadable("its %d bytes at %d are outside a %d-byte reply", length, start, len(reply))
	}
	sec := reply[start : start+length]
	if len(sec) < filesecHeader || le.Uint32(sec) != filesecMagic {
		return "", unreadable("it is not a filesec")
	}
	count := le.Uint32(sec[filesecHeader-8:])
	if count == filesecNoACL {
		return "", nil
	}
	if uint64(filesecHeader)+uint64(count)*aceSize > uint64(len(sec)) {
		return "", unreadable("%d entries do not fit in %d bytes", count, len(sec))
	}
	for i := range uint64(count) {
		ace := sec[filesecHeader+i*aceSize:]
		if le.Uint32(ace[16:])&aceKindMask != acePermit {
			continue
		}
		var granted []string
		for _, r := range aclRights {
			if le.Uint32(ace[20:])&r.bit == 0 {
				continue
			}
			if dir {
				granted = append(granted, r.dir)
			} else {
				granted = append(granted, r.file)
			}
		}
		if len(granted) == 0 {
			continue
		}
		if who, root := grantee(ace[:16]); !root {
			return fmt.Sprintf("%s has an ACL that grants %s %s", p, who, strings.Join(granted, ", ")), nil
		}
	}
	return "", nil
}

// grantee names the user or group an ACL entry's GUID stands for, and says
// whether it is root. Only uid 0's GUID is root's: a group's counts whatever
// the group, as a group bit does, since it can have other members, and a GUID
// nobody can name counts as someone else's.
func grantee(guid []byte) (string, bool) {
	// The GUIDs macOS gives a uid or gid that has none of its own: a fixed
	// prefix, and the id in the last four bytes.
	const userPrefix, groupPrefix = "ffffeeeeddddccccbbbbaaaa", "abcdefabcdefabcdefabcdef"
	s := hex.EncodeToString(guid)
	id, _ := strconv.ParseUint(s[24:], 16, 32)
	switch s[:24] {
	case userPrefix:
		if id == 0 {
			return "", true
		}
		return fmt.Sprintf("uid %d", id), false
	case groupPrefix:
		if id == 12 { // everyone
			return "every user", false
		}
		return fmt.Sprintf("group %d", id), false
	}
	return fmt.Sprintf("the user or group %s-%s-%s-%s-%s", s[:8], s[8:12], s[12:16], s[16:20], s[20:]), false
}
