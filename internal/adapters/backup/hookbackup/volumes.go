package hookbackup

import (
	"context"
	"fmt"
	"math"
	"path/filepath"
	"sort"
	"strings"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/infra/agecrypt"
	"github.com/morzecrew/morzer/internal/infra/atomicfs"
	"github.com/morzecrew/morzer/internal/ports"
)

// VolumeDir is the subdirectory volume tarballs live under inside a backup.
//
// A directory rather than a flat name, so a volume called `database` cannot
// collide with the hook's `database.sql`, and so an operator listing a backup
// can see at a glance which parts the manager took and which the product did.
const VolumeDir = "volumes"

// plannedVolume is one volume the backup decided to capture, and how.
type plannedVolume struct {
	volume      ports.NamedVolume
	consistency ports.Consistency
}

// volumePlan is what a backup decided to do about the project's storage,
// before it does any of it.
//
// Deciding first and acting second is what makes the space check possible --
// the total is knowable before a byte is written -- and what lets the manifest
// record the volumes that were deliberately skipped alongside the ones that
// were taken.
type volumePlan struct {
	capture    []plannedVolume
	uncaptured []ports.UncapturedVolume
}

// hasCold reports whether anything in the plan needs its writers stopped.
func (p volumePlan) hasCold() bool {
	for _, v := range p.capture {
		if v.consistency == ports.ConsistencyCold {
			return true
		}
	}
	return false
}

