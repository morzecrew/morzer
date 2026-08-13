package blob

import (
	"bytes"
	"context"
	"path"
	"strings"

	"github.com/morzecrew/morzer/internal/domain"
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
