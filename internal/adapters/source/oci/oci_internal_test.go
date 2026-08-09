package oci

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"oras.land/oras-go/v2/errdef"
	"oras.land/oras-go/v2/registry/remote/errcode"

	"github.com/morzecrew/morzer/internal/domain"
)

// responseFor builds the error a registry client returns for a status, with the
// request URL it really carries.
//
// The URL is the point. It is part of the error's text, and every classifier
// that reads the text rather than the status is reading the repository path,
// the port and the digest along with it.
func responseFor(status int, rawURL string) error {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		panic(err)
	}
	return &errcode.ErrorResponse{Method: http.MethodGet, URL: parsed, StatusCode: status}
}

// A digest is 64 hex characters, so roughly one release in seventy has "404"
// somewhere inside it -- and the same three characters appear in any repository
// path or port that happens to contain them. Reading them as a status is how an
// operator who needs `docker login` is told to check their reference instead,
// on the one release whose digest spelled it.
func TestARefusalIsNotReadAsAMissingArtefactBecauseItsDigestSpellsOne(t *testing.T) {
	err := registryError(
		responseFor(http.StatusUnauthorized,
			"https://registry.example/v2/demo/bundle/blobs/sha256:"+
				"a1b2404c5d6e7f8091a2b3c4d5e6f708192a3b4c5d6e7f8091a2b3c4d5e6f708"),
		"registry.example/demo/bundle:1.2.0")

	require.Error(t, err)
	assert.False(t, errors.Is(err, domain.ErrReleaseNotFound),
		"a 401 was reported as a missing artefact: the operator is sent to check a "+
			"reference that is correct, and never told to log in")
	assert.Contains(t, strings.ToLower(domain.AsError(err).Hint), "docker login",
		"the remedy for a refused pull is a login, and it is the whole reason "+
			"this error is interpreted rather than passed through")
}

// The same reading in the other direction: a port, a repository path or a digest
// that contains "401" must not turn a genuinely missing artefact into a refusal,
// which would tell an operator to fix credentials that already work.
func TestAMissingArtefactIsNotReadAsARefusalBecauseItsPortSpellsOne(t *testing.T) {
	err := registryError(
		responseFor(http.StatusNotFound, "http://127.0.0.1:40123/v2/demo/bundle/manifests/9.9.9"),
		"registry.example/demo/bundle:9.9.9")

	require.Error(t, err)
	assert.True(t, errors.Is(err, domain.ErrReleaseNotFound),
		"a 404 must classify as a missing release whatever its URL spells")
}

// The status is what the classification is *for*, so each one it names is
// pinned. Without this the two tests above could both pass against a
// classifier that had stopped distinguishing anything at all.
func TestEachStatusTheRegistryReportsIsClassifiedByItsCode(t *testing.T) {
	cases := map[int]struct {
		missing bool
		hint    string
	}{
		http.StatusNotFound:            {missing: true},
		http.StatusUnauthorized:        {hint: "docker login"},
		http.StatusForbidden:           {hint: "docker login"},
		http.StatusInternalServerError: {},
		http.StatusBadGateway:          {},
	}

	for status, want := range cases {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			err := registryError(
				responseFor(status, "https://registry.example/v2/demo/bundle/manifests/1.2.0"),
				"registry.example/demo/bundle:1.2.0")

			require.Error(t, err)
			assert.Equal(t, want.missing, errors.Is(err, domain.ErrReleaseNotFound),
				"status %d classified as missing=%v", status, !want.missing)
			if want.hint != "" {
				assert.Contains(t, strings.ToLower(domain.AsError(err).Hint), want.hint)
			}
		})
	}
}

// The client reports a missing tag as its own sentinel rather than as a
// response, so resolving one never reaches a status code at all.
func TestTheClientsOwnNotFoundIsStillAMissingRelease(t *testing.T) {
	err := registryError(fmt.Errorf("resolve tag: %w", errdef.ErrNotFound),
		"registry.example/demo/bundle:9.9.9")

	require.Error(t, err)
	assert.True(t, errors.Is(err, domain.ErrReleaseNotFound),
		"the client's own not-found sentinel is the other way a missing release arrives")
}

// Anything with no status and no sentinel is a transport failure, and saying so
// beats guessing: a DNS failure is not a missing release.
func TestAnErrorThatNamesNoStatusIsReportedAsUnreachable(t *testing.T) {
	err := registryError(errors.New("dial tcp: lookup registry.example: no such host"),
		"registry.example/demo/bundle:1.2.0")

	require.Error(t, err)
	assert.False(t, errors.Is(err, domain.ErrReleaseNotFound))
	assert.Contains(t, domain.AsError(err).Message, "cannot reach")
}

// TestOnlyLoopbackRegistriesAreReachedOverPlainHTTP.
//
// The rule is Docker's, and matching it is what makes `release pack`'s claim
// of parity with `docker pull` true. What matters more is the other direction:
// a host that merely *looks* local must still require TLS, or an attacker who
// can answer for `localhost.evil.example` gets an unencrypted channel by
// naming it well.
func TestOnlyLoopbackRegistriesAreReachedOverPlainHTTP(t *testing.T) {
	cases := []struct {
		registry string
		plain    bool
	}{
		{"localhost:5000", true},
		{"localhost", true},
		{"127.0.0.1:5000", true},
		{"127.0.0.1", true},
		{"127.1.2.3:5000", true}, // the whole 127/8 block is loopback
		{"[::1]:5000", true},
		// SplitHostPort strips the brackets when a port is present, so
		// this is the only case the trim in isLoopbackRegistry serves.
		{"[::1]", true},
		{"::1", true},
		{"[2001:db8::1]:5000", false},

		// Everything else, including the near misses.
		{"registry.example", false},
		{"registry.example:5000", false},
		{"localhost.evil.example", false},
		{"notlocalhost", false},
		{"ghcr.io", false},
		{"192.168.1.10:5000", false},
		{"10.0.0.1", false},
		{"", false},
	}

	for _, tc := range cases {
		t.Run(tc.registry, func(t *testing.T) {
			if got := isLoopbackRegistry(tc.registry); got != tc.plain {
				t.Errorf("isLoopbackRegistry(%q) = %t, want %t",
					tc.registry, got, tc.plain)
			}
		})
	}
}