// quiesceServices is every service that must be stopped, deduplicated across
// the cold volumes and sorted.
//
// The union, stopped once, rather than a stop-and-start per volume: the total
// downtime is the same either way, and one window is less disruptive than five
// -- and far less confusing to somebody watching the deployment while it
// happens.
func (p volumePlan) quiesceServices() []string {
	set := map[string]bool{}
	for _, v := range p.capture {
		if v.consistency != ports.ConsistencyCold {
			continue
		}
		for _, s := range v.volume.Services {
			set[s] = true
		}
	}
	out := make([]string, 0, len(set))
	for s := range set {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// planVolumes decides what to capture and what to leave, and why.
//
// The default is cold, and that is the whole safety argument. A volume the
// vendor has not classified is one the manager knows nothing about, and reading
// an unknown volume live produces a crash-consistent copy -- which for anything
// with a write-ahead log or a rebuilt index is a copy that restores nine times
// out of ten. The safe default is the slow one.
func planVolumes(
	storage ports.ProjectStorage, spec domain.BackupSpec, allowDowntime bool,
) volumePlan {
	var plan volumePlan

	for _, vol := range storage.Volumes {
		switch spec.Consistency(vol.Name) {
		case domain.VolumeExclude:
			plan.uncaptured = append(plan.uncaptured, ports.UncapturedVolume{
				Volume:   vol.Name,
				Kind:     ports.VolumeKindNamed,
				Services: vol.Services,
				Reason: "the release declares `consistency: exclude` for it, " +
					"so its data belongs to the backup hook",
			})

		case domain.VolumeHot:
			plan.capture = append(plan.capture, plannedVolume{
				volume: vol, consistency: ports.ConsistencyHot,
			})

		default:
			// Cold. Stopping a service is the one thing a backup does
			// that an operator can feel, so an operator who has said
			// not to gets a named omission rather than a silent
			// downgrade to a hot copy. A hot copy of an undeclared
			// volume is precisely the thing nobody may take on the
			// vendor's behalf.
			if !allowDowntime {
				plan.uncaptured = append(plan.uncaptured, ports.UncapturedVolume{
					Volume:   vol.Name,
					Kind:     ports.VolumeKindNamed,
					Services: vol.Services,
					Reason: "it is undeclared, so it can only be captured with " +
						"its services stopped, and this backup was told not to " +
						"stop anything",
				})
				continue
			}
			plan.capture = append(plan.capture, plannedVolume{
				volume: vol, consistency: ports.ConsistencyCold,
			})
		}
	}

	// A bind mount is an arbitrary host path: it can be `/`, it can be a
	// network mount, it can be shared with something the manager knows
	// nothing about. Capturing one means the manager deciding how much of
	// somebody's filesystem to copy. Reported so an operator is not
	// silently short a volume they thought was covered.
	for _, bind := range storage.Binds {
		plan.uncaptured = append(plan.uncaptured, ports.UncapturedVolume{
			Volume:   bind.Source,
			Kind:     ports.VolumeKindBind,
			Services: bind.Services,
			Reason: "it is a bind mount to a host path, which the manager " +
				"never captures",
		})
	}

	// An anonymous volume holds real data and cannot be put back: the
	// runtime invents a name that changes when the container is recreated,
	// so a restore would have nowhere to write it. Recorded anyway, because
	// the operator's remedy -- ask the vendor to name it -- only exists if
	// they know.
	for _, anon := range storage.Anonymous {
		plan.uncaptured = append(plan.uncaptured, ports.UncapturedVolume{
			Volume:   anon.Target,
			Kind:     ports.VolumeKindAnonymous,
			Services: []string{anon.Service},
			Reason: "it is an anonymous volume, which is renamed whenever its " +
				"container is recreated, so no restore could put it back",
		})
	}

	return plan
}

// checkVolumeNames refuses a volume whose name would not stay inside the backup
// directory when it becomes a filename.
//
// A volume name is release-supplied: it comes out of a Compose file somebody
// else wrote. Compose is unlikely to accept `../../etc/cron.d/x` as a volume
// key, but "the other tool probably rejects it" is not a containment argument,
// and this is the one place a release's own string becomes a path the manager
// writes to. The same rule `recordArtifacts` applies to a hook's artifacts.
func checkVolumeNames(storage ports.ProjectStorage) error {
	for _, vol := range storage.Volumes {
		if safeVolumeName(vol.Name) {
			continue
		}
		return domain.BackupError(domain.ErrPathEscape,
			"the release declares a volume named %q, which is not a usable file name",
			vol.Name).
			WithHint("volume names become file names inside the backup, so they must " +
				"not contain a path separator")
	}
	return nil
}

// safeVolumeName reports whether a name is a single, ordinary path element.
func safeVolumeName(name string) bool {
	if name == "" || name == "." || name == ".." {
		return false
	}
	if strings.ContainsRune(name, filepath.Separator) || strings.Contains(name, "/") {
		return false
	}
	// Cleaning must be a no-op: anything it changes was not a plain name.
	return filepath.Clean(name) == name
}

// volumeCapabilities is the pair of optional capabilities volume capture needs.
type volumeCapabilities struct {
	inspector ports.VolumeInspector
	capturer  ports.VolumeCapturer
}

// volumeSupport reports whether this engine can read volumes at all.
//
// Both capabilities or neither: a runtime that can enumerate volumes but not
// read them could produce a manifest full of volumes it did not capture, which
// reads exactly like a backup that covered them.
func (e *Engine) volumeSupport() (volumeCapabilities, bool) {
	if e.runtime == nil {
		return volumeCapabilities{}, false
	}
	inspector, okI := e.runtime.(ports.VolumeInspector)
	capturer, okC := e.runtime.(ports.VolumeCapturer)
	if !okI || !okC {
		return volumeCapabilities{}, false
	}
	return volumeCapabilities{inspector: inspector, capturer: capturer}, true
}

// volumeCapture is a decided plan together with the capabilities that will
// carry it out.
//
// The pair exists because deciding and copying happen at different moments. The
// decision -- what to take, and whether it fits -- has to come before the
// release's backup hook writes its first byte, since the hook's own artifacts
// land on the disk the space check is about. The copy has to come after it,
// because a cold capture stops services and the hook needs the stack up.
type volumeCapture struct {
	caps volumeCapabilities
	plan volumePlan

	// space is what the planned copies need, measured before the hook ran.
	// Kept so it can be checked again afterwards against the space the hook
	// left behind -- and against the size of what the hook left there.
	space volumeSpace
}

// volumeSpace is what the planned volume copies will need, kept as the numbers
// the requirement is built from rather than as their sum.
//
// The sum is a high-water mark, not a total: encryptComponents writes each
// ciphertext beside its plaintext and removes the plaintext afterwards, one
// component at a time, so the peak is everything plus one more copy of the
// largest *component*. Collapsing it to `total + largest volume` before the hook
// has run assumes the largest component is a volume, and the hook's database
// dump is a component too -- routinely the biggest one in the backup. Keeping
// the parts lets the check after the hook name the real largest.
type volumeSpace struct {
	total   int64
	largest int64

	// overflowed records that the volumes could not be summed, rather than
	// letting a wrapped total be carried forward as a number.
	overflowed bool

	// measured separates "the volumes need nothing" from "the manager could
	// not measure them". Only the first may lower a reservation: a backup
	// refused because a volume could not be sized would be refused for a
	// reason that has nothing to do with whether it fits.
	measured bool
}

// required is the free space a capture still needs, given the largest single
// component already on disk that encryption will have to duplicate.
//
// Saturating, like the sum it is built from: a requirement no disk can satisfy
// must compare as larger than the free space, never as negative.
func (s volumeSpace) required(largestWritten int64) int64 {
	if !s.measured {
		return 0
	}
	largest := max(s.largest, largestWritten)
	if s.overflowed || s.total > math.MaxInt64-largest {
		return math.MaxInt64
	}
	return s.total + largest
}

// planVolumeCapture reads the project's storage, decides what to capture, and
// refuses a backup that will not fit -- without writing or stopping anything.
func (e *Engine) planVolumeCapture(ctx context.Context) (volumeCapture, error) {
	caps, ok := e.volumeSupport()
	if !ok {
		return volumeCapture{}, nil
	}

	storage, err := caps.inspector.Volumes(ctx, e.runtimeConfig)
	if err != nil {
		return volumeCapture{}, err
	}
	if err := checkVolumeNames(storage); err != nil {
		return volumeCapture{}, err
	}

	plan := planVolumes(storage, e.release.Manifest.Backup, e.allowDowntime)
	if len(plan.capture) == 0 {
		return volumeCapture{caps: caps, plan: plan}, nil
	}

	space, err := e.checkVolumeSpace(ctx, caps.capturer, plan)
	if err != nil {
		return volumeCapture{}, err
	}
	return volumeCapture{caps: caps, plan: plan, space: space}, nil
}

// captureVolumes copies the planned volumes into the backup directory.
//
// Returns the component records and the storage it deliberately did not take.
// Everything it writes is plaintext at this point; the caller encrypts it with
// everything else, so a volume tarball is protected by exactly the same
// mechanism as the database dump beside it.
//
// largestWritten is the biggest component already in the backup directory --
// the hook's dump, in practice. It is the caller's to supply because only the
// caller knows what was recorded, and it belongs in the space check because
// encryption duplicates it.
func (e *Engine) captureVolumes(
	ctx context.Context, dir string, capture volumeCapture, largestWritten int64,
) (records []ports.ComponentRecord, uncaptured []ports.UncapturedVolume, err error) {
	caps, plan := capture.caps, capture.plan
	if len(plan.capture) == 0 {
		return nil, plan.uncaptured, nil
	}

	// Measured before the hook ran, checked again now that it has.
	//
	// The pre-hook gate can only reserve what it can measure, and the size
	// of a database dump is not knowable until the hook has written it --
	// so a backup can pass a gate that said "the volumes fit" and then meet
	// a disk the hook has since filled. Re-reading free space costs one
	// statfs and no re-measuring, and it is the last moment before the part
	// that stops services and writes gigabytes.
	if err := e.recheckVolumeSpace(capture.space, largestWritten); err != nil {
		return nil, nil, err
	}

	if err := atomicfs.MkdirAll(filepath.Join(dir, VolumeDir), 0o700); err != nil {
		return nil, nil, err
	}

	// Hot volumes first, outside the downtime window. There is no reason
	// for a volume the vendor said may be read live to be read while the
	// product is down.
	for _, planned := range plan.capture {
		if planned.consistency != ports.ConsistencyHot {
			continue
		}
		rec, err := e.captureOne(ctx, caps.capturer, dir, planned)
		if err != nil {
			return nil, nil, err
		}
		records = append(records, rec)
	}

	if plan.hasCold() {
		cold, err := e.captureCold(ctx, caps.capturer, dir, plan)
		if err != nil {
			return nil, nil, err
		}
		records = append(records, cold...)
	}

	return records, plan.uncaptured, nil
}

// captureCold stops the writers, copies, and starts them again.
//
// The restart is deferred and unconditional. A backup that failed partway
// through and left the product stopped would have turned a routine nightly job
// into an outage, and the operator would find out from their users.
func (e *Engine) captureCold(
	ctx context.Context, capturer ports.VolumeCapturer, dir string, plan volumePlan,
) (records []ports.ComponentRecord, err error) {
	// Only the services that are actually up.
	//
	// A service that is already stopped needs no stopping, and starting it
	// afterwards would have a backup starting a product its operator had
	// deliberately taken down. Worse, `compose start` on a service with no
	// container at all fails outright -- so a backup taken before
	// maintenance, of a deployment that was already down, captured its
	// volumes perfectly and then deleted them while reporting that it could
	// not start what was never running.
	services, err := e.quiesceAmong(ctx, plan.quiesceServices())
	if err != nil {
		return nil, err
	}

	if len(services) > 0 {
		if stopErr := e.runtime.Stop(ctx, e.runtimeConfig, services, e.stopTimeout()); stopErr != nil {
			return nil, domain.BackupError(stopErr,
				"cannot stop %s to read its volumes", joinServices(services)).
				WithHint("the services are still running and no volume was captured; " +
					"declare `consistency: hot` in the release manifest for volumes " +
					"that may be read live")
		}

		defer func() {
			// A detached context, because the commonest reason a
			// capture failed is that this one was cancelled -- and
			// starting the product back up on a dead context fails
			// instantly, leaving it stopped. Bounded, so a runtime
			// that has stopped answering cannot hold a failed
			// backup open indefinitely.
			resumeCtx, cancel := detach(ctx, resumeTimeout)
			defer cancel()

			if startErr := e.runtime.Start(resumeCtx, e.runtimeConfig, services); startErr != nil {
				// Reported, and it outranks whatever else went
				// wrong: a failed backup is a problem for
				// tomorrow, and a product that is still down is
				// a problem right now.
				err = domain.BackupError(startErr,
					"the backup stopped %s to read a volume and could not start them again",
					joinServices(services)).
					WithHint("the deployment is down; run `morzer apply` to bring it back up")
			}
		}()
	}

	for _, planned := range plan.capture {
		if planned.consistency != ports.ConsistencyCold {
			continue
		}
		rec, captureErr := e.captureOne(ctx, capturer, dir, planned)
		if captureErr != nil {
			return nil, captureErr
		}
		records = append(records, rec)
	}
	return records, nil
}

// captureOne reads a single volume and records it.
func (e *Engine) captureOne(
	ctx context.Context, capturer ports.VolumeCapturer, dir string, planned plannedVolume,
) (ports.ComponentRecord, error) {
	rel := filepath.Join(VolumeDir, planned.volume.Name+".tar")
	path := filepath.Join(dir, rel)

	if err := capturer.CaptureVolume(ctx, e.runtimeConfig, planned.volume.Actual, path); err != nil {
		// The cause's remedy is kept: the commonest one is "pull the
		// helper image", and an operator who loses that sentence to a
		// wrap is left with a diagnosis and nothing to do about it.
		return ports.ComponentRecord{}, domain.BackupError(err,
			"cannot capture volume %s", planned.volume.Name).WithHintFrom(err)
	}

	size, err := fileSize(path)
	if err != nil {
		return ports.ComponentRecord{}, err
	}
	sum, err := atomicfs.DigestFile(path)
	if err != nil {
		return ports.ComponentRecord{}, err
	}

	return ports.ComponentRecord{
		Component: ports.ComponentVolumes,
		Path:      rel,
		Size:      size,
		SHA256:    sum,
		Volume: &ports.VolumeRecord{
			Volume:      planned.volume.Name,
			Actual:      planned.volume.Actual,
			Services:    planned.volume.Services,
			Consistency: planned.consistency,
		},
	}, nil
}

// checkVolumeSpace refuses a backup that will not fit, before anything is
// written.
//
// "No space left on device" halfway through a hundred-gigabyte copy is a worse
// message than a refusal naming both numbers, and it arrives after the product
// has already been stopped.
func (e *Engine) checkVolumeSpace(
	ctx context.Context, capturer ports.VolumeCapturer, plan volumePlan,
) (volumeSpace, error) {
	var space volumeSpace
	for _, planned := range plan.capture {
		size, err := capturer.VolumeSize(ctx, e.runtimeConfig, planned.volume.Actual)
		if err != nil {
			// Not fatal. A backup refused because the manager could
			// not measure a volume would be a backup refused for a
			// reason that has nothing to do with whether it fits,
			// and the copy itself will fail honestly if it does not.
			return volumeSpace{}, nil
		}
		if size < 0 || space.total > math.MaxInt64-size {
			// A total that wrapped would come out negative and
			// compare as *smaller* than the free space, turning a
			// refusal into a pass -- the one direction this check
			// must never fail in.
			space.overflowed = true
			break
		}
		space.total += size
		space.largest = max(space.largest, size)
	}
	if space.total == 0 && !space.overflowed {
		return volumeSpace{}, nil
	}
	space.measured = true

	free, err := e.freeSpace(e.paths.BackupsDir())
	if err != nil {
		return volumeSpace{}, nil
	}

	// Nothing has been written yet, so the largest component that will be
	// duplicated is the largest volume. What the hook is about to write is
	// not sizeable from here -- measuring a database dump means taking it --
	// and is why this requirement is checked again once it exists.
	required := space.required(0)
	if free >= required {
		return space, nil
	}

	return space, domain.BackupError(nil,
		"this backup needs about %s for the project's volumes and %s is free on %s",
		domain.ByteSize(required), domain.ByteSize(free), e.paths.BackupsDir()).
		WithHint("prune old backups (`morzer backup list`), give %s more room, or "+
			"exclude a volume in the release manifest -- nothing has been "+
			"written and nothing has been stopped",
			e.paths.BackupsDir())
}

// recheckVolumeSpace re-verifies the requirement against the space that is
// actually left, now that the hook's own output is on the disk.
//
// Two things change between the two checks, and the second is the one that is
// easy to miss. The dump's bytes are gone from the free figure, which is what
// re-reading it is for. But the dump has also *become* a component, and
// encryption duplicates the largest one: a dump bigger than every volume, which
// is the ordinary case, moves the high-water mark by the difference. Reserving
// the largest volume there would leave that difference unclaimed and meet ENOSPC
// during encryption -- after the copy, and after the downtime the copy cost.
//
// Unmeasurable free space is not fatal here for the same reason it is not in
// checkVolumeSpace: a backup refused because the manager could not read `df` is
// refused for a reason that has nothing to do with whether it fits.
func (e *Engine) recheckVolumeSpace(space volumeSpace, largestWritten int64) error {
	required := space.required(largestWritten)
	if required <= 0 {
		return nil
	}
	free, err := e.freeSpace(e.paths.BackupsDir())
	if err != nil || free >= required {
		return nil
	}

	return domain.BackupError(nil,
		"this backup needs about %s more and only %s is free on %s now that the "+
			"backup hook has written its own output",
		domain.ByteSize(required), domain.ByteSize(free), e.paths.BackupsDir()).
		WithHint("the volumes fit when this backup started; what the hook wrote used " +
			"the room they needed. The figure covers the volumes plus one more " +
			"copy of the largest component, which encryption writes beside the " +
			"original before removing it. Prune old backups, give the directory " +
			"more space, or exclude a volume in the release manifest -- nothing " +
			"has been stopped and no volume has been copied.")
}

// restoreVolumes writes the backup's volume tarballs back.
//
// Volumes before the hook, because this is the failure worth having first: a
// volume that will not restore stops the operation while the database is still
// the one that was there, and a database the hook has begun overwriting cannot
// be put back by anything.
func (e *Engine) restoreVolumes(
	ctx context.Context, staged string, manifest ports.BackupManifest, opts ports.RestoreOptions,
) error {
	volumes := manifest.VolumeRecords()
	if len(volumes) == 0 {
		return nil
	}
	if !componentSelected(opts.Components, ports.ComponentVolumes) {
		return nil
	}

	caps, ok := e.volumeSupport()
	if !ok {
		return domain.BackupError(domain.ErrUnsupported,
			"this backup contains %d volume(s) but the configured runtime cannot write them",
			len(volumes)).
			WithHint("restore the rest with `--component database,config,secrets`, " +
				"and put the volumes back by hand")
	}

	// The project as it is *now*, not as the backup recorded it. Two things
	// depend on it: which volume to write into, and which services must be
	// out of the way first.
	live, err := e.currentVolumes(ctx, caps.inspector)
	if err != nil {
		return err
	}

	if err := e.refuseOccupiedVolumes(ctx, volumes, live); err != nil {
		return err
	}

	for _, c := range volumes {
		target := c.Volume.Actual
		if v, ok := live[c.Volume.Volume]; ok {
			target = v.Actual
		}

		path := filepath.Join(staged, strings.TrimSuffix(c.Path, agecrypt.Extension))
		if err := caps.capturer.RestoreVolume(ctx, e.runtimeConfig, target, path); err != nil {
			return domain.BackupError(err,
				"cannot restore volume %s", c.Volume.Volume).WithHintFrom(err)
		}
	}
	return nil
}

// refuseOccupiedVolumes is decision 6: a volume is never written while a
// container has it open.
//
// Untarring into a volume a container is reading is how a restore corrupts the
// thing it was restoring, and the container would then be holding file handles
// to files that no longer exist. The refusal names the services and the state
// each is in, because "stop the services" is not an instruction anybody can
// follow -- and because a paused one does not look stopped or running.
func (e *Engine) refuseOccupiedVolumes(
	ctx context.Context, volumes []ports.ComponentRecord, live map[string]ports.NamedVolume,
) error {
	occupied, err := e.occupiedServices(ctx)
	if err != nil {
		return err
	}

	// Collected across every volume before reporting, so an operator stops
	// everything once rather than discovering the next name after each
	// retry.
	blockers := map[string][]string{}
	for _, c := range volumes {
		for _, service := range mountingServices(c, live) {
			if _, up := occupied[service]; up {
				blockers[service] = append(blockers[service], c.Volume.Volume)
			}
		}
	}
	if len(blockers) == 0 {
		return nil
	}

	names := make([]string, 0, len(blockers))
	for service := range blockers {
		names = append(names, service)
	}
	sort.Strings(names)

	details := make([]string, 0, len(names))
	for _, service := range names {
		mounted := blockers[service]
		sort.Strings(mounted)
		details = append(details, fmt.Sprintf("%s is %s (%s)",
			service, occupied[service].State, strings.Join(mounted, ", ")))
	}

	return domain.BackupError(nil,
		"cannot restore a volume while a service that mounts it still holds it open: %s",
		strings.Join(details, ", ")).
		WithHint("stop them first -- `morzer restore` does this for you, so this "+
			"message means %s was still up when the volume was about to be "+
			"written", joinServices(names))
}

// occupiedServices maps each service that may still hold a volume open to the
// state the runtime reports for it.
//
// The state is carried rather than discarded because the refusal quotes it: an
// operator told to stop a service that is already paused has been given the
// wrong instruction.
//
// A failure is an error rather than an empty map, and both callers depend on
// that: one is deciding what to stop before writing, the other is deciding
// whether writing is safe at all, and "cannot tell" answered as "nothing is
// running" would let both proceed against a live product.
func (e *Engine) occupiedServices(ctx context.Context) (map[string]ports.ServiceState, error) {
	states, err := e.runtime.Status(ctx, e.runtimeConfig)
	if err != nil {
		return nil, domain.BackupError(err,
			"cannot tell which services are running, so a volume cannot be read or "+
				"written safely")
	}

	occupied := make(map[string]ports.ServiceState, len(states))
	for _, s := range states {
		if s.OccupiesVolume() {
			occupied[s.Name] = s
		}
	}
	return occupied, nil
}

// quiesceAmong narrows a list of services to those that must be stopped for a
// capture and can be started again, preserving order -- and refuses when one of
// them holds a volume open and cannot be stopped.
//
// A paused service is included, because it holds the volume open and must be
// out of the way before it is read. It comes back *running* rather than paused,
// which is a change to how the operator left the deployment -- and the lesser
// evil beside recording a copy as `cold` that was taken while a container had
// the volume open mid-write.
//
// A service the runtime reports as `removing`, or in any state this manager has
// never seen, is the same evil arrived at by omission. It occupies the volume
// (ports.ServiceState.OccupiesVolume counts an unknown state as occupied) but
// cannot be quiesced (ports.ServiceState.Quiescible does not), and dropping it
// from the list left the capture reading a volume a container still had open
// while the manifest called the result `cold`. That is the one claim the whole
// component rests on, so this refuses instead, naming the service and the state
// it is in -- `removing` is transient and the remedy is to wait, which nobody
// can act on without being told which container it is.
func (e *Engine) quiesceAmong(ctx context.Context, services []string) ([]string, error) {
	if len(services) == 0 {
		return nil, nil
	}
	occupied, err := e.occupiedServices(ctx)
	if err != nil {
		return nil, err
	}

	out := make([]string, 0, len(services))
	var stuck []string
	for _, s := range services {
		state, up := occupied[s]
		switch {
		case !up:
			// Already down: nothing to stop, and nothing holding
			// the volume.
		case !state.Quiescible():
			stuck = append(stuck, fmt.Sprintf("%s is %s", s, state.State))
		default:
			out = append(out, s)
		}
	}

	if len(stuck) > 0 {
		return nil, domain.BackupError(nil,
			"cannot take a cold copy while a service that holds the volume open "+
				"cannot be stopped: %s", strings.Join(stuck, ", ")).
			WithHint("nothing was stopped and the backup was abandoned; `removing` " +
				"is transient, so wait for the container to finish going away and " +
				"run the backup again -- or declare `consistency: hot` in the " +
				"release manifest for the volumes that may be read live")
	}
	return out, nil
}

// currentVolumes maps each logical volume name to the project's current view of
// it.
//
// An error rather than an empty map, and it is the same reasoning as
// occupiedServices: "cannot tell" answered as "nothing here" lets a restore
// proceed on an answer nobody has. What it proceeded on was the volume name the
// backup recorded -- which for a project that has since been renamed is a
// volume no container mounts, so the restore untarred somebody's uploads into
// storage nothing reads, reported success, and changed nothing the deployment
// uses. A refusal the operator can act on is the only honest outcome.
func (e *Engine) currentVolumes(
	ctx context.Context, inspector ports.VolumeInspector,
) (map[string]ports.NamedVolume, error) {
	storage, err := inspector.Volumes(ctx, e.runtimeConfig)
	if err != nil {
		return nil, domain.BackupError(err,
			"cannot read the project's volumes, so a restore cannot tell which "+
				"volume to write into").
			WithHint("nothing was written; the name recorded in the backup belongs " +
				"to the project as it was configured then, and writing into it " +
				"blind would fill a volume nothing mounts")
	}
	out := make(map[string]ports.NamedVolume, len(storage.Volumes))
	for _, v := range storage.Volumes {
		out[v.Name] = v
	}
	return out, nil
}

// mountingServices is who mounts this volume *now*, falling back to who mounted
// it when the backup was taken.
//
// The live list is the one that matters. A release that has since added a
// service on the same volume records nothing about it in an old backup, so a
// refusal reading only the recorded list would let the restore untar into a
// volume that new service is holding open -- the same blindness as checking
// only for `running`, arriving by a different route.
//
// The recorded list is the fallback rather than the source, and it covers the
// opposite case: the project resolved fine and no longer declares this volume
// at all, because the release dropped it. The containers that mounted it may
// still be there, so what the backup knew is better than knowing nothing. A
// project that cannot be resolved is not this case and never reaches here --
// currentVolumes refuses the restore outright, because falling back for *every*
// volume would write a renamed project's data into storage nothing mounts.
func mountingServices(c ports.ComponentRecord, live map[string]ports.NamedVolume) []string {
	if v, ok := live[c.Volume.Volume]; ok {
		return v.Services
	}
	return c.Volume.Services
}

// componentSelected reports whether a component is in scope. An empty
// selection is everything, matching RestoreOptions.Components.
func componentSelected(selected []ports.Component, want ports.Component) bool {
	if len(selected) == 0 {
		return true
	}
	for _, c := range selected {
		if c == want {
			return true
		}
	}
	return false
}

func joinServices(services []string) string {
	if len(services) == 0 {
		return "the project"
	}
	return strings.Join(services, ", ")
}
