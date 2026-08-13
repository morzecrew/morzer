package blob

import (
	"bytes"
	"context"
	"io"
	"path"
	"strings"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/ports"
)

// The ports.ObjectStore half of a target: bytes at a key, and what keys are
// already there.
//
// Here rather than three times over, for the same reason Push and List are: the
// transports differ in how bytes move and in nothing else, and a rule enforced
// in two of three adapters is a rule that holds or not depending on which
// medium an operator happened to choose.

// PutObject writes data at key.
//
// The key is guarded here rather than trusted from the caller. Every key that
// reaches this function is built from a filename read off the local disk, and
// the directory it is read from is one an operator can put anything in -- so
// `../../authorized_keys` as a statement's name must not be a way to choose
// what a push overwrites on the far side. The stores enforce containment too;
// this refuses with a message about the file rather than about a path.
func PutObject(ctx context.Context, s Store, key string, data []byte) error {
	if err := guardObjectKey(key); err != nil {
		return err
	}
	return s.Put(ctx, key, bytes.NewReader(data), int64(len(data)))
}

// GetObject reads back what is at key, bounded.
//
// The key is guarded here for the same reason PutObject's is, and against a
// wider input: `fleet ls` builds keys from a *listing of somebody else's
// bucket*, so the names it reads back are chosen by whoever can write there.
//
// Bounded by ports.MaxObjectBytes, and a body at the limit is refused rather
// than truncated. Handing back the first megabyte of a larger object would
// produce bytes that parse as far as they go and fail a signature check for a
// reason nobody could diagnose -- the reader would be looking at half a
// document while the error talked about cryptography.
func GetObject(ctx context.Context, s Store, key string) ([]byte, error) {
	if err := guardObjectKey(key); err != nil {
		return nil, err
	}

	r, err := s.Get(ctx, key)
	if err != nil {
		// Passed through unwrapped when it is an absence: the port
		// promises errors.Is(err, fs.ErrNotExist), and a caller telling
		// "never published" from "unreachable" is the whole reason.
		return nil, err
	}
	defer func() { _ = r.Close() }()

	// One byte past the limit, so "exactly at the limit" and "larger than
	// the limit" are distinguishable. Reading only the limit would make a
	// 1 MiB object and a 1 GiB object arrive identically.
	data, err := io.ReadAll(io.LimitReader(r, ports.MaxObjectBytes+1))
	if err != nil {
		return nil, domain.BackupError(err, "cannot read the object at %s", key)
	}
	if len(data) > ports.MaxObjectBytes {
		return nil, domain.BackupError(nil,
			"the object at %s is larger than %d bytes", key, ports.MaxObjectBytes).
			WithHint("this manager writes small documents there; something else " +
				"is writing to that target")
	}
	return data, nil
}

// ObjectKeys lists what is under prefix.
func ObjectKeys(ctx context.Context, s Store, prefix string) ([]string, error) {
	if prefix != "" {
		if err := guardObjectKey(prefix); err != nil {
			return nil, err
		}
	}
	return s.Keys(ctx, prefix)
}

// guardObjectKey refuses a key that would address something outside the
// target's own root.
func guardObjectKey(key string) error {
	clean := strings.TrimSpace(key)
	if clean == "" || strings.HasPrefix(clean, "/") || HasParentComponent(clean) ||
		path.Clean(clean) != clean {
		return domain.BackupError(nil, "%q is not a key this manager writes", key).
			WithHint("object keys are relative to the target and have no `..` in them")
	}
	return nil
}
