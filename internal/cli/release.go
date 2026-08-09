package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/morzecrew/morzer/internal/adapters/render/gotemplate"
	"github.com/morzecrew/morzer/internal/adapters/verify/checksum"
	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/infra/atomicfs"
	"github.com/morzecrew/morzer/internal/lifecycle/ops"
	"github.com/morzecrew/morzer/internal/ports"
	"github.com/morzecrew/morzer/internal/release"
)

func newReleaseCommand(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "release",
		Short: "Inspect and manage release bundles",
	}
	cmd.AddCommand(
		newReleaseListCommand(app),
		newReleaseShowCommand(app),
		newReleaseNewCommand(app),
		newReleaseVerifyCommand(app),
		newReleasePackCommand(app),
		newReleaseBuildCommand(app),
		newReleaseArchiveCommand(app),
		newReleaseFetchCommand(app),
		newReleaseIngestCommand(app),
		newReleasePruneCommand(app),
	)
	return cmd
}

// releaseEntry is one installed release, as `release list` reports it.
type releaseEntry struct {
	Version  domain.Version `json:"version"`
	Root     string         `json:"root"`
	Digest   string         `json:"digest"`
	Current  bool           `json:"current"`
	Previous bool           `json:"previous"`
}

func newReleaseListCommand(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List installed releases, newest first",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			entries, err := app.installedReleases(cmd.Context())
			if err != nil {
				return err
			}

			if app.json != nil {
				app.jsonData = entries
				return nil
			}
			if len(entries) == 0 {
				_, _ = fmt.Fprintln(app.Stream.Out, "no releases are installed")
				return nil
			}
			for _, e := range entries {
				marker := " "
				switch {
				case e.Current:
					marker = "*"
				case e.Previous:
					marker = "-"
				}
				fmt.Fprintf(app.Stream.Out, "%s %-12s %s\n", marker, e.Version, e.Root)
			}
			return nil
		},
	}
}

// installedReleases enumerates the release store, marking current and previous.
func (a *App) installedReleases(ctx context.Context) ([]releaseEntry, error) {
	dir := a.Deps.Paths.ReleasesDir()

	dirEntries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, domain.InstallationError(err, "cannot list %s", dir)
	}

	current, _ := a.Deps.State.CurrentRelease(ctx)
	previous, _ := a.Deps.State.PreviousRelease(ctx)

	var out []releaseEntry
	for _, entry := range dirEntries {
		if !entry.IsDir() {
			continue
		}
		root := filepath.Join(dir, entry.Name())

		// A directory whose manifest will not load is not a release.
		// Skipping it keeps `release list` working when a fetch was
		// interrupted midway.
		manifest, err := release.LoadManifest(filepath.Join(root, release.ManifestFileName))
		if err != nil {
			continue
		}

		out = append(out, releaseEntry{
			Version:  manifest.Metadata.Version,
			Root:     root,
			Current:  !current.IsZero() && current.Root == root,
			Previous: !previous.IsZero() && previous.Root == root,
		})
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Version.GreaterThan(out[j].Version) })
	return out, nil
}

