package sopsage

// The two seams the failure tests drive: a sops that is somewhere else, and a
// clock that does not move.
//
// Unexported in the package itself because nothing outside this directory has
// ever set either -- production takes `sops` from PATH and the real time -- and
// an option only tests pass is an option no test exercises as production leaves
// it.
var (
	WithSOPSBinary = withSOPSBinary
	WithClock      = withClock
)
