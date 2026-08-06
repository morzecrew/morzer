package compose

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/infra/exec"
	"github.com/morzecrew/morzer/internal/ports"
)

// DefaultHelperImage is the container a volume is read and written through.
//
// busybox, pinned by digest, and pinned in the manager's own source rather than
// in a release manifest -- it is the manager's tool, not the product's, and a
// release that could name it could name anything. The same rule the manifest
// enforces on a vendor applies here: an unpinned helper would make every backup
// depend on whatever the registry served that night.
//
// busybox because the whole image is `tar`, `du` and a shell. It is the
// smallest thing an operator has to trust, cache offline, and audit.
const DefaultHelperImage = "busybox@sha256:9db7b59979c38555a39def84a31fb98b5296952f9e3afd4f6f11f05b07adfab0"

// WithHelperImage overrides the image volumes are read through.
//
// The escape hatch for the operator whose registry does not carry busybox, or
// whose air-gapped mirror carries something else: any image with a POSIX `tar`,
// `du`, `find`, `wc` and `sh` will do -- pinned by digest, like everything else
// this manager runs. `find` and `wc` are there for the measurement alone, which
// has to count a volume's entries to bound what tar's framing adds to them.
func WithHelperImage(ref string) Option {
	return func(r *Runtime) {
		// Trimmed once and *stored* trimmed. It arrives from an
		// environment variable, and a systemd `Environment=` line
		// carrying a trailing space would otherwise reach `docker run`
		// with the space attached, failing as an image nobody can find.
		//
		// Stored even when it is not a digest, and refused later in
		// requireHelper. An Option cannot report an error, and both
		// alternatives are worse than a late refusal: running the
		// default instead would mount the product's data into an image
		// the operator did not ask for, and it would do it without
		// saying so.
		if trimmed := strings.TrimSpace(ref); trimmed != "" {
			r.helperImage = trimmed
		}
	}
}

// digestPinned matches an image reference pinned by digest.
//
// The rule the manifest holds every release image to, applied to the one image
// the manager runs on its own behalf. A tag names whatever the registry served
// that night, and this image runs with the product's data mounted -- so
// `busybox:latest` means two backups a week apart can be taken by two different
// programs, with no record of which.
var digestPinned = regexp.MustCompile(`^[^\s@]+@sha256:[a-f0-9]{64}$`)

// helperImageEnv is the variable an operator sets to reach WithHelperImage.
//
// Spelled here rather than imported from the CLI that reads it, because the CLI
// imports this package. Named in the refusal all the same: an operator told
// only that "the helper image" is wrong has to go looking for which knob set
// it, and they are reading the message at 3am because a backup stopped.
const helperImageEnv = "MORZER_VOLUME_HELPER_IMAGE"

// HelperImagePinned reports whether the configured helper image is pinned by
// digest, which every volume operation requires.
//
// Exported so `doctor` can apply the same rule the capture enforces. Without
// it, an unpinned MORZER_VOLUME_HELPER_IMAGE reads as "available" right up
// until backup night, when every volume operation refuses it -- and the check
// whose whole job is to find that out beforehand said it was fine.
func (r *Runtime) HelperImagePinned() bool {
	return digestPinned.MatchString(r.HelperImage())
}

// HelperImage reports the image this runtime reads volumes through.
func (r *Runtime) HelperImage() string {
	if r.helperImage == "" {
		return DefaultHelperImage
	}
	return r.helperImage
}

var (
	_ ports.VolumeInspector = (*Runtime)(nil)
	_ ports.VolumeCapturer  = (*Runtime)(nil)
)

// configDoc is the part of `compose config --format json` this file reads.
//
// A narrow struct rather than the whole document: Compose adds fields between
// versions, and decoding into a shape that names only what is needed means a
// new field is ignored rather than breaking the parse.
type configDoc struct {
	Name     string `json:"name"`
	Services map[string]struct {
		Volumes []struct {
			Type   string `json:"type"`
			Source string `json:"source"`
			Target string `json:"target"`
		} `json:"volumes"`
	} `json:"services"`
	Volumes map[string]struct {
		Name     string `json:"name"`
		External bool   `json:"external"`
	} `json:"volumes"`
}

