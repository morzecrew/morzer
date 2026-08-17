package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/infra/atomicfs"
	"github.com/morzecrew/morzer/internal/lifecycle/ops"
	"github.com/morzecrew/morzer/internal/ports"
	"github.com/morzecrew/morzer/internal/release"
	"github.com/morzecrew/morzer/internal/ui/views"
)

func newReleaseNewCommand(app *App) *cobra.Command {
	var name, vendor string

	cmd := &cobra.Command{
		Use:   "new <dir>",
		Short: "Scaffold a bundle skeleton that already verifies",
		Long: "Writes a bundle that passes `release verify` with no edits, carrying the\n" +
			"conventions the documentation teaches: the schema modeline, templates\n" +
			"named .yaml.tmpl, and the secret schema outside templates/.\n\n" +
			"A skeleton, not a product. It guesses no architecture, infers no\n" +
			"services and stubs no hooks — a generated bundle that pretended to know\n" +
			"your product would be work to un-write.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, err := filepath.Abs(args[0])
			if err != nil {
				return domain.ValidationError(err, "cannot resolve %s", args[0])
			}
			if name == "" {
				name = filepath.Base(dir)
			}
			// Validated before anything is written, and against the
			// same rule the manifest enforces: the name becomes
			// /etc/<name> on someone's machine, so a scaffold that
			// accepted one the loader refuses would produce a
			// bundle whose first `verify` fails.
			if err := domain.ValidateProductName(name); err != nil {
				return err
			}

			files := scaffoldFiles(name, vendor)
			if err := writeScaffold(dir, files, app.Flags.dryRun); err != nil {
				return err
			}

			if app.Flags.dryRun {
				app.finish(ops.Result{
					Summary: fmt.Sprintf("would write %d file(s) into %s", len(files), dir)})
				return nil
			}

			// The scaffold's own output is verified, not assumed.
			// This is what couples the generator to the verifier in
			// both directions: a scaffold that drifts fails here,
			// and so does a verifier that grows stricter than the
			// scaffold.
			if _, err := release.Load(dir); err != nil {
				return domain.Internal(err,
					"the scaffolded bundle does not load, which is a bug in the scaffold")
			}

			if app.json != nil {
				app.jsonData = map[string]any{"root": dir, "name": name}
				return nil
			}
			if err := app.render(views.Value{Value: dir}); err != nil {
				return err
			}
			app.finish(ops.Result{
				Summary: fmt.Sprintf(
					"scaffolded %s; edit the TODOs, then `morzer release build %s --version 0.1.0`",
					name, args[0])})
			return nil
		},
	}

	cmd.Flags().StringVar(&name, "name", "",
		"product name; defaults to the directory's own name")
	cmd.Flags().StringVar(&vendor, "vendor", "",
		"who publishes this release")
	return cmd
}

// writeScaffold creates the tree, refusing to write over anything.
//
// Refusing rather than merging or overwriting: `release new` into a directory
// that already holds a bundle would silently replace a vendor's manifest with a
// skeleton, and there is no undo for that on a machine with no VCS.
func writeScaffold(dir string, files map[string]string, dryRun bool) error {
	for _, rel := range sortedScaffoldPaths(files) {
		path := filepath.Join(dir, filepath.FromSlash(rel))
		if _, err := os.Stat(path); err == nil {
			return domain.ValidationError(nil, "%s already exists", path).
				WithHint("scaffold into an empty directory; nothing here is overwritten")
		}
	}
	if dryRun {
		return nil
	}

	// Written with a rollback rather than left where it fell. A half-written
	// scaffold is worse than none: the retry meets the files the failed
	// attempt created and refuses over them, so the operator has to work out
	// which of the files in front of them this command wrote before they can
	// try again.
	//
	// Only paths this call created are removed, and only on the failure
	// path -- never a file that was already there, which is what the check
	// above has just established cannot be any of these.
	var written []string
	for _, rel := range sortedScaffoldPaths(files) {
		path := filepath.Join(dir, filepath.FromSlash(rel))
		if err := atomicfs.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return rollbackScaffold(written, err)
		}
		if err := atomicfs.WriteFile(path, []byte(files[rel]), 0o644); err != nil {
			return rollbackScaffold(written, err)
		}
		written = append(written, path)
	}
	return nil
}

// rollbackScaffold removes what a failed scaffold managed to write.
//
// A failure to clean up does not replace the failure that caused it: the
// operator needs to know why the scaffold stopped, and "and also could not tidy
// up" is a detail on that, not a substitute for it.
func rollbackScaffold(written []string, cause error) error {
	for _, path := range written {
		_ = os.Remove(path)
	}
	return cause
}

func sortedScaffoldPaths(files map[string]string) []string {
	out := make([]string, 0, len(files))
	for rel := range files {
		out = append(out, rel)
	}
	// Sorted so the existence check and the write visit the same order,
	// and so a failure reports the same file twice in a row rather than a
	// different one each run.
	sort.Strings(out)
	return out
}

