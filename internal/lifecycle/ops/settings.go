package ops

import (
	"context"
	"errors"
	"io/fs"
	"maps"
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/infra/agecrypt"
	"github.com/morzecrew/morzer/internal/lifecycle/engine"
	"github.com/morzecrew/morzer/internal/ports"
	"github.com/morzecrew/morzer/internal/release"
)

// A setting is an installation-level knob, as opposed to a release parameter.
//
// The two are different things wearing similar words. A parameter is declared by
// the vendor, resolved into the deployment and re-created when it changes; a
// setting is the operator's arrangement with the manager -- whether it may
// contact a registry, which reference it watches -- and changes nothing that is
// running.
//
// They share `morzer config` because that is where an operator looks for "change
// a value", and they cannot collide: a parameter name is
// [a-z][a-z0-9_]* (domain.ValidateParameters), so a dotted name is never one.
//
// Until this existed, `update.check` was settable by nothing at all. It shipped
// with an error message telling operators to enable it with `morzer config`,
// which read and wrote release parameters exclusively -- so the feature had an
// off switch, a documented name, and no way to turn it on.
type setting struct {
	Description string

	// Read renders the current value, for `config get`.
	Read func(domain.Installation) string

	// Stored renders what the installation records, where Read renders a
	// default in its place. Change detection compares it, so a value equal
	// to the default is still recorded when set and removed when unset.
	Stored func(domain.Installation) string

	// Apply validates the raw text and records it. It takes a pointer
	// because a setting may touch more than the field it names.
	Apply func(context.Context, *Deps, *domain.Installation, string) error

	// Clear returns the setting to its absent state, which is the
	// conservative one for every setting but `release.require_signature`:
	// nothing here defaults to "on", and that one is safe only when on.
	Clear func(*domain.Installation)
}

