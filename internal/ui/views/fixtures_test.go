package views_test

import (
	"time"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/events"
	"github.com/morzecrew/morzer/internal/lifecycle/ops"
	"github.com/morzecrew/morzer/internal/ports"
	"github.com/morzecrew/morzer/internal/ui/views"
)

// The fixtures every rendering test runs against.
//
// Built here rather than captured from a live run, so the density rule and the
// widths are pinned independently of what a real machine's checks happen to
// find. Each carries the words its rendering must not lose.

func version(s string) domain.Version {
	v, err := domain.ParseVersion(s)
	if err != nil {
		panic(err)
	}
	return v
}

func statusFixtures() []fixture {
	healthy := ops.Status{
		Product:        "demo",
		InstallationID: "inst_01KZP9ZZTKTQE1SS3JRZ0000",
		Profile:        "embedded",
		PublicURL:      "https://demo.example",
		SupportURL:     "https://support.example/demo",
		CurrentRelease: &domain.ReleaseRecord{Version: version("1.2.0")},
		Services: []ports.ServiceState{
			{Name: "app", State: "running", Health: ports.HealthHealthy},
			{Name: "postgres", State: "running", Health: ports.HealthHealthy},
		},
		Health: []ports.HealthResult{
			{Name: "http", OK: true, Message: "200 in 12ms"},
		},
		LastBackup: &ops.BackupSummary{ID: "bkp_01KZP9ZZTK", Age: "2h"},
		LastOperation: &domain.OperationRecord{
			ID: "op_01KZP9ZZTK", Type: domain.OpTypeApply, Status: domain.StatusSucceeded,
		},
	}

	// Everything that only appears when something is wrong, in one report:
	// a sandbox banner, a staged release nobody applied, a stopped service,
	// a failing health probe, a held lock, an operation needing hands, and
	// a problem long enough to wrap.
	troubled := ops.Status{
		Product:         "demo",
		InstallationID:  "inst_01KZP9ZZTKTQE1SS3JRZ0000",
		Mode:            "sandbox",
		Profile:         "embedded",
		PublicURL:       "https://demo.example",
		CurrentRelease:  &domain.ReleaseRecord{Version: version("1.2.0")},
		PreviousRelease: &domain.ReleaseRecord{Version: version("1.1.0")},
		StagedRelease:   &domain.UpdateCandidate{Version: version("1.3.0")},
		Services: []ports.ServiceState{
			{Name: "app", State: "running", Health: ports.HealthHealthy},
			{Name: "background-worker", State: "exited (137)"},
		},
		Health: []ports.HealthResult{
			{Name: "http", OK: false, Message: "connection refused after 3 attempts"},
		},
		LastOperation: &domain.OperationRecord{
			ID: "op_01KZP9ZZTK", Type: domain.OpTypeUpdate, Status: domain.StatusFailed,
		},
		LockHeldBy: &ports.LockOwner{
			Type: string(domain.OpTypeBackup), OperationID: "op_01KZPA1111", PID: 4242,
		},
		NeedsAttention: []domain.OperationRecord{{
			ID: "op_01KZPA2222", Type: domain.OpTypeRestore,
			Error: &domain.Error{
				Message: "the database was restored but the volume copy did not finish",
				Hint:    "run `morzer restore --resume bkp_01KZP9ZZTK` or restore the volumes by hand",
			},
		}},
		Problems: []string{
			"the container registry could not be reached, so this report cannot say " +
				"whether a newer release is available",
		},
	}

	return []fixture{
		{
			name:   "status-healthy",
			value:  healthy,
			fields: []string{"demo", "1.2.0", "embedded", "postgres", "running", "bkp_01KZP9ZZTK"},
		},
		{
			name:  "status-troubled",
			value: troubled,
			fields: []string{
				"sandbox", "1.3.0", "background-worker", "exited (137)",
				"connection refused", "op_01KZPA2222", "pid 4242", "registry",
			},
		},
	}
}

