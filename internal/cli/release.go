package cli

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/morzecrew/morzer/internal/adapters/render/gotemplate"
	"github.com/morzecrew/morzer/internal/adapters/verify/checksum"
	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/infra/atomicfs"
	"github.com/morzecrew/morzer/internal/lifecycle/ops"
	"github.com/morzecrew/morzer/internal/ports"
	"github.com/morzecrew/morzer/internal/release"
	"github.com/morzecrew/morzer/internal/ui/views"
)

func newReleaseCommand(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "release",
		Short: "Inspect and manage release bundles",
		Long: "A release is a signed, digest-pinned bundle: a manifest, the Compose\n" +
			"files, the templates, and optionally the container images themselves.\n" +
			"These commands inspect the ones installed on this machine, fetch new\n" +
			"ones, and build bundles for publication.\n\n" +
			"None of them changes what is deployed. `morzer update` does that.",
	}
	// The one subtree that is split down the middle. `list`, `fetch`,
	// `ingest` and `prune` read or write *this* installation's release
	// store, so they need to know which installation that is; the five
	// authoring commands act on a directory or an archive named on the
	// command line and would work on a laptop with no installation at all.
	//
	// `release list` is why the split is declared rather than inherited: it
	// read the store without loading the installation, so on a machine with
	// three of them it answered about the placeholder layout -- "no releases
	// are installed" -- and looked like a bare machine.
	//
	// `show` is both, which is why it is machine-scoped here and refuses
	// inside releaseRoot instead: `release show ./bundle` inspects a
	// directory and needs no installation, while `release show` and `release
	// show 1.4.0` read the store and do. A scope asked before the argument
	// is parsed cannot tell those apart.
	cmd.AddCommand(
		installationScope(newReleaseListCommand(app)),
		machineScope(newReleaseShowCommand(app)),
		machineScope(newReleaseNewCommand(app)),
		machineScope(newReleaseVerifyCommand(app)),
		machineScope(newReleasePackCommand(app)),
		machineScope(newReleaseBuildCommand(app)),
		machineScope(newReleaseArchiveCommand(app)),
		installationScope(newReleaseFetchCommand(app)),
		installationScope(newReleaseIngestCommand(app)),
		installationScope(newReleasePruneCommand(app)),
		installationScope(newReleaseTrustCommand(app)),
	)
	return cmd
}

// newReleaseTrustCommand builds `morzer release trust`.
//
// Under `release` because what it governs is which releases this machine will
// install. Not `config set`: the keys are a list, and rotating one is two steps
// a single assignment cannot express.
func newReleaseTrustCommand(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "trust",
		Short: "Manage the keys a release must be signed with",
		Long: "The minisign public keys this installation accepts a release signature\n" +
			"from. To rotate a vendor key, trust the new one, install a release it\n" +
			"signed, then remove the old one: while both are trusted, a release\n" +
			"signed with either installs.\n\n" +
			"The last key cannot be removed while a signature is required, which\n" +
			"update.auto_apply also needs.",
	}

	add := &cobra.Command{
		Use:     "add <minisign-public-key>",
		Short:   "Trust release signatures made with a key",
		Long:    "Parses the key before recording it. Adding a key already trusted changes nothing.",
		Example: "  morzer release trust add RWQf6LRCGA9i53mlYecO4IzT51TGPpvWucNSCh1CBM0QTaLn73Y7GFO3",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			result, err := ops.TrustAdd(cmd.Context(), app.Deps, app.operationOptions(), args[0])
			if err != nil {
				return err
			}
			app.finish(result)
			return nil
		},
	}

	remove := &cobra.Command{
		Use:   "remove <minisign-public-key>",
		Short: "Stop trusting release signatures made with a key",
		Long: "A release already installed stays installed. One signed only with this\n" +
			"key is refused from the next update on, including one already staged.\n\n" +
			"Removing the last key, which only a machine that requires no signature\n" +
			"may do, turns signature checking off.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			result, err := ops.TrustRemove(cmd.Context(), app.Deps, app.operationOptions(), args[0])
			if err != nil {
				return err
			}
			app.finish(result)
			return nil
		},
	}

	list := &cobra.Command{
		Use:   "list",
		Short: "List the trusted release signing keys",
		Long: "The keys a release signature is checked against, and whether a\n" +
			"signature is required at all. With none required, a signed release is\n" +
			"still checked and an unsigned one is installed.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			report, err := ops.TrustList(cmd.Context(), app.Deps)
			if err != nil {
				return err
			}
			return app.render(report)
		},
	}

	cmd.AddCommand(add, remove, list)
	return cmd
}