var settings = map[string]setting{
	"update.check": {
		Description: "contact the vendor's registry unprompted, for `doctor` and `status`",
		Read:        func(i domain.Installation) string { return strconv.FormatBool(i.Update.Check) },
		Apply: func(_ context.Context, _ *Deps, i *domain.Installation, raw string) error {
			v, err := strconv.ParseBool(strings.TrimSpace(raw))
			if err != nil {
				return domain.Usage("%q is not a boolean (true or false)", raw)
			}
			i.Update.Check = v
			return nil
		},
		Clear: func(i *domain.Installation) { i.Update.Check = false },
	},
	"manager.check": {
		Description: "ask GitHub unprompted whether a newer manager is published, for `doctor`",
		Read:        func(i domain.Installation) string { return strconv.FormatBool(i.Update.ManagerCheck) },
		Apply: func(_ context.Context, _ *Deps, i *domain.Installation, raw string) error {
			v, err := strconv.ParseBool(strings.TrimSpace(raw))
			if err != nil {
				return domain.Usage("%q is not a boolean (true or false)", raw)
			}
			i.Update.ManagerCheck = v
			return nil
		},
		Clear: func(i *domain.Installation) { i.Update.ManagerCheck = false },
	},
	"update.auto_apply": {
		Description: "install what the channel offers, when the release declares it is recoverable",
		Read:        func(i domain.Installation) string { return strconv.FormatBool(i.Update.AutoApply) },
		Apply: func(_ context.Context, _ *Deps, i *domain.Installation, raw string) error {
			v, err := strconv.ParseBool(strings.TrimSpace(raw))
			if err != nil {
				return domain.Usage("%q is not a boolean (true or false)", raw)
			}
			// The refusal for an installation with no signature policy
			// lives in Installation.Validate rather than here, so it
			// fires wherever the state is written -- including a path
			// that sets the two in the other order.
			i.Update.AutoApply = v
			return nil
		},
		Clear: func(i *domain.Installation) { i.Update.AutoApply = false },
	},
	"backup.schedule": {
		Description: "when scheduled backups run, as a systemd OnCalendar expression",
		Read:        func(i domain.Installation) string { return i.Policy.BackupSchedule },
		Apply: func(ctx context.Context, d *Deps, i *domain.Installation, raw string) error {
			schedule := strings.TrimSpace(raw)
			if err := domain.ValidateBackupSchedule(schedule); err != nil {
				return err
			}
			if err := d.checkSchedule(ctx, schedule); err != nil {
				return err
			}
			i.Policy.BackupSchedule = schedule
			return nil
		},
		// Clearing returns the supervisor's default rather than an empty
		// OnCalendar, which systemd refuses to load -- so unsetting this
		// is "back to nightly", not "no backups".
		Clear: func(i *domain.Installation) { i.Policy.BackupSchedule = "" },
	},
	"update.schedule": {
		Description: "when the update timer follows the channel, as a systemd OnCalendar expression",
		Read:        func(i domain.Installation) string { return i.Update.Schedule },
		Apply: func(ctx context.Context, d *Deps, i *domain.Installation, raw string) error {
			schedule := strings.TrimSpace(raw)
			if err := domain.ValidateUpdateSchedule(schedule); err != nil {
				return err
			}
			if err := d.checkSchedule(ctx, schedule); err != nil {
				return err
			}
			i.Update.Schedule = schedule
			return nil
		},
		// Back to the daily default, as `backup.schedule` goes back to
		// nightly. Whether the timer exists at all is `update.channel`
		// and `update.check`'s question, not this one's.
		Clear: func(i *domain.Installation) { i.Update.Schedule = "" },
	},
	"update.randomized_delay": {
		Description: "how long each update tick may wait at random, as a duration in whole seconds such as 30m; 0 for none",
		// The value in force rather than the stored one, because unset
		// means half an hour in production and none on a sandbox.
		Read: func(i domain.Installation) string {
			return domain.Duration(i.UpdateRandomizedDelay()).String()
		},
		Apply: func(_ context.Context, _ *Deps, i *domain.Installation, raw string) error {
			if strings.TrimSpace(raw) == "" {
				i.Update.RandomizedDelay = nil
				return nil
			}
			var delay domain.Duration
			if err := delay.UnmarshalText([]byte(raw)); err != nil {
				return err
			}
			if err := domain.ValidateUpdateRandomizedDelay(delay); err != nil {
				return err
			}
			i.Update.RandomizedDelay = &delay
			return nil
		},
		Stored: func(i domain.Installation) string {
			if i.Update.RandomizedDelay == nil {
				return ""
			}
			return i.Update.RandomizedDelay.String()
		},
		Clear: func(i *domain.Installation) { i.Update.RandomizedDelay = nil },
	},
	"retention.pre_update_backups": {
		Description: "how many of the newest pre-update backups retention keeps; at least 1",
		Read: func(i domain.Installation) string {
			return strconv.Itoa(i.RetentionPreUpdateBackups())
		},
		Apply: func(_ context.Context, _ *Deps, i *domain.Installation, raw string) error {
			n, err := strconv.Atoi(strings.TrimSpace(raw))
			if err != nil || n < 1 {
				return domain.Usage("%q is not a count of at least 1", raw).
					WithHint("the newest pre-update backup is never pruned; "+
						"`config unset` returns to the default, %d",
						domain.DefaultRetentionPreUpdateBackups)
			}
			i.Policy.RetainPreUpdateBackups = n
			return nil
		},
		Stored: func(i domain.Installation) string { return strconv.Itoa(i.Policy.RetainPreUpdateBackups) },
		Clear:  func(i *domain.Installation) { i.Policy.RetainPreUpdateBackups = 0 },
	},
	"doctor.schedule": {
		Description: "when the scheduled doctor runs, as a systemd OnCalendar expression",
		Read:        func(i domain.Installation) string { return i.Policy.DoctorSchedule },
		Apply: func(ctx context.Context, d *Deps, i *domain.Installation, raw string) error {
			schedule := strings.TrimSpace(raw)
			if err := domain.ValidateDoctorSchedule(schedule); err != nil {
				return err
			}
			if err := d.checkSchedule(ctx, schedule); err != nil {
				return err
			}
			i.Policy.DoctorSchedule = schedule
			return nil
		},
		// Back to the morning default. Whether the timer exists is
		// `doctor.scheduled`'s question and the notify targets'.
		Clear: func(i *domain.Installation) { i.Policy.DoctorSchedule = "" },
	},
	"metrics.textfile": {
		Description: "the node exporter's textfile directory, which a timer writes this installation's metrics into",
		Read:        func(i domain.Installation) string { return i.Policy.MetricsTextfile },
		Apply: func(_ context.Context, _ *Deps, i *domain.Installation, raw string) error {
			dir := strings.TrimSpace(raw)
			if dir == "" {
				return domain.Usage("metrics.textfile needs a directory").
					WithHint("to stop exporting, run `morzer config unset metrics.textfile`")
			}
			if err := domain.ValidateMetricsTextfile(dir); err != nil {
				return err
			}
			// Not created: the node exporter decides where it reads, and
			// a directory nothing reads is a quiet failure (decision 10).
			info, err := os.Stat(dir)
			switch {
			case errors.Is(err, fs.ErrNotExist):
				return domain.ValidationError(err, "%s does not exist", dir).
					WithHint("name the directory the node exporter's --collector.textfile.directory reads")
			case err != nil:
				return domain.ValidationError(err, "cannot read %s", dir)
			case !info.IsDir():
				return domain.ValidationError(nil, "%s is not a directory", dir).
					WithHint("name the directory the node exporter's --collector.textfile.directory reads")
			}
			i.Policy.MetricsTextfile = dir
			return nil
		},
		Clear: func(i *domain.Installation) { i.Policy.MetricsTextfile = "" },
	},
	"doctor.scheduled": {
		Description: "whether doctor runs on a timer, while a notify target or a metrics export is configured",
		Read: func(i domain.Installation) string {
			return strconv.FormatBool(!i.Policy.SkipScheduledDoctor)
		},
		// Positive here and negative in the record, as `backup.scheduled`
		// is and for its reason: a missing field must mean the timer runs.
		Apply: func(_ context.Context, _ *Deps, i *domain.Installation, raw string) error {
			v, err := strconv.ParseBool(strings.TrimSpace(raw))
			if err != nil {
				return domain.Usage("%q is not a boolean (true or false)", raw)
			}
			i.Policy.SkipScheduledDoctor = !v
			return nil
		},
		Clear: func(i *domain.Installation) { i.Policy.SkipScheduledDoctor = false },
	},
	"backup.scheduled": {
		Description: "whether this manager runs backups on a timer at all",
		Read: func(i domain.Installation) string {
			return strconv.FormatBool(!i.Policy.SkipScheduledBackups)
		},
		// Positive here, negative in the record, and the mismatch is
		// deliberate at both ends. A field missing from a hand-edited
		// state file must mean "backups happen", which makes the stored
		// name the unsafe direction; a command an operator types reads
		// better as the thing they are turning off. The two meet here
		// and nowhere else.
		Apply: func(_ context.Context, _ *Deps, i *domain.Installation, raw string) error {
			v, err := strconv.ParseBool(strings.TrimSpace(raw))
			if err != nil {
				return domain.Usage("%q is not a boolean (true or false)", raw)
			}
			i.Policy.SkipScheduledBackups = !v
			return nil
		},
		// Clearing means scheduled backups, which is the safe direction
		// and the same one an absent field takes.
		Clear: func(i *domain.Installation) { i.Policy.SkipScheduledBackups = false },
	},
	"backup.before_update": {
		Description: "whether an update takes a backup before it changes anything; only a dev-mode installation may turn it off",
		Read: func(i domain.Installation) string {
			return strconv.FormatBool(!i.Policy.SkipBackupBeforeUpdate)
		},
		// Positive here and negative in the record, as `backup.scheduled`
		// is. Off is a sandbox's choice only (RFC 0016 decision 32): on a
		// production machine this backup is what a failed migration is
		// recovered from, and one update can still skip it with
		// `--skip-backup --force`, which the journal records.
		Apply: func(_ context.Context, _ *Deps, i *domain.Installation, raw string) error {
			v, err := strconv.ParseBool(strings.TrimSpace(raw))
			if err != nil {
				return domain.Usage("%q is not a boolean (true or false)", raw)
			}
			if !v && !i.IsDev() {
				return domain.Usage("only a dev-mode installation may turn off the pre-update backup").
					WithHint("to skip it for one update, run `morzer update --skip-backup --force`")
			}
			i.Policy.SkipBackupBeforeUpdate = !v
			return nil
		},
		Clear: func(i *domain.Installation) { i.Policy.SkipBackupBeforeUpdate = false },
	},
	"release.require_signature": {
		Description: "refuse any release not signed by a key `morzer release trust` lists",
		Read:        func(i domain.Installation) string { return strconv.FormatBool(i.Policy.RequireSignature) },
		// The rules that need a trusted key and keep `update.auto_apply`
		// off without it cross into other values, so checkSignaturePolicy
		// asks them of the whole command's result rather than here.
		Apply: func(_ context.Context, _ *Deps, i *domain.Installation, raw string) error {
			v, err := strconv.ParseBool(strings.TrimSpace(raw))
			if err != nil {
				return domain.Usage("%q is not a boolean (true or false)", raw)
			}
			i.Policy.RequireSignature = v
			return nil
		},
		// Off, which is the less safe state, so an unset is the same
		// downgrade `=false` is: refused under `update.auto_apply` and
		// journalled.
		Clear: func(i *domain.Installation) { i.Policy.RequireSignature = false },
	},
	"installation.domains": {
		Description: "the public names the product is served under, comma-separated, the first canonical; " +
			"a change renders the configuration and re-creates every running service",
		Read: func(i domain.Installation) string { return strings.Join(i.Domains, ",") },
		Apply: func(_ context.Context, _ *Deps, i *domain.Installation, raw string) error {
			domains := strings.Split(raw, ",")
			for n := range domains {
				domains[n] = strings.TrimSpace(domains[n])
			}
			if err := domain.ValidateDomains(domains); err != nil {
				return err
			}
			i.Domains = domains
			return nil
		},
		// Rendered as an installation created without a domain is.
		Clear: func(i *domain.Installation) { i.Domains = nil },
	},
	// What `apply --profile` overrides for one run, kept (#286).
	// A comma list, as installation.domains is (RFC 0053 decision 8).
	"installation.profile": {
		Description: "the deployment profiles the release is rendered and started with, comma-separated, " +
			"empty for the base files; a change applies the release under them when the product is running",
		Read: func(i domain.Installation) string { return i.ProfileSetting() },
		Apply: func(ctx context.Context, d *Deps, i *domain.Installation, raw string) error {
			var profiles []string
			if raw = strings.TrimSpace(raw); raw != "" {
				profiles = strings.Split(raw, ",")
				for n := range profiles {
					profiles[n] = strings.TrimSpace(profiles[n])
				}
				var err error
				if profiles, err = d.checkProfileDeclared(ctx, *i, profiles); err != nil {
					return err
				}
			}
			i.Profiles = profiles
			return nil
		},
		Clear: func(i *domain.Installation) { i.Profiles = nil },
	},
	"retention.releases": {
		Description: "how many releases the store keeps beside the current, previous and staged ones; " +
			"at least 1, and unset follows the release",
		Read: func(i domain.Installation) string { return countOrEmpty(i.Policy.RetainReleases) },
		Apply: func(_ context.Context, _ *Deps, i *domain.Installation, raw string) error {
			n, err := retentionCount(raw)
			if err != nil {
				return err
			}
			i.Policy.RetainReleases = n
			return nil
		},
		Clear: func(i *domain.Installation) { i.Policy.RetainReleases = 0 },
	},
	"retention.backups": {
		Description: "how many of the newest backups retention keeps; at least 1, and unset follows the release",
		Read:        func(i domain.Installation) string { return countOrEmpty(i.Policy.RetainBackups) },
		Apply: func(_ context.Context, _ *Deps, i *domain.Installation, raw string) error {
			n, err := retentionCount(raw)
			if err != nil {
				return err
			}
			i.Policy.RetainBackups = n
			return nil
		},
		Clear: func(i *domain.Installation) { i.Policy.RetainBackups = 0 },
	},
	// The installation's only, with no manifest field: how long an audit
	// record is kept is the operator's decision (RFC 0049 decision 6).
	"retention.attestations": {
		Description: "how many of the newest attestations this machine keeps; at least 1, and unset keeps " +
			"them all. A statement not yet on every backup target is never removed",
		Read: func(i domain.Installation) string { return countOrEmpty(i.Policy.RetainAttestations) },
		Apply: func(_ context.Context, _ *Deps, i *domain.Installation, raw string) error {
			n, err := retentionCount(raw)
			if err != nil {
				// No release declares a count to return to.
				return domain.AsError(err).WithHint("`config unset` keeps every statement")
			}
			i.Policy.RetainAttestations = n
			return nil
		},
		Clear: func(i *domain.Installation) { i.Policy.RetainAttestations = 0 },
	},
	"update.channel": {
		Description: "a mutable reference to follow, e.g. oci://registry.example/demo/bundle:stable",
		Read:        func(i domain.Installation) string { return i.Update.Channel },
		Apply: func(ctx context.Context, d *Deps, i *domain.Installation, raw string) error {
			raw = strings.TrimSpace(raw)
			ref, err := ports.ParseRef(raw)
			if err != nil {
				return err
			}
			if err := d.checkChannelIsFollowable(ctx, ref); err != nil {
				return err
			}
			i.Update.Channel = raw
			return nil
		},
		Clear: func(i *domain.Installation) { i.Update.Channel = "" },
	},

	// Desired state (RFC 0057). Local and root-only: none is manageable by
	// a document (domain.ManageableSettings), so a compromised authority
	// cannot move the machine to another authority or target. Whether
	// sync.enabled has an authority and a target to follow is asked of the
	// whole command's result, by checkSyncPolicy.
	"sync.target": {
		Description: "where this installation's desired-state documents are, as a backup target URL",
		Read:        func(i domain.Installation) string { return i.Sync.Target },
		Apply: func(_ context.Context, d *Deps, i *domain.Installation, raw string) error {
			ref, err := ports.TargetURL(strings.TrimSpace(raw))
			if err != nil {
				return err
			}
			// The registry's answer, as `backup target add` gets it: which
			// transports a build has is not this layer's to list.
			if d.Targets != nil && !slices.Contains(d.Targets.Schemes(), ref.Scheme) {
				return domain.Usage("%s is not a transport this build has", ref.Scheme).
					WithHint("this build supports: %s", strings.Join(d.Targets.Schemes(), ", "))
			}
			i.Sync.Target = ref.String()
			return nil
		},
		Clear: func(i *domain.Installation) { i.Sync.Target = "" },
	},
	"sync.credentials": {
		Description: "the secret holding sync.target's credential document, as `backup target add --credentials` names one",
		Read:        func(i domain.Installation) string { return i.Sync.Credentials },
		Apply: func(_ context.Context, _ *Deps, i *domain.Installation, raw string) error {
			raw = strings.TrimSpace(raw)
			if !domain.ValidSecretName(raw) {
				return domain.Usage("%q is not a secret name", raw)
			}
			i.Sync.Credentials = raw
			return nil
		},
		Clear: func(i *domain.Installation) { i.Sync.Credentials = "" },
	},
	"sync.authority": {
		Description: "the minisign public key a desired-state document must be signed with",
		Read:        func(i domain.Installation) string { return i.Sync.Authority },
		// A new authority starts a watermark of its own: a compromised key
		// may have published a very high serial before it was replaced,
		// and every ordinary serial from its successor would be refused
		// against it (decision 27).
		Apply: func(_ context.Context, d *Deps, i *domain.Installation, raw string) error {
			raw = strings.TrimSpace(raw)
			if err := d.checkSigningKeys([]string{raw}); err != nil {
				return err
			}
			if raw != i.Sync.Authority {
				i.Sync.Applied, i.Sync.Refused = nil, nil
			}
			i.Sync.Authority = raw
			return nil
		},
		Clear: func(i *domain.Installation) {
			if i.Sync.Authority != "" {
				i.Sync.Applied, i.Sync.Refused = nil, nil
			}
			i.Sync.Authority = ""
		},
	},
	"sync.recipient": {
		Description: "the authority's age recipient, which this machine's published facts are encrypted to",
		Read:        func(i domain.Installation) string { return i.Sync.Recipient },
		Apply: func(_ context.Context, _ *Deps, i *domain.Installation, raw string) error {
			raw = strings.TrimSpace(raw)
			if err := agecrypt.ValidateRecipient(raw); err != nil {
				return domain.Usage("%q is not an age recipient", raw)
			}
			if raw != i.Sync.Recipient {
				// Facts made for another recipient are not offered to
				// this one; the run after this change makes new ones.
				i.Sync.Facts = nil
			}
			i.Sync.Recipient = raw
			return nil
		},
		Clear: func(i *domain.Installation) { i.Sync.Recipient, i.Sync.Facts = "", nil },
	},
	"sync.interval": {
		Description: "how often the sync timer fetches, a duration of at least 1m; 15m when unset",
		Read:        func(i domain.Installation) string { return i.Sync.SyncInterval().String() },
		Stored:      func(i domain.Installation) string { return i.Sync.Interval },
		Apply: func(_ context.Context, _ *Deps, i *domain.Installation, raw string) error {
			interval, err := time.ParseDuration(strings.TrimSpace(raw))
			if err != nil || interval < domain.MinSyncInterval {
				return domain.Usage("%q is not a duration of at least %s", raw, domain.MinSyncInterval)
			}
			// Whole seconds, which is what a timer ticks in.
			i.Sync.Interval = interval.Round(time.Second).String()
			return nil
		},
		Clear: func(i *domain.Installation) { i.Sync.Interval = "" },
	},
	"sync.enabled": {
		Description: "fetch and apply desired-state documents on a timer; needs sync.authority and sync.target",
		Read:        func(i domain.Installation) string { return strconv.FormatBool(i.Sync.Enabled) },
		Apply: func(_ context.Context, _ *Deps, i *domain.Installation, raw string) error {
			v, err := strconv.ParseBool(strings.TrimSpace(raw))
			if err != nil {
				return domain.Usage("%q is not a boolean (true or false)", raw)
			}
			i.Sync.Enabled = v
			return nil
		},
		Clear: func(i *domain.Installation) { i.Sync.Enabled = false },
	},
}

