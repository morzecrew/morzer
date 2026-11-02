// Package github implements ports.ManagerFeed over the GitHub releases API,
// the feed `install.sh` already reads (RFC 0046 decision 4).
//
// The URL is a constant rather than a setting. A feed an operator could point
// elsewhere would be a setting whose wrong value changes what a machine is told
// to replace its manager with, which is why the RFC refused a configurable one.
package github

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/ports"
)

// Releases is the repository's release list, newest first.
//
// The list rather than `/releases/latest`, which answers with the stable
// release created last: once an older line gets a later patch, that is not
// the newest version. One page of 100 is one request, which is what keeps a
// scheduled check inside the unauthenticated rate limit (RFC 0046 §9).
//
// shortcut: one page, so a stable release followed by more than 100 newer
// drafts and prereleases is missed; page through if that ever happens.
const Releases = "https://api.github.com/repos/morzecrew/morzer/releases?per_page=100"

// DefaultTimeout bounds the one request.
const DefaultTimeout = 30 * time.Second

// maxBody bounds the response as it is read; a page of 100 releases with their
// notes is well under it.
const maxBody = 8 << 20

// Feed reads the release list.
type Feed struct {
	url    string
	client *http.Client
}

// New reads the project's release list.
func New() *Feed { return At(Releases, &http.Client{Timeout: DefaultTimeout}) }

// At reads a release list at another URL, which is what tests serve.
func At(url string, client *http.Client) *Feed { return &Feed{url: url, client: client} }

type release struct {
	Tag        string `json:"tag_name"`
	URL        string `json:"html_url"`
	Draft      bool   `json:"draft"`
	Prerelease bool   `json:"prerelease"`
}

// Newest implements ports.ManagerFeed.
func (f *Feed) Newest(ctx context.Context) (ports.ManagerRelease, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, f.url, nil)
	if err != nil {
		return ports.ManagerRelease{}, domain.Internal(err, "cannot build the release feed request")
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	// GitHub refuses a request without one.
	req.Header.Set("User-Agent", "morzer")

	resp, err := f.client.Do(req)
	if err != nil {
		if errors.Is(ctx.Err(), context.Canceled) {
			return ports.ManagerRelease{}, domain.Interrupted("the release feed request was cancelled")
		}
		return ports.ManagerRelease{}, domain.ValidationError(err, "cannot reach the release feed").
			WithHint("check the network; the releases are listed at https://github.com/morzecrew/morzer/releases")
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		e := domain.ValidationError(nil, "the release feed answered %s", resp.Status)
		if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests {
			return ports.ManagerRelease{}, e.WithHint("GitHub allows 60 unauthenticated " +
				"requests an hour from one address; try again later")
		}
		return ports.ManagerRelease{}, e
	}

	var list []release
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxBody)).Decode(&list); err != nil {
		return ports.ManagerRelease{}, domain.ValidationError(err, "the release feed answered with something that is not a release list")
	}
	return newest(list)
}

// newest is the highest version that is published, not a prerelease, and
// parses as a version; a tag that does not is not a release of this project.
func newest(list []release) (ports.ManagerRelease, error) {
	var out ports.ManagerRelease
	for _, r := range list {
		if r.Draft || r.Prerelease {
			continue
		}
		v, err := domain.ParseVersion(r.Tag)
		if err != nil || v.Prerelease() != "" {
			continue
		}
		if v.GreaterThan(out.Version) {
			out = ports.ManagerRelease{Version: v, URL: r.URL}
		}
	}
	if out.Version.IsZero() {
		return out, domain.ValidationError(nil, "the release feed lists no published release")
	}
	return out, nil
}

var _ ports.ManagerFeed = (*Feed)(nil)
