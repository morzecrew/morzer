package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/goccy/go-yaml"

	"github.com/morzecrew/morzer/internal/domain"
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

	// Encrypted is false in every archive this phase produces, and is
	// reported anyway.
	//
	// P4 adds `support.recipients`. Until then an operator has to be able
	// to tell a plaintext archive from an encrypted one by looking at the
	// archive rather than by knowing which version of the manager wrote it
	// -- and a field that appears the day encryption ships is a field every
	// existing reader treats as absent-means-encrypted.
	Encrypted bool `json:"encrypted"`

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
	defer stream.Close()

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

	names := make([]string, 0, len(perService))
	for name := range perService {
		names = append(names, name)
	}
	sort.Strings(names)

	out := make([]supportFile, 0, len(names))
	for _, name := range names {
		body := perService[name].String()
		if truncated[name] {
			// At the top, where somebody opening the file reads it
			// before deciding the incident started here.
			body = fmt.Sprintf("[truncated at %d bytes and %d lines by `morzer support bundle`]\n",
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

	src := &supportSource{Installation: inst}
	if rel, ok, err := supportRelease(ctx, d); err != nil {
		return SupportReport{}, err
	} else if ok {
		src.Release, src.HasRelease = rel, true
	}

	report := SupportReport{
		Preview:        opts.Preview,
		Product:        inst.Product,
		InstallationID: inst.ID,
		ManagerVersion: d.ManagerVersion.String(),
	}
	if !armed {
		report.Omitted = append(report.Omitted, SupportOmission{
			Name: "redaction",
			Reason: "the installation's secret values could not be loaded, so nothing " +
				"could be scrubbed; components that can carry vendor output were skipped",
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

	files = append(files, supportMeta(&report, files))

	for _, f := range files {
		row := SupportEntry{Name: f.Name, Title: supportTitle(f.Name), Bytes: int64(len(f.Data)), Redactions: f.Redactions}
		report.Entries = append(report.Entries, row)
		report.TotalBytes += row.Bytes
	}

	if opts.Preview {
		return report, nil
	}

	path, err := writeSupportArchive(d, inst, files, opts.Dir)
	if err != nil {
		return SupportReport{}, err
	}
	report.Path = path
	return report, nil
}

// supportRelease reads the current release, tolerating its absence.
//
// An installation with no release still has an answer to "what is this
// machine", and it is a machine somebody may well be asking for help about --
// a failed first `apply` is exactly when this command is useful. So the release
// is optional and the components that need it omit themselves.
func supportRelease(ctx context.Context, d *Deps) (domain.Release, bool, error) {
	rec, err := d.State.CurrentRelease(ctx)
	if err != nil {
		return domain.Release{}, false, err
	}
	if rec.IsZero() {
		return domain.Release{}, false, nil
	}
	rel, err := d.resolveCurrentRelease(ctx, rec)
	if err != nil {
		return domain.Release{}, false, nil
	}
	return rel, true, nil
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

// ----------------------------------------------------------------------------
// Collectors

func collectManifest(_ context.Context, _ *Deps, src *supportSource) ([]supportFile, error) {
	if !src.HasRelease {
		return nil, domain.InstallationError(nil, "no release is installed, so there is no manifest to resolve")
	}
	body, err := yaml.Marshal(src.Release.Manifest)
	if err != nil {
		return nil, domain.Internal(err, "cannot render the manifest")
	}
	return []supportFile{{Name: "manifest.yaml", Data: body}}, nil
}

func collectInstallation(_ context.Context, _ *Deps, src *supportSource) ([]supportFile, error) {
	body, err := yaml.Marshal(src.Installation)
	if err != nil {
		return nil, domain.Internal(err, "cannot render the installation")
	}
	return []supportFile{{Name: "installation.yaml", Data: body}}, nil
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
		return nil, domain.InstallationError(nil, "no release is installed, so nothing renders configuration")
	}

	rendered, err := renderConfiguration(ctx, d, src)
	if err != nil {
		return nil, err
	}

	targets := make([]string, 0, len(rendered))
	for target := range rendered {
		targets = append(targets, target)
	}
	sort.Strings(targets)

	var diffs []string
	for _, target := range targets {
		existing, _ := os.ReadFile(target)
		if diff := unifiedDiff(target, string(existing), string(rendered[target])); diff != "" {
			diffs = append(diffs, diff)
		}
	}

	body := "no drift: every configuration target matches what this release renders\n"
	if len(diffs) > 0 {
		body = strings.Join(diffs, "\n")
	}
	return []supportFile{{Name: "config-diff.txt", Data: []byte(body)}}, nil
}

// renderConfiguration renders every configuration target, read-only.
//
// The schema is release metadata rather than secret values, so loading it here
// has no side effects and reveals nothing -- the same reasoning that lets
// `apply --dry-run` show a configuration diff without executing the step that
// would normally have left the schema in state.
func renderConfiguration(ctx context.Context, d *Deps, src *supportSource) (map[string][]byte, error) {
	schema, err := release.LoadSecretSchema(src.Release)
	if err != nil {
		return nil, err
	}
	data, err := d.templateData(src.Installation, src.Release, "", schema)
	if err != nil {
		return nil, err
	}

	out := make(map[string][]byte, len(src.Release.Manifest.Configuration))
	for _, cfg := range src.Release.Manifest.Configuration {
		// Checked here as well as inside the renderer, as the apply
		// step does: this refuses the "../" spelling with a message
		// about the manifest, while os.Root refuses what only an open
		// can see.
		if _, err := src.Release.Path(cfg.Template); err != nil {
			return nil, err
		}
		body, err := d.Renderer.Render(ctx,
			ports.TemplateRef{Root: src.Release.Root, Name: cfg.Template}, data)
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

func collectManager(_ context.Context, d *Deps, _ *supportSource) ([]supportFile, error) {
	return jsonFile("manager.json", map[string]string{
		"version": d.ManagerVersion.String(),
	})
}

// supportMeta is the archive's account of itself, and is built last because it
// describes everything else.
//
// It carries the omissions as well as the entries. An archive that simply
// lacked a file would leave its reader to guess whether the component does not
// exist on that machine, failed to collect, or was never part of the format --
// three answers with different next steps.
func supportMeta(report *SupportReport, files []supportFile) supportFile {
	entries := make([]SupportEntry, 0, len(files))
	for _, f := range files {
		entries = append(entries, SupportEntry{
			Name:       f.Name,
			Title:      supportTitle(f.Name),
			Bytes:      int64(len(f.Data)),
			Redactions: f.Redactions,
		})
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
func writeSupportArchive(d *Deps, inst domain.Installation, files []supportFile, dir string) (string, error) {
	staging, err := os.MkdirTemp("", "morzer-support-")
	if err != nil {
		return "", domain.Internal(err, "cannot stage the support bundle")
	}
	// Ours, made a line ago, and it holds a copy of everything the archive
	// holds. Leaving it behind would put a second, unencrypted, unnoticed
	// copy in the system temporary directory.
	defer os.RemoveAll(staging)

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

	path := filepath.Join(dir, supportArchiveName(d, inst))
	if err := atomicfs.WriteTarZst(path, staging, names, d.now()); err != nil {
		return "", err
	}
	return path, nil
}

// archiveDir resolves where the archive goes.
//
// An empty `--dir` is the working directory, which is where an operator expects
// a file they are about to attach to something.
func archiveDir(dir string) (string, error) {
	if dir == "" {
		wd, err := os.Getwd()
		if err != nil {
			return "", domain.Internal(err, "cannot tell where to write the support bundle")
		}
		return wd, nil
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", domain.Internal(err, "cannot resolve %s", dir)
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
func supportArchiveName(d *Deps, inst domain.Installation) string {
	return fmt.Sprintf("support-%s-%s-%s.tar.zst",
		inst.Product, inst.ID, d.now().UTC().Format("20060102T150405Z"))
}