// checkSyncPolicy refuses following documents with no key to check them
// against or nowhere to fetch them from, as `update.auto_apply` is refused
// without a signature requirement (RFC 0057 §5.5). Asked of the whole
// command's result, as checkSignaturePolicy is: settings apply in name order,
// and `sync.enabled` sorts before `sync.target`.
func checkSyncPolicy(after domain.Installation) error {
	if !after.Sync.Enabled {
		return nil
	}
	switch {
	case after.Sync.Authority == "":
		return domain.Usage("sync.enabled needs sync.authority, the key a document must be signed with").
			WithHint("set it with `morzer config set sync.authority=<minisign public key>`")
	case after.Sync.Target == "":
		return domain.Usage("sync.enabled needs sync.target, where the documents are").
			WithHint("set it with `morzer config set sync.target=<url> sync.credentials=<secret>`")
	}
	return nil
}

// retentionCount reads a retention override. The release's own count is what
// an absent one takes, and the manifest is not loaded to show it, so a value
// is read back as recorded and empty when unset, as `backup.schedule` is.
func retentionCount(raw string) (int, error) {
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || n < 1 {
		return 0, domain.Usage("%q is not a count of at least 1", raw).
			WithHint("`config unset` returns to the count the release declares")
	}
	return n, nil
}

