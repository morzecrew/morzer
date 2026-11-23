package ops

import (
	"context"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"sync"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/events"
	"github.com/morzecrew/morzer/internal/lifecycle/preflight"
	"github.com/morzecrew/morzer/internal/ports"
)

// The credentials the manager sends to the HTTPS servers it fetches release
// bundles from (RFC 0038). An entry names a server and a secret; the value
// stays in the secret store, and the HTTPS source asks for it per request.
//
// Like `release trust add`, a change converges nothing, so it takes the
// deployment lock and writes the state. Unlike it, a change is journaled:
// where a credential is sent is worth a record (decision 11).

// SourceCredentials is the installation's `sources.https` and their secrets,
// as the HTTPS source asks for them. Built once per command, with the source,
// and kept across the rewire `init` makes when it learns the product's name;
// deps is called on every request for that reason.
type SourceCredentials struct {
	deps func() *Deps

	mu   sync.Mutex
	held *InitSource
}

var _ ports.SourceCredentials = (*SourceCredentials)(nil)

// NewSourceCredentials reads the entries through whichever Deps deps returns.
func NewSourceCredentials(deps func() *Deps) *SourceCredentials {
	return &SourceCredentials{deps: deps}
}

// Hold is `init`'s credential for the server it installs from, answered for
// every fetch from that server from now on: the one that reads the product's
// name before the operation starts included, and before the secret store it
// is stored in exists (RFC 0038 decision 12).
func (c *SourceCredentials) Hold(src *InitSource) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.held = src
}

func (c *SourceCredentials) holding() *InitSource {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.held
}

// InitSource is the credential `init --source-secret-file` gives: the entry
// it writes, and the value it stores under the entry's secret.
type InitSource struct {
	Entry domain.HTTPSSource
	Value string

	// File is where the value was read from, for the command the wizard
	// prints. Never the value.
	File string
}

// NewInitSource builds `init`'s credential for the server release names. The
// secret is named after the server when secret is empty (logs/https-credentials.md).
func NewInitSource(release string, auth domain.SourceAuth, secret, value, file string) (*InitSource, error) {
	rest, ok := strings.CutPrefix(release, "https://")
	if !ok {
		return nil, domain.Usage("--source-secret-file applies to an https:// --release, not %q",
			domain.RedactURLCredentials(release))
	}
	u, err := url.Parse("https://" + rest)
	if err != nil || u.Hostname() == "" {
		return nil, domain.Usage("--release %q names no server", domain.RedactURLCredentials(release))
	}
	authority := domain.SourceAuthority(u.Hostname(), u.Port())
	if secret == "" {
		secret = domain.DefaultSourceSecret(authority)
	}
	entry := domain.HTTPSSource{Host: authority, Auth: auth, Secret: secret}
	if err := entry.Validate(); err != nil {
		return nil, domain.Usage("%s", domain.AsError(err).Message)
	}
	return &InitSource{Entry: entry, Value: value, File: file}, nil
}

func (c *SourceCredentials) SourceFor(ctx context.Context, authority string) (domain.HTTPSSource, bool, error) {
	if held := c.holding(); held != nil && held.Entry.Host == authority {
		return held.Entry, true, nil
	}
	d := c.deps()
	if d == nil {
		return domain.HTTPSSource{}, false, nil
	}
	// No installation, or none chosen among several, configures nothing:
	// `release fetch` works there today, and still does.
	if d.State == nil || d.noSelection() != nil {
		return domain.HTTPSSource{}, false, nil
	}
	exists, err := d.State.InstallationExists(ctx)
	if err != nil || !exists {
		return domain.HTTPSSource{}, false, err
	}
	inst, err := d.State.LoadInstallation(ctx)
	if err != nil {
		return domain.HTTPSSource{}, false, err
	}
	entry, found := inst.Sources.For(authority)
	return entry, found, nil
}

func (c *SourceCredentials) RevealSource(ctx context.Context, entry domain.HTTPSSource) (string, error) {
	if held := c.holding(); held != nil && held.Entry == entry {
		return held.Value, nil
	}
	d := c.deps()
	if d == nil || d.Secrets == nil {
		return "", domain.Internal(nil, "a source credential was asked for with no secret store wired")
	}
	set, err := d.Secrets.Load(ctx)
	if err != nil {
		return "", err
	}
	secret, ok := set.Get(entry.Secret)
	if !ok {
		return "", domain.ValidationError(domain.ErrNotFound,
			"the credential for %s is the secret %q, which is not set", entry.Host, entry.Secret).
			WithHint("set it with `morzer secret set %s`", entry.Secret)
	}
	return secret.Reveal(), nil
}

