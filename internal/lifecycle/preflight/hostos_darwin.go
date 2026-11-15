package preflight

// osListElsewhere is why `system.os` has nothing to compare on a Mac:
// `requirements.os` lists Linux distributions, and the platform gate has
// already said the release runs here, so the check passes rather than warning
// that the host is unknown (RFC 0055 §5.1).
const osListElsewhere = "the release's OS list names Linux distributions, and this host is macOS"