func countOrEmpty(n int) string {
	if n == 0 {
		return ""
	}
	return strconv.Itoa(n)
}

// checkSignaturePolicy refuses what Installation.Validate would refuse at the
// write, before the lock and with the command that makes it possible
// (RFC 0042 decision 3).
//
// Asked of the installation the whole command produces, not inside one
// setting's Apply: settings apply in name order, so `update.auto_apply=false
// release.require_signature=false` would meet the signature first and be
// refused for a state the same command ends.
func checkSignaturePolicy(before, after domain.Installation) error {
	if after.Policy.RequireSignature && len(after.Policy.SigningKeys) == 0 {
		return domain.Usage("release.require_signature needs a trusted signing key, and this installation trusts none").
			WithHint("trust the vendor's key first with `morzer release trust add <key>`")
	}
	if !after.Update.AutoApply || after.Policy.RequireSignature {
		return nil
	}
	if before.Policy.RequireSignature {
		return domain.Usage("release.require_signature cannot be turned off while update.auto_apply is on").
			WithHint("turn update.auto_apply off first, or in the same command: " +
				"`morzer config set update.auto_apply=false release.require_signature=false`")
	}
	return domain.Usage("update.auto_apply needs release.require_signature, " +
		"because it hands the vendor unattended root on this machine").
		WithHint("require a signature with `morzer config set release.require_signature=true`, " +
			"after `morzer release trust add <key>` if no key is trusted")
}

// checkSchedule asks the supervisor whether it can run a schedule, before the
// schedule is stored.
//
// The domain bounds the shape -- one line, a sane length -- and stops there on
// purpose; whether `every 5m` is a calendar is systemd's to say. Without this a
// schedule systemd cannot parse was stored, rendered, and left a timer that
// does not load: no scheduled backups, or no unattended updates, from a
// command that exited 0. Refused here, at the door, for the reason
// checkChannelIsFollowable refuses an unwatchable channel.
func (d *Deps) checkSchedule(ctx context.Context, schedule string) error {
	if d.Supervisor == nil {
		return nil
	}
	return d.Supervisor.ValidateSchedule(ctx, schedule)
}

// checkProfileDeclared refuses a selection the installed release cannot deploy
// for this installation's runtime, with the error the render would give, and
// returns it in that release's layering order. `init --profile` refuses the
// same selection against the release it stages.
func (d *Deps) checkProfileDeclared(ctx context.Context, inst domain.Installation, profiles []string) ([]string, error) {
	current, err := d.State.CurrentRelease(ctx)
	if err != nil {
		return nil, err
	}
	if current.IsZero() {
		return nil, domain.InstallationError(nil,
			"no release is installed, so no profile is declared to choose from").
			WithHint("`morzer config unset installation.profile` returns to the base files")
	}
	rel, err := release.Load(current.Root)
	if err != nil {
		return nil, err
	}
	return rel.Selection(inst.RuntimeName(), profiles)
}