// listFixtures are the four hand-rolled printf tables this wave replaced, plus
// the three listings that grew from the same component.
//
// They carry the shapes those tables broke on: a secret name longer than the
// 28 the format string reserved, a release in every role including previous,
// an unreachable target whose error is a sentence, and a comment nobody set.
func listFixtures() []fixture {
	at := domain.NewTime(time.Date(2026, 8, 10, 9, 15, 0, 0, time.UTC))

	return []fixture{
		{
			name: "secrets",
			value: []ports.SecretMetadata{
				{Name: "db_password", Fingerprint: "ec778838e623", Length: 32, LastChanged: at},
				{Name: "session_signing_key_for_the_web_tier", Fingerprint: "b7d4e86ea2e5", Length: 64, LastChanged: at},
				{Name: "smtp_password", Fingerprint: "0f1a2b3c4d5e", Length: 24},
			},
			fields: []string{"db_password", "session_signing_key_for_the_web_tier", "ec778838e623", "unknown"},
		},
		{
			name: "releases",
			value: []ops.ReleaseEntry{
				{Version: version("1.5.0"), Root: "/opt/demo/releases/1.5.0", Staged: true},
				{Version: version("1.4.0"), Root: "/opt/demo/releases/1.4.0"},
				{Version: version("1.3.0"), Root: "/opt/demo/releases/1.3.0", Current: true},
				{Version: version("1.2.0"), Root: "/opt/demo/releases/1.2.0", Previous: true},
			},
			fields: []string{"1.5.0", "staged", "1.3.0", "current", "1.2.0", "previous"},
		},
		{
			name: "backups",
			value: []ports.BackupRef{
				{ID: "bkp_01KZP9ZZTKTQE1SS3JRZ", At: at, Size: 4_294_967_296},
				{ID: "bkp_01KZP7YYSJSPD0RR2IQY", At: at, Size: 1_048_576},
			},
			fields: []string{"bkp_01KZP9ZZTKTQE1SS3JRZ", "4GiB"},
		},
		{
			name: "recipients",
			value: []ports.Recipient{
				{PublicKey: "age1qutq8nmte0t8fwd2n7qh3lqyrcazy44z2np735cxes05yau7faasgf02hl", Kind: ports.RecipientMachine},
				{PublicKey: "age14zamz0thlnq8atx3t3lanyx2hfl0tdvpphtrfyad4m6fjxcmhgpsvqu82q", Kind: ports.RecipientRecovery, Comment: "offsite safe"},
			},
			fields: []string{"age1qutq8nmte0t8fwd2n7qh3lqyrcazy44z2np735cxes05yau7faasgf02hl", "offsite safe"},
		},
		{
			name:   "backups-empty",
			value:  []ports.BackupRef{},
			fields: []string{"no backups"},
		},
		{
			name: "rendered-secrets",
			value: []ports.RenderedFile{
				{Name: "db_password", Path: "/run/demo/secrets/db_password", Mode: 0o400},
				{Name: "session_key", Path: "/run/demo/secrets/session_key", Mode: 0o440},
			},
			fields: []string{"db_password", "/run/demo/secrets/db_password", "0400"},
		},
		{
			name: "remote-backups",
			value: []ops.RemoteBackup{
				{
					Target: "s3://demo-backups/prod",
					Manifest: ports.BackupManifest{
						ID: "bkp_01KZP9ZZTKTQE1SS3JRZ", CreatedAt: at,
						ReleaseVersion: version("1.3.0"),
					},
				},
			},
			fields: []string{"bkp_01KZP9ZZTKTQE1SS3JRZ", "s3://demo-backups/prod", "1.3.0"},
		},
		{
			name: "targets",
			value: []ops.TargetStatus{
				{URL: "file:///srv/offsite", Reachable: true, Backups: 4, Latest: "bkp_01KZP9ZZTKTQE1SS3JRZ"},
				{
					URL: "s3://demo-backups/prod", Reachable: false,
					// Short enough that the table shows every
					// column at the measure. A pathological
					// error is a different question, and
					// TestACellTooWideForTheMeasureIsCut... is
					// where it is asked.
					Error: "dial tcp 203.0.113.7:443: timeout",
				},
			},
			fields: []string{
				"file:///srv/offsite", "s3://demo-backups/prod", "unreachable",
				// The newest backup a target holds: TargetStatus
				// carries it and the listing dropped it, which is
				// operational data the report already had.
				"bkp_01KZP9ZZTKTQE1SS3JRZ",
			},
		},
	}
}

// calloutFixtures cover the shapes the box arithmetic has to survive: a title
// wider than the body it frames, and a body of one short line.
func calloutFixtures() []fixture {
	return []fixture{
		{
			name: "keypair",
			value: views.KeyPair{
				PublicKey: "age14zamz0thlnq8atx3t3lanyx2hfl0tdvpphtrfyad4m6fjxcmhgpsvqu82q",
				Path:      "/root/demo-recovery.key",
			},
			fields:   []string{"age14zamz0thlnq8atx3t3lanyx2hfl0tdvpphtrfyad4m6fjxcmhgpsvqu82q"},
			verbatim: true,
		},
		{
			name: "version",
			value: views.Version{
				Version:              "1.4.0",
				Commit:               "9ac42a3",
				Built:                "2026-08-10T17:38:58Z",
				SupportedAPIVersions: []string{"selfhost/v1alpha1"},
			},
			fields: []string{"1.4.0", "9ac42a3", "selfhost/v1alpha1"},
		},
	}
}

