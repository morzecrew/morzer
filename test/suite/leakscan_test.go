package suite

import (
	"regexp"
	"testing"

	"github.com/stretchr/testify/require"
)

// Scanning a signed document for a value that must not be in it.
//
// The assertion these tests want is "this parameter value appears nowhere in
// the statement", and scanning the whole document is the right shape for it: a
// leak that reached an unexpected field is exactly what a structural check
// would miss.
//
// The problem is what else the document contains. A content digest is 64
// characters of hex, every one of which is a decimal digit or a-f, so a decimal
// value like a port number appears inside one by chance. It is not a rare
// enough chance to ignore: a four-digit value has 61 places it could land in a
// 64-character digest, which is about one run in a thousand, and the test
// bundle's digest changes every run because `retargetManifest` rewrites the
// manifest with the test's own temporary path.
//
// It has now happened once, on CI, to `TestAConfigChangeFilesAStatement`: the
// digest was 3f563ced…2a41df59000c6455…, and `df59000c` contains `9000`.
//
// So the scan runs over the document with its opaque hashes blanked. A leaked
// port would never be inside a 32-character run of hex; a coincidence always
// is, and the test is asking about the leak.
var hexRun = regexp.MustCompile(`[0-9a-f]{32,}`)

func withoutDigests(document string) string {
	return hexRun.ReplaceAllString(document, "<digest>")
}

// The scan still catches a leak, and stops catching a coincidence.
//
// Verified against the digest that actually failed on CI rather than a made-up
// one, so this test fails if the exclusion is ever narrowed back past the case
// that motivated it.
func TestALeakScanIgnoresDigestsAndNothingElse(t *testing.T) {
	const ciDigest = "3f563cedbb8b0b79a76c2eec2a41df59000c64557c426fd7ff22886e0a0e3dc1"
	require.Contains(t, ciDigest, "9000",
		"the digest that broke CI no longer contains the value, so this test proves nothing")

	coincidence := `{"subject":[{"digest":{"sha256":"` + ciDigest + `"}}]}`
	require.NotContains(t, withoutDigests(coincidence), "9000",
		"a value appearing inside a content digest is still read as a leak")

	// A real one, in the shape the assertion exists to catch: a parameter
	// value rendered into the document beside its name.
	leak := `{"config":{"parameter_names":["http_port"],"values":{"http_port":"9000"}}}`
	require.Contains(t, withoutDigests(leak), "9000",
		"a parameter value in the document was blanked along with the digests")

	// And a short hex-looking run is not a digest: blanking those would
	// hide leaks in ordinary fields.
	require.Contains(t, withoutDigests(`{"port":"9000","ref":"abc123"}`), "9000")
}
