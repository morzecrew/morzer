package domain

import (
	"strings"
	"testing"
	"time"
)

// The manifest contract is what a vendor writes against, so every rule in it
// is a promise: break it and the bundle is refused by name, at `release
// verify`, rather than halfway through an apply on somebody's server.
//
// One table, one mutation each, so a failure names the rule rather than the
// document. The baseline is validManifest() from manifest_test.go.

func TestEveryManifestRuleIsEnforcedByName(t *testing.T) {
	cases := map[string]struct {
		mutate func(*Manifest)
		field  string
	}{
		// The schema gate. Everything else is noise if this is wrong.
		"no api_version": {
			func(m *Manifest) { m.APIVersion = "" }, "api_version",
		},
		"the wrong kind": {
			func(m *Manifest) { m.Kind = "application-bundle" }, "kind",
		},

		// metadata
		"no name": {
			func(m *Manifest) { m.Metadata.Name = "" }, "metadata.name",
		},
		"a name that is a path": {
			func(m *Manifest) { m.Metadata.Name = "../etc" }, "metadata.name",
		},
		"no version": {
			func(m *Manifest) { m.Metadata.Version = Version{} }, "metadata.version",
		},
		// Build metadata is retained by String() -- so it reaches the
		// release store's directory name -- and ignored by Compare, so
		// two builds differing only in metadata occupy different
		// directories that the digest-conflict check never compares.
		// Two bundles then claim one version and nothing notices.
		"a version carrying build metadata": {
			func(m *Manifest) { m.Metadata.Version = MustParseVersion("1.2.0+build.7") },
			"metadata.version",
		},

		"release notes outside the bundle": {
			func(m *Manifest) { m.Metadata.ReleaseNotes = "../../etc/passwd" },
			"metadata.release_notes",
		},
		// A support link is shown to an operator who is already in
		// trouble, which is the worst moment to send them somewhere
		// over plaintext -- or to a URL with no host, or to a string
		// carrying terminal escape sequences, since it is printed to a
		// terminal by `status` and by a failing `doctor`.
		"a plaintext support url": {
			func(m *Manifest) { m.Metadata.SupportURL = "http://support.example" },
			"metadata.support_url",
		},
		"a support url with no host": {
			func(m *Manifest) { m.Metadata.SupportURL = "https://" },
			"metadata.support_url",
		},
		"a support url whose host is empty but path is not": {
			func(m *Manifest) { m.Metadata.SupportURL = "https:///help" },
			"metadata.support_url",
		},
		"a support url carrying an escape sequence": {
			func(m *Manifest) { m.Metadata.SupportURL = "https://ok.example/\x1b[2J" },
			"metadata.support_url",
		},

		// bundle -- the artefact's own description
		"a negative uncompressed size": {
			func(m *Manifest) { m.Bundle.UncompressedSize = ByteSize(-1) },
			"bundle.uncompressed_size",
		},

		// requirements
		"a negative cpu requirement": {
			func(m *Manifest) { m.Requirements.CPUs = -1 }, "requirements.cpus",
		},

		// parameters -- two contradictory statements
		"a parameter that is required and has a default": {
			func(m *Manifest) {
				m.Parameters = map[string]ParameterSpec{
					"http_port": {Type: ParamPort, Required: true, Default: "8080"},
				}
			},
			"parameters.http_port",
		},

		// health
		"a negative start period": {
			func(m *Manifest) {
				m.Health.Checks = []HealthCheck{{
					Name: "api", Type: HealthHTTP, URL: "http://127.0.0.1/health",
					StartPeriod: Duration(-1),
				}}
			},
			"health.checks[0].start_period",
		},

		// providers and runtime
		"no runtime provider": {
			func(m *Manifest) { m.Providers.Runtime.Name = "" }, "providers.runtime.name",
		},
		"no compose files": {
			func(m *Manifest) { m.Runtime.Files = nil }, "runtime.files",
		},
		"a compose file outside the bundle": {
			func(m *Manifest) { m.Runtime.Files = []string{"../../etc/passwd"} }, "runtime.files[0]",
		},
		"a profile that selects nothing": {
			func(m *Manifest) { m.Runtime.Profiles = map[string][]string{"embedded": {}} },
			"runtime.profiles.embedded",
		},
		"a profile file outside the bundle": {
			func(m *Manifest) {
				m.Runtime.Profiles = map[string][]string{"embedded": {"/etc/passwd"}}
			},
			"runtime.profiles.embedded[0]",
		},

		// requirements.ports -- literal or a parameter reference, never junk
		"an empty port entry": {
			func(m *Manifest) { m.Requirements.Ports = []PortSpec{""} },
			"requirements.ports[0]",
		},
		"a port that is not a number": {
			func(m *Manifest) { m.Requirements.Ports = []PortSpec{"http"} },
			"requirements.ports[0]",
		},
		"a port of zero": {
			func(m *Manifest) { m.Requirements.Ports = []PortSpec{"0"} },
			"requirements.ports[0]",
		},
		"a port above the range": {
			func(m *Manifest) { m.Requirements.Ports = []PortSpec{"65536"} },
			"requirements.ports[0]",
		},
		"a template that does not parse": {
			func(m *Manifest) { m.Requirements.Ports = []PortSpec{"{{ .Parameters.http_port "} },
			"requirements.ports[0]",
		},

		// images
		"no images at all": {
			func(m *Manifest) { m.Images = nil }, "images",
		},
		// The key becomes the tail of <PRODUCT>_IMAGE_<NAME>, which is
		// upper-cased with "-" and "." folded to "_". Unconstrained,
		// `web-ui` and `web.ui` produced the same variable and one
		// pinned reference silently overwrote the other -- with Go's
		// randomised map iteration deciding which.
		"an image name with a dot": {
			func(m *Manifest) {
				m.Images = map[string]ImageSpec{
					"web.ui": {Ref: "r/a@sha256:" + strings.Repeat("a", 64)},
				}
			},
			"images.web.ui",
		},
		"an image name with an underscore": {
			func(m *Manifest) {
				m.Images = map[string]ImageSpec{
					"web_ui": {Ref: "r/a@sha256:" + strings.Repeat("a", 64)},
				}
			},
			"images.web_ui",
		},
		"an image name in capitals": {
			func(m *Manifest) {
				m.Images = map[string]ImageSpec{
					"App": {Ref: "r/a@sha256:" + strings.Repeat("a", 64)},
				}
			},
			"images.App",
		},

		// configuration
		"a template outside the bundle": {
			func(m *Manifest) {
				m.Configuration = []ConfigurationFile{
					{Template: "../../../etc/shadow", Target: "/etc/demo/app.yaml"},
				}
			},
			"configuration[0].template",
		},
		"a configuration file with no target": {
			func(m *Manifest) {
				m.Configuration = []ConfigurationFile{{Template: "templates/app.yaml"}}
			},
			"configuration[0].target",
		},

		// secrets
		"a secret schema outside the bundle": {
			func(m *Manifest) { m.Secrets.Schema = "../secrets.yaml" }, "secrets.schema",
		},
		"a relative secrets source": {
			func(m *Manifest) { m.Secrets.Source = "secrets.sops.yaml" }, "secrets.source",
		},
		"a relative render_to": {
			func(m *Manifest) {
				m.Secrets.Source = "/etc/demo/secrets.sops.yaml"
				m.Secrets.RenderTo = "run/demo/secrets"
			},
			"secrets.render_to",
		},

		// operations
		"a runtime-service operation with no service": {
			func(m *Manifest) {
				m.Operations = map[string]OperationSpec{
					"migrate": {Kind: OperationKindRuntimeService},
				}
			},
			"operations.migrate.service",
		},
		"a runtime-service operation that also gives a command": {
			func(m *Manifest) {
				m.Operations = map[string]OperationSpec{
					"migrate": {Kind: OperationKindRuntimeService, Service: "app",
						Command: []string{"migrate"}},
				}
			},
			"operations.migrate.command",
		},
		"a hook operation with no command": {
			func(m *Manifest) {
				m.Operations = map[string]OperationSpec{"migrate": {Kind: OperationKindHook}}
			},
			"operations.migrate.command",
		},
		"a hook command outside the bundle": {
			func(m *Manifest) {
				m.Operations = map[string]OperationSpec{
					"migrate": {Kind: OperationKindHook, Command: []string{"/bin/sh"}},
				}
			},
			"operations.migrate.command[0]",
		},
		"a hook operation that also names a service": {
			func(m *Manifest) {
				m.Operations = map[string]OperationSpec{
					"migrate": {Kind: OperationKindHook, Command: []string{"hooks/migrate"},
						Service: "app"},
				}
			},
			"operations.migrate.service",
		},
		"an operation with no kind": {
			func(m *Manifest) {
				m.Operations = map[string]OperationSpec{"migrate": {Command: []string{"hooks/migrate"}}}
			},
			"operations.migrate.kind",
		},
		"an operation of an unknown kind": {
			func(m *Manifest) {
				m.Operations = map[string]OperationSpec{"migrate": {Kind: "systemd-unit"}}
			},
			"operations.migrate.kind",
		},
		"a negative timeout": {
			func(m *Manifest) {
				m.Operations = map[string]OperationSpec{
					"migrate": {Kind: OperationKindHook, Command: []string{"hooks/migrate"},
						Timeout: Duration(-time.Second)},
				}
			},
			"operations.migrate.timeout",
		},

		// backup.volumes -- a claim about reading a volume live, so the
		// two plausible typos both fail towards a backup the vendor
		// believes is fast and correct
		"a volume listed with no consistency": {
			func(m *Manifest) {
				m.Backup = BackupSpec{Volumes: map[string]VolumeSpec{"uploads": {}}}
			},
			"backup.volumes.uploads.consistency",
		},
		"a consistency that is none of the three": {
			func(m *Manifest) {
				m.Backup = BackupSpec{Volumes: map[string]VolumeSpec{"uploads": {Consistency: "warm"}}}
			},
			"backup.volumes.uploads.consistency",
		},

		// health
		"a health check with no name": {
			func(m *Manifest) {
				m.Health.Checks = []HealthCheck{{Type: HealthHTTP, URL: "http://127.0.0.1/health"}}
			},
			"health.checks[0].name",
		},
		"two health checks with the same name": {
			func(m *Manifest) {
				m.Health.Checks = []HealthCheck{
					{Name: "api", Type: HealthHTTP, URL: "http://127.0.0.1/a"},
					{Name: "api", Type: HealthHTTP, URL: "http://127.0.0.1/b"},
				}
			},
			"health.checks[1].name",
		},
		"an http check with no url": {
			func(m *Manifest) {
				m.Health.Checks = []HealthCheck{{Name: "api", Type: HealthHTTP}}
			},
			"health.checks[0].url",
		},
		"a tcp check with no address": {
			func(m *Manifest) {
				m.Health.Checks = []HealthCheck{{Name: "db", Type: HealthTCP}}
			},
			"health.checks[0].address",
		},
		"a command check with no command": {
			func(m *Manifest) {
				m.Health.Checks = []HealthCheck{{Name: "db", Type: HealthCommand}}
			},
			"health.checks[0].command",
		},
		"a command check pointing outside the bundle": {
			func(m *Manifest) {
				m.Health.Checks = []HealthCheck{
					{Name: "db", Type: HealthCommand, Command: []string{"/usr/bin/pg_isready"}},
				}
			},
			"health.checks[0].command[0]",
		},
		"a health check with no type": {
			func(m *Manifest) {
				m.Health.Checks = []HealthCheck{{Name: "api"}}
			},
			"health.checks[0].type",
		},
		"a health check of an unknown type": {
			func(m *Manifest) {
				m.Health.Checks = []HealthCheck{{Name: "api", Type: "smtp"}}
			},
			"health.checks[0].type",
		},

		// compatibility and retention
		"a schema range that is inside out": {
			func(m *Manifest) {
				m.Compatibility.DatabaseSchemaMin = 12
				m.Compatibility.DatabaseSchemaMax = 10
			},
			"compatibility",
		},
		// Negative schema numbers are refused rather than ignored,
		// because every check that reads one is guarded by `> 0`: a
		// vendor who typed one would have made a declaration that
		// decides nothing while looking like a declaration. For
		// database_schema_produces that is the difference between the
		// unattended gate refusing a release and passing its schema half
		// without looking at it.
		"a schema minimum below zero": {
			func(m *Manifest) { m.Compatibility.DatabaseSchemaMin = -1 },
			"compatibility.database_schema_min",
		},
		"a schema maximum below zero": {
			func(m *Manifest) { m.Compatibility.DatabaseSchemaMax = -1 },
			"compatibility.database_schema_max",
		},
		"a negative prediction of what the migrations produce": {
			func(m *Manifest) { m.Compatibility.DatabaseSchemaProduces = -1 },
			"compatibility.database_schema_produces",
		},
		"keeping no releases": {
			func(m *Manifest) { m.Retention.Releases = 0 }, "retention.releases",
		},
		"keeping no backups": {
			func(m *Manifest) { m.Retention.Backups = 0 }, "retention.backups",
		},

		// extensions
		"an extension key that is not namespaced": {
			func(m *Manifest) {
				m.Extensions = map[string]map[string]any{"telemetry": {"endpoint": "x"}}
			},
			"extensions.telemetry",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			m := validManifest()
			tc.mutate(&m)

			err := m.Validate()
			if err == nil {
				t.Fatalf("%s was accepted, so a bundle with it reaches a "+
					"deployment before anyone notices", name)
			}
			if !strings.Contains(err.Error(), tc.field) {
				t.Errorf("the refusal does not name %q, so the vendor has to "+
					"guess which field is wrong:\n%v", tc.field, err)
			}
		})
	}
}

