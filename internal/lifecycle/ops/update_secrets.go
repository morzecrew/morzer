package ops

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/lifecycle/engine"
	"github.com/morzecrew/morzer/internal/ports"
	"github.com/morzecrew/morzer/internal/release"
)

// stepGenerateSecrets generates each secret the release being installed adds:
// declared with a generator, with no value here, and not declared by the
// release it replaces (RFC 0036).
//
// It is init's rule applied to what is new. A secret the replaced release
// declared and this machine has no value for was left unset by a person, and
// generating it would overrule them. A value it writes is never compensated:
// a failed update's retry needs the same one, and rendering the previous
// release removes its file.
func stepGenerateSecrets(d *Deps, rel domain.Release, replaced *fallbackRelease, opts Options) engine.Step {
	pending := func(ctx context.Context) ([]domain.SecretDeclaration, error) {
		if opts.NoGenerateSecrets {
			return nil, nil
		}
		schema, err := release.LoadSecretSchema(rel)
		if err != nil {
			return nil, err
		}
		existing, err := d.Secrets.Load(ctx)
		if err != nil {
			return nil, err
		}
		var out []domain.SecretDeclaration
		for _, decl := range schema.Secrets {
			if decl.Generator.Auto() && !existing.Has(decl.Name) && !replaced.declares(decl.Name) {
				out = append(out, decl)
			}
		}
		return out, nil
	}

	return engine.Step{
		ID:          "generate-secrets",
		Description: "generate secrets the release adds",
		Idempotent:  true,
		// Nothing else has changed yet, and what it wrote is kept.
		OnFailure: engine.Abort,
		Timeout:   2 * time.Minute,
		Check: func(ctx context.Context, st *engine.State) (bool, error) {
			todo, err := pending(ctx)
			return len(todo) == 0, err
		},
		Execute: func(ctx context.Context, st *engine.State) error {
			todo, err := pending(ctx)
			if err != nil {
				return err
			}
			names := make([]string, 0, len(todo))
			// A message rather than a detail, which plain mode prints only
			// under --verbose: a new credential on the machine is news on
			// an unattended tick too. The names, never the values.
			report := func() {
				if len(names) > 0 {
					st.Info("generated %s %s adds: %s",
						secretCount(len(names)), rel.Version(), strings.Join(names, ", "))
				}
			}
			for _, decl := range todo {
				if err := d.Secrets.Generate(ctx, decl.Name,
					ports.GenSpec{Generator: decl.Generator}); err != nil {
					// Somebody set it since the state was read, and the
					// store refused to overwrite their value: the step's
					// work is done, by them.
					if set, loadErr := d.Secrets.Load(ctx); loadErr == nil && set.Has(decl.Name) {
						continue
					}
					// What was generated stays, and the retry skips
					// it, so this run is the only one that can name it.
					report()
					return err
				}
				names = append(names, decl.Name)
			}
			report()
			return nil
		},
		PlanDetail: func(ctx context.Context, st *engine.State) (string, string) {
			if opts.NoGenerateSecrets {
				return "turned off by --generate-secrets=false", ""
			}
			todo, err := pending(ctx)
			if err != nil || len(todo) == 0 {
				return "", ""
			}
			names := make([]string, 0, len(todo))
			for _, decl := range todo {
				names = append(names, decl.Name)
			}
			return "would generate " + strings.Join(names, ", "), ""
		},
	}
}

// declares reports whether the replaced release declared a secret. With no
// replaced release, or one whose secrets could not be read, every secret is new.
func (f *fallbackRelease) declares(name string) bool {
	return f != nil && f.Secrets[name]
}

// declaredSecrets names the secrets a release declares, or none when its
// schema cannot be read.
func declaredSecrets(rel domain.Release) map[string]bool {
	schema, err := release.LoadSecretSchema(rel)
	if err != nil {
		return nil
	}
	out := make(map[string]bool, len(schema.Secrets))
	for _, decl := range schema.Secrets {
		out[decl.Name] = true
	}
	return out
}

func secretCount(n int) string {
	if n == 1 {
		return "1 secret"
	}
	return fmt.Sprintf("%d secrets", n)
}
