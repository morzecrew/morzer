//go:build !darwin

package domain

// runRoot holds RunDir: `/run`, a tmpfs.
const runRoot = "/run"

// sharedDirMode is the mode of the managed directories a container's binds
// pass through. The engine reads the host as root, so they keep other users
// out.
const sharedDirMode = 0o750
