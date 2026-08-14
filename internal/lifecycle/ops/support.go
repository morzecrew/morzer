package ops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/goccy/go-yaml"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/infra/agecrypt"
	"github.com/morzecrew/morzer/internal/infra/atomicfs"
	"github.com/morzecrew/morzer/internal/ports"
	"github.com/morzecrew/morzer/internal/release"
)

// The support bundle (RFC 0024 P2).
//
// One archive an operator can hand to a stranger. §2 is what makes this
// buildable at all: the operator is a consumer of it too, so nothing here waits
// for a vendor to exist.
//
// A read, like `describe` and unlike `apply`: no deployment lock, no journal
// entry, no steps. Producing evidence changes nothing, and taking the lock to
// write a diagnostic would make an operator's "here is what happened" contend
// with the operation they are trying to explain. It is also the command most
// likely to be run while something else is already stuck.

// SupportOptions are the flags the command honours.
type SupportOptions struct {
	// Preview writes nothing and reports what would be collected.
	//
	// §3.5: an operator who cannot see what leaves will either send nothing
	// or send everything, and both are failures of this feature.
	Preview bool

	// Dir is where the archive is written. Empty means the working
	// directory, which is where an operator expects a file they are about
	// to attach to something.
	Dir string

	// Build is the link-time stamp for `manager.json`. Empty in a build
	// that stamps nothing, which is what `go run` produces.
	Build SupportBuild

	// NoLogs leaves container logs out.
	//
	// Not a redaction switch -- decision 5 refuses one of those, and this is
	// its opposite: it removes a component rather than removing the filter
	// from it, so every value of this flag is safe. It exists because the
	// operator knows things the manager does not, such as that this
	// product logs request bodies.
	NoLogs bool
}

// SupportEntry is one file in the archive, as reported.
type SupportEntry struct {
	Name  string `json:"name"`
	Title string `json:"title"`
	Bytes int64  `json:"bytes"`

	// Redactions is how many secret values were scrubbed out of this file.
	//
	// Reported for every entry, including the many that are structurally
	// incapable of holding one, because a field that appears only when it
	// is non-zero teaches a reader that its absence means "clean" when it
	// means "not applicable". Zero is a number, not a silence.
	Redactions int `json:"redactions"`
}

// SupportOmission is a component that was classified for inclusion and is not
// in the archive.
type SupportOmission struct {
	Name   string `json:"name"`
	Reason string `json:"reason"`
}

// SupportReport is what the command prints and what `meta.json` records.
type SupportReport struct {
	// Path is the archive. Empty for a preview, which is the difference
	// between the two modes as far as any caller is concerned.
	Path    string `json:"path,omitempty"`
	Preview bool   `json:"preview"`

	Product        string `json:"product"`
	InstallationID string `json:"installation_id"`

	// ManagerVersion identifies the redaction logic that ran, since the
	// redactor ships with the manager and has no version of its own
	// (RFC 0024 §3.3 asks for one; §12 A2 records why this is it).
	ManagerVersion string `json:"manager_version"`

	// Encrypted says whether the archive at Path can be read by whoever
	// receives it.
	//
	// Reported since P2, when it was always false, so that a reader could
	// tell the two apart by looking at the archive rather than by knowing
	// which manager wrote it -- a field appearing on the day encryption
	// ships is a field every existing reader treats as
	// absent-means-encrypted.
	Encrypted bool `json:"encrypted"`

	// Recipients are the age keys the archive is encrypted to, in full.
	//
	// Present on a preview too, which is the point of it: decision 3a's
	// refusal protects against a recipient that cannot be parsed, and
	// nothing can protect against one that parses and belongs to the wrong
	// party. Printing them before the archive exists is what lets an
	// operator check the target against what their vendor published.
	Recipients []string `json:"recipients,omitempty"`

	Entries    []SupportEntry    `json:"entries"`
	Omitted    []SupportOmission `json:"omitted,omitempty"`
	TotalBytes int64             `json:"total_bytes"`
}

// supportFile is one collected entry before it is written.
type supportFile struct {
	Name       string
	Data       []byte
	Redactions int
}

// supportCollector produces the files for one inventory row.
//
// Registered under the row's archive name, which is what binds the two halves:
// `TestEveryCollectorIsClassified` fails the build when a collector appears
// whose name is not in the inventory, so a component cannot start leaving the
// machine without a row on the page that promises what leaves it.
type supportCollector struct {
	Name    string
	Collect func(context.Context, *Deps, *supportSource) ([]supportFile, error)
}

// supportSource is what the collectors read, gathered once.
//
// Loaded up front rather than per-collector because half of them need the
// installation and the release, and reading the state six times would let two
// components disagree about which release is current -- in an archive whose
// entire value is being one consistent account of one moment.
type supportSource struct {
	Installation domain.Installation
	Release      domain.Release
	HasRelease   bool

	// Build is the link-time stamp, which only the CLI layer knows.
	Build SupportBuild

	// ReleaseProblem is why the release could not be resolved, empty when
	// there simply is not one. The components that need a release quote it,
	// so the archive records the failure rather than its symptom.
	ReleaseProblem string

	// Record is the release pointer as state holds it, which is what the
	// described document names -- version, digest and source ref -- and is
	// readable even when the release directory it points at is not.
	Record domain.ReleaseRecord
}

