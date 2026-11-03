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
	"os"
	"path/filepath"
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

// Downloads is where a release's files are, under its tag, as `install.sh`
// fetches them.
const Downloads = "https://github.com/morzecrew/morzer/releases/download"

// DefaultTimeout bounds asking for the list.
const DefaultTimeout = 30 * time.Second

// DownloadTimeout bounds fetching a release's three files, which on a slow
// link takes longer than a list.
const DownloadTimeout = 10 * time.Minute

// maxBody bounds the response as it is read; a page of 100 releases with their
// notes is well under it.
const maxBody = 8 << 20

// maxFile bounds one downloaded file. A release archive is a few tens of
// megabytes.
const maxFile = 256 << 20

// Feed reads the release list and a release's files.
type Feed struct {
	url       string
	downloads string
	client    *http.Client
}

// New reads the project's release list.
func New() *Feed { return At(Releases, Downloads, &http.Client{}) }

// At reads a release list and release files at other URLs, which is what
// tests serve.
func At(url, downloads string, client *http.Client) *Feed {
	return &Feed{url: url, downloads: downloads, client: client}
}

type release struct {
	Tag        string `json:"tag_name"`
	URL        string `json:"html_url"`
	Draft      bool   `json:"draft"`
	Prerelease bool   `json:"prerelease"`
}

// Newest implements ports.ManagerFeed.
func (f *Feed) Newest(ctx context.Context) (ports.ManagerRelease, error) {
	listCtx, cancel := context.WithTimeout(ctx, DefaultTimeout)
	defer cancel()
	body, err := f.get(listCtx, f.url, "the release feed")
	if err != nil {
		return ports.ManagerRelease{}, err
	}
	defer func() { _ = body.Close() }()

	var list []release
	if err := json.NewDecoder(io.LimitReader(body, maxBody)).Decode(&list); err != nil {
		return ports.ManagerRelease{}, domain.ValidationError(err, "the release feed answered with something that is not a release list")
	}
	return newest(list)
}

// Download implements ports.ManagerFeed.
func (f *Feed) Download(ctx context.Context, v domain.Version, dir string) error {
	ctx, cancel := context.WithTimeout(ctx, DownloadTimeout)
	defer cancel()
	for _, name := range []string{ports.ManagerArchive(v), ports.SumsFileName, ports.SignatureFileName} {
		if err := f.download(ctx, f.downloads+"/v"+v.String()+"/"+name, filepath.Join(dir, name)); err != nil {
			return err
		}
	}
	return nil
}

func (f *Feed) download(ctx context.Context, url, dst string) error {
	body, err := f.get(ctx, url, url)
	if err != nil {
		return err
	}
	defer func() { _ = body.Close() }()

	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // a name this adapter chose, in a directory its caller made
	if err != nil {
		return domain.Internal(err, "cannot write %s", dst)
	}
	n, err := io.Copy(out, io.LimitReader(body, maxFile+1))
	if closeErr := out.Close(); err == nil {
		err = closeErr
	}
	switch {
	case err != nil && errors.Is(ctx.Err(), context.Canceled):
		return domain.Interrupted("the download of %s was cancelled", url)
	case err != nil:
		return domain.ValidationError(err, "cannot download %s", url)
	case n > maxFile:
		return domain.ValidationError(nil, "%s is larger than any release file, %d bytes", url, maxFile)
	}
	return nil
}

// get asks for one URL, answering an error that names what was asked for.
func (f *Feed) get(ctx context.Context, url, what string) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, domain.Internal(err, "cannot build the request for %s", what)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	// GitHub refuses a request without one.
	req.Header.Set("User-Agent", "morzer")

	resp, err := f.client.Do(req)
	if err != nil {
		if errors.Is(ctx.Err(), context.Canceled) {
			return nil, domain.Interrupted("the request for %s was cancelled", what)
		}
		return nil, domain.ValidationError(err, "cannot reach %s", what).
			WithHint("check the network; the releases are listed at https://github.com/morzecrew/morzer/releases")
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		e := domain.ValidationError(nil, "%s answered %s", what, resp.Status)
		switch resp.StatusCode {
		case http.StatusForbidden, http.StatusTooManyRequests:
			return nil, e.WithHint("GitHub allows 60 unauthenticated " +
				"requests an hour from one address; try again later")
		case http.StatusNotFound:
			return nil, e.WithHint("check the version is published: https://github.com/morzecrew/morzer/releases")
		}
		return nil, e
	}
	return resp.Body, nil
}

// newest is the highest version that is published, not a prerelease, and
// parses as a version with no build metadata; a tag that does not is not a
// release of this project. Metadata would compare equal to the release it
// decorates, leaving which URL wins to the list's order.
func newest(list []release) (ports.ManagerRelease, error) {
	var out ports.ManagerRelease
	for _, r := range list {
		if r.Draft || r.Prerelease {
			continue
		}
		v, err := domain.ParseVersion(r.Tag)
		if err != nil || v.Prerelease() != "" || v.Metadata() != "" {
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
