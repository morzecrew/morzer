package release_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/release"
)

// A bundle arrives from somewhere else. Every way it can be wrong is a way an
// operator's install can fail, so each one has to be refused with a message
// that names the problem rather than a decoder's complaint about line 47.
//
// The fixtures are built by mutating a copy of the real example bundle, so the
// starting point is a bundle that is known to load and the only difference is
// the fault under test.

// bundle copies the example bundle and applies one mutation.
func bundle(t *testing.T, mutate func(dir string)) string {
	t.Helper()

	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(wd, "..", "..", "testdata", "bundle")
	dst := filepath.Join(t.TempDir(), "bundle")

	if err := copyTree(src, dst); err != nil {
		t.Fatalf("cannot copy the example bundle: %v", err)
	}
	if mutate != nil {
		mutate(dst)
	}
	return dst
}

func copyTree(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		// The executable bit matters: a hook that arrives without it is
		// a validation error, which is one of the cases below.
		return os.WriteFile(target, data, info.Mode().Perm())
	})
}

func edit(t *testing.T, path string, replace func(string) string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(replace(string(data))), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestTheExampleBundleLoads(t *testing.T) {
	rel, err := release.Load(bundle(t, nil))
	if err != nil {
		t.Fatalf("the example bundle does not load: %v", err)
	}
	if rel.Name() != "demo" || rel.Version().String() != "1.2.0" {
		t.Errorf("loaded %s %s", rel.Name(), rel.Version())
	}
	// The digest is what makes a release identifiable independently of how
	// it arrived, so it must be computed, not absent.
	if !strings.HasPrefix(rel.Digest, "sha256:") {
		t.Errorf("digest = %q", rel.Digest)
	}
}

func TestABundleIsRefusedForEveryWayItCanBeWrong(t *testing.T) {
	cases := map[string]struct {
		mutate func(t *testing.T, dir string)
		names  string // what the error must mention
	}{
		"no manifest at all": {
			func(t *testing.T, dir string) {
				if err := os.Remove(filepath.Join(dir, "manifest.yaml")); err != nil {
					t.Fatal(err)
				}
			}, "manifest",
		},
		"a manifest that is not YAML": {
			func(t *testing.T, dir string) {
				edit(t, filepath.Join(dir, "manifest.yaml"),
					func(string) string { return "\tnot: [valid" })
			}, "manifest",
		},
		"an unknown field, which is usually a typo": {
			func(t *testing.T, dir string) {
				edit(t, filepath.Join(dir, "manifest.yaml"), func(s string) string {
					return s + "\nunknown_field: surprise\n"
				})
			}, "unknown_field",
		},
		"an api_version this manager cannot read": {
			func(t *testing.T, dir string) {
				edit(t, filepath.Join(dir, "manifest.yaml"), func(s string) string {
					return strings.Replace(s, "selfhost/v1alpha1", "selfhost/v99", 1)
				})
			}, "v99",
		},
		"an image that is not pinned by digest": {
			func(t *testing.T, dir string) {
				edit(t, filepath.Join(dir, "manifest.yaml"), func(s string) string {
					return strings.Replace(s,
						"registry.example/demo/app@sha256:0000000000000000000000000000000000000000000000000000000000000001",
						"registry.example/demo/app:latest", 1)
				})
			}, "digest",
		},
		"a VERSION file disagreeing with the manifest": {
			func(t *testing.T, dir string) {
				edit(t, filepath.Join(dir, "VERSION"), func(string) string { return "9.9.9\n" })
			}, "VERSION",
		},
		"a compose file the manifest names but the bundle lacks": {
			func(t *testing.T, dir string) {
				if err := os.Remove(filepath.Join(dir, "compose", "compose.yaml")); err != nil {
					t.Fatal(err)
				}
			}, "compose/compose.yaml",
		},
		"a template the manifest names but the bundle lacks": {
			func(t *testing.T, dir string) {
				if err := os.Remove(filepath.Join(dir, "templates", "application.yaml")); err != nil {
					t.Fatal(err)
				}
			}, "application.yaml",
		},
		"a hook that is not executable": {
			func(t *testing.T, dir string) {
				if err := os.Chmod(filepath.Join(dir, "hooks", "migrate"), 0o644); err != nil {
					t.Fatal(err)
				}
			}, "migrate",
		},
		"a hook the manifest names but the bundle lacks": {
			func(t *testing.T, dir string) {
				if err := os.Remove(filepath.Join(dir, "hooks", "backup")); err != nil {
					t.Fatal(err)
				}
			}, "backup",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			dir := bundle(t, func(d string) { tc.mutate(t, d) })

			_, err := release.Load(dir)
			if err == nil {
				t.Fatal("the bundle loaded despite the fault")
			}
			// Naming the offending thing is the difference between a
			// vendor fixing their bundle and filing a bug here.
			if !strings.Contains(err.Error(), tc.names) {
				t.Errorf("the error does not mention %q: %v", tc.names, err)
			}
		})
	}
}