// SupportBuild is the binary's own identity, passed in rather than read.
//
// The version is already in Deps; the commit and date are stamped at link time
// into the command layer and have never had a reason to reach an operation
// before. They travel as an option rather than as new Deps fields because they
// are an input to one report, not a capability the lifecycle layer has.
type SupportBuild struct {
	Commit string
	Date   string
}

// supportCollectors is every collector, in the order the entries appear.
//
// `logs/` is last for the same reason it shipped last: it is the only component
// that is raw vendor bytes, and §9 is blunt that collecting it before the phase
// which proves redaction works would be "a leak generator with a progress bar".
var supportCollectors = []supportCollector{
	{Name: "manifest.yaml", Collect: collectManifest},
	{Name: "installation.yaml", Collect: collectInstallation},
	{Name: "parameters.json", Collect: collectParameters},
	{Name: "config-diff.txt", Collect: collectConfigDiff},
	{Name: "journal.jsonl", Collect: collectJournal},
	{Name: "doctor.json", Collect: collectDoctor},
	{Name: "releases.json", Collect: collectReleases},
	{Name: "services.json", Collect: collectServices},
	{Name: "manager.json", Collect: collectManager},
	{Name: logsPrefix, Collect: collectLogs},
}

// The bound on a captured log stream (RFC 0024 §9, §11.3).
//
// A container log stream is unbounded, and an archive somebody has to email is
// not. Both limits apply: lines first because that is the unit an operator
// thinks in, bytes second because one line can be a megabyte of stack trace.
//
// Measured on the acceptance deployment after it had run init, apply, three
// configuration changes, a backup, a restore, an update killed mid-flight, a
// resume and a refused rollback (§12 A4): **the whole archive is 5,882 bytes
// compressed, of which the journal is 10,539 uncompressed** -- roughly a
// kilobyte per operation, so a machine at one operation a day reaches a third of
// a megabyte in a year and needs no bound of its own.
//
// What the measurement cannot say is how loud a real product is: the acceptance
// containers wrote 889 bytes between them, and a production service writes that
// in a second. So the bound is reasoned rather than fitted, and it is what
// decides this artifact's size -- everything else in it is small and roughly
// fixed. At a 200-byte line, 2000 lines is 400KiB before compression, which
// stays attachable to a ticket while holding the minutes around a failure. The
// byte limit is the backstop for the log line that is a whole stack trace.
const (
	supportLogLines = 2000
	supportLogBytes = 1 << 20
)

// collectLogs captures each service's recent output (RFC 0024 P3).
//
// The only component that is raw vendor bytes, which is why it arrives last and
// with the phase that proves the redactor handles it. Two things are load-bearing
// here and neither is the capture itself:
//
//   - It is **omitted entirely** when redaction could not be armed. 0021 lets
//     `morzer logs` print an unfiltered stream and say so, because an operator
//     reading their own terminal can decide what to do with what they see. An
//     archive cannot: it is read by somebody else, later, who has only the file
//     and the count in `meta.json`. Decision 5 refuses a `--raw` flag for the
//     same reason, and shipping unredactable logs by default would be that flag
//     with no way to turn it off.
//   - The bound is recorded when it truncates, so a reader knows the silence
//     before the first line is a limit rather than the start of the incident.
func collectLogs(ctx context.Context, d *Deps, _ *supportSource) ([]supportFile, error) {
	if d.Redactor == nil {
		return nil, domain.InstallationError(nil,
			"no redactor is wired, so container logs cannot be scrubbed and are not collected")
	}

	stream, err := StreamLogs(ctx, d, LogsOptions{
		Tail:       supportLogLines,
		Structured: true,
		Redact:     true,
	})
	if err != nil {
		return nil, err
	}
	defer func() { _ = stream.Close() }()

	if !stream.RedactionArmed {
		return nil, domain.SecretsError(nil,
			"the secret values could not be loaded, so container logs could not be "+
				"scrubbed and are not collected; everything else is here")
	}

	perService := map[string]*strings.Builder{}
	truncated := map[string]bool{}
	err = stream.Lines(func(line ports.LogLine) error {
		name := logFileName(line)
		b, ok := perService[name]
		if !ok {
			b = &strings.Builder{}
			perService[name] = b
		}
		if b.Len()+len(line.Text)+1 > supportLogBytes {
			truncated[name] = true
			return nil
		}
		if !line.At.IsZero() {
			b.WriteString(line.At.UTC().Format(time.RFC3339) + " ")
		}
		b.WriteString(line.Text)
		b.WriteByte('\n')
		return nil
	})
	if err != nil {
		return nil, err
	}

	if len(perService) == 0 {
		// Not silence. A component that produces no files and no
		// explanation leaves its reader unable to tell "this deployment
		// wrote nothing" from "this was never collected" -- the exact
		// ambiguity `meta.json` exists to close for every other
		// component, and it would be closed everywhere except the one
		// place where a missing file is most suspicious.
		return nil, domain.RuntimeError(nil,
			"the deployment produced no log output to capture")
	}

	names := make([]string, 0, len(perService))
	for name := range perService {
		names = append(names, name)
	}
	sort.Strings(names)

	out := make([]supportFile, 0, len(names))
	for _, name := range names {
		body := perService[name].String()
		if truncated[name] {
			// Names the bound that actually cut, which is always the
			// byte one here: the line bound is applied by the
			// runtime as a tail, so a stream that hit *it* arrives
			// already short and this code cannot tell it apart from
			// a service that simply said less. Claiming both bounds
			// would tell a reader their logs were cut at 2000 lines
			// when they were cut at a megabyte, and send them
			// looking for the wrong thing.
			body = fmt.Sprintf(
				"[truncated by `morzer support bundle` at %d bytes; "+
					"at most %d lines per service are requested]\n",
				supportLogBytes, supportLogLines) + body
		}
		out = append(out, supportFile{Name: logsPrefix + name, Data: []byte(body)})
	}
	return out, nil
}

