// Package verify composes verifiers.
//
// A bundle has to satisfy several independent claims -- it is the artifact that
// was pinned, its own checksum manifest matches its contents, and a key the
// operator trusts signed that manifest -- and each is a different adapter
// answering a different question. Composing them means adding a fourth claim is
// adding a verifier, not editing the one that already exists.
package verify

import (
	"context"
	"strings"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/ports"
)

// Chain runs every verifier and refuses if any of them does.
//
// There is no "any one passing is enough" variant and there should not be: the
// verifiers answer different questions, so satisfying one says nothing about
// the others. A bundle whose digest matches but whose signature does not is
// exactly the bundle this exists to refuse.
type Chain struct {
	verifiers []ports.Verifier
}

var _ ports.Verifier = (*Chain)(nil)

func NewChain(verifiers ...ports.Verifier) *Chain {
	out := make([]ports.Verifier, 0, len(verifiers))
	for _, v := range verifiers {
		if v != nil {
			out = append(out, v)
		}
	}
	return &Chain{verifiers: out}
}

// Name joins the members, so a journal record says what was actually checked
// rather than the word "chain".
func (c *Chain) Name() string {
	names := make([]string, 0, len(c.verifiers))
	for _, v := range c.verifiers {
		names = append(names, v.Name())
	}
	return strings.Join(names, "+")
}

// Verify runs every member in order, stopping at the first refusal.
//
// Stopping rather than collecting: unlike manifest validation, where an author
// wants every problem at once, a failed verification means "do not install
// this" and the remaining answers do not change that.
func (c *Chain) Verify(ctx context.Context, bundle ports.BundlePath, expect ports.Expectation) error {
	if len(c.verifiers) == 0 {
		// An empty chain would verify everything by verifying nothing,
		// which is the one failure mode a verification step must not
		// have.
		return domain.Internal(nil, "no verifiers are configured")
	}

	for _, v := range c.verifiers {
		if err := v.Verify(ctx, bundle, expect); err != nil {
			return err
		}
	}
	return nil
}
