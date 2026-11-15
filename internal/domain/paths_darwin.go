package domain

// runRoot holds RunDir. The sealed system volume has no `/run`, so it is
// `/var/run`, which a boot clears (RFC 0055 decision 16, §13 S5).
const runRoot = "/var/run"

// sharedDirMode is the mode of the managed directories a container's binds
// pass through. Docker Desktop reads the Mac's files as the user who runs it,
// who has to be able to list every directory on the way to a bind, so other
// accounts can list them too (decision 16).
const sharedDirMode = 0o755
