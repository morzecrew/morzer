package domain

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// validManifest is the baseline every validation test mutates one field of, so
// each test asserts one rule rather than a whole document.
func validManifest() Manifest {
	m := Manifest{
		APIVersion: APIVersionV1Alpha1,
		Kind:       KindApplicationRelease,
		Metadata: Metadata{
			Name:    "demo",
			Version: MustParseVersion("1.2.0"),
		},
		Providers: Providers{Runtime: Provider{Name: "compose"}},
		Runtime: RuntimeSpec{
			Project: "demo",
			Files:   []string{"compose/compose.yaml"},
		},
		Images: map[string]string{
			"app": "registry.example/demo/app@sha256:" + strings.Repeat("a", 64),
		},
	}
	m.ApplyDefaults()
	return m
}

func TestValidManifestPasses(t *testing.T) {
	m := validManifest()
	require.NoError(t, m.Validate())
}

func TestImagesMustBePinnedByDigest(t *testing.T) {
	// An unpinned image makes a release mutable, and a mutable release
	// makes rollback meaningless.
	cases := map[string]string{
		"bare tag":        "registry.example/demo/app:1.2.0",
		"floating latest": "registry.example/demo/app:latest",
		"no tag at all":   "registry.example/demo/app",
		"short digest":    "registry.example/demo/app@sha256:abc",
		"wrong algorithm": "registry.example/demo/app@sha512:" + strings.Repeat("a", 64),
	}

	for name, ref := range cases {
		t.Run(name, func(t *testing.T) {
			m := validManifest()
			m.Images["app"] = ref

			err := m.Validate()
			require.Error(t, err, "%s must be rejected", name)
			assert.Contains(t, err.Error(), "pinned by digest")
		})
	}
}

func TestUnknownAPIVersionIsRejectedActionably(t *testing.T) {
	m := validManifest()
	m.APIVersion = "selfhost/v99"

	err := m.Validate()
	require.Error(t, err)
	assert.Equal(t, ExitIncompatible, ExitCode(err),
		"an unreadable manifest is an incompatibility, not a usage error")

	// The message must say what this manager *can* read, or the operator
	// has to go looking.
	e := AsError(err)
	assert.Contains(t, e.Hint, string(APIVersionV1Alpha1))
}

// TestARemedySurvivesAWrapThatHasNoneOfItsOwn. The hint is written where the
// failure is diagnosed and the operator reads it several wraps later, so every
// wrap in between is a chance to lose the one sentence that says what to do.
func TestARemedySurvivesAWrapThatHasNoneOfItsOwn(t *testing.T) {
	// Three deep, with the remedy only at the bottom: errors.As -- what
	// AsError uses -- stops at the outermost *Error, which here is the
	// hint-less middle wrap.
	inner := RuntimeError(nil, "the volume helper image is not on this machine").
		WithHint("docker pull the helper image on a machine with a registry, then load it here")
	middle := BackupError(inner, "cannot capture volume uploads")
	outer := BackupError(middle, "backup failed").WithHintFrom(middle)

	assert.Equal(t, inner.Hint, outer.Hint,
		"the remedy was dropped behind a wrap that carried none, which leaves an "+
			"air-gapped operator with a diagnosis and nothing to do about it")

	// A wrap that has its own remedy keeps it: it is closer to what the
	// operator asked for than the cause's.
	own := BackupError(middle, "backup failed").WithHint("free disk space and run it again")
	assert.Equal(t, "free disk space and run it again", own.WithHintFrom(middle).Hint)

	// And a chain with nothing to carry stays empty, rather than gaining a
	// blank remedy line for the operator to read.
	assert.Empty(t, BackupError(nil, "backup failed").WithHintFrom(errors.New("plain")).Hint)
}

func TestPathsMayNotEscapeTheReleaseRoot(t *testing.T) {
	cases := []string{"../outside/compose.yaml", "/etc/passwd", "compose/../../escape.yaml"}

	for _, path := range cases {
		t.Run(path, func(t *testing.T) {
			m := validManifest()
			m.Runtime.Files = []string{path}

			err := m.Validate()
			require.Error(t, err, "%q must be rejected", path)
		})
	}
}

