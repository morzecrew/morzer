package atomicfs

// aclOf is acl_darwin.go's counterpart. A POSIX ACL that grants a named user
// write shows in the group bits, which replaceableEntry already reads.
func aclOf(string, bool) (string, error) { return "", nil }