// logFileName is the per-service file a line belongs in.
//
// By service rather than by container, so a product with three replicas of one
// service produces one readable file. A line the runtime wrote about the stream
// itself belongs to no service and goes to `runtime.log` rather than being
// dropped -- it is often the line that explains why the rest stopped.
func logFileName(line ports.LogLine) string {
	switch {
	case line.Service != "":
		return line.Service + ".log"
	case line.Container != "":
		return line.Container + ".log"
	default:
		return "runtime.log"
	}
}

// supportMetaName is the archive's own index, which is not a collector: it
// describes the others, so it is built after the loop rather than inside it.
const supportMetaName = "meta.json"

// logsPrefix is the container-log component, which is a directory rather than a
// file: one component that happens to be several entries, one per service.
const logsPrefix = "logs/"

// supportProduced is every archive name this build can write.
//
// Exists so a test can ask the builder what it produces rather than restate the
// list beside it -- the restatement is what goes stale, and it goes stale in the
// direction of a component that ships without a row on the operator's page.
func supportProduced() []string {
	out := make([]string, 0, len(supportCollectors)+1)
	for _, c := range supportCollectors {
		out = append(out, c.Name)
	}
	return append(out, supportMetaName)
}

// SupportBundle collects the archive, or reports what one would contain.
func SupportBundle(ctx context.Context, d *Deps, opts SupportOptions) (SupportReport, error) {
	inst, err := d.loadInstallation(ctx)
	if err != nil {
		return SupportReport{}, err
	}

	// Before anything is collected, and this ordering is the whole safety
	// argument rather than a detail.
	//
	// The redactor recognises values it has been told about. A component
	// gathered before registration is gathered in the clear, and nothing
	// downstream can tell that it was -- `Apply` on an already-copied
	// string finds nothing to scrub and reports a redaction count of zero,
	// which reads exactly like a file that was clean. §11.1 found the same
	// shape live in the log handler's `WithAttrs`, which redacts at capture
	// time and stores the result; this is that defect's shape at the level
	// of a whole archive.
	armed := d.armRedaction(ctx)

	src := &supportSource{Installation: inst, Build: opts.Build}
	rel, record, relErr := supportRelease(ctx, d)
	src.Record = record
	switch {
	case relErr == nil && !record.IsZero():
		src.Release, src.HasRelease = rel, true
	case relErr != nil:
		// Why the release-dependent components will be missing, said
		// once here rather than guessed at by each of them.
		//
		// Non-fatal, deliberately, and this widened when the resolution
		// error stopped being discarded: an unreadable release *record*
		// used to fail the whole command and now travels the same way a
		// broken release directory does. That is the right direction --
		// an installation whose state will not answer is one somebody
		// urgently needs a bundle from, and every other component is
		// still collectable. The installation itself is the exception,
		// and it is read above: without it there is nothing to describe.
		src.ReleaseProblem = domain.AsError(relErr).Message
	}

	// Before a single component is collected, and that ordering is the
	// point rather than an optimisation.
	//
	// A malformed declaration is a refusal (decision 3a). Discovering it
	// after the archive is assembled would mean either throwing away the
	// work or -- the failure this guards -- writing the plaintext archive
	// anyway because the encryption step was the only thing that failed.
	// Doing it here also means `--preview` refuses on the same manifest the
	// real run would, which is what makes a preview worth running.
	recipients, recipientNote, err := supportRecipients(src)
	if err != nil {
		return SupportReport{}, err
	}

	report := SupportReport{
		Preview:        opts.Preview,
		Product:        inst.Product,
		InstallationID: inst.ID,
		ManagerVersion: d.ManagerVersion.String(),
		Encrypted:      len(recipients) > 0,
		Recipients:     domain.SupportRecipientFingerprints(recipients),
	}
	if recipientNote != nil {
		report.Omitted = append(report.Omitted, *recipientNote)
	}
	if !armed {
		// Precisely what happened, because this line is the reader's
		// only warning. Nothing was scrubbed from *anything* -- not
		// just from the component that was skipped -- and a reader who
		// takes "0 redactions" beside this omission as "clean" has
		// drawn exactly the wrong conclusion.
		report.Omitted = append(report.Omitted, SupportOmission{
			Name: "redaction",
			Reason: "the installation's secret values could not be loaded, so no " +
				"component was scrubbed and every redaction count below is zero " +
				"for that reason rather than because nothing was found; container " +
				"logs were left out entirely",
		})
	}

	files := make([]supportFile, 0, len(supportCollectors))
	for _, c := range supportCollectors {
		if opts.NoLogs && c.Name == logsPrefix {
			report.Omitted = append(report.Omitted, SupportOmission{
				Name:   c.Name,
				Reason: "left out by --no-logs",
			})
			continue
		}
		collected, err := c.Collect(ctx, d, src)
		if err != nil {
			// Omitted with its reason, never dropped silently and never
			// fatal.
			//
			// This command runs when things are broken. A bundle that
			// refused because `doctor` could not answer would take the
			// tool away at the moment it exists for -- and unlike
			// `installation describe`, whose file is committed as a
			// record and must therefore refuse rather than record an
			// absence it did not verify, an omission here is *stated*
			// in meta.json. A recorded gap is evidence; a silent one
			// is the lie.
			report.Omitted = append(report.Omitted, SupportOmission{
				Name:   c.Name,
				Reason: domain.AsError(err).Message,
			})
			continue
		}
		files = append(files, scrub(d, collected)...)
	}

	// Scrubbed like everything else, and this was the gap that mattered
	// most: `meta.json` is the file a reviewer opens to decide the archive
	// is safe, and it is built from free-form error text. Every omission
	// reason is `domain.AsError(err).Message` from an arbitrary collector --
	// the state layer, the renderer, the runtime, `doctor` -- and any of
	// them can quote a value the redactor would have removed from the file
	// the error was about. The release problem this branch now records is
	// exactly that shape: it carries a resolver message about a path.
	files = append(files, scrub(d, []supportFile{supportMeta(&report, files)})...)

	for _, f := range files {
		row := entryFor(f)
		report.Entries = append(report.Entries, row)
		report.TotalBytes += row.Bytes
	}

	if opts.Preview {
		return report, nil
	}

	path, err := writeSupportArchive(d, inst, files, opts.Dir, recipients)
	if err != nil {
		return SupportReport{}, err
	}
	report.Path = path
	return report, nil
}

