package cli

import (
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/lifecycle/ops"
	"github.com/morzecrew/morzer/internal/ui/views"
)

// `morzer fleet` — RFC 0026 P1 and P2.
//
// Two verbs and, by decision 2, never a third that acts. There is no `fleet
// update`, no `fleet exec` and no fan-out, and the fact that updating ten
// machines therefore means ten `morzer update`s over ssh is load-bearing rather
// than an omission: it keeps the destructive path per-machine, per-decision and
// locally journalled. Every user of this will ask for that to be relaxed within
// a week. The refusal is the product.

func newFleetCommand(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "fleet",
		Short: "See several machines without running a control plane",
		Long: "Each installation publishes one small document at a stable key on a\n" +
			"target it already uses, and a stateless command reads them back. No\n" +
			"agent, no listener, no database, and no inbound connection to a\n" +
			"managed machine — which is the point rather than a limitation.\n\n" +
			"The read is the valuable half, and it is the only half. There is no\n" +
			"command here that acts on a remote machine, and there will not be:\n" +
			"a console that can act needs to know who may act, and that is the\n" +
			"authorisation model this project exists without.",
	}
	cmd.AddCommand(
		installationScope(newFleetPublishCommand(app)),
		machineScope(newFleetListCommand(app)),
	)
	return cmd
}

func newFleetPublishCommand(app *App) *cobra.Command {
	var (
		targetURL       string
		credentialsFile string
	)

	cmd := &cobra.Command{
		Use:   "publish",
		Short: "Write this installation's row to the targets it already uses",
		Long: "Publishes one small document saying what is deployed here, whether it\n" +
			"is running, whether its configuration has drifted and what it last\n" +
			"did — to the same targets this installation keeps its backups on, at\n" +
			"a key derived from the product and the installation id.\n\n" +
			"What it never carries: parameter values, hostnames, container logs,\n" +
			"configuration content. Drift is published as a *count* of targets\n" +
			"that differ, because the number is the signal and the files are on\n" +
			"this machine for whoever is allowed to look.\n\n" +
			"Nothing here is scheduled. Run it from cron or from a systemd timer\n" +
			"and it is safe to repeat: it reads what is already at the key first\n" +
			"and declines to replace a newer row with an older one, or one a newer\n" +
			"manager wrote. `--force` overrides both, which is the way back when a\n" +
			"stray document is sitting at the key. That check is best effort — a\n" +
			"write-only credential cannot perform it, which is the credential this\n" +
			"design wants, so the report says when it was skipped rather than\n" +
			"refusing to publish.\n\n" +
			"A failed publish fails nothing. A row that did not leave is a gap in\n" +
			"a view whose subject is fine, and this machine still knows everything\n" +
			"the row would have said.",
		Example: "  morzer fleet publish\n" +
			"  morzer fleet publish --dry-run\n" +
			"  morzer fleet publish --json | jq -r '.data.key'",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			creds, err := readCredentialsFile(credentialsFile)
			if err != nil {
				return err
			}

			result, err := ops.FleetPublish(cmd.Context(), app.Deps, ops.FleetPublishOptions{
				TargetOptions: ops.TargetOptions{
					Options:     app.operationOptions(),
					URL:         targetURL,
					Credentials: creds,
				},
			})
			if err != nil {
				return err
			}
			app.finish(result)

			// Non-zero when a target did not answer, so a cron job finds
			// out. A row declined because something newer was already
			// there is not a failure: that is the check working.
			if report, ok := result.Data.(ops.FleetPublishReport); ok && report.Unreachable() {
				return domain.Preflight(nil, "some targets did not answer")
			}
			return nil
		},
	}

	f := cmd.Flags()
	f.StringVar(&targetURL, "target", "",
		"target URL to publish to; the installation's targets when omitted")
	f.StringVar(&credentialsFile, "credentials-file", "",
		"YAML file holding the target's credentials")
	return cmd
}

