package suite

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/goccy/go-yaml"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/release"
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

	// What a manifest cannot omit is checked against the loader, below,
	// rather than against a second hand-written list.

	// The scalar wrappers marshal to strings with their own syntax.
	// Reflecting into them would describe Go's representation rather than
	// the YAML a vendor writes, so each is mapped by hand -- and a new one
	// added without a mapping would silently render as an empty object.
	requirements := object(t, object(t, props, "requirements"), "properties")
	assert.Equal(t, "string", object(t, requirements, "memory")["type"],
		"a byte size is written as a string, e.g. 2GiB")

	// An operation is one step or, for migrate, a list of them, and the
	// first spelling is the one every bundle written so far uses.
	operations := object(t, object(t, props, "operations"), "additionalProperties")
	spellings, ok := operations["oneOf"].([]any)
	require.True(t, ok, "an operation's two spellings are not both stated")
	require.Len(t, spellings, 2)
	step, ok := spellings[0].(map[string]any)
	require.True(t, ok)
	timeout := object(t, object(t, step, "properties"), "timeout")
	assert.Equal(t, "string", timeout["type"], "a duration is written as a string, e.g. 10m")
	list, ok := spellings[1].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "array", list["type"])
	assert.Equal(t, step, list["items"], "a step in a list is not the same shape as a step alone")

	// A vendor's own block is passed through untouched, so the schema must
	// not constrain it.
	extensions := object(t, props, "extensions")
	assert.Equal(t, "object", extensions["type"])
	assert.NotEqual(t, false, extensions["additionalProperties"],
		"extensions is deliberately free-form; a schema that closed it would "+
			"reject data the loader accepts")
}

// The schema's `required` and the loader's refusals are two statements of one
// rule, and nothing held them together: the list was written by hand, so it
// went on requiring `runtime` after the loader stopped reading it in 0.3.0, and
// never required the `runtimes` the loader refuses a manifest without. Anything
// validating against the published file -- an editor through `$schema`, a
// vendor's CI -- rejected every bundle this manager accepts.
//
// So each top-level key of the example manifest is removed in turn, and the
// loader has to refuse the result exactly when the schema calls the key
// required.
func TestTheManifestSchemaRequiresWhatTheLoaderRefusesWithout(t *testing.T) {
	var node map[string]any
	raw, err := os.ReadFile(schemaPath(t, schema.ManifestSchemaFile))
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(raw, &node))

	list, ok := node["required"].([]any)
	require.True(t, ok, "the schema must state what a manifest cannot omit")
	required := map[string]bool{}
	for _, field := range list {
		name, ok := field.(string)
		require.True(t, ok, "a required entry is not a field name: %v", field)
		required[name] = true
	}

	data, err := os.ReadFile(filepath.Join(testBundlePath(t), "manifest.yaml"))
	require.NoError(t, err)
	var doc map[string]any
	require.NoError(t, yaml.Unmarshal(data, &doc))

	parse := func(doc map[string]any) error {
		out, err := yaml.Marshal(doc)
		require.NoError(t, err)
		_, err = release.ParseManifest(out, "manifest.yaml")
		return err
	}
	// Through the same round trip as every case below, or a value the
	// re-encoding mangles would read as a key the loader requires.
	require.NoError(t, parse(doc), "the example manifest has to load before "+
		"removing anything from it means something")

	for key := range doc {
		t.Run(key, func(t *testing.T) {
			without := maps.Clone(doc)
			delete(without, key)
			err := parse(without)
			if required[key] {
				assert.Error(t, err, "the schema requires %s and the loader "+
					"accepts a manifest without it", key)
			} else {
				assert.NoError(t, err, "the loader refuses a manifest without %s "+
					"and the schema does not require it", key)
			}
		})
	}
	for key := range required {
		_, carried := doc[key]
		assert.True(t, carried, "the schema requires %s, which no manifest "+
			"the loader accepts here carries", key)
	}

	// Refused by the loader, so an editor should underline it too.
	_, listed := object(t, node, "properties")["runtime"]
	assert.False(t, listed, "the schema offers `runtime:`, which the loader refuses")
}