// supportRecipients resolves who this archive is encrypted to.
//
// Three outcomes, and each one is a different sentence to an operator. A
// manifest that declares nobody produces a plaintext archive and no note --
// decision 3 keeps that available and the view says so on every run. A manifest
// that declares recipients unusably is a refusal, before any work.
//
// The third is the one the design did not have a row for: the release cannot be
// resolved, so there is no manifest to ask, so a vendor's declaration -- if
// there is one -- silently does not apply. That happens on exactly the machine
// this command exists for, and it must not be the case that reads as "your
// vendor asked for nothing". It produces plaintext, because refusing would take
// the tool away at the moment it is needed, and an omission naming the reason,
// because an unstated gap here is the lie meta.json exists to prevent.
func supportRecipients(src *supportSource) ([]string, *SupportOmission, error) {
	if !src.HasRelease {
		note := &SupportOmission{
			Name: "encryption",
			Reason: "the release could not be resolved, so any support recipients " +
				"its manifest declares were not applied and this archive is " +
				"plaintext",
		}
		return nil, note, nil
	}

	declared, err := src.Release.Manifest.SupportRecipients()
	if err != nil {
		return nil, nil, err
	}
	if len(declared) == 0 {
		return nil, nil, nil
	}

	// Each one checked by the parser that will encrypt with it, so a typo
	// is named here rather than surfacing as a failure after the archive is
	// built -- or, if this check did not exist, as an encryption step that
	// fails and leaves the caller deciding what to do with the plaintext.
	for _, key := range declared {
		if err := agecrypt.ValidateRecipient(key); err != nil {
			return nil, nil, domain.ValidationError(err,
				"`extensions.%q.recipients` names something that is not an age recipient: %s",
				domain.SupportExtension, domain.AsError(err).Message).
				WithHintFrom(err)
		}
	}
	return declared, nil, nil
}

// supportRelease reads the current release, tolerating its absence.
//
// An installation with no release still has an answer to "what is this
// machine", and it is a machine somebody may well be asking for help about --
// a failed first `apply` is exactly when this command is useful. So the release
// is optional and the components that need it omit themselves.
// supportRelease reads the release pointer and resolves what it points at.
//
// Returns the record as well as the release, because they fail independently: a
// machine whose release directory is unreadable still has a record saying which
// release it believes it is running, and that is most of the question on
// exactly that machine.
func supportRelease(ctx context.Context, d *Deps) (domain.Release, domain.ReleaseRecord, error) {
	rec, err := d.State.CurrentRelease(ctx)
	if err != nil {
		return domain.Release{}, domain.ReleaseRecord{}, err
	}
	if rec.IsZero() {
		return domain.Release{}, rec, nil
	}

	rel, err := d.resolveCurrentRelease(ctx, rec)
	if err != nil {
		// Kept, not discarded, and this is the same distinction
		// `describe` draws: absent is absent and broken is broken, and
		// the state layer already tells them apart.
		//
		// Collapsing them here made a release that failed to load --
		// a digest mismatch, which the resolver raises deliberately
		// when the directory was modified after installation -- report
		// itself as a release that was never installed. That is a
		// support bundle hiding the answer on the one machine that most
		// needed it to say something.
		//
		// Still not fatal: the archive is worth more than the
		// components this costs, and the failure travels as their
		// omission reason.
		return domain.Release{}, rec, err
	}
	return rel, rec, nil
}

// supportTitle is the inventory's title for an archive entry, so the printed
// report and the generated page name a component the same way.
//
// `logs/service.log` is titled by its directory row, because per-service files
// are one component that happens to be several files.
func supportTitle(name string) string {
	for _, c := range domain.SupportInventory {
		if c.Name == name || (strings.HasSuffix(c.Name, "/") && strings.HasPrefix(name, c.Name)) {
			return c.Title
		}
	}
	return name
}

// noRelease explains the absence of a release to a component that needed one.
//
// Two different sentences, because they send a reader to two different places:
// an installation that has never had a release is a machine waiting for its
// first `update`, and one whose release will not load is a machine with a
// problem somebody has to look at.
func (s *supportSource) noRelease(consequence string) error {
	if s.ReleaseProblem != "" {
		return domain.InstallationError(nil,
			"the installed release could not be read (%s), %s", s.ReleaseProblem, consequence)
	}
	return domain.InstallationError(nil, "no release is installed, %s", consequence)
}