// checkChannelIsFollowable refuses a reference this manager could never watch.
//
// At configuration time rather than at the first tick, which is the same shape
// as `--skip-backup` requiring `--force`: a machine that accepts a setting and
// then silently does nothing with it is worse than one that refuses the setting.
//
// Only a *capability* answer refuses. A registry that is unreachable right now
// says nothing about whether the reference is followable, and refusing on it
// would make configuring a channel depend on the network being up at that
// moment -- which is exactly the situation an operator is often configuring
// their way out of.
func (d *Deps) checkChannelIsFollowable(ctx context.Context, ref ports.Ref) error {
	peeker, ok := d.Source.(ports.ChannelPeeker)
	if !ok {
		return nil
	}
	if _, err := peeker.Peek(ctx, ref); err != nil && errors.Is(err, domain.ErrUnsupported) {
		return err
	}
	return nil
}

// SettingNames lists what may be set, sorted.
func SettingNames() []string {
	out := slices.Sorted(maps.Keys(settings))
	return out
}

// IsSetting reports whether a name addresses an installation setting rather
// than a release parameter.
func IsSetting(name string) bool {
	_, ok := settings[name]
	return ok
}

// LooksLikeSetting reports whether a name was *meant* as one.
//
// A dotted name can never be a parameter, so `update.chanel` is a typo in a
// setting rather than an unknown parameter -- and telling an operator "the
// release declares no parameter update.chanel" would send them to the manifest
// to look for something that was never going to be there.
func LooksLikeSetting(name string) bool { return strings.Contains(name, ".") }

// IsSettingName is the routing question `config` asks: does this name belong to
// the settings side at all, known or mistyped.
//
// One predicate rather than the pair spelled out at each of `get`, `set` and
// `unset`. They must route a name identically -- an operator who sets
// `update.chanel` and then unsets it has to reach the same refusal both times --
// and three copies of a two-term condition is how that stops being true.
func IsSettingName(name string) bool { return IsSetting(name) || LooksLikeSetting(name) }

// SettingsReport is every installation setting and its current value.
type SettingsReport struct {
	Settings []SettingEntry `json:"settings"`
}

// SettingEntry is one setting as an operator sees it.
type SettingEntry struct {
	Name        string `json:"name"`
	Value       string `json:"value"`
	Description string `json:"description"`
}

// ListSettings reads them all. Read-only: no lock, no journal.
func ListSettings(ctx context.Context, d *Deps) (SettingsReport, error) {
	inst, err := d.loadInstallation(ctx)
	if err != nil {
		return SettingsReport{}, err
	}

	out := SettingsReport{}
	for _, name := range SettingNames() {
		out.Settings = append(out.Settings, SettingEntry{
			Name:        name,
			Value:       settings[name].Read(inst),
			Description: settings[name].Description,
		})
	}
	return out, nil
}

// GetSetting reads one.
func GetSetting(ctx context.Context, d *Deps, name string) (SettingEntry, error) {
	s, ok := settings[name]
	if !ok {
		return SettingEntry{}, unknownSetting(name)
	}
	inst, err := d.loadInstallation(ctx)
	if err != nil {
		return SettingEntry{}, err
	}
	return SettingEntry{Name: name, Value: s.Read(inst), Description: s.Description}, nil
}

// SetSettingsOptions is a change to the installation's own knobs.
type SetSettingsOptions struct {
	Options

	Set   map[string]string
	Unset []string
}

// refuseManagedSettings is decision 6 of RFC 0057 for settings, which reads
// the release only when a changed setting could be managed at all.
func (d *Deps) refuseManagedSettings(ctx context.Context, inst domain.Installation, changed []string, force bool) error {
	if force || !inst.Sync.Enabled || !slices.ContainsFunc(changed, domain.IsManageableSetting) {
		return nil
	}
	current, err := d.State.CurrentRelease(ctx)
	if err != nil || current.IsZero() {
		return err
	}
	rel, err := d.resolveCurrentRelease(ctx, current)
	if err != nil {
		return err
	}
	change := ConfigChange{Settings: map[string]string{}}
	for _, name := range changed {
		change.Settings[name] = ""
	}
	return refuseManagedLocally(inst, rel, true, change, force)
}

