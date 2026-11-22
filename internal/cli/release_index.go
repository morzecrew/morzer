package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"oras.land/oras-go/v2/registry"

	signer "github.com/morzecrew/morzer/internal/adapters/sign/minisign"
	"github.com/morzecrew/morzer/internal/adapters/source/oci"
	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/lifecycle/ops"
	"github.com/morzecrew/morzer/internal/ports"
)

// defaultIndexValidity is how long an index stays valid when --valid-for is
// not given (RFC 0037 decision 24).
const defaultIndexValidity = 7 * 24 * time.Hour

// releaseIndexReport is what `release index` did, and the `--json` shape.
type releaseIndexReport struct {
	Channel   string      `json:"channel"`
	Reference string      `json:"reference"`
	Sequence  int64       `json:"sequence"`
	IssuedAt  domain.Time `json:"issued_at"`
	ExpiresAt domain.Time `json:"expires_at"`

	Release domain.ChannelIndexRelease `json:"release"`

	KeyID          string `json:"key_id"`
	ManifestDigest string `json:"manifest_digest,omitempty"`

	// Replaced says why an index already under the tag did not count
	// against the sequence, when one was there and did not.
	Replaced string `json:"replaced,omitempty"`

	DryRun bool `json:"dry_run,omitempty"`
}

func newReleaseIndexCommand(app *App) *cobra.Command {
	var (
		keyPath  string
		validFor time.Duration
	)
	cmd := &cobra.Command{
		Use:   "index <oci-reference>",
		Short: "Sign and publish a channel's index, so machines can tell a freeze",
		Long: "Writes a signed statement of which release a channel points at, and\n" +
			"until when, beside the channel as the tag <channel>.index. A machine\n" +
			"that pins your key warns once that statement expires, which is how it\n" +
			"tells a channel that is quiet from one an attacker froze.\n\n" +
			"The release is the bundle the channel tag points at now, named by its\n" +
			"content digest. The sequence is the time in milliseconds, and a\n" +
			"sequence not higher than the index already published, signed by this\n" +
			"key, is refused. Re-signing is the same command run again, before the\n" +
			"index expires, whether or not the channel moved.\n\n" +
			"--key is the minisign secret key that signs your bundles, unencrypted,\n" +
			"since a scheduled job runs this without a prompt. Credentials are the\n" +
			"ones `docker login` stored. --dry-run reads, checks and signs, and\n" +
			"pushes nothing.",
		Example: "  morzer release index oci://registry.example/demo/bundle:stable --key ./release.key\n" +
			"  morzer release index oci://registry.example/demo/bundle:stable --key ./release.key --valid-for 72h",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ref, channel, err := channelReference(args[0])
			if err != nil {
				return err
			}
			if keyPath == "" {
				return domain.Usage("release index needs --key, the minisign secret key your bundles are signed with")
			}
			if validFor < time.Minute {
				// The index's times are whole seconds, and one that
				// expires as it is issued warns on every machine.
				return domain.Usage("--valid-for must be at least 1m")
			}

			ctx := cmd.Context()
			if app.Flags.timeout > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, app.Flags.timeout)
				defer cancel()
			}

			// The key first: a missing or encrypted one is the vendor's to
			// fix, and the registry has nothing to say about it.
			key := signer.New(keyPath, "vendor")
			pub, err := key.PublicKey(ctx)
			if err != nil {
				return indexKeyError(err, keyPath)
			}

			source := oci.New()
			defer func() { _ = source.Close() }()
			report, warning, err := publishChannelIndex(ctx, source, key, pub.Line,
				ref, channel, time.Now(), validFor, app.Flags.dryRun)
			if warning != "" {
				fmt.Fprintf(app.Stream.Err, "warning: %s\n", warning)
			}
			if err != nil {
				return err
			}
			app.finish(ops.Result{Summary: releaseIndexSummary(report), Data: report})
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&keyPath, "key", "", "the minisign secret key your bundles are signed with, unencrypted")
	f.DurationVar(&validFor, "valid-for", defaultIndexValidity, "how long the index stays valid, at least 1m")
	return cmd
}

// channelReference parses the reference `release index` takes, and refuses
// one that names no channel before the registry is asked anything.
func channelReference(arg string) (ports.Ref, string, error) {
	ref, err := ports.ParseRef(arg)
	if err != nil {
		return ports.Ref{}, "", err
	}
	shown := ports.RedactRefCredentials(ref.String())
	if ref.Scheme != oci.Scheme {
		return ports.Ref{}, "", domain.Usage("release index publishes to an OCI registry, not to %s", shown).
			WithHint("pass a channel reference like oci://registry.example/demo/bundle:stable")
	}
	location := ref.Location
	slash := strings.LastIndex(location, "/")
	colon := strings.LastIndex(location, ":")
	if strings.Contains(location, "@") || colon < slash || colon == len(location)-1 {
		return ports.Ref{}, "", domain.Usage("%s names no channel", shown).
			WithHint("an index sits beside a channel tag: pass oci://registry.example/demo/bundle:stable")
	}
	channel := location[colon+1:]
	switch {
	case strings.HasSuffix(channel, domain.ChannelIndexTagSuffix):
		return ports.Ref{}, "", domain.Usage("%s names an index tag, not a channel", shown).
			WithHint("pass the channel the index is for: oci://%s",
				strings.TrimSuffix(location, domain.ChannelIndexTagSuffix))
	case isVersion(channel):
		// A version never moves, so it has nothing to go stale.
		return ports.Ref{}, "", domain.Usage("%s names the version %s, not a channel", shown, channel).
			WithHint("an index is for a tag that moves, such as stable")
	}
	if err := (registry.Reference{Reference: domain.ChannelIndexTag(channel)}).ValidateReferenceAsTag(); err != nil {
		return ports.Ref{}, "", domain.Usage("the channel %q is too long to carry an index tag", channel).
			WithHint("a tag is up to 128 characters, and the index's adds %q", domain.ChannelIndexTagSuffix)
	}
	return ref, channel, nil
}