// ----------------------------------------------------------------------------
// Collectors

func collectManifest(_ context.Context, _ *Deps, src *supportSource) ([]supportFile, error) {
	if !src.HasRelease {
		return nil, src.noRelease("so there is no manifest to resolve")
	}
	body, err := yaml.Marshal(src.Release.Manifest)
	if err != nil {
		return nil, domain.Internal(err, "cannot render the manifest")
	}
	return []supportFile{{Name: "manifest.yaml", Data: body}}, nil
}

// collectInstallation writes the *described* installation, not the record.
//
// `Installation.Describe` is the document `installation describe` produces, and
// it exists because the raw record holds things that must not be published.
// `AttestationSalt` is the one that decides this: it makes the attestation's
// configuration digest resistant to being brute-forced back over a small space
// of ports and booleans, and `describe.go` excludes it by name because
// "publishing it in a document meant for a git repository would make the digest
// it salts brute-forceable again". This archive travels further than a
// repository -- it is built to be handed to a stranger.
//
// Marshalling the record also made this component's inventory entry false. That
// entry cites `installation describe` being safe to commit as the reason this
// is safe to send, which is only an argument if this is that document.
func collectInstallation(ctx context.Context, d *Deps, src *supportSource) ([]supportFile, error) {
	doc := src.Installation.Describe(releaseFromRecord(src.Record), supportSecretNames(ctx, d))
	body, err := yaml.Marshal(doc)
	if err != nil {
		return nil, domain.Internal(err, "cannot render the installation")
	}
	return []supportFile{{Name: "installation.yaml", Data: body}}, nil
}

// releaseFromRecord names the release the document points at.
//
// From the record rather than the loaded release, so a machine whose release
// directory is unreadable still says which release it believes it is running --
// which is most of the question on exactly that machine.
func releaseFromRecord(rec domain.ReleaseRecord) domain.DescribedRelease {
	if rec.IsZero() {
		return domain.DescribedRelease{}
	}
	return domain.DescribedRelease{
		Name:    rec.Name,
		Version: rec.Version,
		Digest:  rec.Digest,
		Ref:     rec.SourceRef,
	}
}

// supportSecretNames lists the secrets that must exist, best-effort.
//
// `Describe` refuses when the store will not answer, because its document is
// committed as a record and `secrets: []` would be a false one. This archive
// makes the opposite trade: it is produced *because* something is broken, and a
// store that will not open is one more thing the reader should see -- so the
// names are omitted and every other field still ships.
func supportSecretNames(ctx context.Context, d *Deps) []string {
	if d.Secrets == nil {
		return nil
	}
	ready, err := d.Secrets.Initialized(ctx)
	if err != nil || !ready {
		return nil
	}
	meta, err := d.Secrets.Metadata(ctx)
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(meta))
	for _, m := range meta {
		names = append(names, m.Name)
	}
	return names
}

func collectParameters(ctx context.Context, d *Deps, _ *supportSource) ([]supportFile, error) {
	report, err := ConfigList(ctx, d)
	if err != nil {
		return nil, err
	}
	return jsonFile("parameters.json", report)
}

// collectConfigDiff renders what the release would write and diffs it against
// what is on disk.
//
// Safe to include by an invariant rather than by inspection: `templateData`
// puts secret *references* in the render context -- a name to the path of its
// rendered file -- and never the values, so a configuration target cannot embed
// a secret to leak here.
func collectConfigDiff(ctx context.Context, d *Deps, src *supportSource) ([]supportFile, error) {
	if !src.HasRelease {
		return nil, src.noRelease("so nothing renders configuration")
	}

	comparison, err := configComparison(ctx, d, src.Installation, src.Release)
	if err != nil {
		return nil, err
	}

	// slices.Concat rather than append: appending writes into Diffs' spare
	// capacity when it has any, and `configComparison` is now shared with
	// the fleet row's drift count. Nothing reads the comparison after this
	// line today, which is exactly the state in which the trap is invisible.
	body := "no drift: every configuration target matches what this release renders\n"
	if reports := slices.Concat(comparison.Diffs, comparison.Unreadable); len(reports) > 0 {
		body = strings.Join(reports, "\n")
	}
	return []supportFile{{Name: "config-diff.txt", Data: []byte(body)}}, nil
}

// ConfigComparison is what the files on disk say versus what the release
// renders.
//
// Two lists rather than one, because there are two facts and a caller that
// counts them needs to tell them apart. A target that differs is drift. A
// target that cannot be *read* is a different thing entirely -- an absent file
// is drift, an unreadable one is a permission problem -- and folding it into
// the first would let a broken `/etc` publish itself as configuration change.
//
// The support bundle prints both, because a person reading a ticket wants both.
// A fleet row counts only the first, and says how many were not compared.
type ConfigComparison struct {
	// Diffs is one unified diff per target that differs, in target order.
	Diffs []string

	// Unreadable is one line per target that could not be read.
	Unreadable []string
}