func newReleaseListCommand(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List installed releases, newest first",
		Long: "What is on this machine, with the role of each: the current release, the\n" +
			"previous one that `rollback` returns to, and any staged release fetched\n" +
			"but not installed.\n\n" +
			"Those three are marked because `prune` refuses to remove them. A listing\n" +
			"that showed no reason for the refusal leaves an operator arguing with\n" +
			"the retention policy about a release they can see and cannot delete.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			entries, err := app.installedReleases(cmd.Context())
			if err != nil {
				return err
			}

			return app.render(entries)
		},
	}
}

// installedReleases reads the store through the lifecycle layer.
//
// The listing and the retention pass share one implementation because they share
// one definition of what a release *is*: a directory whose manifest loads, with
// the roles that make it unprunable. Two readers of the same directory is how
// `release list` and `release prune` come to disagree about what is there.
//
// The entries are passed through rather than copied into a second struct. The
// copy is what let `release list` drop the staged role while `release prune`
// enforced it -- the disagreement this function exists to prevent, reintroduced
// one field at a time.
func (a *App) installedReleases(ctx context.Context) ([]ops.ReleaseEntry, error) {
	return ops.InstalledReleases(ctx, a.Deps)
}

func newReleaseShowCommand(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "show [version]",
		Short: "Show a release manifest; the installed one when no version is given",
		Long: "The manifest as the manager reads it: images, profiles, the digest and\n" +
			"the compatibility declarations that `update` and `rollback` gate on.\n\n" +
			"Reads what is installed. To inspect a bundle that is not installed yet,\n" +
			"`release verify --bundle <path>` checks it and names what it is.",
		Example: "  morzer release show\n" +
			"  morzer release show 1.4.0\n" +
			"  morzer release show --json | jq .manifest.compatibility",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := app.releaseRoot(cmd.Context(), args)
			if err != nil {
				return err
			}

			rel, err := release.Load(root)
			if err != nil {
				return err
			}

			return app.render(views.Release{
				Manifest: rel.Manifest,
				Root:     rel.Root,
				Digest:   rel.Digest,
			})
		},
	}
}

func newReleaseVerifyCommand(app *App) *cobra.Command {
	var (
		expectDigest string
		signingKeys  []string
		renderCheck  bool
	)

	cmd := &cobra.Command{
		Use:   "verify <path>",
		Short: "Validate a bundle's manifest and check its integrity",
		Long: "Loads and validates the manifest, checks every referenced file exists,\n" +
			"parses every template it declares, computes the content digest, and\n" +
			"verifies SHA256SUMS when the bundle ships one.\n\n" +
			"Pass --signing-key to check the bundle's signature too. This is the\n" +
			"command a bundle vendor runs in their own CI, so it needs no\n" +
			"installation on the machine.\n\n" +
			"--render-check additionally renders each template against a synthetic\n" +
			"context. It is a smoke test, not a guarantee: the values are invented,\n" +
			"so a template that branches on them exercises only the branch they\n" +
			"choose. What it does catch is a template referring to a field or a\n" +
			"secret that nothing declares.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := args[0]

			rel, err := release.Load(path)
			if err != nil {
				return err
			}

			// The vendor's CI is the one place a deprecation warning
			// can still change the bundle before anyone installs it.
			if warning, deprecated := rel.Manifest.DeprecationWarning(); deprecated {
				fmt.Fprintf(app.Stream.Err,
					"warning: api_version %s is deprecated: %s\n",
					rel.Manifest.APIVersion, warning)
			}
			// Warnings, not failures, and deliberately: `verify`
			// answers "is this bundle installable", and a deprecated
			// field still is until the release that stops reading it.
			// A vendor who wants the clock enforced has the exit code
			// of their own CI to spend on it.
			for _, f := range rel.Manifest.DeprecatedFields() {
				fmt.Fprintf(app.Stream.Err, "warning: %s\n", f.Message())
			}

			if err := checkBundleIntegrity(rel); err != nil {
				return err
			}

			// After integrity, not before: a bundle whose SHA256SUMS
			// disagrees with its contents has a more fundamental
			// problem than a template, and rendering the file that
			// is not the file the vendor signed would report on the
			// wrong bytes.
			if renderCheck {
				if err := checkTemplatesRender(rel); err != nil {
					return err
				}
			}

			// The signature check is opt-in here rather than driven by
			// policy: `release verify` runs on a build machine with no
			// installation, so there is no policy to read.
			if len(signingKeys) > 0 {
				if err := app.Deps.Verifier.Verify(cmd.Context(),
					ports.BundlePath(rel.Root), ports.Expectation{
						PublicKeys: signingKeys,
						Required:   true,
					}); err != nil {
					return err
				}
			}

			if expectDigest != "" && !atomicfs.SameDigest(rel.Digest, expectDigest) {
				return domain.ValidationError(domain.ErrDigestMismatch,
					"bundle digest is %s, expected %s", rel.Digest, expectDigest).
					WithHint("the bundle does not match the digest it was published with")
			}

			if err := app.render(views.Verified{
				Valid:       true,
				Name:        rel.Name(),
				VersionInfo: rel.Version(),
				Digest:      rel.Digest,
				RenderCheck: renderCheck,
			}); err != nil {
				return err
			}
			// The summary says which check ran. "bundle is valid" after
			// a render check and after a parse check would be the same
			// sentence for two different claims, and the stronger one is
			// the one a vendor would quote.
			summary := "bundle is valid"
			if renderCheck {
				summary = "bundle is valid; templates render against a synthetic context"
			}
			app.finish(ops.Result{Summary: summary})
			return nil
		},
	}

	cmd.Flags().StringVar(&expectDigest, "digest", "", "expected content digest; a mismatch is an error")
	cmd.Flags().StringArrayVar(&signingKeys, "signing-key", nil,
		"minisign public key the bundle's signature must verify against; repeat for several")
	cmd.Flags().BoolVar(&renderCheck, "render-check", false,
		"also render each template against a synthetic context; a smoke test, "+
			"not a promise about a real installation")
	return cmd
}