func isVersion(s string) bool {
	_, err := domain.ParseVersion(s)
	return err == nil
}

// publishChannelIndex resolves the release the channel points at, checks the
// sequence against the index already there, and signs and pushes a new one.
// The warning is for the caller to print whether or not err is set.
func publishChannelIndex(ctx context.Context, source *oci.Source, key *signer.Signer, publicKey string,
	ref ports.Ref, channel string, now time.Time, validFor time.Duration, dryRun bool,
) (releaseIndexReport, string, error) {
	shown := ports.RedactRefCredentials(ref.String())
	report := releaseIndexReport{Channel: channel, Reference: shown + domain.ChannelIndexTagSuffix, DryRun: dryRun}

	// Pinned at the peek, so the bundle whose digest is written is the one
	// the tag named then, even if the tag moves before the pull.
	state, err := source.Peek(ctx, ref)
	if err != nil {
		return report, "", err
	}
	resolved, err := source.Resolve(ctx, state.Pinned)
	if err != nil {
		return report, "", err
	}

	idx := domain.NewChannelIndex(channel, resolved.Version, resolved.Digest, now, validFor)
	report.Sequence, report.IssuedAt, report.ExpiresAt, report.Release = idx.Sequence, idx.IssuedAt, idx.ExpiresAt, idx.Release

	published, found, err := source.FetchIndex(ctx, ref)
	if err != nil {
		return report, "", err
	}
	var warning string
	if found {
		prior, problem := checkedIndex(published, publicKey, channel)
		switch {
		case problem != "":
			// Only an index this key signed counts: the tag is anyone's
			// who can push, and a planted index at a high sequence must
			// not stop the vendor re-signing.
			report.Replaced = problem
			warning = fmt.Sprintf("the index already under %s %s, so it is replaced", report.Reference, problem)
		case idx.Sequence <= prior.Sequence:
			return report, "", domain.Usage("sequence %d is not higher than %d, the index already published under %s",
				idx.Sequence, prior.Sequence, report.Reference).
				WithHint("that index was issued at %s; check this machine's clock", prior.IssuedAt.Format(time.RFC3339))
		}
	}

	data, err := idx.Encode()
	if err != nil {
		return report, warning, err
	}
	sig, err := key.Sign(ctx, data, idx.Comment())
	if err != nil {
		return report, warning, err
	}
	report.KeyID = sig.KeyID
	if dryRun {
		return report, warning, nil
	}
	report.ManifestDigest, err = source.PushIndex(ctx, ref, data, sig.Encoded)
	return report, warning, err
}

// indexKeyError puts a key the signer cannot use in the vendor's terms. The
// signer's own hints are an installation's, which mints its key at `init`.
func indexKeyError(err error, path string) error {
	if errors.Is(err, domain.ErrNoSigningKey) {
		return domain.Usage("no key at %s", path).
			WithHint("pass the minisign secret key your bundles are signed with")
	}
	return domain.AsError(err).WithHint("release index takes a minisign secret key with no password; " +
		"a key with one signs the index by hand, as the publishing guide shows")
}

// checkedIndex is the published index when its signature verifies against
// publicKey and it reads as an index of channel; otherwise problem says why
// not. Each of those is one a machine following the channel would refuse.
func checkedIndex(published oci.PublishedIndex, publicKey, channel string) (domain.ChannelIndex, string) {
	if published.Problem != "" {
		return domain.ChannelIndex{}, "cannot be read: " + published.Problem
	}
	if !signer.NewChecker().Check(published.Index, published.Signature, publicKey) {
		return domain.ChannelIndex{}, "is not signed by this key"
	}
	idx, err := domain.ParseChannelIndex(published.Index)
	if err != nil {
		return domain.ChannelIndex{}, "cannot be read: " + domain.AsError(err).Message
	}
	if idx.Channel != channel {
		return domain.ChannelIndex{}, "names the channel " + idx.Channel
	}
	return idx, ""
}

func releaseIndexSummary(r releaseIndexReport) string {
	verb := "published"
	if r.DryRun {
		verb = "would publish"
	}
	return fmt.Sprintf("%s %s, sequence %d: %s %s, valid until %s",
		verb, r.Reference, r.Sequence, r.Release.Version, r.Release.Digest, r.ExpiresAt.Format(time.RFC3339))
}