// configComparison renders every configuration target and compares it with what
// is on disk.
//
// One computation with two presentations, rather than one per consumer. Two
// would agree on the day they were written and drift into a drift detector that
// disagrees with the support bundle sitting beside it -- and the operator
// holding both would have no way to tell which was lying.
func configComparison(
	ctx context.Context, d *Deps, inst domain.Installation, rel domain.Release,
) (ConfigComparison, error) {
	rendered, err := renderConfiguration(ctx, d, inst, rel)
	if err != nil {
		return ConfigComparison{}, err
	}

	targets := make([]string, 0, len(rendered))
	for target := range rendered {
		targets = append(targets, target)
	}
	sort.Strings(targets)

	var out ConfigComparison
	for _, target := range targets {
		existing, err := os.ReadFile(target)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			// An absent file is drift; an unreadable one is a
			// different fact. Treating them alike renders the whole
			// file as added and tells the reader the copy on disk is
			// empty, which is a claim about the machine nobody made
			// -- on a command that exists to report facts about
			// broken machines.
			out.Unreadable = append(out.Unreadable, fmt.Sprintf(
				"%s: cannot be read (%v), so no comparison was made\n", target, err))
			continue
		}
		if diff := unifiedDiff(target, string(existing), string(rendered[target])); diff != "" {
			out.Diffs = append(out.Diffs, diff)
		}
	}
	return out, nil
}

// renderConfiguration renders every configuration target, read-only.
//
// The schema is release metadata rather than secret values, so loading it here
// has no side effects and reveals nothing -- the same reasoning that lets
// `apply --dry-run` show a configuration diff without executing the step that
// would normally have left the schema in state.
func renderConfiguration(
	ctx context.Context, d *Deps, inst domain.Installation, rel domain.Release,
) (map[string][]byte, error) {
	schema, err := release.LoadSecretSchema(rel)
	if err != nil {
		return nil, err
	}
	data, err := d.templateData(inst, rel, "", schema)
	if err != nil {
		return nil, err
	}

	out := make(map[string][]byte, len(rel.Manifest.Configuration))
	for _, cfg := range rel.Manifest.Configuration {
		// Checked here as well as inside the renderer, as the apply
		// step does: this refuses the "../" spelling with a message
		// about the manifest, while os.Root refuses what only an open
		// can see.
		if _, err := rel.Path(cfg.Template); err != nil {
			return nil, err
		}
		body, err := d.Renderer.Render(ctx,
			ports.TemplateRef{Root: rel.Root, Name: cfg.Template}, data)
		if err != nil {
			return nil, err
		}
		out[d.configTarget(cfg.Target)] = body
	}
	return out, nil
}

// collectJournal writes the operation journal newest-first, one record a line.
//
// JSONL rather than one array, because a journal is appended to and a reader
// with a truncated archive can still parse every line before the truncation.
func collectJournal(ctx context.Context, d *Deps, _ *supportSource) ([]supportFile, error) {
	records, err := d.State.Operations(ctx, ports.Filter{})
	if err != nil {
		return nil, err
	}

	var b strings.Builder
	for _, rec := range records {
		line, err := json.Marshal(rec)
		if err != nil {
			return nil, domain.Internal(err, "cannot render a journal record")
		}
		b.Write(line)
		b.WriteByte('\n')
	}
	return []supportFile{{Name: "journal.jsonl", Data: []byte(b.String())}}, nil
}

func collectDoctor(ctx context.Context, d *Deps, _ *supportSource) ([]supportFile, error) {
	report, err := Doctor(ctx, d)
	if err != nil {
		return nil, err
	}
	return jsonFile("doctor.json", report)
}

func collectReleases(ctx context.Context, d *Deps, _ *supportSource) ([]supportFile, error) {
	entries, err := InstalledReleases(ctx, d)
	if err != nil {
		return nil, err
	}
	return jsonFile("releases.json", entries)
}

func collectServices(ctx context.Context, d *Deps, _ *supportSource) ([]supportFile, error) {
	services, err := ListServices(ctx, d)
	if err != nil {
		return nil, err
	}
	return jsonFile("services.json", services)
}

// collectManager records which binary produced the archive.
//
// Version *and* build, which §3.2 asks for and the first pass narrowed to the
// version alone. The commit is what distinguishes two binaries claiming the
// same version -- a development build and a release, or a patched host -- and
// this file is also the archive's statement about which redaction logic ran
// (§12 A2). "1.4.0" is not enough to answer that.
func collectManager(_ context.Context, d *Deps, src *supportSource) ([]supportFile, error) {
	manager := map[string]string{"version": d.ManagerVersion.String()}
	if src.Build.Commit != "" {
		manager["commit"] = src.Build.Commit
	}
	if src.Build.Date != "" {
		manager["built"] = src.Build.Date
	}
	return jsonFile("manager.json", manager)
}

// supportMeta is the archive's account of itself, and is built last because it
// describes everything else.
//
// It does not list itself, and that is the honest answer to a real problem
// rather than an omission: a file cannot state its own redaction count, because
// the count is only known after the file exists and scrubbing it would change
// the bytes the count describes. Recording a zero there would be the one
// misreading this whole feature is arranged to prevent -- so the terminal
// report and `--json` carry `meta.json`'s own count, where they are produced
// after it has been scrubbed, and the file describes the components it is an
// index of.
//
// It carries the omissions as well as the entries. An archive that simply
// lacked a file would leave its reader to guess whether the component does not
// exist on that machine, failed to collect, or was never part of the format --
// three answers with different next steps.
func supportMeta(report *SupportReport, files []supportFile) supportFile {
	entries := make([]SupportEntry, 0, len(files))
	for _, f := range files {
		entries = append(entries, entryFor(f))
	}

	meta := struct {
		Product        string            `json:"product"`
		InstallationID string            `json:"installation_id"`
		ManagerVersion string            `json:"manager_version"`
		Encrypted      bool              `json:"encrypted"`
		Entries        []SupportEntry    `json:"entries"`
		Omitted        []SupportOmission `json:"omitted,omitempty"`
	}{
		Product:        report.Product,
		InstallationID: report.InstallationID,
		ManagerVersion: report.ManagerVersion,
		Entries:        entries,
		Omitted:        report.Omitted,
	}

	body, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		// Unreachable for this shape, and if it were reachable an
		// archive without its own index is still worth more than no
		// archive.
		body = []byte("{}")
	}
	return supportFile{Name: supportMetaName, Data: append(body, '\n')}
}