// TestAConstraintMayStillCarryBuildMetadata is the other half of the refusal
// above.
//
// A release's own version may not carry metadata, because that identity keys
// the store; a *constraint* may, because it is a range rather than an identity
// and metadata already decides nothing inside one. Without this, widening the
// refusal into the constraint path would break `upgrade_from` and nothing would
// fail.
func TestAConstraintMayStillCarryBuildMetadata(t *testing.T) {
	m := validManifest()
	constraint, err := ParseConstraint(">=1.0.0+build.7")
	if err != nil {
		t.Fatal(err)
	}
	m.Compatibility.UpgradeFrom = constraint

	if err := m.Validate(); err != nil {
		t.Fatalf("a constraint carrying build metadata was refused: %v", err)
	}
	if !m.Compatibility.UpgradeFrom.Allows(MustParseVersion("1.5.0")) {
		t.Error("the constraint no longer admits a version inside its range")
	}
}

// TestApplyDefaultsFillsInWhatAVendorLeftOut. Defaults are applied before
// validation, so a manifest that omits a timeout is complete rather than
// invalid -- and the retention floor is why the two `retention` rules above
// have to be provoked deliberately.
func TestDefaultsAreAppliedBeforeValidation(t *testing.T) {
	m := Manifest{
		APIVersion: APIVersionV1Alpha1,
		Kind:       KindApplicationRelease,
		Metadata:   Metadata{Name: "demo", Version: MustParseVersion("1.0.0")},
		Providers:  Providers{Runtime: Provider{Name: "compose"}},
		Runtime:    RuntimeSpec{Files: []string{"compose/compose.yaml"}},
		Images: map[string]ImageSpec{
			"app": {Ref: "registry.example/demo/app@sha256:" + strings.Repeat("a", 64)},
		},
		Operations: map[string]OperationSpec{
			"migrate": {Kind: OperationKindHook, Command: []string{"hooks/migrate"}},
		},
		Health: HealthSpec{
			Checks: []HealthCheck{{Name: "api", Type: HealthHTTP, URL: "http://127.0.0.1/health"}},
		},
	}
	m.ApplyDefaults()

	if err := m.Validate(); err != nil {
		t.Fatalf("a manifest that only omitted defaultable fields was refused: %v", err)
	}
	if m.Operations["migrate"].Timeout == 0 {
		t.Error("an operation with no timeout would run forever")
	}
	if m.Health.Checks[0].Timeout == 0 {
		t.Error("a health check with no timeout would hang an apply")
	}
	if m.Retention.Releases < 1 || m.Retention.Backups < 1 {
		t.Error("the retention defaults would delete the only copy of something")
	}
}

