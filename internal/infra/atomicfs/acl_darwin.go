package atomicfs

import (
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"

	"github.com/morzecrew/morzer/internal/domain"
)

// aclReply holds what getattrlist returns for an ACL of 680 entries. One
// larger comes back cut short, which aclGrant cannot read and so refuses.
const aclReply = 16 << 10

// aclOf says which entry of p's own ACL, a link's included, lets someone other
// than root replace it, or "" when none does. x/sys wraps setattrlist and not
// getattrlist, so this calls libSystem as x/sys does, through a trampoline
// (logs/macos-host.md).
func aclOf(p string, dir bool) (string, error) {
	path, err := unix.BytePtrFromString(p)
	if err != nil {
		return "", domain.Internal(err, "cannot read the ACL of %s", p)
	}
	attrs := unix.Attrlist{
		Bitmapcount: unix.ATTR_BIT_MAP_COUNT,
		Commonattr:  unix.ATTR_CMN_RETURNED_ATTRS | unix.ATTR_CMN_EXTENDED_SECURITY,
	}
	reply := make([]byte, aclReply)
	//nolint:gosec // getattrlist takes pointers to the path, the list and the reply, as x/sys passes them.
	_, _, errno := syscall_syscall6(libcGetattrlistTrampolineAddr,
		uintptr(unsafe.Pointer(path)), uintptr(unsafe.Pointer(&attrs)),
		uintptr(unsafe.Pointer(&reply[0])), uintptr(len(reply)), unix.FSOPT_NOFOLLOW, 0)
	if errno != 0 {
		return "", domain.Internal(errno, "cannot read the ACL of %s", p)
	}
	return aclGrant(p, dir, reply)
}

// syscall_syscall6 calls a libSystem function by its trampoline, as package
// syscall does. The standard library leaves it open to linkname, for x/sys
// and packages like this one.
//
//go:linkname syscall_syscall6 syscall.syscall6
func syscall_syscall6(fn, a1, a2, a3, a4, a5, a6 uintptr) (r1, r2 uintptr, err syscall.Errno)

var libcGetattrlistTrampolineAddr uintptr

//go:cgo_import_dynamic libc_getattrlist getattrlist "/usr/lib/libSystem.B.dylib"