// scrub runs every collected file through the redactor and records what it
// removed.
//
// Every component, not only the ones classified `redact`, and the reason is
// §11.1's defect at the scale of an archive. A component's class describes
// where its bytes came from -- `redact` is the one that is raw vendor output --
// but redaction here is about *when* the bytes were written, and most of these
// components were written long before this command ran.
//
// The journal is the clear case. It is appended to across every operation this
// installation has ever run, and a step message that embedded a secret was
// written at a moment when the redactor may not have been told about that
// secret yet; `logging`'s own `TestRegisteringAfterWithIsAKnownLimit` pins that
// the log handler captures eagerly and keeps the clear copy. Redacting at
// collection time is what makes registration order stop mattering: whatever is
// on disk, whenever it was written, is scrubbed against what this installation
// holds now.
//
// It also means a redaction count is meaningful for every entry rather than for
// one, which is what makes a zero in that column readable at all.
func scrub(d *Deps, files []supportFile) []supportFile {
	if d.Redactor == nil {
		return files
	}
	for i, f := range files {
		clean, n := d.Redactor.ApplyCount(string(f.Data))
		files[i].Data = []byte(clean)
		files[i].Redactions += n
	}
	return files
}

// entryFor is how a collected file is reported, in one place.
//
// The printed table and `meta.json` are built from the same function because
// they are the same claim made to two readers -- the operator at the terminal
// and whoever opens the archive later -- and two constructions of it would be
// two chances for those readers to be told different things.
func entryFor(f supportFile) SupportEntry {
	return SupportEntry{
		Name:       f.Name,
		Title:      supportTitle(f.Name),
		Bytes:      int64(len(f.Data)),
		Redactions: f.Redactions,
	}
}

func jsonFile(name string, v any) ([]supportFile, error) {
	body, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, domain.Internal(err, "cannot render %s", name)
	}
	return []supportFile{{Name: name, Data: append(body, '\n')}}, nil
}

// ----------------------------------------------------------------------------
// Writing

// writeSupportArchive stages the entries and packs them.
//
// Staged through a directory rather than tarred from memory so that the archive
// is written by `atomicfs.WriteTarZst`, which normalises every header -- uid 0,
// no owner names, one mtime, single-threaded compression. Those properties were
// written for release bundles and are worth as much here: an archive that
// carries the operator's account name in its headers is an archive that says
// something about the operator nobody asked it to say.
func writeSupportArchive(
	d *Deps,
	inst domain.Installation,
	files []supportFile,
	dir string,
	recipients []string,
) (string, error) {
	staging, err := os.MkdirTemp("", "morzer-support-")
	if err != nil {
		return "", domain.Internal(err, "cannot stage the support bundle")
	}
	// Ours, made a line ago, and it holds a copy of everything the archive
	// holds. Leaving it behind would put a second, unencrypted, unnoticed
	// copy in the system temporary directory.
	defer func() { _ = os.RemoveAll(staging) }()

	names := make([]string, 0, len(files))
	for _, f := range files {
		target := filepath.Join(staging, filepath.FromSlash(f.Name))
		if err := atomicfs.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return "", err
		}
		if err := atomicfs.WriteFile(target, f.Data, 0o600); err != nil {
			return "", err
		}
		names = append(names, f.Name)
	}

	// Absolute, always, and that is a contract rather than a nicety.
	//
	// `--json` puts this path on stdout, where it is read by something that
	// then acts on it -- and the thing that acts on it is not necessarily in
	// the directory the archive was written to. A relative name is correct
	// only for a reader who is standing where the writer stood, which is the
	// one assumption a machine-readable field must not make.
	dir, err = archiveDir(dir)
	if err != nil {
		return "", err
	}

	path := filepath.Join(dir, supportArchiveName(d, inst, recipients))
	if len(recipients) == 0 {
		if err := atomicfs.WriteTarZst(path, staging, names, d.now()); err != nil {
			return "", err
		}
		return path, nil
	}

	// The plaintext archive is built inside the staging directory, never in
	// the directory the operator asked for.
	//
	// Writing it to `path` and encrypting in place afterwards would put a
	// readable copy of everything, under the name an operator is watching
	// for, in a directory they are about to attach a file from -- for
	// however long the encryption takes, and permanently if the process
	// dies in between. The archive that appears at `path` has never been
	// anything but ciphertext.
	plain := filepath.Join(staging, ".archive.tar.zst")
	if err := atomicfs.WriteTarZst(plain, staging, names, d.now()); err != nil {
		return "", err
	}
	if err := encryptSupportArchive(plain, path, recipients); err != nil {
		return "", err
	}
	// Overwritten rather than only unlinked, like the backup components
	// this borrows from: it is the plaintext of an archive somebody
	// deliberately asked to be unreadable, and the staging directory is
	// removable but the bytes are not until something writes over them.
	if err := atomicfs.RemoveWithOverwrite(plain); err != nil {
		return "", err
	}
	return path, nil
}