// SourcesReport is the configured servers.
type SourcesReport struct {
	HTTPS []domain.HTTPSSource `json:"https"`
}

// SourceList reads the entries. Read-only: no lock.
func SourceList(ctx context.Context, d *Deps) (SourcesReport, error) {
	inst, err := d.loadInstallation(ctx)
	if err != nil {
		return SourcesReport{}, err
	}
	return SourcesReport{HTTPS: append([]domain.HTTPSSource{}, inst.Sources.HTTPS...)}, nil
}

// SourceAddOptions is one `release source add`.
type SourceAddOptions struct {
	Options

	// URL names the server: `https://host[:port]`.
	URL    string
	Auth   domain.SourceAuth
	Secret string
}

// SourceAdd configures the credential a server is sent. An entry for the same
// server is replaced and the summary says what it was; the same entry again
// changes nothing and journals nothing.
func SourceAdd(ctx context.Context, d *Deps, opts SourceAddOptions) (Result, error) {
	authority, err := domain.ParseSourceURL(opts.URL)
	if err != nil {
		return Result{}, err
	}
	entry := domain.HTTPSSource{Host: authority, Auth: opts.Auth, Secret: opts.Secret}
	if err := entry.Validate(); err != nil {
		return Result{}, domain.Usage("%s", domain.AsError(err).Message)
	}

	var replaced *domain.HTTPSSource
	unchanged := false
	opID := d.newOpID()
	err = d.withLock(ctx, opID, domain.OpTypeConfig, opts.Options, func(ctx context.Context) error {
		current, err := d.loadInstallation(ctx)
		if err != nil {
			return err
		}
		idx := slices.IndexFunc(current.Sources.HTTPS, func(s domain.HTTPSSource) bool { return s.Host == authority })
		if idx >= 0 {
			if current.Sources.HTTPS[idx] == entry {
				unchanged = true
				return nil
			}
			was := current.Sources.HTTPS[idx]
			replaced = &was
		}
		if opts.DryRun {
			return nil
		}
		flags := map[string]string{"source_add": authority, "auth": string(entry.Auth), "secret": entry.Secret}
		if replaced != nil {
			flags["replaced"] = string(replaced.Auth) + " " + replaced.Secret
		}
		if err := d.journalSourceChange(ctx, opID, current, flags); err != nil {
			return err
		}
		if idx >= 0 {
			current.Sources.HTTPS[idx] = entry
		} else {
			current.Sources.HTTPS = append(current.Sources.HTTPS, entry)
		}
		return d.saveInstallation(ctx, current)
	})
	if err != nil {
		return Result{}, err
	}
	what := fmt.Sprintf("https://%s gets the %s credential in the secret %s", authority, entry.Auth, entry.Secret)
	switch {
	case unchanged:
		return Result{Summary: what + ", as it already did"}, nil
	case opts.DryRun:
		return Result{Summary: "would configure: " + what + sourceReplacedNote(replaced)}, nil
	}
	d.warnUnsetSourceSecret(ctx, entry)
	return Result{Summary: what + sourceReplacedNote(replaced)}, nil
}

func sourceReplacedNote(replaced *domain.HTTPSSource) string {
	if replaced == nil {
		return ""
	}
	return fmt.Sprintf(", in place of the %s credential in %s", replaced.Auth, replaced.Secret)
}