// checkBundleIntegrity runs the checks every command that blesses a bundle
// shares: `verify`, and the two commands that produce one.
//
// Shared rather than restated so `archive` cannot come to accept a bundle
// `verify` refuses. A vendor whose CI runs `verify` and whose release step runs
// `archive` would otherwise be gated by two rules that drift apart, and the
// direction they drift in is always the same one -- the producing command grows
// lenient, because that is the one somebody is fighting at the time.
func checkBundleIntegrity(rel domain.Release) error {
	// Every declared template must at least parse. Checking it here rather
	// than in Load is deliberate: Load also runs during an operator's
	// `apply`, and a parse failure there is the failure this moves earlier,
	// not a place to add work.
	if err := checkTemplatesParse(rel); err != nil {
		return err
	}

	// A per-file sums list is what a third party can check with sha256sum,
	// independently of this tool.
	return checksum.VerifySumsFile(rel.Root)
}

// checkEveryTemplate runs one check over every declared template and collects
// what it says.
//
// The walk is shared because the two checks differ only in what they do with the
// bytes: both label the problem `configuration[i].template`, both read through
// the release root, and both report every template rather than the first. What
// they must *not* share is the error and the hint, so the caller keeps those --
// a parse failure and a render failure send an author to different places.
//
// Reading through the release root is the part that must not be duplicated by
// accident: os.ReadFile would follow a symlink out of the bundle, so a template
// pointing at a host file would check clean here and be refused at apply, which
// is verifying something other than what installs.
func checkEveryTemplate(rel domain.Release, check func(name string, raw []byte) error) []string {
	var problems []string
	for i, c := range rel.Manifest.Configuration {
		field := fmt.Sprintf("configuration[%d].template", i)

		raw, err := gotemplate.ReadTemplate(rel.Root, c.Template)
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s: %s", field, domain.AsError(err).Message))
			continue
		}
		if err := check(c.Template, raw); err != nil {
			problems = append(problems, fmt.Sprintf("%s: %s", field, domain.AsError(err).Message))
		}
	}
	return problems
}

// checkTemplatesParse parses every template the manifest declares.
//
// Reports all of them rather than the first: a vendor fixing one broken
// template should not have to run the command again to discover the next, which
// is the same reasoning CompatibilityReport already applies to compatibility
// problems.
//
// Parsing only. A template that parses can still fail to render against a real
// context -- the manifest names no target format and the render context has
// values only an installation can supply -- and reporting "valid" for more than
// was checked is the over-claim this whole check exists to remove.
func checkTemplatesParse(rel domain.Release) error {
	problems := checkEveryTemplate(rel, gotemplate.CheckSyntax)

	if len(problems) > 0 {
		return domain.ValidationError(domain.ErrTemplateSyntax,
			"the bundle declares templates that do not parse:\n  - %s",
			strings.Join(problems, "\n  - ")).
			WithHint("a template that cannot parse cannot render, so this bundle " +
				"would fail during an operator's `apply`")
	}
	return nil
}

