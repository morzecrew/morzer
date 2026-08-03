// Command schemagen writes the generated JSON Schemas into schemas/.
//
// The generation itself lives in internal/schema, where it is under test. This
// is the thin edge that puts the result on disk:
//
//	go run ./tools/schemagen            # write schemas/
//	go run ./tools/schemagen --stdout   # print the manifest schema
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/morzecrew/morzer/internal/schema"
)

func main() {
	toStdout := flag.Bool("stdout", false, "print the manifest schema instead of writing files")
	flag.Parse()

	manifest, err := schema.Manifest()
	if err != nil {
		fail(err)
	}
	if *toStdout {
		fmt.Print(string(manifest))
		return
	}

	secrets, err := schema.Secrets()
	if err != nil {
		fail(err)
	}

	root, err := repoRoot()
	if err != nil {
		fail(err)
	}
	dir := filepath.Join(root, schema.Dir)
	// 0755: these are published artifacts a vendor reads, checked into the
	// repository and shipped in the release archive.
	if err := os.MkdirAll(dir, 0o755); err != nil { //nolint:gosec // a public schema directory
		fail(err)
	}

	for name, data := range map[string][]byte{
		schema.ManifestSchemaFile: manifest,
		schema.SecretSchemaFile:   secrets,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
			fail(err)
		}
		fmt.Printf("wrote %s\n", filepath.Join(schema.Dir, name))
	}
}

func fail(err error) {
	fmt.Fprintf(os.Stderr, "schemagen: %v\n", err)
	os.Exit(1)
}

// repoRoot walks up to the module root so the generator can be run from
// anywhere.
func repoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("no go.mod above %s", dir)
		}
		dir = parent
	}
}
