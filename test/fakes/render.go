package fakes

import (
	"os"
	"path/filepath"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/infra/atomicfs"
	"github.com/morzecrew/morzer/internal/ports"
)

// renderToDisk writes secrets with the same permissions the real adapter
// uses.
//
// The fake writes real files rather than recording intentions, because the
// contract suite asserts on the mode bits. A fake that only remembered "I was
// asked to write 0400" would let a real adapter regress to 0644 while the
// shared suite stayed green.
func renderToDisk(targetDir string, schema domain.SecretSchema, set domain.SecretSet) ([]ports.RenderedFile, error) {
	if err := atomicfs.MkdirExact(targetDir, 0o700); err != nil {
		return nil, err
	}

	root, err := atomicfs.OpenRoot(targetDir)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()

	expected := make(map[string]bool, len(schema.Secrets))
	var out []ports.RenderedFile

	for _, decl := range schema.Secrets {
		value, ok := set.Get(decl.Name)
		if !ok || value.IsEmpty() {
			continue
		}
		name := decl.FileName()
		expected[name] = true

		// The owner too, or the contract suite's owner cases would pass
		// against one implementation only.
		uid, gid := decl.Owner.IDs()
		if err := atomicfs.WriteFileInAs(root, name, []byte(value.Reveal()), 0o400, uid, gid); err != nil {
			return nil, decl.RenderError(err)
		}
		info, err := root.Stat(name)
		if err != nil {
			return nil, decl.RenderError(err)
		}
		uid, gid = atomicfs.Owner(info)
		out = append(out, ports.RenderedFile{
			Name: decl.Name,
			Path: filepath.Join(targetDir, name),
			Mode: 0o400,
			Size: value.Len(),
			UID:  uid,
			GID:  gid,
		})
	}

	// Stale files are pruned here too: a fake that skipped this would let
	// the contract suite pass while the real adapter leaked a credential
	// after a schema change.
	entries, err := os.ReadDir(targetDir)
	if err != nil {
		return out, nil
	}
	for _, e := range entries {
		if !e.IsDir() && !expected[e.Name()] {
			_ = os.Remove(filepath.Join(targetDir, e.Name()))
		}
	}

	return out, nil
}
