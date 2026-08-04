package ports

// The Compose interpolation ABI.
//
// A bundle's Compose files may interpolate these, and nothing else: the runtime
// subprocess receives an allow-listed environment plus exactly this set, so a
// variable absent here is a variable that resolves to its `:-` fallback or to
// empty. It is a published contract for the same reason the hook ABI is -- a
// vendor writes against it and a rename breaks every bundle in the field.
//
// Declared here rather than inferred from the builder in internal/lifecycle/ops
// so `tools/docscheck` can read it without constructing a whole dependency
// graph. A test asserts the builder produces exactly this set, so the two
// cannot drift.

// ComposeVars are the fixed variable suffixes, namespaced per product as
// <PRODUCT>_<SUFFIX>.
var ComposeVars = []string{
	"DATA_DIR",
	"SECRETS_DIR",
	"CONFIG_FILE",
	"RELEASE_DIR",
	"VERSION",
	"PROFILE",
	// Only when the installation has one, which is why the builder sets it
	// conditionally and a Compose file should carry a `:-` fallback.
	"DOMAIN",
}

// ComposeVarPatterns are the two families whose names come from the manifest
// rather than from this list.
var ComposeVarPatterns = []string{
	// One per entry in `images`: app becomes IMAGE_APP.
	"IMAGE_<NAME>",
	// One per entry in `parameters`: http_port becomes PARAM_HTTP_PORT.
	"PARAM_<NAME>",
}

// ComposeVarNames renders the fixed set for a product, matching HookEnvVars.
func ComposeVarNames(product string) []string {
	e := HookEnv{Product: product}
	out := make([]string, len(ComposeVars))
	for i, suffix := range ComposeVars {
		out[i] = e.Var(suffix)
	}
	return out
}
