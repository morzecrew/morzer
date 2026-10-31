package ops

import (
	"context"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/ports"
)

// History is the operation journal as `morzer history` reports it: the last
// record of each operation, newest first.
type History struct {
	Operations []domain.OperationRecord `json:"operations"`

	// Total is how many operations the journal holds, so a listing cut at a
	// limit says how much it left out.
	Total int `json:"total"`
}

// ListHistory reads the journal, keeping the newest limit operations, or all
// of them when limit is zero.
func ListHistory(ctx context.Context, d *Deps, limit int) (History, error) {
	recs, err := d.State.Operations(ctx, ports.Filter{})
	if err != nil {
		return History{}, err
	}
	out := History{Operations: recs, Total: len(recs)}
	if limit > 0 && len(recs) > limit {
		out.Operations = recs[:limit]
	}
	return out, nil
}

// ShowOperation reads one operation's record.
func ShowOperation(ctx context.Context, d *Deps, id string) (domain.OperationRecord, error) {
	recs, err := d.State.Operations(ctx, ports.Filter{ID: id})
	if err != nil {
		return domain.OperationRecord{}, err
	}
	if len(recs) == 0 {
		return domain.OperationRecord{}, domain.ValidationError(domain.ErrNotFound, "no operation with id %q", id).
			WithHint("run `morzer history` to see the operations this installation recorded")
	}
	return recs[0], nil
}