func TestConfigurationTargetsMustBeAbsolute(t *testing.T) {
	m := validManifest()
	m.Configuration = []ConfigurationFile{
		{Template: "templates/app.yaml", Target: "etc/demo/app.yaml"},
	}

	err := m.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "absolute path")
}

func TestValidationReportsEveryProblemAtOnce(t *testing.T) {
	// A bundle author fixing one problem should not discover the next one
	// on the retry.
	m := validManifest()
	m.Metadata.Name = ""
	m.Runtime.Files = nil
	m.Images["app"] = "unpinned:latest"

	err := m.Validate()
	require.Error(t, err)

	msg := err.Error()
	assert.Contains(t, msg, "metadata.name")
	assert.Contains(t, msg, "runtime.files")
	assert.Contains(t, msg, "images.app")
}

func TestOperationKindRules(t *testing.T) {
	t.Run("runtime-service needs a service", func(t *testing.T) {
		m := validManifest()
		m.Operations = map[string]OperationSpec{
			"migrate": {Kind: OperationKindRuntimeService},
		}
		require.Error(t, m.Validate())
	})

	t.Run("hook needs a command", func(t *testing.T) {
		m := validManifest()
		m.Operations = map[string]OperationSpec{"backup": {Kind: OperationKindHook}}
		require.Error(t, m.Validate())
	})

	t.Run("a hook may not also name a service", func(t *testing.T) {
		m := validManifest()
		m.Operations = map[string]OperationSpec{
			"backup": {Kind: OperationKindHook, Command: []string{"hooks/backup"}, Service: "db"},
		}
		require.Error(t, m.Validate(), "the two kinds have different semantics and must not be mixed")
	})

	t.Run("both kinds are valid on their own", func(t *testing.T) {
		m := validManifest()
		m.Operations = map[string]OperationSpec{
			"migrate": {Kind: OperationKindRuntimeService, Service: "migrate"},
			"backup":  {Kind: OperationKindHook, Command: []string{"hooks/backup"}},
		}
		require.NoError(t, m.Validate())
	})
}

func TestHealthCheckRules(t *testing.T) {
	m := validManifest()
	m.Health.Checks = []HealthCheck{
		{Name: "api", Type: HealthHTTP}, // missing url
	}
	require.Error(t, m.Validate())

	m.Health.Checks = []HealthCheck{
		{Name: "api", Type: HealthHTTP, URL: "http://127.0.0.1:8080/health"},
		{Name: "api", Type: HealthTCP, Address: "127.0.0.1:5432"},
	}
	err := m.Validate()
	require.Error(t, err, "duplicate check names make results ambiguous")
	assert.Contains(t, err.Error(), "duplicate")
}

func TestExtensionsMustBeNamespaced(t *testing.T) {
	m := validManifest()
	m.Extensions = map[string]map[string]any{"telemetry": {"endpoint": "https://x"}}

	err := m.Validate()
	require.Error(t, err,
		"an un-namespaced extension key could shadow a future core field")
	assert.Contains(t, err.Error(), "namespaced")

	m.Extensions = map[string]map[string]any{"example.com/telemetry": {"endpoint": "https://x"}}
	require.NoError(t, m.Validate())
}

func TestComposeFilesForProfile(t *testing.T) {
	spec := RuntimeSpec{
		Files: []string{"compose/base.yaml"},
		Profiles: map[string][]string{
			"embedded":    {"compose/embedded.yaml"},
			"external-db": {"compose/external.yaml"},
		},
	}

	t.Run("no profile yields the base files", func(t *testing.T) {
		files, err := spec.ComposeFiles("")
		require.NoError(t, err)
		assert.Equal(t, []string{"compose/base.yaml"}, files)
	})

	t.Run("a profile appends its files", func(t *testing.T) {
		files, err := spec.ComposeFiles("embedded")
		require.NoError(t, err)
		assert.Equal(t, []string{"compose/base.yaml", "compose/embedded.yaml"}, files)
	})

	t.Run("an unknown profile is an error naming the valid ones", func(t *testing.T) {
		_, err := spec.ComposeFiles("nonexistent")
		require.Error(t, err, "deploying the wrong topology silently is worse than refusing")

		e := AsError(err)
		assert.Contains(t, e.Hint, "embedded")
		assert.Contains(t, e.Hint, "external-db")
	})

	t.Run("a file listed twice is passed once", func(t *testing.T) {
		dup := RuntimeSpec{
			Files:    []string{"compose/base.yaml"},
			Profiles: map[string][]string{"p": {"compose/base.yaml", "compose/extra.yaml"}},
		}
		files, err := dup.ComposeFiles("p")
		require.NoError(t, err)
		assert.Equal(t, []string{"compose/base.yaml", "compose/extra.yaml"}, files,
			"passing a file twice makes Compose merge it with itself")
	})
}