func TestLoadRefusesSomethingThatIsNotADirectory(t *testing.T) {
	file := filepath.Join(t.TempDir(), "not-a-bundle")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := release.Load(file); err == nil {
		t.Error("a regular file was loaded as a bundle")
	}
	if _, err := release.Load(filepath.Join(t.TempDir(), "absent")); err == nil {
		t.Error("a directory that does not exist was loaded as a bundle")
	}
}

// TestAVersionFileIsOptionalButMustAgree pins the rule: the manifest is the
// authority, and a VERSION file exists to make the version greppable. Absent is
// fine; disagreeing is not.
func TestAVersionFileIsOptionalButMustAgree(t *testing.T) {
	dir := bundle(t, func(d string) {
		_ = os.Remove(filepath.Join(d, "VERSION"))
	})
	if _, err := release.Load(dir); err != nil {
		t.Errorf("a bundle with no VERSION file was refused: %v", err)
	}
}

func TestParseManifestNamesWhereTheProblemIs(t *testing.T) {
	_, err := release.ParseManifest([]byte("api_version: [oh dear\n"), "manifest.yaml")
	if err == nil {
		t.Fatal("malformed YAML parsed")
	}
	if !strings.Contains(err.Error(), "manifest.yaml") {
		t.Errorf("the error does not name the source: %v", err)
	}
}

// TestRetentionDistinguishesAbsentFromZero.
//
// ApplyDefaults runs before Validate, and it filled any zero -- so an explicit
// `releases: 0`, which Validate exists to refuse, was quietly rewritten to 3.
// The vendor meant something by the zero, and keeping three releases is not it.
func TestRetentionDistinguishesAbsentFromZero(t *testing.T) {
	t.Run("absent takes the default", func(t *testing.T) {
		dir := bundle(t, func(d string) {
			edit(t, filepath.Join(d, "manifest.yaml"), func(s string) string {
				return strings.Replace(s, "retention:\n  releases: 3\n  backups: 7\n", "", 1)
			})
		})

		rel, err := release.Load(dir)
		if err != nil {
			t.Fatalf("a manifest with no retention block was refused: %v", err)
		}
		if rel.Manifest.Retention.Releases != domain.DefaultRetentionReleases {
			t.Errorf("releases = %d, want the default %d",
				rel.Manifest.Retention.Releases, domain.DefaultRetentionReleases)
		}
		if rel.Manifest.Retention.Backups != domain.DefaultRetentionBackups {
			t.Errorf("backups = %d, want the default %d",
				rel.Manifest.Retention.Backups, domain.DefaultRetentionBackups)
		}
	})

	for field, want := range map[string]string{
		"releases": "retention.releases",
		"backups":  "retention.backups",
	} {
		t.Run("an explicit zero for "+field+" is refused", func(t *testing.T) {
			dir := bundle(t, func(d string) {
				edit(t, filepath.Join(d, "manifest.yaml"), func(s string) string {
					return strings.Replace(s, "  "+field+": ", "  "+field+": 0 #", 1)
				})
			})

			_, err := release.Load(dir)
			if err == nil {
				t.Fatal("a manifest keeping zero of something was accepted")
			}
			if !strings.Contains(err.Error(), want) {
				t.Errorf("the refusal does not name the field: %v", err)
			}
		})
	}
}

func TestLoadSecretSchemaRefusesWhatItCannotRead(t *testing.T) {
	dir := bundle(t, func(d string) {
		edit(t, filepath.Join(d, "templates", "secrets.yaml"),
			func(string) string { return "\tnot: [valid" })
	})

	// The bundle itself still loads: the schema is read separately, when a
	// secret operation needs it.
	rel, err := release.Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, err := release.LoadSecretSchema(rel); err == nil {
		t.Error("an unparseable secret schema was accepted")
	}
}

func TestLoadSecretSchemaOnABundleThatDeclaresNone(t *testing.T) {
	dir := bundle(t, func(d string) {
		edit(t, filepath.Join(d, "manifest.yaml"), func(s string) string {
			return strings.Replace(s, "  schema: templates/secrets.yaml\n", "", 1)
		})
	})

	rel, err := release.Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	schema, err := release.LoadSecretSchema(rel)
	if err != nil {
		t.Fatalf("a release declaring no secret schema must not be an error: %v", err)
	}
	if len(schema.Secrets) != 0 {
		t.Errorf("schema = %+v, want empty", schema)
	}
}