// TestTheLoaderAndTheGeneratedSchemaAgreeOnConsistency. The schema's enum is
// the three values and nothing else, so anything the loader accepts here that
// the enum does not is a document an editor refuses and the manager takes --
// or the reverse, which is worse.
func TestTheLoaderAndTheGeneratedSchemaAgreeOnConsistency(t *testing.T) {
	for _, c := range VolumeConsistencies {
		m := validManifest()
		m.Backup = BackupSpec{Volumes: map[string]VolumeSpec{"uploads": {Consistency: c}}}
		if err := m.Validate(); err != nil {
			t.Errorf("%q is in the schema's enum and was refused by the loader: %v", c, err)
		}
	}

	// Absence from the map is how a vendor asks for the default, and it
	// stays legal -- the rule is about a volume listed and left blank.
	m := validManifest()
	m.Backup = BackupSpec{Volumes: map[string]VolumeSpec{}}
	if err := m.Validate(); err != nil {
		t.Errorf("an empty backup section was refused, so a release that declares "+
			"nothing about its volumes cannot be published: %v", err)
	}
	if got := m.Backup.Consistency("anything"); got != VolumeCold {
		t.Errorf("an undeclared volume resolves to %q; anything but cold makes the "+
			"vendor's claim on their behalf", got)
	}
}

// TestOperationLookup is what every operation calls before it runs a hook.
func TestOperationLookup(t *testing.T) {
	m := validManifest()
	m.Operations = map[string]OperationSpec{
		OpBackup: {Kind: OperationKindHook, Command: []string{"hooks/backup"}},
	}

	if op, ok := m.Operation(OpBackup); !ok || op.Command[0] != "hooks/backup" {
		t.Errorf("Operation(%q) = %+v, %v", OpBackup, op, ok)
	}
	if _, ok := m.Operation(OpRestore); ok {
		t.Error("an operation the manifest does not declare was found")
	}
}
