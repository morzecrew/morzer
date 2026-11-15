package cli

// engineUser is engine_darwin.go's counterpart: on Linux the engine reads the
// host as root, so a rendered secret keeps the owner its release declares.
var engineUser func() (uid, gid int, err error)