func newFleetListCommand(app *App) *cobra.Command {
	var (
		credentialsFile string
		rosterFile      string
		staleAfter      time.Duration
	)

	cmd := &cobra.Command{
		Use:     "ls [target-url]",
		Aliases: []string{"list"},
		Short:   "Read every installation's row off a target",
		Long: "Lists the rows published under a target's `fleet/` prefix, fetches\n" +
			"each one and prints a table. Stateless: no daemon, no database, no\n" +
			"cache, and nothing on this machine is read or written. It runs on a\n" +
			"laptop.\n\n" +
			"A row that will not parse, that a newer manager wrote, or that sits\n" +
			"at a key naming a different installation is printed carrying that\n" +
			"problem. A view that quietly dropped what it could not read would\n" +
			"report health it never observed, which is worse than no view.\n\n" +
			"**Without `--expect` it cannot authenticate anything, and says so\n" +
			"on every run.** The `signature` column then says a signature is\n" +
			"*there*, never that it checks out — and checking one against the key\n" +
			"the row itself carries would establish nothing, because a machine\n" +
			"overwriting its neighbour's row rewrites the payload, the key and\n" +
			"the signature together.\n\n" +
			"`--expect` takes a roster: which installations exist, and the public\n" +
			"key each one signs with. It is the anchor, and it is also the only\n" +
			"way an installation that stopped publishing can appear at all — an\n" +
			"object that was never written cannot announce itself. Both answers\n" +
			"arrive together because both have the same cause.\n\n" +
			"With no target URL, the installation on this machine names them.",
		Example: "  morzer fleet ls s3://bucket/prefix\n" +
			"  morzer fleet ls s3://bucket/prefix --expect ./roster.yaml\n" +
			"  morzer fleet ls s3://bucket/prefix --credentials-file ./read.yaml\n" +
			"  morzer fleet ls --stale-after 2h\n" +
			"  morzer fleet ls --json | jq -r '.data.rows[] | select(.absent) | .installation_id'",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			creds, err := readCredentialsFile(credentialsFile)
			if err != nil {
				return err
			}
			roster, err := readRosterFile(rosterFile)
			if err != nil {
				return err
			}
			var url string
			if len(args) == 1 {
				url = args[0]
			}

			report, err := ops.FleetList(cmd.Context(), app.Deps, ops.FleetListOptions{
				TargetOptions: ops.TargetOptions{
					Options:     app.operationOptions(),
					URL:         url,
					Credentials: creds,
				},
				StaleAfter: staleAfter,
				Roster:     roster,
			})
			if err != nil {
				return err
			}

			if err := app.render(fleetView(report)); err != nil {
				return err
			}

			// Non-zero for a row carrying a problem, so this is usable in
			// a cron job without parsing the output: a row nobody can
			// read, one the roster expects and nothing published, and one
			// signed by a key the roster does not name all count.
			// Staleness does not: it is a judgement against a threshold
			// the reader chose, and a machine deliberately published
			// weekly must not fail a check that defaults to a day.
			if report.Problems() > 0 {
				return domain.Preflight(nil, "%d row(s) carry a problem", report.Problems())
			}
			return nil
		},
	}

	f := cmd.Flags()
	f.StringVar(&credentialsFile, "credentials-file", "",
		"YAML file holding the target's credentials")
	f.StringVar(&rosterFile, "expect", "",
		"roster of the installations that should be there, and the key each signs with")
	f.DurationVar(&staleAfter, "stale-after", 0,
		"call a row stale once it is this old (default 24h; a negative value judges nothing)")
	return cmd
}

// readRosterFile reads the roster `--expect` names.
//
// A file rather than repeated flags, and the shape is not a convenience: a
// roster is the trust anchor for every verdict the reader prints, so it wants
// to live in version control beside whatever else describes the fleet, be
// reviewed when a machine joins, and be diffed when one leaves.
func readRosterFile(path string) (domain.FleetRoster, error) {
	if path == "" {
		return domain.FleetRoster{}, nil
	}

	data, err := os.ReadFile(path) //nolint:gosec // a path the operator named
	if err != nil {
		return domain.FleetRoster{}, domain.Usage(
			"cannot read the roster %s: %v", path, err)
	}
	roster, err := ops.ParseFleetRoster(string(data))
	if err != nil {
		return domain.FleetRoster{}, domain.Usage(
			"%s is not a roster: %s", path, domain.AsError(err).Message).
			WithHint("%s", domain.AsError(err).Hint)
	}
	return roster, nil
}

// fleetView maps the operation's report onto the view that draws it.
//
// A mapping rather than the same struct twice, the same seam as
// verificationView and for the same reason: the report is what the operation
// computed, the view is the published `--json` shape, and they agree today.
// Keeping the seam means the table can change without moving a monitoring
// contract.
func fleetView(r ops.FleetReport) views.Fleet {
	out := views.Fleet{
		Targets:     r.Targets,
		Expected:    r.Expected,
		StaleAfter:  r.StaleAfter,
		Limitations: r.Limitations,
		Rows:        make([]views.FleetRow, 0, len(r.Rows)),
	}
	for _, row := range r.Rows {
		out.Rows = append(out.Rows, views.FleetRow{
			Target:         row.Target,
			Key:            row.Key,
			Product:        row.Product,
			InstallationID: row.InstallationID,
			Row:            row.Row,
			Signature:      string(row.Signature),
			Expected:       row.Expected,
			Absent:         row.Absent,
			Age:            row.Age,
			Stale:          row.Stale,
			Problem:        row.Problem,
		})
	}
	return out
}