// SourceRemove stops sending a server a credential. The secret stays in the
// store, since another entry or a later one may name it, and the summary says
// how to remove it. A server with no entry changes nothing.
func SourceRemove(ctx context.Context, d *Deps, opts Options, raw string) (Result, error) {
	authority, err := domain.ParseSourceURL(raw)
	if err != nil {
		return Result{}, err
	}
	var removed *domain.HTTPSSource
	opID := d.newOpID()
	err = d.withLock(ctx, opID, domain.OpTypeConfig, opts, func(ctx context.Context) error {
		current, err := d.loadInstallation(ctx)
		if err != nil {
			return err
		}
		idx := slices.IndexFunc(current.Sources.HTTPS, func(s domain.HTTPSSource) bool { return s.Host == authority })
		if idx < 0 {
			return nil
		}
		was := current.Sources.HTTPS[idx]
		removed = &was
		if opts.DryRun {
			return nil
		}
		if err := d.journalSourceChange(ctx, opID, current, map[string]string{
			"source_remove": authority, "auth": string(was.Auth), "secret": was.Secret,
		}); err != nil {
			return err
		}
		current.Sources.HTTPS = slices.Delete(current.Sources.HTTPS, idx, idx+1)
		return d.saveInstallation(ctx, current)
	})
	switch {
	case err != nil:
		return Result{}, err
	case removed == nil:
		return Result{Summary: "no credential is configured for https://" + authority}, nil
	case opts.DryRun:
		return Result{Summary: "would stop sending https://" + authority + " a credential"}, nil
	}
	return Result{Summary: fmt.Sprintf("https://%s gets no credential now; the secret %s is kept, "+
		"and `morzer secret remove %s` removes it", authority, removed.Secret, removed.Secret)}, nil
}

// journalSourceChange records a change before it is written (decision 11), as
// `--forget-index` journals before it forgets: a change with no record is the
// one that matters, and a record whose write then fails is followed by the
// retry's own.
func (d *Deps) journalSourceChange(ctx context.Context, opID string, inst domain.Installation, flags map[string]string) error {
	now := domain.NewTime(d.now())
	return d.State.AppendOperation(ctx, domain.OperationRecord{
		SchemaVersion:  domain.OperationSchemaVersion,
		ID:             opID,
		Type:           domain.OpTypeConfig,
		Status:         domain.StatusSucceeded,
		StartedAt:      now,
		FinishedAt:     now,
		ManagerVersion: d.ManagerVersion.String(),
		InstallationID: inst.ID,
		Flags:          flags,
	})
}

// warnUnsetSourceSecret says so when an entry names a secret not set yet: the
// next fetch from that server would be refused for it.
func (d *Deps) warnUnsetSourceSecret(ctx context.Context, entry domain.HTTPSSource) {
	if d.Secrets == nil {
		return
	}
	set, err := d.Secrets.Load(ctx)
	if err != nil {
		return
	}
	if _, ok := set.Get(entry.Secret); !ok {
		d.warnf("the secret %s is not set yet; set it with `morzer secret set %s` before fetching from https://%s",
			entry.Secret, entry.Secret, entry.Host)
	}
}

// WarnPasswordInRef is decision 16: a password typed into a reference works,
// and is on the command line and in shell history while it does. Called
// before the first fetch, which for `release fetch`, and for `init` reading
// the product's name, is the CLI's rather than an operation's.
func WarnPasswordInRef(d *Deps, ref string) {
	if d == nil {
		return
	}
	rest, ok := strings.CutPrefix(ref, "https://")
	if !ok {
		return
	}
	u, err := url.Parse("https://" + rest)
	if err != nil || u.User == nil {
		return
	}
	if _, hasPassword := u.User.Password(); !hasPassword {
		return
	}
	d.warnf("the reference carries a password, which is on the command line and in shell history; " +
		"store it as a secret and configure the server with `morzer release source add`")
}

// checkSourceCredentials is decision 15: an entry whose secret is not set is
// a fetch that will be refused, and the machine still runs meanwhile.
func (d *Deps) checkSourceCredentials(inst domain.Installation) preflight.Check {
	return preflight.Check{
		ID:          "release.source-credentials",
		Category:    preflight.CategoryNetwork,
		Description: "the secrets the configured HTTPS servers' credentials name",
		Fatal:       false,
		Run: func(ctx context.Context) events.CheckResult {
			set, err := d.Secrets.Load(ctx)
			if err != nil {
				return preflight.Warn("check the secret store; `morzer doctor` reports it under its own check",
					"cannot read the secrets the source credentials name: %s", domain.AsError(err).Message)
			}
			var missing []string
			for _, s := range inst.Sources.HTTPS {
				if _, ok := set.Get(s.Secret); !ok {
					missing = append(missing, fmt.Sprintf("%s (for %s)", s.Secret, s.Host))
				}
			}
			if len(missing) > 0 {
				return preflight.Warn("set each with `morzer secret set <name>`",
					"the credential for a configured server is not set: %s; a fetch from it will be refused",
					strings.Join(missing, ", "))
			}
			return preflight.OK("every configured server's credential is set (%d)", len(inst.Sources.HTTPS))
		},
	}
}
