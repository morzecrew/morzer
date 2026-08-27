package systemd

// WithSystemctl pins the systemctl path, so a test asserting on an argv does
// not depend on where the host keeps the binary.
//
// Unexported in the package: production takes `systemctl` from PATH and never
// names a path. WithUnitDir stays exported, because the suites in test/ install
// real units into a temporary directory and cannot reach an export_test.go.
var WithSystemctl = withSystemctl