// SetSettings records installation settings.
//
// Not an engine operation, and the difference from ConfigSet is the reason: a
// parameter change alters what the containers are running, so it plans, renders,
// re-creates and unwinds. A setting changes what the *manager* does next time.
// There is nothing to converge and nothing to compensate, so wrapping it in a
// six-step pipeline would be ceremony that makes the journal harder to read
// rather than the change safer.
//
// It still takes the deployment lock: two writers of installation state at once
// is how one of them loses their change silently.
//
// Two changes are the exception, and run as a journalled `config` operation
// (RFC 0042 decisions 5 and 9). A domain change reaches the deployment, so it
// renders and re-creates as a parameter change does. The signature requirement
// turned off is a downgrade, and an incident review has to be able to find it.
func SetSettings(ctx context.Context, d *Deps, opts SetSettingsOptions) (Result, error) {
	if len(opts.Set) == 0 && len(opts.Unset) == 0 {
		return Result{}, domain.Usage("nothing to change")
	}

	inst, err := d.loadInstallation(ctx)
	if err != nil {
		return Result{}, err
	}

	next := inst
	var changed []string

	for _, name := range opts.Unset {
		s, ok := settings[name]
		if !ok {
			return Result{}, unknownSetting(name)
		}
		if s.stored(next) == s.stored(zeroFor(next, s)) {
			continue
		}
		s.Clear(&next)
		changed = append(changed, name)
	}

	for _, name := range sortedSettingNames(opts.Set) {
		s, ok := settings[name]
		if !ok {
			return Result{}, unknownSetting(name)
		}
		before := s.stored(next)
		if err := s.Apply(ctx, d, &next, opts.Set[name]); err != nil {
			return Result{}, err
		}
		if s.stored(next) == before {
			continue
		}
		changed = append(changed, name)
	}

	// This pass exists to refuse an unknown name, to reject a value before
	// anything is locked, and to answer a plan. It is *not* the authority on
	// what changed: a plan is the only caller that can be, because it takes
	// no lock and so has nothing fresher to compare against. The real run
	// recomputes the list under the lock.
	sort.Strings(changed)
	if err := checkSignaturePolicy(inst, next); err != nil {
		return Result{}, err
	}
	if err := checkSyncPolicy(next); err != nil {
		return Result{}, err
	}
	if err := d.refuseManagedSettings(ctx, inst, changed, opts.Force); err != nil {
		return Result{}, err
	}

	if opts.DryRun && !journalled(inst, next) {
		if len(changed) == 0 {
			return Result{Summary: "no change: every value already matches"}, nil
		}
		return Result{Summary: "would set " + strings.Join(changed, ", ")}, nil
	}

	// A run that changes no value still reconciles the units, and that is
	// the only reason it does any work at all. Unit installation is the half
	// of a setting change that can fail after the state has been written --
	// leaving a machine whose state says it follows a channel and whose
	// supervisor has no timer -- and without this, repeating the command
	// that failed would match every value, report "no change", and never
	// reach the step that did not finish. Reconciliation is idempotent, so
	// the cost of the ordinary no-op case is one unit comparison.

	opID := d.newOpID()
	var (
		prior  domain.Installation
		plan   settingsPlan
		result engine.Result
		ran    bool
	)
	err = d.withLock(ctx, opID, domain.OpTypeConfig, opts.Options, func(ctx context.Context) error {
		// Re-read under the lock and re-apply, so a concurrent writer's
		// change to a *different* setting is not reverted by this one's
		// copy of the state -- the same reason every other operation
		// re-checks after taking the lock.
		fresh, err := d.loadInstallation(ctx)
		if err != nil {
			return err
		}
		prior = fresh
		// Recomputed against the reloaded state, and it is this answer
		// that decides both the write and the summary. The pre-lock list
		// was computed from a copy another operation may have changed
		// since: `update.check=false` that read false, was set to true
		// by somebody else, and is being set back to false here would
		// look like "no change", skip the write, and leave the operator
		// told their value was already in place while the file says
		// otherwise.
		changed = changed[:0]
		for _, name := range opts.Unset {
			s := settings[name]
			before := s.stored(fresh)
			s.Clear(&fresh)
			if s.stored(fresh) != before {
				changed = append(changed, name)
			}
		}
		for _, name := range sortedSettingNames(opts.Set) {
			s := settings[name]
			before := s.stored(fresh)
			if err := s.Apply(ctx, d, &fresh, opts.Set[name]); err != nil {
				return err
			}
			if s.stored(fresh) != before {
				changed = append(changed, name)
			}
		}
		sort.Strings(changed)
		if err := checkSignaturePolicy(prior, fresh); err != nil {
			return err
		}
		if err := checkSyncPolicy(fresh); err != nil {
			return err
		}
		// Again under the lock: sync turned on while this waited is
		// sync on for this change.
		if err := d.refuseManagedSettings(ctx, prior, changed, opts.Force); err != nil {
			return err
		}

		switch {
		case len(changed) > 0 && journalled(prior, fresh):
			if plan, err = d.planSettings(ctx, prior, fresh); err != nil {
				return err
			}
			ran = true
			result, err = d.Engine.Run(ctx, engine.Operation{
				ID:          opID,
				Type:        domain.OpTypeConfig,
				Description: "set " + strings.Join(changed, ", "),
				Steps:       plan.steps,
				Flags:       settingsFlags(prior, fresh),
			}, d.engineOptions(opts.Options, fresh.ID, nil))
			if err != nil {
				return err
			}
		case opts.DryRun:
		case len(changed) > 0:
			if err := d.saveInstallation(ctx, fresh); err != nil {
				return err
			}
		}
		if opts.DryRun {
			return nil
		}
		// After the state, not before: the units are derived from it,
		// and a crash between the two leaves a timer that the next
		// setting change reconciles rather than a state nothing polls.
		if err := d.refreshUnits(ctx, fresh); err != nil {
			return err
		}
		d.retireMetricsFiles(ctx, prior, fresh)
		return nil
	})
	if ran {
		d.notifyFinished(ctx, opID, domain.OpTypeConfig, result.Record, err)
		if plan.render && !opts.DryRun {
			attestConfig(ctx, d, result.Record, prior, plan.rel, result.State, nil)
		}
	}
	if err != nil {
		return Result{Record: result.Record}, err
	}

	if len(changed) == 0 {
		return Result{Summary: "no change: every value already matches"}, nil
	}
	if !opts.DryRun {
		d.refreshFacts(ctx, false)
	}
	return Result{Record: result.Record, Summary: settingsSummary(changed, plan, opts.DryRun)}, nil
}

// journalled reports whether a change runs as an operation rather than a
// write: see SetSettings.
func journalled(before, after domain.Installation) bool {
	return !slices.Equal(before.Domains, after.Domains) || !sameProfiles(before.Profiles, after.Profiles) ||
		signatureDropped(before, after) || before.Sync.Authority != after.Sync.Authority
}

func signatureDropped(before, after domain.Installation) bool {
	return before.Policy.RequireSignature && !after.Policy.RequireSignature
}

// settingsFlags is the journal's record of a downgrade (RFC 0042 decision 5).
// It records a change of the sync authority too, which hands the machine to
// whoever holds the new key (RFC 0057 decision 4).
func settingsFlags(before, after domain.Installation) map[string]string {
	flags := map[string]string{}
	if signatureDropped(before, after) {
		flags["release.require_signature"] = "false"
	}
	if before.Sync.Authority != after.Sync.Authority {
		flags["sync.authority"] = after.Sync.Authority
	}
	if len(flags) == 0 {
		return nil
	}
	return flags
}

// settingsPlan is what a journalled setting change runs, and what its summary
// may claim.
type settingsPlan struct {
	steps []engine.Step

	// domains is whether the domain list changed, profile whether the
	// profile did. render is whether a release is installed to render them
	// for, rel that release.
	domains bool
	profile bool
	render  bool
	rel     domain.Release

	stopped  bool
	services []string

	// converged is whether a profile change applies the release, under the
	// profile named.
	converged bool
	under     string
}

