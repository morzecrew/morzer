package ops

import (
	"context"

	"github.com/morzecrew/morzer/internal/domain"
)

// Describe reads a live installation and assembles the document that describes
// it (RFC 0027 P1).
//
// A read, not an operation: no lock, no journal, no steps. It is `status`'s kind
// of command rather than `apply`'s -- nothing about producing a description
// changes anything, and taking the deployment lock to write a file would make
// an operator's documentation gesture contend with their deployment.
//
// Every part degrades on its own, for the reason `status` does: an installation
// with no release yet still has an answer to "what is this machine", and a
// secret store that cannot be opened costs the list of names and nothing else.
// A description that refused whenever one piece was missing would be useless in
// exactly the situations where somebody wants to write down what they have.
func Describe(ctx context.Context, d *Deps) (domain.InstallationDocument, error) {
	inst, err := d.loadInstallation(ctx)
	if err != nil {
		return domain.InstallationDocument{}, err
	}

	return inst.Describe(describedRelease(ctx, d), secretNames(ctx, d)), nil
}

// describedRelease reads the release pointer, tolerating its absence.
//
// An installation between `init` and the first `apply` has none, and that is a
// state the document has to be able to describe rather than refuse.
func describedRelease(ctx context.Context, d *Deps) domain.DescribedRelease {
	rec, err := d.State.CurrentRelease(ctx)
	if err != nil || rec.IsZero() {
		return domain.DescribedRelease{}
	}
	return domain.DescribedRelease{
		Name:    rec.Name,
		Version: rec.Version,
		Digest:  rec.Digest,
		Ref:     rec.SourceRef,
	}
}

// secretNames lists what `secret set` would have to be run for.
//
// Metadata rather than Load: the names are what the document carries, and
// reading the values to list their names would decrypt the state for no reason
// -- which is the sort of avoidable decryption 0003 bounds deliberately.
func secretNames(ctx context.Context, d *Deps) []string {
	if d.Secrets == nil {
		return nil
	}
	meta, err := d.Secrets.Metadata(ctx)
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(meta))
	for _, m := range meta {
		names = append(names, m.Name)
	}
	return names
}