// Present is not the same as non-empty. `runtimes: {}`, `images: {}` and a
// runtime with `files: []` are each refused by the loader, and a schema that
// only requires the key accepts all three -- so each is emptied in turn, the
// loader has to refuse it, and the schema has to say it may not be empty.
func TestTheManifestSchemaRefusesWhatTheLoaderRefusesEmpty(t *testing.T) {
	var node map[string]any
	raw, err := os.ReadFile(schemaPath(t, schema.ManifestSchemaFile))
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(raw, &node))
	props := object(t, node, "properties")

	data, err := os.ReadFile(filepath.Join(testBundlePath(t), "manifest.yaml"))
	require.NoError(t, err)

	for _, tc := range []struct {
		name   string
		empty  func(t *testing.T, doc map[string]any)
		node   map[string]any
		bound  string
		reason string
	}{
		{
			name:  "runtimes",
			empty: func(_ *testing.T, doc map[string]any) { doc["runtimes"] = map[string]any{} },
			node:  object(t, props, "runtimes"), bound: "minProperties",
		},
		{
			name:  "images",
			empty: func(_ *testing.T, doc map[string]any) { doc["images"] = map[string]any{} },
			node:  object(t, props, "images"), bound: "minProperties",
		},
		{
			name: "a runtime's files",
			empty: func(t *testing.T, doc map[string]any) {
				object(t, object(t, doc, "runtimes"), "compose")["files"] = []any{}
			},
			node: object(t, object(t, object(t, object(t, props, "runtimes"),
				"additionalProperties"), "properties"), "files"),
			bound: "minItems",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var doc map[string]any
			require.NoError(t, yaml.Unmarshal(data, &doc))
			tc.empty(t, doc)
			out, err := yaml.Marshal(doc)
			require.NoError(t, err)
			_, err = release.ParseManifest(out, "manifest.yaml")
			require.Error(t, err, "the loader accepts %s empty, so there is nothing to agree with", tc.name)

			assert.EqualValues(t, 1, tc.node[tc.bound],
				"the schema accepts %s empty, which the loader refuses", tc.name)
		})
	}
}

// TestTheManifestSchemaNamesOnlyTheOperationsTheLoaderRuns: an operation under
// any other name loads nowhere, and an editor underlining `smoke-test` is the
// earliest a vendor can learn it would never have run.
func TestTheManifestSchemaNamesOnlyTheOperationsTheLoaderRuns(t *testing.T) {
	var node map[string]any
	raw, err := os.ReadFile(schemaPath(t, schema.ManifestSchemaFile))
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(raw, &node))

	operations := object(t, object(t, node, "properties"), "operations")
	enum, ok := object(t, operations, "propertyNames")["enum"].([]any)
	require.True(t, ok, "operations has no closed list of names")
	var names []string
	for _, n := range enum {
		names = append(names, fmt.Sprint(n))
	}
	assert.Equal(t, domain.OperationNames, names)
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
		// Kept, never regenerated: statements written before v2
		// validate against it.
		schema.AttestationV1SchemaFile,
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

// A check made at staging is three facts, and the writer always emits all
// three. Without them required, `{}` validates as one -- a provenance record
// naming no key, no operation and no time.
func TestAStagedSignatureCheckNamesItsKeyOperationAndTime(t *testing.T) {
	var node map[string]any
	raw, err := os.ReadFile(schemaPath(t, schema.AttestationSchemaFile))
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(raw, &node))

	verification := object(t, object(t, object(t, object(t, node, "properties"), "predicate"), "properties"), "verification")
	staged := object(t, object(t, verification, "properties"), "signature_verified_when_staged")
	required, _ := staged["required"].([]any)
	assert.ElementsMatch(t, []any{"key_id", "operation_id", "at"}, required,
		"an empty staged check validates")
}