// planSettings is RFC 0042 §5.2's three steps: record the installation, render
// the configuration, re-create every running service. The last two only for a
// domain change, and the last is left out when nothing runs to re-create: under
// a stop, `start` runs `apply`, which starts every service with the new value.
//
// Forced, because no release declares which services read the domain, and a
// plain `up` re-creates only those whose Compose definition changed. One that
// reads the domain from a rendered file would keep the old value.
//
// A profile change on a running product is apply's steps under the new profile
// instead, since it adds and removes services, which re-creating the running
// ones cannot (#286). Every container is re-created, for the reason above. The
// restore point before them brings the old profile's services back if a step
// fails, after the configuration compensator has put its files back.
func (d *Deps) planSettings(ctx context.Context, before, after domain.Installation) (settingsPlan, error) {
	plan := settingsPlan{steps: []engine.Step{stepRecordSettings(d, before, after)}}
	plan.domains = !slices.Equal(before.Domains, after.Domains)
	plan.profile = !sameProfiles(before.Profiles, after.Profiles)
	if !plan.domains && !plan.profile {
		return plan, nil
	}
	current, err := d.State.CurrentRelease(ctx)
	if err != nil || current.IsZero() {
		return plan, err
	}
	if plan.rel, err = release.Load(current.Root); err != nil {
		return plan, err
	}
	plan.render = true
	if plan.stopped = after.Stopped != nil; !plan.stopped {
		// The deployment as it stands, under the profile it runs. A
		// runtime that cannot answer refuses a profile change, which
		// counted as nothing running would record a topology it never
		// deployed.
		var err error
		if plan.services, err = d.runningServices(ctx, before, plan.rel); err != nil && plan.profile {
			return plan, err
		}
	}
	if plan.profile && len(plan.services) > 0 {
		plan.converged, plan.under = true, profileName(after.Profiles)
		plan.steps = append(plan.steps, convergeUnderProfiles(d, before, after, current, plan.rel)...)
		return plan, nil
	}
	plan.steps = append(plan.steps, stepRenderConfiguration(d, after, plan.rel, Options{}))
	if !plan.profile && len(plan.services) > 0 {
		plan.steps = append(plan.steps, stepRecreateServices(d, before, after, plan.rel, plan.services))
	}
	return plan, nil
}

// convergeUnderProfiles is a profile change on a running product: a restore
// point that brings the old profiles' services back on failure, then apply's
// steps under the new profiles. staged runs between the two, so that its undo
// comes before the restore point's: `config apply` puts its secrets back there,
// before the restore point renders them for the old services (RFC 0056 §5.4).
func convergeUnderProfiles(
	d *Deps, before, after domain.Installation, current domain.ReleaseRecord, rel domain.Release,
	staged ...engine.Step,
) []engine.Step {
	keep := stepRestorePoint(d, func(context.Context) (domain.Installation, error) { return before, nil }, current, true)
	keep.Description = "keep " + profileName(before.Profiles) + " to bring back on failure"
	steps := append([]engine.Step{keep}, staged...)
	// Apply's checks abort, since in `apply` nothing before them needs
	// undoing. Here the profile is already recorded. Not record-state, which
	// fails with the new profile's services up under the new profile's
	// record.
	for _, step := range applySteps(d, after, rel, Options{ForceRecreate: true}) {
		if step.OnFailure == engine.Abort && step.ID != "record-state" {
			step.OnFailure = engine.Compensate
		}
		steps = append(steps, step)
	}
	return steps
}

// runningServices names each service with a running container, once however
// many replicas it has, an unhealthy one included: it reads the domain too. A runtime that cannot answer is the error, which a
// domain change takes as nothing running, as `config set` does: nothing is
// re-created.
func (d *Deps) runningServices(
	ctx context.Context, inst domain.Installation, rel domain.Release,
) ([]string, error) {
	cfg, err := d.runtimeConfig(rel, inst, nil)
	if err != nil {
		return nil, err
	}
	states, err := d.Runtime.Status(ctx, cfg)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, st := range states {
		if isUp(st) && !slices.Contains(out, st.Name) {
			out = append(out, st.Name)
		}
	}
	sort.Strings(out)
	return out, nil
}

// settingsSummary says what a change did, in the tense it happened in, and for
// a domain or profile change what it reached.
func settingsSummary(changed []string, plan settingsPlan, dryRun bool) string {
	names := strings.Join(changed, ", ")
	tense := func(done, planned string) string {
		if dryRun {
			return planned
		}
		return done
	}
	switch {
	case !plan.domains && !plan.profile:
		return tense("set ", "would set ") + names
	case !plan.render:
		return tense("set ", "would set ") + names + "; no release is installed, so there is nothing to render"
	case plan.stopped:
		return tense("set ", "would set ") + names + tense(" and rendered", " and render") +
			" the configuration; the product is stopped, so its services " +
			tense("see", "would see") + " it on `morzer start`"
	case plan.converged:
		return tense("set ", "would set ") + names + tense(" and applied ", " and apply ") +
			plan.rel.Name() + " " + plan.rel.Version().String() + " under " + plan.under
	case len(plan.services) > 0:
		return tense("set ", "would set ") + names + tense(", rendered", ", render") +
			" the configuration and " + tense("re-created ", "re-create ") + strings.Join(plan.services, ", ")
	default:
		return tense("set ", "would set ") + names + tense(" and rendered", " and render") +
			" the configuration; nothing is running, so it " + tense("takes", "would take") +
			" effect on the next `morzer apply`"
	}
}

// unitParams is which units this installation should have.
//
// One computation, two callers, and the second is why it is a function.
// `refreshUnits` writes the set and `doctor` checks it; two spellings of "does
// this machine want a timer" would agree the day they were written and
// disagree on the day an operator ran `doctor` to find out why the timer they
// configured is missing. Worse, they disagreed in the direction that trains
// somebody to ignore the check: doctor walked the *removal* superset and
// reported the conditional pairs as "not installed" on every ordinary machine.
// checkManagerBinary refuses units that would run, as root, a manager binary
// another user could replace: whoever could would run as root at the next
// timer tick. Only in the production layout, where the units are systemd's;
// under --root they are files in a tree nothing runs.
func (d *Deps) checkManagerBinary() error {
	if d.Paths.Root() != "" || d.ManagerPath == "" {
		return nil
	}
	who, err := d.replaceableByNonRoot(d.ManagerPath)
	if err != nil || who == "" {
		return err
	}
	return domain.Preflight(nil, "the units would run %s as root, and %s", d.ManagerPath, who).
		WithHint("install the manager where only root can replace it, then run this again with that copy: "+
			"curl -fsSL https://morzecrew.github.io/morzer/install.sh | sudo sh -s -- --version %s --dir /usr/local/bin",
			d.ManagerVersion)
}

