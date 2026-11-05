package ops

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/events"
)

// pruneStatements removes the oldest statements past retention.attestations
// (RFC 0049). It runs after a statement is filed and pushed, and nothing it
// does fails the operation (decision 5). Emission runs after the lock, so two
// prunes can overlap; a statement the other one removed is not an error.
func pruneStatements(ctx context.Context, d *Deps, inst domain.Installation) {
	limit := inst.Policy.RetainAttestations
	if limit == 0 {
		return
	}
	dir := d.Paths.AttestationsDir()
	local, err := localStatements(dir)
	if err != nil {
		d.warnf("cannot prune the attestations in %s: %s", dir, domain.AsError(err).Message)
		return
	}
	if len(local) <= limit {
		return
	}
	keep, err := d.statementsToKeep(ctx, inst, local)
	if err != nil {
		d.warnf("cannot tell which attestations have left this machine, so none is removed: %s",
			domain.AsError(err).Message)
		return
	}

	// Oldest first, and only a prefix: a statement removed from between two
	// kept ones would read to `attest verify` as a release installed by
	// something that filed no statement (decision 4).
	removed := 0
	for _, file := range local[:len(local)-limit] {
		if keep[file] {
			break
		}
		if err := removeStatement(file); err != nil {
			d.warnf("cannot remove %s: %v", file, err)
			break
		}
		removed++
	}
	if removed > 0 && d.Bus != nil {
		d.Bus.Publish(events.Message(events.LevelInfo,
			"removed the %d oldest attestation(s) in %s: retention.attestations keeps %d", removed, dir, limit))
	}
}

// statementsToKeep names what the prune must not remove: every statement a
// backup target is missing, all of them when a target cannot be listed, and the
// newest successful one, which `attest verify --against-live` compares the
// deployment with (decision 4).
func (d *Deps) statementsToKeep(
	ctx context.Context, inst domain.Installation, local []string,
) (map[string]bool, error) {
	keep := map[string]bool{}
	if file, ok := newestSucceededFile(local); ok {
		keep[file] = true
	}
	if !inst.Backup.HasTargets() {
		return keep, nil
	}
	state, err := d.attestationPushState(ctx, inst)
	if err != nil {
		return nil, err
	}
	for _, target := range state.Targets {
		if target.Error != "" {
			// Nothing can be known about what it holds.
			for _, file := range local {
				keep[file] = true
			}
			return keep, nil
		}
		for _, file := range local {
			if target.missing[objectKeyFor(file)] {
				keep[file] = true
			}
		}
	}
	return keep, nil
}

// newestSucceededFile is the file of the statement `--against-live` compares
// against, chosen by the function that command uses.
func newestSucceededFile(local []string) (string, bool) {
	parsed := make([]domain.Statement, 0, len(local))
	files := make(map[string]string, len(local))
	for _, file := range local {
		body, err := os.ReadFile(file)
		if err != nil {
			continue
		}
		var stmt domain.Statement
		if json.Unmarshal(body, &stmt) != nil {
			continue
		}
		parsed = append(parsed, stmt)
		files[stmt.Predicate.Operation.ID] = file
	}
	newest, ok := newestSucceeded(parsed)
	if !ok {
		return "", false
	}
	return files[newest.Predicate.Operation.ID], true
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