// checkTemplatesRender renders every declared template against a synthetic
// context.
//
// Opt-in, and permanently so (RFC 0013 decision 12). The context invents its
// values, so a passing render check says the template referred to nothing that
// does not exist -- not that it will render on a customer's machine. Making it
// default would turn "verify passed" into a guarantee it cannot keep, which is
// the failure `verify` parsing templates at all exists to remove.
//
// The bundle's own declarations are not invented: the secret names come from the
// schema the manifest points at, so `{{ secretFile .Secrets "typo" }}` fails
// here. That is the half of this check that carries information a parse cannot.
func checkTemplatesRender(rel domain.Release) error {
	// A declared-but-broken schema is this function's failure to report,
	// not a reason to render against an empty secret map: every
	// `secretFile` call would then fail with a message about the secret
	// rather than about the schema that could not be read.
	schema, err := release.LoadSecretSchema(rel)
	if err != nil {
		return err
	}

	problems := checkEveryTemplate(rel, func(name string, raw []byte) error {
		return gotemplate.CheckRender(rel, schema, name, raw)
	})

	if len(problems) > 0 {
		// The field list is derived from the render context rather than
		// restated, so it cannot come to advertise a field that was
		// renamed -- which is the one thing an author reading this hint
		// would take at face value.
		return domain.ValidationError(domain.ErrTemplateRender,
			"the bundle declares templates that do not render:\n  - %s",
			strings.Join(problems, "\n  - ")).
			WithHint("the context is synthetic, so this is a smoke test -- but a "+
				"template that cannot render against invented values usually "+
				"refers to something no installation defines either. "+
				"Top-level fields: %s", strings.Join(ports.TemplateFields(), ", "))
	}
	return nil
}

func newReleaseFetchCommand(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "fetch <ref>",
		Short: "Fetch a release bundle into the release store",
		Long: "Resolves a reference, verifies it, and copies it into the release store\n" +
			"without making it current. Use `morzer apply` to activate it.\n\n" +
			"Takes a bundle directory or a tar.zst archive; https and oci arrive in a\n" +
			"later milestone.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ref, err := ports.ParseRef(args[0])
			if err != nil {
				return err
			}
			// No scheme pre-check: the source registry refuses a
			// scheme it has no adapter for, and naming the supported
			// ones is its job rather than every call site's.
			resolved, err := app.Deps.Source.Resolve(cmd.Context(), ref)
			if err != nil {
				return err
			}

			// Shared with channel staging rather than restated here:
			// a poll and an operator must land the same bundle under
			// the same signature policy.
			rel, present, err := ops.FetchIntoStore(cmd.Context(), app.Deps, ref, resolved)
			if err != nil {
				return err
			}

			// Carried on the result rather than assigned beside it:
			// finish is what publishes JSON, and it publishes
			// result.Data -- so a payload written to app.jsonData
			// first is overwritten by this call's empty one, and
			// `release fetch --json` reports nothing about what it
			// fetched.
			data := map[string]any{
				"version": rel.Version(), "digest": rel.Digest, "root": rel.Root,
			}
			if present {
				app.finish(ops.Result{Data: data,
					Summary: fmt.Sprintf("release %s is already present", rel.Version())})
				return nil
			}
			app.finish(ops.Result{Data: data,
				Summary: fmt.Sprintf("fetched %s %s into %s", rel.Name(), rel.Version(), rel.Root)})
			return nil
		},
	}
}

func newReleasePruneCommand(app *App) *cobra.Command {
	var keep int

	cmd := &cobra.Command{
		Use:   "prune",
		Short: "Remove old releases beyond the retention policy",
		Long: "Never removes the current or previous release, whatever the retention\n" +
			"setting says: rollback depends on both being present. A release\n" +
			"staged and not yet installed is exempt too, for the same reason:\n" +
			"pruning the candidate a poll just fetched makes staging pointless.\n\n" +
			"The images a pruned release put in the runtime's store go with it, unless\n" +
			"a release left on this machine, in any installation, still names them.\n" +
			"Takes the deployment lock, so it waits for or refuses beside an update.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			res, err := ops.PruneReleasesLocked(cmd.Context(), app.Deps, keep, app.operationOptions())
			if err != nil {
				return err
			}

			if app.json != nil {
				app.jsonData = res
				return nil
			}
			app.finish(ops.Result{Summary: pruneSummary(res)})
			return nil
		},
	}

	cmd.Flags().IntVar(&keep, "keep", 0, "number of non-active releases to retain; 0 uses the policy")
	return cmd
}