// Volumes reports what the project mounts.
//
// It reads the *resolved* configuration rather than the files on disk, because
// that is the only form in which a volume's real name is known: Compose
// prefixes the project name, an `external:` volume names itself, and a
// `name:` key overrides both.
func (r *Runtime) Volumes(ctx context.Context, cfg ports.RuntimeConfig) (ports.ProjectStorage, error) {
	cmd := r.command(cfg, 60*time.Second, r.args(cfg, "config", "--format", "json")...)
	cmd.OnLine = nil

	res, err := r.runner.Run(ctx, cmd)
	if err != nil {
		return ports.ProjectStorage{}, wrapExit(err, "cannot read the project's volumes",
			"run `docker compose config` against the release to see the full diagnostic")
	}
	return parseStorage(res.Stdout)
}

// parseStorage turns the merged configuration into the storage inventory.
func parseStorage(raw string) (ports.ProjectStorage, error) {
	var doc configDoc
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		return ports.ProjectStorage{}, domain.RuntimeError(err,
			"cannot parse the merged compose configuration")
	}

	// Who mounts what, accumulated across services before anything is
	// reported: a volume mounted by three services has to name all three,
	// because that is the list a restore refuses against.
	volumeUsers := map[string]map[string]bool{}
	bindUsers := map[string]map[string]bool{}
	var anonymous []ports.AnonymousVolume

	for service, spec := range doc.Services {
		for _, mount := range spec.Volumes {
			switch mount.Type {
			case "volume":
				if mount.Source == "" {
					// An anonymous volume: Compose invents a
					// name that changes when the container
					// is recreated, so there is nothing to
					// capture that could later be restored.
					// Reported rather than dropped, so the
					// operator learns their data is in no
					// backup instead of discovering it.
					anonymous = append(anonymous, ports.AnonymousVolume{
						Service: service, Target: mount.Target,
					})
					continue
				}
				addUser(volumeUsers, mount.Source, service)
			case "bind":
				addUser(bindUsers, mount.Source, service)
			default:
				// tmpfs and npipe hold nothing that outlives
				// the container, so there is nothing to back up.
			}
		}
	}

	out := ports.ProjectStorage{}

	for name, users := range volumeUsers {
		declared, ok := doc.Volumes[name]
		actual := declared.Name
		if !ok || actual == "" {
			actual = resolvedName(doc.Name, name, ok && declared.External)
		}
		out.Volumes = append(out.Volumes, ports.NamedVolume{
			Name:     name,
			Actual:   actual,
			External: declared.External,
			Services: sortedKeys(users),
		})
	}
	// A declared volume nothing mounts is still the project's storage, and
	// still holds whatever a previous release put there.
	for name, declared := range doc.Volumes {
		if _, mounted := volumeUsers[name]; mounted {
			continue
		}
		actual := declared.Name
		if actual == "" {
			actual = resolvedName(doc.Name, name, declared.External)
		}
		out.Volumes = append(out.Volumes, ports.NamedVolume{
			Name: name, Actual: actual, External: declared.External,
		})
	}

	for source, users := range bindUsers {
		out.Binds = append(out.Binds, ports.BindMount{
			Source: source, Services: sortedKeys(users),
		})
	}

	out.Anonymous = anonymous

	// Sorted, so a backup manifest and a plan read the same between runs.
	sort.Slice(out.Volumes, func(i, j int) bool { return out.Volumes[i].Name < out.Volumes[j].Name })
	sort.Slice(out.Binds, func(i, j int) bool { return out.Binds[i].Source < out.Binds[j].Source })
	sort.Slice(out.Anonymous, func(i, j int) bool {
		if out.Anonymous[i].Service != out.Anonymous[j].Service {
			return out.Anonymous[i].Service < out.Anonymous[j].Service
		}
		return out.Anonymous[i].Target < out.Anonymous[j].Target
	})

	return out, nil
}