// encryptSupportArchive writes the encrypted archive, and leaves nothing at the
// destination if it cannot finish.
func encryptSupportArchive(plain, path string, recipients []string) error {
	in, err := os.Open(plain) //nolint:gosec // the path is this function's own staging file
	if err != nil {
		return domain.Internal(err, "cannot read the staged support bundle")
	}
	defer func() { _ = in.Close() }()

	// 0600 and O_EXCL: created before a byte is written, so the archive is
	// never briefly world-readable, and an existing file is never silently
	// replaced.
	out, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return domain.Internal(err, "cannot create %s", path)
	}

	if err := agecrypt.Encrypt(out, in, recipients); err != nil {
		_ = out.Close()
		// A partial file at the destination is worse than none: it
		// carries the name of an encrypted archive and decrypts to
		// nothing, so whoever receives it learns that the operator sent
		// something rather than that the operator sent nothing.
		_ = atomicfs.RemoveAll(path)
		return err
	}
	if err := out.Close(); err != nil {
		_ = atomicfs.RemoveAll(path)
		return domain.Internal(err, "cannot finish writing %s", path)
	}
	return nil
}

// archiveDir resolves where the archive goes.
//
// An empty `--dir` is the working directory, which is where an operator expects
// a file they are about to attach to something -- and `filepath.Abs` already
// answers that, because joining the working directory with "" is the working
// directory. An explicit branch for the empty case was here and a sabotage
// survived it: the special case could not fail, because the general one was
// already handling it.
func archiveDir(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", domain.Internal(err, "cannot tell where to write the support bundle")
	}
	return abs, nil
}

// supportArchiveName is `support-<product>-<installation-id>-<timestamp>.tar.zst`.
//
// The timestamp is RFC 3339's basic form -- `20260813T170405Z`, no colons --
// which is a **departure from §3.1** recorded as amendment A1. A colon in a
// filename is legal on every platform this runs on and is a trap on the one
// journey this file is built to make: `scp bundle-...T17:04:05Z.tar.zst host:`
// makes `scp` read everything before the first colon as a hostname. A filename
// that breaks the tool an operator uses to send it is a bad filename for a file
// whose purpose is to be sent.
// An encrypted archive gains `.age`, which is what every other encrypted
// artifact this manager writes is called and what tells the operator, their
// vendor and `file(1)` what they are holding before anyone tries to open it.
func supportArchiveName(d *Deps, inst domain.Installation, recipients []string) string {
	name := fmt.Sprintf("support-%s-%s-%s.tar.zst",
		inst.Product, inst.ID, d.now().UTC().Format("20060102T150405Z"))
	if len(recipients) > 0 {
		name += agecrypt.Extension
	}
	return name
}

// ----------------------------------------------------------------------------
// support redact --check

// RedactCheckReport is what `support redact --check` answers.
type RedactCheckReport struct {
	Path  string `json:"path"`
	Bytes int64  `json:"bytes"`

	// Redactions is how many values this installation holds were found.
	Redactions int `json:"redactions"`

	// Armed says whether the secret values could be loaded at all.
	//
	// Load-bearing, and the reason this is not a bare count: zero
	// redactions from an unarmed redactor means "nothing was checked",
	// which is the opposite of what zero looks like. A caller reading the
	// number without this field can be told the file is clean by a check
	// that never ran.
	Armed bool `json:"armed"`
}

// maxCheckedFile bounds what `--check` will read.
//
// The file is an operator's own paste rather than anything this program wrote,
// so its size is not something the manager controls. Reading it whole is what
// makes the answer exact -- a secret split across a chunk boundary is exactly
// the case a streaming reader gets wrong -- so the bound is what keeps that
// from meaning "read whatever you are pointed at".
const maxCheckedFile = 32 << 20

// SupportRedactCheck runs this installation's redactor over a file the operator
// was going to send anyway (RFC 0024 decision 7).
//
// Cheap, and the feature most likely to actually prevent a leak: the archive is
// safe by construction, and the thing an operator pastes into a chat window is
// not. It reports and writes nothing -- the file is theirs, and a command that
// rewrote it would have destroyed the evidence they were about to send.
func SupportRedactCheck(ctx context.Context, d *Deps, path string) (RedactCheckReport, error) {
	if _, err := d.loadInstallation(ctx); err != nil {
		return RedactCheckReport{}, err
	}

	info, err := os.Stat(path)
	if err != nil {
		return RedactCheckReport{}, domain.Usage("cannot read %s: %v", path, err)
	}
	if info.IsDir() {
		return RedactCheckReport{}, domain.Usage("%s is a directory", path).
			WithHint("name the file you were going to send")
	}
	if info.Size() > maxCheckedFile {
		return RedactCheckReport{}, domain.Usage(
			"%s is %d bytes, past the %d this can check", path, info.Size(), maxCheckedFile).
			WithHint("check the part you were going to paste")
	}

	body, err := os.ReadFile(path)
	if err != nil {
		return RedactCheckReport{}, domain.Usage("cannot read %s: %v", path, err)
	}

	armed := d.armRedaction(ctx)
	report := RedactCheckReport{Path: path, Bytes: int64(len(body)), Armed: armed}
	if !armed || d.Redactor == nil {
		return report, nil
	}
	_, report.Redactions = d.Redactor.ApplyCount(string(body))
	return report, nil
}