func (d *Deps) unitParams(inst domain.Installation) ports.UnitParams {
	return ports.UnitParams{
		Product:     inst.Product,
		ManagerPath: d.ManagerPath,
		ConfigPath:  d.Paths.InstallationFile(),

		// From the installation, which is the whole point of the field
		// existing there. It used to come from an `init` flag and be
		// stored nowhere, so every reconciliation after `init` rendered
		// the default and moved an operator's maintenance window.
		BackupSchedule: inst.Policy.BackupSchedule,

		// The one unit pair that was unconditional (RFC 0030 row 4). A
		// machine whose backups are taken at the storage layer declares
		// it, and then has no timer to disable rather than a timer it
		// must keep disabling. Same polarity as the field it comes
		// from, so neither spelling has to be read backwards.
		SkipBackupTimer: inst.Policy.SkipScheduledBackups,

		// Both, not either. A timer exists to poll, and polling is
		// gated by `update.check` (RFC 0016 §5.6) -- so a machine with a
		// channel and checking off would install a unit that fails every
		// night on a refusal, which is how an operator learns to ignore
		// the unit.
		UpdateTimer: inst.Update.FollowsChannel() && inst.Update.Check,

		// The operator's window, as the backup schedule above is
		// (RFC 0016 decision 25). It was never set here, so every
		// machine ticked at 03:30 whatever anybody wanted.
		UpdateSchedule: inst.Update.Schedule,

		// A sandbox polls its vendor's own registry, so the jitter
		// that spreads customers across that registry protects nobody
		// and turns "every five minutes" into "every five to
		// thirty-five" (RFC 0016 decision 26). An operator's
		// `update.randomized_delay` wins in either mode (decision 31).
		SkipUpdateJitter: inst.UpdateRandomizedDelay() == 0,
		UpdateJitter:     inst.UpdateRandomizedDelay(),

		// A target to publish to is the whole precondition (RFC 0026
		// P4). There is no second flag gating this the way `update.check`
		// gates the poll: a fleet row is derived entirely from what this
		// machine already computes, goes to a target the operator chose,
		// and reveals nothing to anybody who could not already read that
		// target. The phone-home question that made update checking
		// opt-in does not arise.
		FleetTimer: inst.Backup.HasTargets(),

		// While sync is on, and only then (RFC 0057 §5.8): a timer
		// with no authority to follow would fetch nothing it could use.
		SyncTimer:    inst.Sync.Enabled,
		SyncInterval: inst.Sync.SyncInterval(),

		// Whenever someone is configured to be told or its findings are
		// exported, and not when the operator turned it off (RFC 0043
		// decision 4, RFC 0047 decision 3): a scheduled `doctor` with
		// neither writes only to the journal.
		DoctorTimer: (inst.Notify.HasTargets() || inst.Policy.MetricsTextfile != "") &&
			!inst.Policy.SkipScheduledDoctor,
		DoctorSchedule: inst.Policy.DoctorSchedule,

		// The metrics pair, carrying the directory it writes into
		// (RFC 0047 decisions 2 and 15).
		MetricsDir: inst.Policy.MetricsTextfile,
	}
}

// refreshUnits makes the installed unit set match what the installation
// declares.
//
// It exists because the update timer is generated from a setting rather than
// from a flag at `init`: an operator who configures a channel a month later must
// get a timer, and one who clears it must stop having one. Units installed only
// at creation would make the second impossible, and a machine polling a channel
// nobody has configured any more is a phone-home with no owner.
//
// Reconciliation, not installation: the units this installation should have are
// written, and every *managed* unit not among them is removed. ManagedUnitNames
// is deliberately the superset for exactly this -- it is what lets a machine
// that once followed a channel have its timer taken away without the lifecycle
// layer knowing what a timer is called.
//
// A host with no supervisor is not an error. That is the same host that gets no
// units at `init`, and a container deployment is a legitimate one.
// reconcileUnitsAfterConverge rewrites the units to what this manager writes,
// after an apply, start or update succeeded. Without it a unit an older manager
// wrote keeps its contents (RFC 0040 decision 13), and one this version adds,
// such as the doctor timer, waits for an unrelated setting to change. Not at
// boot: `apply --startup` runs inside the product's own unit, and the next
// command an operator runs reconciles. A failure warns and changes nothing
// about the operation's outcome.
func (d *Deps) reconcileUnitsAfterConverge(ctx context.Context) {
	inst, err := d.State.LoadInstallation(ctx)
	if err == nil {
		err = d.refreshUnits(ctx, inst)
	}
	if err != nil {
		d.warnf("the systemd units were not reconciled: %s; `morzer doctor` reports any that differ",
			domain.AsError(err).Message)
	}
}

func (d *Deps) refreshUnits(ctx context.Context, inst domain.Installation) error {
	if d.Supervisor == nil || !d.Supervisor.Available(ctx) {
		return nil
	}

	// A machine that manages no units keeps managing none. `init
	// --install-units=false` is a supported choice -- a container, a host
	// where this does not run as root -- and installing units into it now
	// would be the manager overruling a decision somebody already made.
	installed, err := d.Supervisor.InstalledUnits(ctx, inst.Product)
	if err != nil || len(installed) == 0 {
		return err
	}

	if err := d.checkManagerBinary(); err != nil {
		return err
	}
	units, err := d.Supervisor.Units(d.unitParams(inst))
	if err != nil {
		return err
	}

	wanted := make(map[string]bool, len(units))
	for _, unit := range units {
		wanted[unit.Name] = true
	}
	var stale []string
	for _, name := range d.Supervisor.ManagedUnitNames(inst.Product) {
		if !wanted[name] {
			stale = append(stale, name)
		}
	}

	// EnableNew, and this is the line RFC 0030 row 1 is about.
	//
	// A reconciliation runs because a *setting* changed -- a channel, a
	// schedule, a backup target -- and it is here to make unit contents and
	// unit existence match that. Enablement travelled on the same call, so
	// `systemctl disable demo-backup.timer` held until the next unrelated
	// `config set` and was then undone with no message. A unit this call
	// creates is still enabled, because a timer nobody has seen yet is not
	// a decision anybody has made.
	if err := d.Supervisor.InstallUnits(ctx, units, ports.EnableNew); err != nil {
		return err
	}
	if len(stale) == 0 {
		return nil
	}
	return d.Supervisor.RemoveUnits(ctx, stale)
}

func (s setting) stored(inst domain.Installation) string {
	if s.Stored != nil {
		return s.Stored(inst)
	}
	return s.Read(inst)
}

// zeroFor returns the installation with one setting cleared, so "already at its
// absent value" can be asked without a second Read function per setting.
func zeroFor(inst domain.Installation, s setting) domain.Installation {
	s.Clear(&inst)
	return inst
}

// sortedSettingNames orders a change so two runs report the same list, and so
// a failure part-way through fails on the same setting each time.
func sortedSettingNames(m map[string]string) []string {
	out := slices.Sorted(maps.Keys(m))
	return out
}

func unknownSetting(name string) error {
	return domain.Usage("no installation setting named %q", name).
		WithHint("settable: %s", strings.Join(SettingNames(), ", "))
}