// scaffoldFiles is the skeleton, keyed by bundle-relative path.
func scaffoldFiles(name, vendor string) map[string]string {
	if vendor == "" {
		vendor = "TODO"
	}
	sub := func(text string) string {
		text = strings.ReplaceAll(text, "__NAME__", name)
		// The same prefix Compose interpolation and the hook ABI use,
		// taken from the one definition rather than upcased here: two
		// spellings of <PRODUCT>_ is how a scaffolded Compose file
		// comes to reference a variable nothing sets.
		text = strings.ReplaceAll(text, "__ENV__", ports.HookEnv{Product: name}.Prefix())
		return strings.ReplaceAll(text, "__VENDOR__", vendor)
	}

	return map[string]string{
		release.ManifestFileName:     sub(scaffoldManifest),
		release.VersionFileName:      "0.0.0\n",
		release.ReleaseNotesFileName: sub(scaffoldReleaseNotes),
		"secrets.schema.yaml":        sub(scaffoldSecretSchema),
		"compose/compose.yaml":       sub(scaffoldCompose),
		"templates/app.yaml.tmpl":    sub(scaffoldTemplate),
	}
}

// placeholderDigest is a real, syntactically valid digest that refers to
// nothing.
//
// The manifest requires every image to be pinned by digest, so the skeleton
// has to carry one to verify at all -- and a plausible-looking digest would be
// worse than an obvious placeholder, because the failure it causes is a pull
// error rather than a TODO nobody removed.
const placeholderDigest = "sha256:0000000000000000000000000000000000000000000000000000000000000000"

const scaffoldManifest = `# yaml-language-server: $schema=https://morzecrew.github.io/morzer/schemas/selfhost-v1alpha1-manifest.json
#
# A skeleton. It verifies as it stands and deploys nothing useful, which is the
# honest starting point -- every TODO below is a decision only you can make.
api_version: selfhost/v1alpha1
kind: application-release

metadata:
  name: __NAME__
  # A placeholder, deliberately. ` + "`morzer release build --version X`" + ` stamps this
  # and VERSION together; hand-maintaining the one field the tooling owns is how
  # the two come to disagree.
  version: 0.0.0
  description: TODO -- one line, shown by ` + "`release show`" + `
  vendor: __VENDOR__
  release_notes: RELEASE.md
  # support_url: https://support.example/__NAME__

providers:
  runtime: {name: compose}

runtimes:
  compose:
    files:
      - compose/compose.yaml
    options:
      # The namespace Compose puts every volume, network and container in.
      # Changing it later points the deployment at storage nothing has written
      # to, so a manager that has recorded it refuses the change.
      project: __NAME__

compatibility:
  # ` + "`runtimes:`" + ` is not a field a manager older than this one knows, and under
  # strict decoding an unknown field refuses the whole manifest. Declaring the
  # floor is what turns that into "you need a newer manager" instead of a
  # report about a typo.
  min_manager_version: ` + domain.RuntimesMinManagerVersion + `
  # rollback_safe: true
  # upgrade_from: ">=0.1.0"

requirements:
  memory: 512MiB
  disk: 1GiB

images:
  # TODO: your published image, pinned by digest. A tag would make the release
  # mutable, and a mutable release makes rollback meaningless.
  app: registry.example/__NAME__/app@` + placeholderDigest + `

configuration:
  - template: templates/app.yaml.tmpl
    target: /etc/__NAME__/app.yaml
    mode: "0640"

secrets:
  source: /etc/__NAME__/secrets.sops.yaml
  schema: secrets.schema.yaml

# health:
#   checks:
#     - name: api
#       type: http
#       url: http://127.0.0.1:8080/health
#       start_period: 90s
`

const scaffoldSecretSchema = `# yaml-language-server: $schema=https://morzecrew.github.io/morzer/schemas/selfhost-v1alpha1-secrets.json
#
# What secrets this product needs. Declaring them here is what lets ` + "`init`" + `
# provision them and ` + "`doctor`" + ` audit them without knowing anything else about
# the product.
#
# It lives at the bundle root rather than under templates/ because nothing
# renders it: the manager reads it.
api_version: selfhost/v1alpha1

secrets:
  - name: app_secret_key
    description: TODO -- what this signs or encrypts
    required: true
    generator:
      kind: hex
      length: 32
    services: [app]
`

const scaffoldTemplate = `# Rendered into /etc/__NAME__/app.yaml with this installation's own facts.
#
# Unknown keys are an error rather than an empty string, so a typo here fails
# the deployment instead of shipping a config that looks fine and is not.
server:
  url: {{ .Installation.URL }}

# Secrets reach a config file as *paths*, never as values: a credential in
# /etc is a credential in every backup and every support bundle.
secrets:
  app_secret_key_file: {{ secretFile .Secrets "app_secret_key" }}
`

const scaffoldCompose = `# One service, because a scaffold that guessed your architecture would produce
# work to un-write.
services:
  app:
    # The manager exports every manifest image as __ENV___IMAGE_<NAME>, so the
    # digest lives in the manifest -- the file the manager verifies -- rather
    # than in two places that have to agree.
    image: ${__ENV___IMAGE_APP}
    restart: unless-stopped
    volumes:
      - /etc/__NAME__/app.yaml:/etc/__NAME__/app.yaml:ro
`

const scaffoldReleaseNotes = `# __NAME__ 0.0.0

TODO -- what changed in this release.

Declared as ` + "`metadata.release_notes`" + `, so the manager finds this file by
declaration rather than by convention, and ` + "`release verify`" + ` fails if a bundle
promises notes and ships none.
`
