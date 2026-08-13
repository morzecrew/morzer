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

	"github.com/morzecrew/morzer/internal/domain"
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
		schema.ManifestSchemaFile:             schema.Manifest,
		schema.SecretSchemaFile:               schema.Secrets,
		schema.InstallationDocumentSchemaFile: schema.InstallationDocument,
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
			filepath.Join(testBundlePath(t), "secrets.schema.yaml")},
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

// TestTheInstallationSchemaConstrainsWhatTheWriterProduces.
//
// Generated from struct shape alone, the schema said every field was optional
// and any string was a valid `api_version` -- so `{}` validated, and so did a
// manifest, and so did a document from a contract that does not exist. A schema
// that accepts everything is worse than no schema: it is a green check beside a
// file nobody validated.
//
// The other half is the trap in stating `required` at all: the writer must
// actually emit all of them, on the emptiest installation there can be. An
// `omitempty` added to one of these fields would turn this schema into one that
// rejects the command's own output.
func TestTheInstallationSchemaConstrainsWhatTheWriterProduces(t *testing.T) {
	var node map[string]any
	raw, err := os.ReadFile(schemaPath(t, schema.InstallationDocumentSchemaFile))
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(raw, &node))

	props := object(t, node, "properties")
	assert.Contains(t, object(t, props, "api_version")["enum"], "selfhost/v1alpha1")
	assert.Equal(t, []any{domain.KindInstallationDocument}, object(t, props, "kind")["enum"],
		"a document of another kind must not validate against this schema")

	required, ok := node["required"].([]any)
	require.True(t, ok, "the schema must state what the writer never omits")

	// The emptiest document the command can produce: an installation
	// between `init` and the first `apply`, which is a case the CLI suite
	// covers precisely because it is the one somebody most wants to
	// document.
	empty, err := json.Marshal(domain.Installation{}.Describe(domain.DescribedRelease{}, nil))
	require.NoError(t, err)
	var emitted map[string]any
	require.NoError(t, json.Unmarshal(empty, &emitted))

	for _, field := range required {
		name, _ := field.(string)
		assert.Contains(t, emitted, name,
			"the schema requires %q, and a bare installation's document does not carry it", name)
	}
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
	assert.ElementsMatch(t, []string{
		schema.ManifestSchemaFile,
		schema.SecretSchemaFile,
		schema.InstallationDocumentSchemaFile,
		schema.AttestationSchemaFile,
	}, found,
		"schemas/ must hold exactly what the generator produces")
}

// TestTheAttestationSchemaConstrainsWhatTheWriterProduces is the same guard for
// the statement, and it has one extra thing to pin.
//
// The schema must reject a document from another contract. An in-toto Statement
// generated from struct shape alone accepts any `predicateType`, so it would
// validate a SLSA provenance document — which is exactly the confusion morzer's
// own predicate type exists to prevent, since SLSA describes how an artifact
// was *built* and this describes how one was *deployed*.
//
// And the required set has to be one the writer actually emits, on the emptiest
// statement there can be: an operation with no release, no images and no steps,
// on a machine that has never signed. `bound` is in that set deliberately — a
// statement without it is one whose reader was never told what the signature
// proves.
func TestTheAttestationSchemaConstrainsWhatTheWriterProduces(t *testing.T) {
	var node map[string]any
	raw, err := os.ReadFile(schemaPath(t, schema.AttestationSchemaFile))
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(raw, &node))

	props := object(t, node, "properties")
	assert.Equal(t, []any{domain.StatementType}, object(t, props, "_type")["enum"])
	assert.Equal(t, []any{domain.PredicateType}, object(t, props, "predicateType")["enum"],
		"a document with another predicate type must not validate against this schema")

	required, ok := node["required"].([]any)
	require.True(t, ok, "the schema must state what the writer never omits")

	// The emptiest statement the manager can produce.
	empty, err := json.Marshal(domain.Attest(domain.OperationRecord{}, domain.AttestationInputs{}))
	require.NoError(t, err)
	var emitted map[string]any
	require.NoError(t, json.Unmarshal(empty, &emitted))

	for _, field := range required {
		name, _ := field.(string)
		assert.Contains(t, emitted, name,
			"the schema requires %q, and the barest statement does not carry it", name)
	}

	predicate := object(t, props, "predicate")
	predRequired, ok := predicate["required"].([]any)
	require.True(t, ok, "the predicate must state what it never omits")

	emittedPredicate, ok := emitted["predicate"].(map[string]any)
	require.True(t, ok)
	for _, field := range predRequired {
		name, _ := field.(string)
		assert.Contains(t, emittedPredicate, name,
			"the predicate requires %q, and the barest statement does not carry it", name)
	}

	// The bound is not merely present, it is the sentence. A statement that
	// carried an empty string here would satisfy `required` and tell the
	// reader nothing.
	assert.Equal(t, domain.AttestationBound, emittedPredicate["bound"])

	// And the absence that matters: an operation that established no
	// signature verification must not render the field at all, because a
	// `false` there reads as "checked, and it failed".
	verification, ok := emittedPredicate["verification"].(map[string]any)
	require.True(t, ok)
	assert.NotContains(t, verification, "signature_verified",
		"an unestablished check was rendered as a finding")
}
