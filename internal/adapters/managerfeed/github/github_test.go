package github_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/adapters/managerfeed/github"
	"github.com/morzecrew/morzer/internal/domain"
)

func serve(t *testing.T, status int, body string) *github.Feed {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.NotEmpty(t, r.Header.Get("User-Agent"), "GitHub refuses a request without a User-Agent")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return github.At(srv.URL, srv.Client())
}

// The newest is the highest published version, not the first listed: drafts,
// prereleases, tags with build metadata and tags that are not versions are
// skipped, and a later patch to an older line does not outrank the newer line.
func TestTheNewestIsTheHighestPublishedVersion(t *testing.T) {
	feed := serve(t, http.StatusOK, `[
		{"tag_name":"v0.11.4","html_url":"https://example.test/v0.11.4"},
		{"tag_name":"v0.14.0","html_url":"https://example.test/v0.14.0","draft":true},
		{"tag_name":"v0.15.0","html_url":"https://example.test/v0.15.0","prerelease":true},
		{"tag_name":"v0.13.0-rc.2","html_url":"https://example.test/v0.13.0-rc.2"},
		{"tag_name":"nightly","html_url":"https://example.test/nightly"},
		{"tag_name":"v0.12.1+rebuild","html_url":"https://example.test/v0.12.1+rebuild"},
		{"tag_name":"v0.12.1","html_url":"https://example.test/v0.12.1"},
		{"tag_name":"v0.12.0","html_url":"https://example.test/v0.12.0"}
	]`)

	got, err := feed.Newest(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "0.12.1", got.Version.String())
	assert.Equal(t, "https://example.test/v0.12.1", got.URL)
}

// A feed that cannot answer is an error, never a release.
func TestAFeedThatCannotAnswerIsAnError(t *testing.T) {
	cases := map[string]struct {
		status int
		body   string
		want   string
	}{
		"rate limited":         {http.StatusForbidden, `{"message":"API rate limit exceeded"}`, "60 unauthenticated requests"},
		"failing":              {http.StatusBadGateway, ``, "answered 502"},
		"not a list":           {http.StatusOK, `{"tag_name":"v0.12.0"}`, "not a release list"},
		"no published release": {http.StatusOK, `[{"tag_name":"v0.13.0-rc.1","prerelease":true}]`, "no published release"},
		"an empty list":        {http.StatusOK, `[]`, "no published release"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := serve(t, tc.status, tc.body).Newest(context.Background())
			require.Error(t, err)
			e := domain.AsError(err)
			assert.Contains(t, e.Message+" "+e.Hint, tc.want)
			assert.Equal(t, domain.ExitUsage, domain.ExitCode(err), "a feed failure is a validation error")
		})
	}
}

func TestAnUnreachableFeedIsAnError(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	srv.Close()
	_, err := github.At(srv.URL, srv.Client()).Newest(context.Background())
	require.Error(t, err)
	assert.Contains(t, domain.AsError(err).Message, "cannot reach the release feed")
}