// pruneSummary is `release prune` for a terminal: the releases, then what
// became of their images, one name to a line.
func pruneSummary(res ops.PruneResult) string {
	if len(res.Removed) == 0 {
		return "nothing to prune"
	}
	removed, kept := "removed", "kept"
	if res.DryRun {
		removed, kept = "would remove", "would keep"
	}

	lines := []string{fmt.Sprintf("%s %d release(s): %v", removed, len(res.Removed), res.Removed)}
	if len(res.Images.Removed) > 0 {
		lines = append(lines, fmt.Sprintf("%s %d image(s):", removed, len(res.Images.Removed)))
		for _, ref := range res.Images.Removed {
			lines = append(lines, "  "+ref)
		}
	}
	if len(res.Images.Kept) > 0 {
		lines = append(lines, fmt.Sprintf("%s %d image(s):", kept, len(res.Images.Kept)))
		for _, k := range res.Images.Kept {
			lines = append(lines, fmt.Sprintf("  %s: %s", k.Ref, k.Why))
		}
	}
	if res.Images.Skipped != "" {
		lines = append(lines, fmt.Sprintf("%s no images: %s", removed, res.Images.Skipped))
	}
	return strings.Join(lines, "\n")
}

// releaseRoot resolves an optional version argument to a release directory.
func (a *App) releaseRoot(ctx context.Context, args []string) (string, error) {
	if len(args) == 1 {
		// A path takes precedence over a version, so `release show
		// ./bundle` works on a machine with no installation at all.
		if info, err := os.Stat(args[0]); err == nil && info.IsDir() {
			return args[0], nil
		}
	}

	// Everything below reads this installation's release store -- the
	// version branch resolves against Paths.ReleaseDir, the bare form reads
	// the current-release record -- so this is where a machine holding
	// several installations has to be told which. Asked here rather than by
	// the command's declared scope, because the scope is resolved before the
	// argument is parsed and the argument is what decides.
	if err := a.Deps.RequireInstallationChosen(); err != nil {
		return "", err
	}

	if len(args) == 1 {
		version, err := domain.ParseVersion(args[0])
		if err != nil {
			return "", domain.Usage("%q is neither a directory nor a version", args[0])
		}
		root := a.Deps.Paths.ReleaseDir(version.String())
		if _, err := os.Stat(root); err != nil {
			return "", domain.ValidationError(domain.ErrReleaseNotFound,
				"release %s is not installed", version).
				WithHint("run `morzer release list` to see what is available")
		}
		return root, nil
	}

	current, err := a.Deps.State.CurrentRelease(ctx)
	if err != nil {
		return "", err
	}
	if current.IsZero() {
		return "", domain.InstallationError(domain.ErrReleaseNotFound,
			"no release is installed").
			WithHint("pass a bundle path, or install one first")
	}
	return current.Root, nil
}

// newReleaseIngestCommand loads the images the installed release carries.
//
// An operator-facing command because the condition it fixes is one an operator
// meets: `apply` refuses to converge while an image marked `from: bundle` is
// not in the local store, and the alternative to this command would be
// re-installing a release to load images it already contains.
//
// It is also what `init` and `update` run, rather than a second implementation
// of the same idea -- RFC 0011 decision 12, which asked for an explicit,
// re-runnable step precisely so that the lifecycle and the operator would not
// be doing two different things.
func newReleaseIngestCommand(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "ingest",
		Short: "Load the images the installed release carries into the local store",
		Long: "Serves the release's OCI layout on loopback and has the container\n" +
			"runtime pull each bundled image out of it, leaving each one named\n" +
			"locally so the deployment can resolve it with no registry.\n\n" +
			"Idempotent: an image already loaded is not read again. Nothing here\n" +
			"touches the network -- the bytes are the ones the bundle shipped, and\n" +
			"the signature that covered them covered these.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return app.runOperation(cmd.Context(), func(ctx context.Context) (ops.Result, error) {
				return ops.IngestImages(ctx, app.Deps, app.operationOptions())
			})
		},
	}
}
