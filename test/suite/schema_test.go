package suite

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/goccy/go-yaml"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/schema"
)

// schemaPath locates a checked-in schema relative to this test file.
func schemaPath(t *testing.T, name string) string {
	t.Helper()
	wd, err := os.Getwd()
	require.NoError(t, err)
	return filepath.Join(wd, "..", "..", schema.Dir, name)
}

// TestCheckedInSchemasMatchTheTypes is the gate that makes generation worth
// doing.
//
// A generated file nobody regenerates is a hand-written file with extra steps.
// This fails the build the moment a manifest field is added, renamed or removed
// without `just schemas` being run, which is the only thing standing between
// "generated from the types" and a claim that used to be true.
func TestCheckedInSchemasMatchTheTypes(t *testing.T) {
	for name, generate := range map[string]func() ([]byte, error){
		schema.ManifestSchemaFile: schema.Manifest,
		schema.SecretSchemaFile:   schema.Secrets,
	} {
		t.Run(name, func(t *testing.T) {
			want, err := generate()
			require.NoError(t, err)

			got, err := os.ReadFile(schemaPath(t, name))
			require.NoError(t, err, "the schema must be checked in, not generated on demand: "+
				"a vendor validates against the published file")

			assert.Equal(t, string(want), string(got),
				"schemas/%s is stale — run `just schemas`", name)
		})
	}
}

// TestTheExampleBundleSatisfiesItsSchema checks the schema against a manifest
// the loader definitely accepts.
//
// The failure this catches is the one that matters and is easy to miss: a
// generated schema that rejects valid manifests. `additionalProperties: false`
// makes every missed field a rejection, and an editor that flags a legitimate
// field as an error is worse than no editor support at all.
//
// It walks the real manifest against the schema's property names rather than
// running a full JSON Schema validator. That is a deliberate limit -- types,
// patterns and enums are not checked here -- taken because every validator
// available brings a dependency tree larger than this program, and because the
// loader already enforces everything a validator would and more.
func TestTheExampleBundleSatisfiesItsSchema(t *testing.T) {
	cases := []struct {
		name     string
		schema   string
		document string
	}{
		{"manifest", schema.ManifestSchemaFile,
			filepath.Join(testBundlePath(t), "manifest.yaml")},
		{"secret schema", schema.SecretSchemaFile,
			filepath.Join(testBundlePath(t), "templates", "secrets.yaml")},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var doc any
			raw, err := os.ReadFile(tc.document)
			require.NoError(t, err)
			require.NoError(t, yaml.Unmarshal(raw, &doc))

			var node map[string]any
			rawSchema, err := os.ReadFile(schemaPath(t, tc.schema))
			require.NoError(t, err)
			require.NoError(t, json.Unmarshal(rawSchema, &node))

			problems := checkAgainstSchema(node, doc, "")
			assert.Empty(t, problems,
				"the example bundle is valid by definition — the test suite runs against it — "+
					"so anything the schema rejects here is the schema being wrong")
		})
	}
}

// checkAgainstSchema reports fields the schema has no place for.
//
// Only the shape the generator emits is understood: object properties,
// additionalProperties, and array items. Anything else is passed over rather
// than guessed at.
func checkAgainstSchema(node map[string]any, value any, path string) []string {
	var problems []string

	switch v := value.(type) {
	case map[string]any:
		props, _ := node["properties"].(map[string]any)
		additional, hasAdditional := node["additionalProperties"]

		for key, child := range v {
			at := join(path, key)

			if prop, ok := props[key]; ok {
				if sub, ok := prop.(map[string]any); ok {
					problems = append(problems, checkAgainstSchema(sub, child, at)...)
				}
				continue
			}

			// No named property. Either the schema allows free-form
			// keys here -- a map, extensions -- or the field has
			// nowhere to go and would be rejected.
			if !hasAdditional {
				continue
			}
			switch a := additional.(type) {
			case bool:
				if !a {
					problems = append(problems, at+": the schema has no property for this field")
				}
			case map[string]any:
				problems = append(problems, checkAgainstSchema(a, child, at)...)
			}
		}

	case []any:
		items, _ := node["items"].(map[string]any)
		if items == nil {
			return nil
		}
		for i, child := range v {
			problems = append(problems,
				checkAgainstSchema(items, child, fmt.Sprintf("%s[%d]", path, i))...)
		}
	}

	return problems
}

func join(path, key string) string {
	if path == "" {
		return key
	}
	return path + "." + key
}

// TestSchemaDescribesTheFieldsAVendorWrites guards the two places the generator
// has to be told something reflection cannot see.
func TestSchemaDescribesTheFieldsAVendorWrites(t *testing.T) {
	var node map[string]any
	raw, err := os.ReadFile(schemaPath(t, schema.ManifestSchemaFile))
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(raw, &node))

	props := object(t, node, "properties")

	apiVersion := object(t, props, "api_version")
	assert.Contains(t, apiVersion["enum"], "selfhost/v1alpha1",
		"the schema must name the api_version this manager reads")

	required, ok := node["required"].([]any)
	require.True(t, ok, "the schema must state what a manifest cannot omit")
	for _, field := range []string{"api_version", "kind", "metadata", "runtime", "images"} {
		assert.Contains(t, required, field)
	}

	// The scalar wrappers marshal to strings with their own syntax.
	// Reflecting into them would describe Go's representation rather than
	// the YAML a vendor writes, so each is mapped by hand -- and a new one
	// added without a mapping would silently render as an empty object.
	requirements := object(t, object(t, props, "requirements"), "properties")
	assert.Equal(t, "string", object(t, requirements, "memory")["type"],
		"a byte size is written as a string, e.g. 2GiB")

	operations := object(t, object(t, props, "operations"), "additionalProperties")
	timeout := object(t, object(t, operations, "properties"), "timeout")
	assert.Equal(t, "string", timeout["type"], "a duration is written as a string, e.g. 10m")

	// A vendor's own block is passed through untouched, so the schema must
	// not constrain it.
	extensions := object(t, props, "extensions")
	assert.Equal(t, "object", extensions["type"])
	assert.NotEqual(t, false, extensions["additionalProperties"],
		"extensions is deliberately free-form; a schema that closed it would "+
			"reject data the loader accepts")
}

// object descends one level, failing the test rather than panicking when the
// schema is not the shape the assertion assumes.
func object(t *testing.T, node map[string]any, key string) map[string]any {
	t.Helper()
	child, ok := node[key].(map[string]any)
	require.True(t, ok, "the schema has no object at %q", key)
	return child
}

// TestEverySchemaIsCheckedIn catches a generated file that was added to the
// generator and never written to disk.
func TestEverySchemaIsCheckedIn(t *testing.T) {
	wd, err := os.Getwd()
	require.NoError(t, err)

	entries, err := os.ReadDir(filepath.Join(wd, "..", "..", schema.Dir))
	require.NoError(t, err)

	var found []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".json") {
			found = append(found, e.Name())
		}
	}
	assert.ElementsMatch(t, []string{schema.ManifestSchemaFile, schema.SecretSchemaFile}, found,
		"schemas/ must hold exactly what the generator produces")
}