func newReleaseShowCommand(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "show [version]",
		Short: "Show a release manifest; the installed one when no version is given",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := app.releaseRoot(cmd.Context(), args)
			if err != nil {
				return err
			}

			rel, err := release.Load(root)
			if err != nil {
				return err
			}

			if app.json != nil {
				app.jsonData = map[string]any{
					"manifest": rel.Manifest,
					"root":     rel.Root,
					"digest":   rel.Digest,
				}
				return nil
			}

			m := rel.Manifest
			f := func(format string, a ...any) { fmt.Fprintf(app.Stream.Out, format+"\n", a...) }

			f("%s %s", m.Metadata.Name, m.Metadata.Version)
			if m.Metadata.Description != "" {
				f("  %s", m.Metadata.Description)
			}
			f("")
			f("  api version    %s", m.APIVersion)
			f("  digest         %s", rel.Digest)
			f("  root           %s", rel.Root)
			f("  runtime        %s (project %s)", m.Providers.Runtime.Name, m.Runtime.Project)

			f("")
			f("  images")
			names := make([]string, 0, len(m.Images))
			for name := range m.Images {
				names = append(names, name)
			}
			sort.Strings(names)
			for _, name := range names {
				f("    %-16s %s", name, m.Images[name])
			}

			if len(m.Runtime.Profiles) > 0 {
				profiles := make([]string, 0, len(m.Runtime.Profiles))
				for p := range m.Runtime.Profiles {
					profiles = append(profiles, p)
				}
				sort.Strings(profiles)
				f("")
				f("  profiles       %v", profiles)
			}

			f("")
			f("  compatibility")
			f("    rollback safe    %t", m.Compatibility.RollbackSafe)
			if !m.Compatibility.UpgradeFrom.IsZero() {
				f("    upgrade from     %s", m.Compatibility.UpgradeFrom)
			}
			if m.Compatibility.DatabaseSchemaMax > 0 {
				f("    database schema  %d–%d",
					m.Compatibility.DatabaseSchemaMin, m.Compatibility.DatabaseSchemaMax)
			}
			if !m.Compatibility.MinManagerVersion.IsZero() {
				f("    min manager      %s", m.Compatibility.MinManagerVersion)
			}
			return nil
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

			if app.json != nil {
				app.jsonData = map[string]any{
					"valid":        true,
					"name":         rel.Name(),
					"version":      rel.Version(),
					"digest":       rel.Digest,
					"render_check": renderCheck,
				}
				return nil
			}
			fmt.Fprintf(app.Stream.Out, "%s %s\n%s\n", rel.Name(), rel.Version(), rel.Digest)
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
	var problems []string

	for i, c := range rel.Manifest.Configuration {
		field := fmt.Sprintf("configuration[%d].template", i)

		// Read exactly as the renderer will, through the release root.
		// os.ReadFile would follow a symlink out of the bundle, so a
		// template pointing at a host file would parse here and be
		// refused at apply -- verifying something other than what
		// installs is the failure this check exists to remove.
		raw, err := gotemplate.ReadTemplate(rel.Root, c.Template)
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s: %s", field, domain.AsError(err).Message))
			continue
		}
		if err := gotemplate.CheckSyntax(c.Template, raw); err != nil {
			problems = append(problems, fmt.Sprintf("%s: %s", field, domain.AsError(err).Message))
		}
	}

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

	var problems []string
	for i, c := range rel.Manifest.Configuration {
		field := fmt.Sprintf("configuration[%d].template", i)

		raw, err := gotemplate.ReadTemplate(rel.Root, c.Template)
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s: %s", field, domain.AsError(err).Message))
			continue
		}
		if err := gotemplate.CheckRender(rel, schema, c.Template, raw); err != nil {
			problems = append(problems,
				fmt.Sprintf("%s: %s", field, domain.AsError(err).Message))
		}
	}

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

			if app.json != nil {
				app.jsonData = map[string]any{
					"version": rel.Version(), "digest": rel.Digest, "root": rel.Root,
				}
			}
			if present {
				app.finish(ops.Result{
					Summary: fmt.Sprintf("release %s is already present", rel.Version())})
				return nil
			}
			app.finish(ops.Result{
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
			"pruning the candidate a poll just fetched makes staging pointless.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			entries, err := app.installedReleases(cmd.Context())
			if err != nil {
				return err
			}

			// Read once, outside the loop: a candidate that appeared
			// mid-prune would exempt some entries and not others.
			candidate, err := app.Deps.State.UpdateCandidate(cmd.Context())
			if err != nil {
				return err
			}

			retain := keep
			if retain <= 0 {
				inst, err := app.Deps.State.LoadInstallation(cmd.Context())
				if err != nil {
					return err
				}
				current, err := app.Deps.State.CurrentRelease(cmd.Context())
				if err != nil {
					return err
				}
				retain = domain.DefaultRetentionReleases
				if !current.IsZero() {
					if rel, err := release.Load(current.Root); err == nil {
						retain = inst.RetentionReleases(rel.Manifest)
					}
				}
			}

			var removed []string
			kept := 0
			for _, e := range entries {
				// The current and previous releases are exempt
				// unconditionally: pruning either would remove
				// the thing rollback returns to.
				if e.Current || e.Previous {
					kept++
					continue
				}
				// And so is a staged candidate, in either mode
				// (RFC 0016 §5.4): it was fetched ahead of the
				// operator's decision, which is the whole value
				// of staging, and retention that removed it
				// would leave the decision waiting on a bundle
				// the machine had thrown away. The exemption
				// ends when it is applied or superseded, which
				// is when the record is cleared.
				if candidate.IsStaged() && candidate.Version.Equal(e.Version) {
					kept++
					continue
				}
				if kept < retain {
					kept++
					continue
				}
				if app.Flags.dryRun {
					removed = append(removed, e.Version.String())
					continue
				}
				if err := atomicfs.RemoveAll(e.Root); err != nil {
					return err
				}
				removed = append(removed, e.Version.String())
			}

			if app.json != nil {
				app.jsonData = map[string]any{"removed": removed, "retained": retain}
				return nil
			}
			if len(removed) == 0 {
				app.finish(ops.Result{Summary: "nothing to prune"})
				return nil
			}
			verb := "removed"
			if app.Flags.dryRun {
				verb = "would remove"
			}
			app.finish(ops.Result{
				Summary: fmt.Sprintf("%s %d release(s): %v", verb, len(removed), removed)})
			return nil
		},
	}

	cmd.Flags().IntVar(&keep, "keep", 0, "number of non-active releases to retain; 0 uses the policy")
	return cmd
}

// releaseRoot resolves an optional version argument to a release directory.
func (a *App) releaseRoot(ctx context.Context, args []string) (string, error) {
	if len(args) == 1 {
		// A path takes precedence over a version, so `release show
		// ./bundle` works on a machine with no installation at all.
		if info, err := os.Stat(args[0]); err == nil && info.IsDir() {
			return args[0], nil
		}
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
