package blob

import (
	"context"

	"github.com/morzecrew/morzer/internal/ports"
)

// Opener returns the store a call is about to use.
//
// write says whether the caller is about to put something there. It is the only
// thing an adapter has needed to vary so far: a directory target creates its
// root before a push and refuses to create one before a read, where reading a
// directory into existence would make "never published to" indistinguishable
// from "empty".
type Opener func(ctx context.Context, ref ports.TargetRef, write bool) (Store, error)

// Delegate is the half of a store-backed target that is the same for every
// transport: open the store, hand it to the function in this package that knows
// what a backup is.
//
// Three adapters had written all nine of these out, identically, and the copies
// were the kind that stay identical right up until one of them does not. What
// actually differs between a directory, an SFTP server and a bucket is how the
// store is opened, so that is what an adapter supplies.
//
// Two openers rather than one, because the difference between the halves is not
// "reading or writing". The S3 target probes the bucket for the backup half and
// deliberately skips the probe for the object half, where the prefix being
// absent is the ordinary state before the first publish.
//
// Embedded rather than called, so an adapter that has a reason to do something
// either side of one call -- localdir refusing to push a directory onto itself,
// sftp tidying an emptied directory -- overrides that method and inherits the
// other eight.
type Delegate struct {
	// Backup opens the store for the backup half: Push, List, Fetch,
	// FetchFile, Verify, Remove.
	Backup Opener

	// Object opens the store for the object half: PutObject, ObjectKeys,
	// GetObject.
	Object Opener
}

func (d Delegate) Push(ctx context.Context, ref ports.TargetRef, localDir, id string) (ports.RemoteRef, error) {
	s, err := d.Backup(ctx, ref, true)
	if err != nil {
		return ports.RemoteRef{}, err
	}
	return Push(ctx, s, ref, localDir, id)
}

func (d Delegate) List(ctx context.Context, ref ports.TargetRef) ([]ports.BackupManifest, error) {
	s, err := d.Backup(ctx, ref, false)
	if err != nil {
		return nil, err
	}
	return List(ctx, s)
}

func (d Delegate) Fetch(ctx context.Context, ref ports.RemoteRef, destDir string) error {
	s, err := d.Backup(ctx, ref.Target, false)
	if err != nil {
		return err
	}
	return Fetch(ctx, s, ref, destDir)
}

func (d Delegate) FetchFile(ctx context.Context, ref ports.RemoteRef, name, destDir string) error {
	s, err := d.Backup(ctx, ref.Target, false)
	if err != nil {
		return err
	}
	return FetchFile(ctx, s, ref, name, destDir)
}

func (d Delegate) Verify(ctx context.Context, ref ports.RemoteRef) error {
	s, err := d.Backup(ctx, ref.Target, false)
	if err != nil {
		return err
	}
	return Verify(ctx, s, ref)
}

func (d Delegate) Remove(ctx context.Context, ref ports.RemoteRef) error {
	s, err := d.Backup(ctx, ref.Target, false)
	if err != nil {
		return err
	}
	return Remove(ctx, s, ref)
}

func (d Delegate) PutObject(ctx context.Context, ref ports.TargetRef, key string, data []byte) error {
	s, err := d.Object(ctx, ref, true)
	if err != nil {
		return err
	}
	return PutObject(ctx, s, key, data)
}

func (d Delegate) ObjectKeys(ctx context.Context, ref ports.TargetRef, prefix string) ([]string, error) {
	s, err := d.Object(ctx, ref, false)
	if err != nil {
		return nil, err
	}
	return ObjectKeys(ctx, s, prefix)
}

func (d Delegate) GetObject(ctx context.Context, ref ports.TargetRef, key string) ([]byte, error) {
	s, err := d.Object(ctx, ref, false)
	if err != nil {
		return nil, err
	}
	return GetObject(ctx, s, key)
}