func TestApplyDefaults(t *testing.T) {
	m := Manifest{
		APIVersion: APIVersionV1Alpha1,
		Kind:       KindApplicationRelease,
		Metadata:   Metadata{Name: "demo", Version: MustParseVersion("1.0.0")},
		Runtime:    RuntimeSpec{Files: []string{"compose/compose.yaml"}},
		Images:     map[string]string{"app": "r/a@sha256:" + strings.Repeat("a", 64)},
		Configuration: []ConfigurationFile{
			{Template: "templates/a.yaml", Target: "/etc/demo/a.yaml"},
		},
		Operations: map[string]OperationSpec{
			"backup": {Kind: OperationKindHook, Command: []string{"hooks/backup"}},
		},
		Health: HealthSpec{Checks: []HealthCheck{
			{Name: "api", Type: HealthHTTP, URL: "http://127.0.0.1/health"},
		}},
	}

	m.ApplyDefaults()

	assert.Equal(t, DefaultRetentionReleases, m.Retention.Releases)
	assert.Equal(t, DefaultRetentionBackups, m.Retention.Backups)
	assert.Equal(t, "compose", m.Providers.Runtime.Name)
	assert.Equal(t, "demo", m.Runtime.Project, "the project defaults to the product name")
	assert.Equal(t, DefaultConfigMode, m.Configuration[0].Mode)
	assert.Equal(t, Duration(DefaultOperationTimeout), m.Operations["backup"].Timeout)
	assert.Equal(t, Duration(DefaultHealthTimeout), m.Health.Checks[0].Timeout)

	require.NoError(t, m.Validate())
}

func TestImageRefsAreStablyOrdered(t *testing.T) {
	m := validManifest()
	m.Images = map[string]string{
		"zebra": "r/z@sha256:" + strings.Repeat("1", 64),
		"alpha": "r/a@sha256:" + strings.Repeat("2", 64),
		"mid":   "r/m@sha256:" + strings.Repeat("3", 64),
	}

	// Map iteration is random; plans and dry-run output must not be.
	first := m.ImageRefs()
	for i := 0; i < 10; i++ {
		assert.Equal(t, first, m.ImageRefs(), "image order must be stable across calls")
	}
	assert.Equal(t, "r/a@sha256:"+strings.Repeat("2", 64), first[0])
}

func TestProductNameIsValidatedAsAPathComponent(t *testing.T) {
	// The name reaches /etc, /var/lib and /run, so it is validated as a
	// path component rather than merely as a string.
	invalid := []string{
		"", "../escape", "with/slash", "Upper", "with space",
		".hidden", "-leading-dash", strings.Repeat("x", 64),
	}
	for _, name := range invalid {
		assert.Error(t, ValidateProductName(name), "%q must be rejected", name)
	}

	for _, name := range []string{"demo", "my-product", "product2", "a"} {
		assert.NoError(t, ValidateProductName(name), "%q must be accepted", name)
	}
}

func TestSecretSchemaValidation(t *testing.T) {
	t.Run("valid schema passes", func(t *testing.T) {
		s := SecretSchema{Secrets: []SecretDeclaration{
			{Name: "db_password", Required: true,
				Generator: Generator{Kind: GeneratorPassword, Length: 32}},
		}}
		require.NoError(t, s.Validate())
	})

	t.Run("names must be usable as filenames", func(t *testing.T) {
		s := SecretSchema{Secrets: []SecretDeclaration{{Name: "../escape"}}}
		require.Error(t, s.Validate())
	})

	t.Run("duplicate names are rejected", func(t *testing.T) {
		s := SecretSchema{Secrets: []SecretDeclaration{{Name: "a"}, {Name: "a"}}}
		require.Error(t, s.Validate())
	})

	t.Run("generated passwords have a floor", func(t *testing.T) {
		s := SecretSchema{Secrets: []SecretDeclaration{
			{Name: "weak", Generator: Generator{Kind: GeneratorPassword, Length: 4}},
		}}
		require.Error(t, s.Validate(), "a four-character generated password is not a password")
	})
}

