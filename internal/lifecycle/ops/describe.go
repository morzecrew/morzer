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
// An installation with no release yet, or with no secrets yet, still has an
// answer to "what is this machine", and this produces one -- a description that
// refused until a release existed would be useless in the case where somebody
// most wants to write down what they have.
//
// What it will *not* do is treat "cannot read" as "there is none". This
// document is written to be committed, so it outlives the run that produced it;
// a file recording `secrets: []` because the store would not open is a false
// record somebody reviews next quarter. Absent is absent and broken is broken,
// and the state layer already tells them apart.
func Describe(ctx context.Context, d *Deps) (domain.InstallationDocument, error) {
	inst, err := d.loadInstallation(ctx)
	if err != nil {
		return domain.InstallationDocument{}, err
	}

	release, err := describedRelease(ctx, d)
	if err != nil {
		return domain.InstallationDocument{}, err
	}
	names, err := secretNames(ctx, d)
	if err != nil {
		return domain.InstallationDocument{}, err
	}

	return inst.Describe(release, names), nil
}

// describedRelease reads the release pointer, tolerating its absence and not
// its corruption.
//
// The state layer already draws that line -- an absent pointer is `(zero, nil)`
// because `status` on a fresh install must work, and an unreadable one is an
// error -- so collapsing both into an empty release here would be this code
// throwing away a distinction the layer below took care to make.
func describedRelease(ctx context.Context, d *Deps) (domain.DescribedRelease, error) {
	rec, err := d.State.CurrentRelease(ctx)
	if err != nil {
		return domain.DescribedRelease{}, err
	}
	if rec.IsZero() {
		return domain.DescribedRelease{}, nil
	}
	return domain.DescribedRelease{
		Name:    rec.Name,
		Version: rec.Version,
		Digest:  rec.Digest,
		Ref:     rec.SourceRef,
	}, nil
}

// secretNames lists what `secret set` would have to be run for.
//
// Metadata rather than Load: the names are what the document carries, and
// reading the values to list their names would decrypt the state for no reason
// -- which is the sort of avoidable decryption 0003 bounds deliberately.
//
// An uninitialised store is a machine with no secrets yet, and the document says
// so. A store that is initialised and will not answer is a refusal: the
// alternative is a committed file asserting that an installation has no secrets
// when nobody ever established that.
func secretNames(ctx context.Context, d *Deps) ([]string, error) {
	if d.Secrets == nil {
		return nil, nil
	}
	ready, err := d.Secrets.Initialized(ctx)
	if err != nil {
		return nil, err
	}
	if !ready {
		return nil, nil
	}

	meta, err := d.Secrets.Metadata(ctx)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(meta))
	for _, m := range meta {
		names = append(names, m.Name)
	}
	return names, nil
}