func doctorFixtures() []fixture {
	check := func(category, id, desc string, status events.CheckStatus, message, remedy string) events.CheckResult {
		return events.CheckResult{
			ID: id, Category: category, Description: desc,
			Status: status, Message: message, Remedy: remedy,
			Duration: time.Millisecond,
		}
	}

	clean := ops.DoctorReport{Results: []events.CheckResult{
		check("configuration", "config.installation", "installation configuration is valid", events.CheckOK, "demo (inst_01KZ)", ""),
		check("configuration", "config.parameters", "every declared parameter has a value", events.CheckOK, "9 parameters", ""),
		check("storage", "storage.disk", "enough free disk space", events.CheckOK, "41 GiB free", ""),
		check("storage", "storage.dirs", "the managed directories exist", events.CheckOK, "", ""),
		check("tools", "tools.docker", "docker is available", events.CheckOK, "28.1.1", ""),
	}}
	clean.Summary.OK = len(clean.Results)

	// The report the density rule exists for: most groups fine, two with
	// something to say, and a message long enough to prove it goes beneath
	// its check rather than beside it.
	mixed := ops.DoctorReport{
		Results: []events.CheckResult{
			check("configuration", "config.installation", "installation configuration is valid", events.CheckOK, "demo (inst_01KZ)", ""),
			check("configuration", "config.parameters", "every declared parameter has a value", events.CheckOK, "9 parameters", ""),
			check("storage", "storage.disk", "enough free disk space", events.CheckOK, "41 GiB free", ""),
			check("network", "network.registry", "the container registry is reachable",
				events.CheckWarn,
				"cannot reach registry.example/demo/app: dial tcp 203.0.113.7:443: i/o timeout",
				"check the machine's outbound network, or run `morzer update --bundle` from a copy"),
			check("runtime", "runtime.images", "release images are available offline",
				events.CheckFail,
				"2 of 2 images referenced by the release are not present locally",
				"run `morzer release ingest <bundle>` on this machine"),
			check("runtime", "runtime.services", "all services are running", events.CheckOK, "2 running", ""),
			check("tools", "tools.docker", "docker is available", events.CheckOK, "28.1.1", ""),
		},
		SupportURL: "https://support.example/demo",
	}
	mixed.Summary.OK, mixed.Summary.Warn, mixed.Summary.Fail = 5, 1, 1
	mixed.Worst = events.CheckFail

	return []fixture{
		{
			name:   "doctor-clean",
			value:  clean,
			fields: []string{"configuration", "storage", "tools", "5 ok"},
		},
		{
			name:  "doctor-mixed",
			value: mixed,
			fields: []string{
				"network", "registry.example", "i/o timeout", "runtime",
				"release ingest", "5 ok", "1 warning", "1 failed", "support.example",
			},
		},
		{
			name:   "doctor-verbose",
			value:  views.Verbose{DoctorReport: mixed},
			fields: []string{"enough free disk space", "41 GiB free", "docker is available"},
		},
	}
}

// machineFixtures are the listing RFC 0020 adds.
//
// The three shapes it has to survive, in one machine: an installation running
// normally, a sandbox that has never had a release installed, and one whose
// state will not load -- which is the row that must be present rather than
// tidied away, because the moment it breaks is the moment somebody is looking
// for it.
func machineFixtures() []fixture {
	five, none := 5, 0

	entries := []ops.InstallationEntry{
		{
			Product: "demo", Path: "/etc/demo", SchemaVersion: 5,
			Release: version("1.4.0"), Units: &five,
		},
		{
			Product: "sandbox", Path: "/etc/sandbox", SchemaVersion: 5,
			Mode: domain.ModeDev, Units: &none,
		},
		{
			// A unit count nobody could read, beside a state that
			// loaded fine: the two failures are independent, and
			// the table has to be able to say so on one row.
			Product: "staging", Path: "/etc/staging", SchemaVersion: 5,
			Release: version("1.3.0"),
		},
		{
			Product: "legacy", Path: "/etc/legacy", Units: &five,
			Problem: "installation state at /var/lib/legacy/manager/installation.json " +
				"is invalid: installation was written by a newer manager " +
				"(schema 9, this manager reads 5)",
		},
		{
			// A directory discovery could not open. On a real host
			// most of these belong to somebody else, so it is a
			// warning rather than a failure and is not counted.
			Product: "credstore", Path: "/etc/credstore", Skipped: true,
			Problem: "cannot be read by this process, so it is not counted as an " +
				"installation; re-run as root if it is one",
		},
	}

	// The same machine with --status. The timeout is on a *readable*
	// installation, because that is the only kind that gets as far as asking
	// the runtime: a row with a Problem returns before the query, so a
	// fixture carrying both would pin an output the code cannot produce.
	withServices := views.WithServices{
		entries[0], entries[1], entries[2], entries[3], entries[4],
	}
	withServices[0].Services = &ops.ServiceCounts{Running: 3, Total: 3}
	withServices[1].Services = &ops.ServiceCounts{Running: 1, Total: 4}
	withServices[2].ServicesProblem = "timed out after 5s"

	return []fixture{
		{
			name:  "installations",
			value: entries,
			fields: []string{
				"demo", "1.4.0", "sandbox", "dev", "staging", "unknown",
				"legacy", "unreadable", "newer manager",
				"credstore", "not counted",
			},
		},
		{
			name:   "installations-status",
			value:  withServices,
			fields: []string{"3/3", "1/4", "unknown", "timed out after 5s", "without the deployment lock"},
		},
		{
			name:   "installations-empty",
			value:  []ops.InstallationEntry{},
			fields: []string{"no installations"},
		},
	}
}