func TestSecretSchemaServicesFor(t *testing.T) {
	s := SecretSchema{Secrets: []SecretDeclaration{
		{Name: "db_password", Services: []string{"app", "db"}},
		{Name: "session_key", Services: []string{"app"}},
		{Name: "unrelated", Services: []string{"worker"}},
	}}

	// Restarting only the dependents is the difference between a blip and
	// a full outage.
	assert.Equal(t, []string{"app", "db"}, s.ServicesFor([]string{"db_password"}))
	assert.Equal(t, []string{"app"}, s.ServicesFor([]string{"session_key"}))
	assert.Equal(t, []string{"app", "db"}, s.ServicesFor([]string{"db_password", "session_key"}),
		"services must be deduplicated across secrets")
	assert.Empty(t, s.ServicesFor([]string{"nonexistent"}))
}

func TestSecretSchemaMissing(t *testing.T) {
	s := SecretSchema{Secrets: []SecretDeclaration{
		{Name: "required_a", Required: true},
		{Name: "required_b", Required: true},
		{Name: "optional", Required: false},
	}}

	set := NewSecretSet(map[string]Secret{"required_a": NewSecret("value")})

	assert.Equal(t, []string{"required_b"}, s.Missing(set))
	assert.Equal(t, []string{"required_a", "required_b"}, s.RequiredNames())
}

func TestScalarRoundTrips(t *testing.T) {
	t.Run("duration", func(t *testing.T) {
		var d Duration
		require.NoError(t, d.UnmarshalText([]byte("10m")))
		assert.Equal(t, 10*time.Minute, d.Duration())
		assert.Equal(t, "10m0s", d.String())

		require.Error(t, d.UnmarshalText([]byte("-5m")), "a negative timeout is meaningless")
		require.Error(t, d.UnmarshalText([]byte("soon")))
	})

	t.Run("file mode rejects non-permission bits", func(t *testing.T) {
		var m FileMode
		require.NoError(t, m.UnmarshalText([]byte("0640")))
		assert.Equal(t, uint32(0o640), m.Perm())
		assert.Equal(t, "0640", m.String())

		require.Error(t, m.UnmarshalText([]byte("99999")))
		require.Error(t, m.UnmarshalText([]byte("0999")), "9 is not an octal digit")
	})

	t.Run("byte size accepts what operators type", func(t *testing.T) {
		cases := map[string]int64{
			"4GiB": 4 * GiB, "512MiB": 512 * MiB, "40G": 40 * GiB,
			"1KB": 1000, "2MB": 2000000, "1024": 1024, "1B": 1,
		}
		for input, want := range cases {
			var b ByteSize
			require.NoError(t, b.UnmarshalText([]byte(input)), "input %q", input)
			assert.Equal(t, want, b.Bytes(), "input %q", input)
		}

		var b ByteSize
		require.Error(t, b.UnmarshalText([]byte("-1GiB")))
		require.Error(t, b.UnmarshalText([]byte("plenty")))
	})
}

func TestByteSizeIsReadable(t *testing.T) {
	// Manifest values divide evenly and must round-trip unchanged.
	for _, exact := range []string{"4GiB", "512MiB", "2TiB", "40GiB"} {
		var b ByteSize
		require.NoError(t, b.UnmarshalText([]byte(exact)))
		assert.Equal(t, exact, b.String(), "a manifest value must round-trip exactly")
	}

	// Measured values -- free disk, backup sizes -- get one decimal in the
	// largest sensible unit. "1066834032KiB free" is unreadable.
	assert.Equal(t, "1017.4GiB", ByteSize(1066834032*KiB).String())
	assert.Equal(t, "1.5MiB", ByteSize(1536*KiB+512).String())
	assert.Equal(t, "0", ByteSize(0).String())
	assert.Equal(t, "512", ByteSize(512).String(), "sub-KiB values stay a byte count")

	// Whatever String produces must parse back.
	for _, n := range []ByteSize{0, 512, 1536*KiB + 512, 1066834032 * KiB, 4 * GiB} {
		var round ByteSize
		require.NoError(t, round.UnmarshalText([]byte(n.String())), "cannot re-parse %q", n)
	}
}