// resolvedName is the runtime name of a volume whose resolved document does not
// spell one out.
//
// Compose's own defaults, and they differ by ownership. A volume the project
// declares is created as `project_key`; an *external* one is a volume the
// project does not own and does not rename, so its name is the key exactly as
// written. Prefixing an external volume would name something that does not
// exist -- and `docker run --volume` creates a missing volume rather than
// failing, so the backup would succeed and hold an empty tar of a volume nobody
// mounts, which is only discovered by a restore that brings back nothing.
func resolvedName(project, key string, external bool) string {
	if external {
		return key
	}
	return project + "_" + key
}

func addUser(index map[string]map[string]bool, key, service string) {
	if index[key] == nil {
		index[key] = map[string]bool{}
	}
	index[key][service] = true
}

func sortedKeys(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// CaptureVolume writes a volume's contents to destPath as an uncompressed tar.
//
// The tar arrives on the helper's stdout and is written here rather than into a
// staging directory bind-mounted into the container. Two reasons, and the
// second is the one that bites: a bind mount would put a root-owned file in a
// directory the manager may not run as root in, and the manager then cannot
// overwrite or remove it -- so the plaintext copy of somebody's uploads would
// survive the backup that encrypted it.
func (r *Runtime) CaptureVolume(ctx context.Context, cfg ports.RuntimeConfig, volume, destPath string) error {
	if err := r.requireHelper(ctx); err != nil {
		return err
	}

	// 0600 and O_EXCL: a volume tarball is the product's data in the
	// clear until the backup engine encrypts it, and it must never be
	// briefly readable by anyone else.
	out, err := os.OpenFile(destPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return domain.RuntimeError(err, "cannot create %s", destPath)
	}
	defer func() { _ = out.Close() }()

	// Short options, not GNU long ones. busybox accepts both, but the
	// escape hatch documented for an operator whose registry does not carry
	// busybox is "any image with a POSIX tar" -- and a strict POSIX tar has
	// no long options at all.
	cmd := r.helperCommand(cfg, volume, true, "tar", "-C", "/src", "-cf", "-", ".")
	cmd.Stdout = out

	if _, err := r.runner.Run(ctx, cmd); err != nil {
		_ = out.Close()
		_ = os.Remove(destPath)
		return wrapExit(err, "cannot read volume "+volume,
			"check that the Docker daemon is running and that "+shortImage(r.HelperImage())+" is available")
	}
	if err := out.Close(); err != nil {
		// Removed like every other failure here, and for the reason
		// this function writes the tar itself: a close that failed is
		// a write that did not land, so what is on disk is a truncated
		// plaintext copy of the product's data that nothing downstream
		// will encrypt or delete. A failed capture leaves nothing.
		_ = os.Remove(destPath)
		return domain.RuntimeError(err, "cannot finish writing %s", destPath)
	}
	return nil
}

// RestoreVolume replaces a volume's contents with a tar.
//
// Replaces: the volume is emptied first. A merge would leave files the backup
// does not contain beside files it does, producing a volume that matches no
// point in time -- and beside a database restored to an exact one, that is how
// an upload record without its file is made.
func (r *Runtime) RestoreVolume(ctx context.Context, cfg ports.RuntimeConfig, volume, srcPath string) error {
	if err := r.requireHelper(ctx); err != nil {
		return err
	}

	in, err := os.Open(srcPath)
	if err != nil {
		return domain.RuntimeError(err, "cannot read %s", srcPath)
	}
	defer func() { _ = in.Close() }()

	// The globs are three because a shell expands none of them into
	// nothing: `*` misses dotfiles, `.[!.]*` catches `.env` but not
	// `..config`, and `..?*` catches that without ever matching `..`
	// itself. `rm -f` makes an unmatched glob -- which the shell leaves
	// as its literal self -- a no-op rather than an error, so an empty
	// volume still succeeds. `--` keeps a file named `-rf` a file.
	//
	// `&&` and not `;`: a wipe that failed and then extracted anyway would
	// merge the backup into what was already there, which is the one thing
	// replacing exists to prevent -- and it would do it silently.
	const replace = `cd /dst && rm -rf -- ..?* .[!.]* * 2>/dev/null && exec tar -C /dst -xf -`

	cmd := r.helperCommand(cfg, volume, false, "sh", "-c", replace)
	cmd.Stdin = in

	if _, err := r.runner.Run(ctx, cmd); err != nil {
		return wrapExit(err, "cannot restore volume "+volume,
			"the volume may now be partially written; it holds what the backup "+
				"contained up to the point of failure")
	}
	return nil
}

// VolumeSize reports an upper bound on the bytes a capture of this volume
// writes.
//
// It answers one question -- will the backup fit -- and only one direction of
// wrong is dangerous. A size that reads too low passes the space check and then
// fills the disk during the copy, which happens after the services have already
// been stopped; a size that reads too high refuses a backup early, in front of
// an operator who can look at `df`. So this errs high wherever it must choose.
func (r *Runtime) VolumeSize(ctx context.Context, cfg ports.RuntimeConfig, volume string) (int64, error) {
	if err := r.requireHelper(ctx); err != nil {
		return 0, err
	}

	// Two readings, and the larger wins, because each one is wrong in a
	// different direction and the safe answer is the bigger.
	//
	// `du -sk` counts allocated blocks, which reads *small* for a sparse
	// file: an image with holes can occupy four kilobytes of blocks and tar
	// to a hundred megabytes. Apparent size fixes that and is wrong the
	// other way -- two hundred two-byte files are five kilobytes apparent
	// and eight hundred of blocks, which is nearer what tar writes.
	//
	// Getting apparent size portably is the fiddly part, and the ordering
	// below is load-bearing. GNU spells it `--apparent-size`. busybox
	// rejects that and spells it `-b`. But GNU *also* accepts `-b`, where it
	// additionally means `--block-size=1` -- so `du -skb` reports KiB on
	// busybox and **bytes** on GNU, and a form that tried `-b` first would
	// read a thousand times high on GNU and refuse every backup. Trying the
	// GNU spelling first means GNU never reaches `-b`; busybox fails it and
	// does. Verified against both.
	//
	// Neither reading says anything about tar's own framing, and that is
	// why the entries are counted too. `du` measures contents; tar writes
	// contents *plus* a header per entry, a trailer, and padding -- so a
	// volume of files that are exact multiples of the block size measures
	// perfectly and tars to more than was budgeted. A million four-kilobyte
	// files read as 4 GiB and tar to 4.5 GiB, which is the one direction
	// this must never be wrong in.
	//
	// A third traversal, and the cheapest of the three: it stats nothing
	// and reads no file contents, only the directory entries the other two
	// already walked. The exact answer is `tar | wc -c`, which reads every
	// byte of the volume a second time -- on the machine this matters for,
	// that is the difference between a measurement and a second backup.
	//
	// Counted before the sizes so the shell's exit status still comes from
	// `du -sk /src`: it is the reading no implementation can lack, and a
	// helper that failed at it must fail the command rather than report
	// whatever the optional ones managed.
	const measure = `printf 'entries %s\n' "$(find /src | wc -lc)"; ` +
		`{ du -sk --apparent-size /src 2>/dev/null || ` +
		`du -skb /src 2>/dev/null; }; du -sk /src`

	cmd := r.helperCommand(cfg, volume, true, "sh", "-c", measure)
	cmd.CaptureOutput = true
	cmd.Timeout = 30 * time.Minute
	cmd.OnLine = nil

	res, err := r.runner.Run(ctx, cmd)
	if err != nil {
		// The only failure here the space check may proceed past, and it
		// is marked at the one place that knows the measurement never
		// ran. Everything below this line ran it and could not read the
		// answer, which is a property of the helper rather than of the
		// attempt, and refuses.
		return 0, wrapExit(measureIncomplete(err), "cannot measure volume "+volume, "")
	}
	return volumeBound(volume, res.Stdout)
}

// measureIncomplete marks a failure to run the measurement at all.
//
// An interruption is left unmarked: a cancelled backup is not a volume that
// cannot be measured, and letting the space check step past it would have the
// check answering on behalf of an operation that is already over.
func measureIncomplete(err error) error {
	if domain.AsError(err).Code == domain.CodeInterrupted {
		return err
	}
	return fmt.Errorf("%w: %w", domain.ErrMeasureIncomplete, err)
}

// volumeBound is the helper's output read as the upper bound the port promises:
// the larger of the two content readings, plus the framing tar wraps it in.
//
// Two errors, and the content one is reported first. A helper whose `du` printed
// a diagnostic instead of a number has a problem worth naming, and the missing
// entry count is a consequence of it rather than the fault.
func volumeBound(volume, stdout string) (int64, error) {
	sizes, framingLine := splitFraming(stdout)

	contents, err := largestSize(volume, sizes)
	if err != nil {
		return 0, err
	}
	framing, err := tarFraming(volume, framingLine)
	if err != nil {
		return 0, err
	}
	return saturatingAdd(contents, framing), nil
}

// framingMarker labels the entry-count line so it cannot be mistaken for a size.
//
// largestSize takes the largest number it is shown, and an entry count is not a
// KiB figure: a volume of ten million files would be read as ten gigabytes of
// contents. The label keeps the two vocabularies apart in the one stream the
// helper has to say both things on.
const framingMarker = "entries"

// splitFraming separates the labelled entry-count line from the size readings.
func splitFraming(stdout string) (sizes, framing string) {
	var kept []string
	for _, line := range strings.Split(stdout, "\n") {
		if fields := strings.Fields(line); len(fields) > 0 && fields[0] == framingMarker {
			framing = line
			continue
		}
		kept = append(kept, line)
	}
	return strings.Join(kept, "\n"), framing
}

// The framing tar wraps a volume's contents in, which no `du` reading accounts
// for. Each figure is a worst case, because the whole number is one:
//
//   - per entry, a 512-byte header; up to 511 bytes padding the last block of
//     its contents (`du --apparent-size` counts the bytes, tar rounds them up);
//     and, for a path over 100 bytes, GNU tar's long-name pseudo-entry -- its
//     own 512-byte header and the path padded to another 512. The path itself
//     is counted separately, from the same `find`, since it has no fixed size.
//   - per archive, a 1024-byte zero trailer and the padding out to the blocking
//     factor, 20 blocks in both GNU and busybox tar.
//
// Roughly four times what an ordinary volume of short-named, unsparse files
// actually costs. That is the direction to be wrong in: `du`'s block reading
// already rounds every small file up to a filesystem block, so this figure was
// never tight -- and the alternative to a worst case is a bound that holds for
// the volumes somebody thought of.
const (
	tarEntryFraming   = 2048
	tarArchiveFraming = 1024 + 10240
)

// tarFraming reads the entry count and the bytes their paths occupy, and
// returns what tar will add on top of the contents.
func tarFraming(volume, line string) (int64, error) {
	fields := strings.Fields(line)
	if len(fields) < 3 {
		return 0, domain.RuntimeError(nil,
			"cannot measure volume %s: the helper did not report how many entries "+
				"it holds, so what `tar` adds on top of them cannot be bounded",
			volume).
			WithHint("the volume helper image needs `find` and `wc` beside `du`, `tar` " +
				"and `sh`; a measurement that only counted the contents would let " +
				"a backup start that does not fit")
	}

	entries, entriesErr := strconv.ParseInt(fields[1], 10, 64)
	pathBytes, pathErr := strconv.ParseInt(fields[2], 10, 64)
	if entriesErr != nil || pathErr != nil || entries < 0 || pathBytes < 0 {
		return 0, domain.RuntimeError(nil,
			"cannot measure volume %s: %q is not an entry count", volume, strings.TrimSpace(line))
	}

	// Zero is not a small volume, it is a missing `find`.
	//
	// The count includes /src itself, so a helper that can walk the volume
	// reports at least one entry even when the volume is empty. A helper
	// that cannot reports nothing, `wc` counts the nothing, and the line
	// parses perfectly as `entries 0 0` -- leaving the bound at the bare
	// per-archive figure and quietly back to measuring contents alone,
	// which is the failure this whole reading exists to remove. Silent and
	// in the dangerous direction is the worst pair available, so it is
	// spelled out as the same missing tool the count above names.
	if entries < 1 {
		return 0, domain.RuntimeError(nil,
			"cannot measure volume %s: the helper reported no entries at all, "+
				"not even the mount point, so it cannot be walking the volume",
			volume).
			WithHint("the volume helper image needs `find` and `wc` beside `du`, `tar` " +
				"and `sh`; a measurement that only counted the contents would let " +
				"a backup start that does not fit")
	}

	// Saturating, not wrapping, and deliberately not a refusal. A sum that
	// wrapped would come out negative and read as *smaller* than the free
	// space; a refusal would be no better, because the space check treats a
	// volume it cannot measure as one it need not check -- so the honest
	// answer to "more bytes than a byte count holds" is the largest one
	// there is, which no disk satisfies.
	return saturatingAdd(saturatingMul(entries, tarEntryFraming),
		saturatingAdd(pathBytes, tarArchiveFraming)), nil
}

// saturatingAdd and saturatingMul stop at MaxInt64 rather than wrapping. Both
// take non-negative arguments only; every caller has already refused a negative.
func saturatingAdd(a, b int64) int64 {
	if a > math.MaxInt64-b {
		return math.MaxInt64
	}
	return a + b
}

func saturatingMul(a, b int64) int64 {
	if b != 0 && a > math.MaxInt64/b {
		return math.MaxInt64
	}
	return a * b
}

// maxMeasurableKiB is the largest KiB figure that still converts to bytes.
//
// The port promises bytes, and the conversion is a multiplication by 1024: a
// figure past this wraps to a negative number, which every space check reads as
// *smaller* than the free space and lets through. A refusal naming the number is
// the only safe reading of a size nothing can express.
const maxMeasurableKiB = math.MaxInt64 / 1024

// unreadableSize refuses a `du` reading, carrying the one remedy every such
// refusal shares.
//
// A hint rather than a bare diagnosis, because these now stop a backup instead
// of quietly disabling the space check: an operator told only that a number
// could not be read has nothing to act on, and the thing to act on is always
// the same -- the image the volume is measured through.
func unreadableSize(format string, args ...any) error {
	return domain.RuntimeError(nil, format, args...).
		WithHint("volumes are measured with `du` inside the helper image -- the one "+
			"%s names, or the pinned busybox this manager ships with when it is "+
			"unset -- and its output has to be a plain KiB figure. Nothing was "+
			"written and nothing was stopped", helperImageEnv)
}

// largestSize turns du's output into bytes, taking the largest size it reported.
func largestSize(volume, stdout string) (int64, error) {
	largest := int64(-1)
	var firstField string

	for _, line := range strings.Split(stdout, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if firstField == "" {
			firstField = fields[0]
		}
		// A line that is not a measurement is skipped rather than
		// refused: the first `du` may have printed a diagnostic on its
		// way to failing, and the measurement that matters is behind
		// it. Nothing parsing at all is still an error below.
		kib, err := strconv.ParseInt(fields[0], 10, 64)
		if err != nil {
			continue
		}
		if kib < 0 {
			return 0, unreadableSize(
				"cannot measure volume %s: du reported %d KiB, and a volume "+
					"cannot hold a negative number of bytes", volume, kib)
		}
		if kib > maxMeasurableKiB {
			return 0, unreadableSize(
				"cannot measure volume %s: du reported %d KiB, which is more "+
					"than a byte count can hold", volume, kib)
		}
		if kib > largest {
			largest = kib
		}
	}

	if firstField == "" {
		return 0, unreadableSize("cannot measure volume %s: no output from du", volume)
	}
	if largest < 0 {
		// Zero would pass every space check in silence, which is the
		// one answer worse than refusing.
		return 0, unreadableSize(
			"cannot measure volume %s: %q is not a size", volume, firstField)
	}
	return largest * 1024, nil
}

// helperCommand builds a `docker run` that mounts one volume.
func (r *Runtime) helperCommand(
	cfg ports.RuntimeConfig, volume string, readOnly bool, argv ...string,
) exec.Command {
	mount := volume + ":/dst"
	if readOnly {
		// Read-only on the source, so a helper that misbehaves cannot
		// write into the product's data. The whole reason this runs in
		// a container is that the manager should not need to be able to
		// touch the volume directly; a writable mount would give that
		// back.
		mount = volume + ":/src:ro"
	}

	full := append([]string{
		r.docker, "run", "--rm", "--interactive",
		// The helper reads a volume and writes a tar. It has no reason
		// to reach a network, and taking it away means a compromised
		// helper image cannot exfiltrate what it was given to copy.
		"--network", "none",
		"--volume", mount,
		r.HelperImage(),
	}, argv...)

	// Zero timeout: the caller's context governs. A hundred-gigabyte
	// volume takes as long as it takes, and a fixed limit here would be a
	// limit on how large a volume the manager can back up.
	cmd := r.command(cfg, 0, full...)
	cmd.CaptureOutput = false
	return cmd
}

// requireHelper refuses before running anything when the image cannot be
// trusted to be the same image next time, or is not on this machine.
//
// The second is why it exists: the alternative is `docker run` trying to pull
// it, which on the machine this matters for -- air-gapped, or backing up at 3am
// on a flaky link -- produces a registry error in the middle of a backup
// instead of a sentence naming the one command that fixes it. It is also the
// last point at which an override that was never a digest can be caught, since
// the Option that took it could not refuse.
func (r *Runtime) requireHelper(ctx context.Context) error {
	ref := r.HelperImage()

	// Before asking whether the image is here, because the dangerous case
	// is the one where it *is*: a tag that resolves locally runs, and runs
	// whatever the last `docker pull` put under that name. The refusal is
	// deliberate -- quietly using the default instead would take the backup
	// through an image the operator did not choose, and the operator would
	// go on believing their own was in use.
	if !digestPinned.MatchString(ref) {
		return domain.ValidationError(nil,
			"the volume helper image %q is not pinned by digest", ref).
			WithHint("volumes are read through this image with the product's "+
				"data mounted, so it must name a digest: set %s to a "+
				"`name@sha256:...` reference. `docker image inspect "+
				"--format '{{index .RepoDigests 0}}' %s` prints the digest "+
				"of the tag you have; leaving the variable unset uses the "+
				"pinned busybox this manager ships with",
				helperImageEnv, ref)
	}

	present, err := r.HasImage(ctx, ref)
	if err != nil {
		return err
	}
	if !present {
		return domain.RuntimeError(domain.ErrToolMissing,
			"the volume helper image %s is not on this machine", shortImage(ref)).
			WithHint("run `docker pull %s` while this machine has a network; "+
				"volumes are read through a container, so a backup cannot "+
				"capture them without it", ref)
	}
	return nil
}
