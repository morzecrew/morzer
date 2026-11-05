package ops

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"slices"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/events"
)

// pruneStatements removes the oldest statements past retention.attestations
// (RFC 0049). It runs after a statement is filed and pushed, and nothing it
// does fails the operation (decision 5).
func pruneStatements(ctx context.Context, d *Deps, inst domain.Installation) {
	limit := inst.Policy.RetainAttestations
	// Below zero only by a hand edit that validation refuses; a slice bound
	// past the list would panic after the operation has run.
	if limit <= 0 {
		return
	}
	// The lock a statement is written and signed under, so no statement this
	// reads is still waiting for its signature.
	release, err := d.lockAttestations(ctx, "")
	if err != nil {
		d.warnf("cannot take the attestation lock, so no attestation is removed: %s", domain.AsError(err).Message)
		return
	}
	defer release()

	dir := d.Paths.AttestationsDir()
	local, err := localStatements(dir)
	if err != nil {
		d.warnf("cannot prune the attestations in %s: %s", dir, domain.AsError(err).Message)
		return
	}
	if len(local) <= limit {
		return
	}
	ordered := oldestFirst(local)
	keep, err := d.statementsToKeep(ctx, inst, ordered)
	if err != nil {
		d.warnf("cannot tell which attestations have left this machine, so none is removed: %s",
			domain.AsError(err).Message)
		return
	}

	// Oldest first, and only a prefix: a statement removed from between two
	// kept ones would read to `attest verify` as a release installed by
	// something that filed no statement (decision 4).
	removed := 0
	for _, s := range ordered[:len(ordered)-limit] {
		if keep[s.file] {
			break
		}
		if err := removeStatement(s.file); err != nil {
			d.warnf("cannot remove %s: %v", s.file, err)
			break
		}
		removed++
	}
	if removed > 0 && d.Bus != nil {
		d.Bus.Publish(events.Message(events.LevelInfo,
			"removed the %d oldest attestation(s) in %s: retention.attestations keeps %d", removed, dir, limit))
	}
}

// filedStatement is a statement on disk, read if it could be.
type filedStatement struct {
	file string
	stmt domain.Statement
	read bool
}

// oldestFirst orders statements as `attest verify` does, by when their
// operation started and then by id (newer). Not by name: an id is minted before
// the deployment lock, so a command that waited for the lock can start after
// one with a later id. A statement that cannot be read has no start, and sorts
// first in name order.
func oldestFirst(local []string) []filedStatement {
	out := make([]filedStatement, 0, len(local))
	for _, file := range local {
		s := filedStatement{file: file}
		if body, err := os.ReadFile(file); err == nil && json.Unmarshal(body, &s.stmt) == nil {
			s.read = true
		}
		out = append(out, s)
	}
	slices.SortStableFunc(out, func(a, b filedStatement) int {
		switch {
		case a.read != b.read:
			if a.read {
				return 1
			}
			return -1
		case !a.read:
			return 0
		case newer(b.stmt, a.stmt):
			return -1
		case newer(a.stmt, b.stmt):
			return 1
		}
		return 0
	})
	return out
}

// statementsToKeep names what the prune must not remove: every statement a
// backup target is missing, all of them when a target cannot be listed, and the
// newest successful one, which `attest verify --against-live` compares the
// deployment with (decision 4).
func (d *Deps) statementsToKeep(
	ctx context.Context, inst domain.Installation, ordered []filedStatement,
) (map[string]bool, error) {
	keep := map[string]bool{}
	// The last that succeeded, in the order newestSucceeded uses.
	for i := len(ordered) - 1; i >= 0; i-- {
		if s := ordered[i]; s.read && s.stmt.Predicate.Operation.Outcome == string(domain.StatusSucceeded) {
			keep[s.file] = true
			break
		}
	}
	if !inst.Backup.HasTargets() {
		return keep, nil
	}
	if d.Objects == nil {
		// A build with no object store pushes nothing (attestationTargets),
		// and cannot tell what a target holds.
		return nil, domain.Internal(nil, "this build cannot read the backup targets")
	}
	state, err := d.attestationPushState(ctx, inst)
	if err != nil {
		return nil, err
	}
	for _, target := range state.Targets {
		if target.Error != "" {
			// Nothing can be known about what it holds.
			for _, s := range ordered {
				keep[s.file] = true
			}
			return keep, nil
		}
		for _, s := range ordered {
			if target.missing[objectKeyFor(s.file)] {
				keep[s.file] = true
			}
		}
	}
	return keep, nil
}

// removeStatement removes the signature, then the statement, so an interrupted
// prune leaves an unsigned statement rather than a signature for nothing
// (decision 8).
func removeStatement(file string) error {
	for _, path := range []string{file + minisigExt, file} {
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	return nil
}
