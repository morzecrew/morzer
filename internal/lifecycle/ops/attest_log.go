package ops

import (
	"context"
	"encoding/json"
	"os"
	"sort"

	"github.com/morzecrew/morzer/internal/domain"
)

// `morzer attest log` — the local record, newest first.
//
// Deliberately not `verify` with less output. This reads what the statements
// *say*; verify establishes whether to believe them, which needs a key and can
// fail. An operator asking "what has this machine done" during an incident
// should not have their answer withheld because a signature is missing, and an
// operator asking "can I trust this" should not be answered by a listing.

// LogEntry is one statement as a listing shows it.
type LogEntry struct {
	Operation string      `json:"operation"`
	Kind      string      `json:"kind"`
	Outcome   string      `json:"outcome"`
	Started   domain.Time `json:"started"`

	// From and To are the versions the operation moved between. Empty for
	// an operation that moved neither -- an `apply` or a `config`.
	From string `json:"from_version,omitempty"`
	To   string `json:"to_version,omitempty"`

	// Signed is whether a detached signature sits beside the document. Not
	// whether it verifies: that is `attest verify`, and saying "signed"
	// here for a signature nobody checked would be the overclaim RFC 0025
	// §4.3 exists to refuse.
	Signed bool `json:"signed"`

	File string `json:"file"`

	// Unreadable marks a file in the directory that is not a statement,
	// reported rather than skipped: something unparseable among the audit
	// records is itself worth seeing.
	Unreadable string `json:"unreadable,omitempty"`
}

// AttestLog reads this installation's statements, newest first.
func AttestLog(ctx context.Context, d *Deps, opts VerifyOptions) ([]LogEntry, error) {
	path := opts.Path
	if path == "" {
		path = d.Paths.AttestationsDir()
	}

	files, err := statementFiles(path)
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, domain.InstallationError(domain.ErrNotFound, "no attestations at %s", path).
			WithHint("statements are written after each operation; " +
				"an installation that has not run one since upgrading has none")
	}

	out := make([]LogEntry, 0, len(files))
	for _, file := range files {
		entry := LogEntry{File: file, Signed: hasSignature(file)}

		body, err := os.ReadFile(file)
		if err != nil {
			entry.Unreadable = domain.AsError(err).Message
			out = append(out, entry)
			continue
		}

		var stmt domain.Statement
		if err := json.Unmarshal(body, &stmt); err != nil {
			entry.Unreadable = "not a JSON statement: " + err.Error()
			out = append(out, entry)
			continue
		}

		entry.Operation = stmt.Predicate.Operation.ID
		entry.Kind = stmt.Predicate.Operation.Kind
		entry.Outcome = stmt.Predicate.Operation.Outcome
		entry.Started = stmt.Predicate.Operation.Started
		entry.From = stmt.Predicate.Release.FromVersion
		entry.To = stmt.Predicate.Release.ToVersion
		out = append(out, entry)
	}

	// By the operation's own start time, not by filename. A directory an
	// auditor assembled by hand need not be this machine's directory at
	// all, so ordering by what the documents say keeps the answer about the
	// history rather than about how the files were named.
	//
	// Ties are broken by id, and that is for **determinism, not for
	// truth**. Ids are ULIDs minted from a random tail, so two statements
	// sharing a timestamp cannot be ordered by their ids in any meaningful
	// sense -- what this buys is that the same directory always prints in
	// the same order, rather than flapping between runs and making an
	// operator wonder what changed. Operations serialise on the deployment
	// lock, so a real tie means two machines' records in one directory.
	//
	// An unreadable file carries no time and sorts last, where it stays
	// visible rather than leading a listing nobody reads past.
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Unreadable != "" || out[j].Unreadable != "" {
			return out[j].Unreadable != "" && out[i].Unreadable == ""
		}
		if !out[i].Started.Equal(out[j].Started.Time) {
			return out[i].Started.After(out[j].Started.Time)
		}
		return out[i].Operation > out[j].Operation
	})
	return out, nil
}

// hasSignature reports whether a detached signature sits beside a statement.
func hasSignature(file string) bool {
	_, err := os.Stat(file + minisigExt)
	return err == nil
}
