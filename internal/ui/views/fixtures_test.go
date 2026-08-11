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
